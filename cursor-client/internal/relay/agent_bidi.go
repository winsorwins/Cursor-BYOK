package relay

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cursor-client/internal/database"

	agentv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/agent/v1"
	aiserverv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/aiserver/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	agentRunSSEPath                             = "/agent.v1.AgentService/RunSSE"
	bidiAppendRPCPath                           = "/aiserver.v1.BidiService/BidiAppend"
	agentIntermediateConversationCheckpointMode = false
	agentKVFetchTimeout                         = 2 * time.Second
	agentKVBlobMaxEntries                       = 2048
)

type agentSessionState struct {
	RequestID              string
	CursorConversationID   string // Stable conversation ID from Cursor client
	ModelName              string
	AgentMode              cursorAgentMode
	WorkspaceRoot          string
	ThinkingLevel          int
	ThinkingEffort         string
	Messages               []chatMessage
	Conversation           *agentv1.ConversationStateStructure
	PriorUsedTokens        int
	UpdatedAt              time.Time
	Ready           chan struct{}
	readyClosed     bool
}

func (g *Gateway) tryHandleAgentBidi(req *http.Request) (*http.Response, bool) {
	if req == nil || req.URL == nil || !isCursorRequest(req) {
		return nil, false
	}
	switch req.URL.Path {
	case bidiAppendRPCPath:
		return g.handleLocalBidiAppend(req), true
	case agentRunSSEPath:
		return g.handleLocalAgentRunSSE(req), true
	default:
		return nil, false
	}
}

func (g *Gateway) handleLocalBidiAppend(req *http.Request) *http.Response {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		g.completeHTTPRequest(req, http.StatusBadRequest, "local/bidi_append", true, false, "", "failed to read request")
		return textHTTPResponse(req, http.StatusBadRequest, "failed to read request")
	}
	_ = req.Body.Close()

	appendReq, err := parseBidiAppendRequestBody(body, req.Header)
	if err != nil {
		log.Printf("[Gateway] BidiAppend decode warning: %s", bidiDecodeSummary(body, req.Header, err))
		g.completeHTTPRequest(req, http.StatusOK, "local/bidi_append", true, false, "", err.Error())
		return g.bidiAppendOKResponse(req)
	}
	requestID := ""
	if appendReq.RequestId != nil {
		requestID = appendReq.RequestId.GetRequestId()
	}
	if requestID == "" {
		requestID = "default"
	}

	parsed, parseErr := g.parseAgentClientPayload(requestID, appendReq.GetData())
	if parseErr != nil {
		log.Printf("[Gateway] BidiAppend parse warning request=%s: %v", requestID, parseErr)
	}
	if parsed.RequestID == "" {
		parsed.RequestID = requestID
	}
	if parsed.UpdatedAt.IsZero() {
		parsed.UpdatedAt = time.Now()
	}
	g.mergeAgentSession(parsed)

	return g.bidiAppendOKResponse(req)
}

func (g *Gateway) handleLocalAgentRunSSE(req *http.Request) *http.Response {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		g.completeHTTPRequest(req, http.StatusBadRequest, "byok/agent", true, true, "", "failed to read request")
		return textHTTPResponse(req, http.StatusBadRequest, "failed to read request")
	}
	_ = req.Body.Close()

	runReq, err := parseAgentRunRequestBody(body, req.Header)
	if err != nil {
		g.completeHTTPRequest(req, http.StatusBadRequest, "byok/agent", true, true, "", err.Error())
		return textHTTPResponse(req, http.StatusBadRequest, err.Error())
	}
	requestID := runReq.GetRequestId()
	if requestID == "" {
		requestID = "default"
	}

	state := g.ensureAgentSession(requestID)
	select {
	case <-state.Ready:
	case <-time.After(3 * time.Second):
	case <-req.Context().Done():
		g.completeHTTPRequest(req, http.StatusRequestTimeout, "byok/agent", true, true, "", req.Context().Err().Error())
		return textHTTPResponse(req, http.StatusRequestTimeout, req.Context().Err().Error())
	}

	chatReq, adapter := g.agentChatRequest(requestID)
	if adapter == nil {
		if chatReq.ModelName == "" {
			chatReq.ModelName = g.defaultAdapterName()
		}
		adapter = g.findAdapterByCursorName(chatReq.ModelName)
	}
	if adapter == nil {
		errText := "BYOK model adapter not found"
		g.emit(Event{Type: EventBYOKFailure, Model: chatReq.ModelName, Error: errText})
		g.completeHTTPRequest(req, http.StatusNotFound, "byok/agent", true, true, chatReq.ModelName, errText)
		return textHTTPResponse(req, http.StatusNotFound, errText)
	}

	// Create conversation and turn records in database
	var turnID int64
	var conversationID string
	if g.db != nil {
		turnID = g.createConversationAndTurn(requestID, chatReq, state)
		conversationID = agentConversationID(requestID, chatReq, state)
	}

	reader, writer := io.Pipe()
	streamCtx := req.Context()
	done := make(chan error, 1)
	go func() {
		err := g.streamAgentBYOK(streamCtx, writer, chatReq, adapter, turnID, conversationID)
		if err != nil {
			// Update turn status to failed on error
			if g.db != nil && turnID > 0 {
				if updateErr := g.db.UpdateTurnStatus(turnID, "failed", err.Error()); updateErr != nil {
					log.Printf("[Gateway] Failed to update turn status to failed: %v", updateErr)
				} else {
					log.Printf("[Gateway] Turn failed: turn=%d error=%v", turnID, err)
				}
			}
			_ = writer.CloseWithError(err)
			done <- err
			return
		}
		_ = writer.Close()
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			_ = reader.Close()
			g.emit(Event{Type: EventBYOKFailure, Model: chatReq.ModelName, Error: err.Error()})
			g.completeHTTPRequest(req, http.StatusBadGateway, "byok/agent", true, true, chatReq.ModelName, err.Error())
			return textHTTPResponse(req, http.StatusBadGateway, err.Error())
		}
		g.completeHTTPRequest(req, http.StatusOK, "byok/agent", true, true, chatReq.ModelName, "")
		return agentStreamResponse(req, reader)
	case <-time.After(50 * time.Millisecond):
		g.completeHTTPRequest(req, http.StatusOK, "byok/agent", true, true, chatReq.ModelName, "")
		return agentStreamResponse(req, reader)
	}
}

func (g *Gateway) bidiAppendOKResponse(req *http.Request) *http.Response {
	if strings.Contains(strings.ToLower(req.Header.Get("Content-Type")), "json") {
		return g.localJSONResponse(req, http.StatusOK, map[string]any{}, "local/bidi_append")
	}
	payloadResp, _ := proto.Marshal(&aiserverv1.BidiAppendResponse{})
	return g.localProtoResponse(req, payloadResp, "local/bidi_append")
}

func parseBidiAppendRequestBody(body []byte, header http.Header) (*aiserverv1.BidiAppendRequest, error) {
	payload, err := firstPayloadFromHeaders(body, header)
	if err != nil {
		return nil, err
	}
	req := &aiserverv1.BidiAppendRequest{}
	if looksJSONPayload(payload, header.Get("Content-Type")) {
		if err := protojson.Unmarshal(payload, req); err != nil {
			return nil, err
		}
		return req, nil
	}
	if err := proto.Unmarshal(payload, req); err != nil {
		return nil, err
	}
	return req, nil
}

func parseAgentRunRequestBody(body []byte, header http.Header) (*agentv1.BidiRequestId, error) {
	payload, err := firstPayloadFromHeaders(body, header)
	if err != nil {
		return nil, err
	}
	req := &agentv1.BidiRequestId{}
	if looksJSONPayload(payload, header.Get("Content-Type")) {
		if err := protojson.Unmarshal(payload, req); err != nil {
			return nil, err
		}
		return req, nil
	}
	if err := proto.Unmarshal(payload, req); err != nil {
		return nil, err
	}
	return req, nil
}

func firstPayloadFromHeaders(body []byte, header http.Header) ([]byte, error) {
	decoded, err := decodeBodyContent(body, header)
	if err != nil {
		return nil, err
	}
	return firstPayload(decoded, header.Get("Content-Type"))
}

func decodeBodyContent(body []byte, header http.Header) ([]byte, error) {
	decoded := body
	if encoding := header.Get("Content-Encoding"); encoding != "" {
		var err error
		decoded, err = decodeContentEncoding(decoded, encoding)
		if err != nil {
			return nil, fmt.Errorf("Content-Encoding %s: %w", encoding, err)
		}
	}
	for _, headerName := range []string{"Connect-Content-Encoding", "Grpc-Encoding"} {
		encoding := header.Get(headerName)
		if encoding == "" || !looksCompressedBody(decoded, encoding) {
			continue
		}
		next, err := decodeContentEncoding(decoded, encoding)
		if err != nil {
			log.Printf("[Gateway] ignoring %s body decode failure: %v", headerName, err)
			continue
		}
		decoded = next
	}
	return decoded, nil
}

func looksCompressedBody(body []byte, encoding string) bool {
	for _, part := range strings.Split(encoding, ",") {
		switch strings.TrimSpace(strings.ToLower(part)) {
		case "gzip":
			return len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b
		case "deflate", "zlib":
			return len(body) >= 2 && body[0] == 0x78
		}
	}
	return false
}

func decodeContentEncoding(body []byte, encoding string) ([]byte, error) {
	out := body
	for _, part := range strings.Split(encoding, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		switch part {
		case "", "identity":
			continue
		case "gzip":
			reader, err := gzip.NewReader(bytes.NewReader(out))
			if err != nil {
				return nil, err
			}
			data, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			out = data
		case "deflate", "zlib":
			reader, err := zlib.NewReader(bytes.NewReader(out))
			if err != nil {
				return nil, err
			}
			data, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			out = data
		default:
			return nil, fmt.Errorf("unsupported content encoding")
		}
	}
	return out, nil
}

func bidiDecodeSummary(body []byte, header http.Header, err error) string {
	previewLen := len(body)
	if previewLen > 16 {
		previewLen = 16
	}
	return fmt.Sprintf(
		"contentType=%q contentEncoding=%q connectEncoding=%q grpcEncoding=%q bodyLen=%d bodyPrefix=%s err=%v",
		header.Get("Content-Type"),
		header.Get("Content-Encoding"),
		header.Get("Connect-Content-Encoding"),
		header.Get("Grpc-Encoding"),
		len(body),
		hex.EncodeToString(body[:previewLen]),
		err,
	)
}

func looksJSONPayload(payload []byte, contentType string) bool {
	if strings.Contains(strings.ToLower(contentType), "json") {
		return true
	}
	payload = bytes.TrimSpace(payload)
	return len(payload) > 0 && (payload[0] == '{' || payload[0] == '[')
}

func agentStreamResponse(req *http.Request, body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		ContentLength: -1,
		Header: http.Header{
			"Content-Type":             []string{"text/event-stream"},
			"Cache-Control":            []string{"no-cache"},
			"Connection":               []string{"keep-alive"},
			"X-Cursor-Assistant-Local": []string{"1"},
			"X-Accel-Buffering":        []string{"no"},
		},
		Body:    body,
		Request: req,
	}
}

func (g *Gateway) parseAgentClientPayload(requestID string, hexData string) (*agentSessionState, error) {
	state := &agentSessionState{RequestID: requestID, UpdatedAt: time.Now()}
	if strings.TrimSpace(hexData) == "" {
		return state, nil
	}
	raw, err := hex.DecodeString(hexData)
	if err != nil {
		return state, err
	}
	var msg agentv1.AgentClientMessage
	if err := proto.Unmarshal(raw, &msg); err != nil {
		return state, err
	}
	if execMsg := msg.GetExecClientMessage(); execMsg != nil {
		g.completeAgentExec(execMsg)
	}
	if kvMsg := msg.GetKvClientMessage(); kvMsg != nil {
		g.handleAgentKVClientMessage(kvMsg)
	}
	if runReq := msg.GetRunRequest(); runReq != nil {
		state.ModelName, state.ThinkingLevel, state.ThinkingEffort = modelFromAgentRunRequest(runReq)
		state.AgentMode = agentModeFromRunRequest(runReq)
		state.WorkspaceRoot = workspaceRootFromAgentRunRequest(runReq)

		// Priority 1: Read explicit conversation_id from AgentRunRequest
		if explicitConvID := runReq.GetConversationId(); explicitConvID != "" {
			state.CursorConversationID = buildCursorConversationID(explicitConvID, state.WorkspaceRoot, state.AgentMode)
			log.Printf("[Gateway] Agent run explicit conversation_id request=%s cursorConvID=%s explicitID=%s",
				requestID, state.CursorConversationID, explicitConvID)
		}

		// Extract conversation state for context restoration
		if conversation := runReq.GetConversationState(); conversation != nil {
			if cloned, ok := proto.Clone(conversation).(*agentv1.ConversationStateStructure); ok {
				state.Conversation = cloned
			}
			state.PriorUsedTokens = int(conversation.GetTokenDetails().GetUsedTokens())
			if state.AgentMode == "" {
				state.AgentMode = cursorAgentModeFromProto(conversation.GetMode())
			}

			// Priority 2: If no explicit conversation_id, use ConversationStateStructure anchor
			if state.CursorConversationID == "" {
				state.CursorConversationID = extractConversationIDFromState(conversation, state.WorkspaceRoot, state.AgentMode)
			}

			log.Printf("[Gateway] Agent run conversation state request=%s cursorConvID=%s rootRefs=%d turns=%d priorUsedTokens=%d",
				requestID, state.CursorConversationID, len(conversation.GetRootPromptMessagesJson()), len(conversation.GetTurns()), state.PriorUsedTokens)
		}

		// Priority 3: Fallback to requestID if no explicit ID and no stable anchor
		// This allows first-turn bootstrap - each request without explicit ID creates separate conversation
		if state.CursorConversationID == "" {
			state.CursorConversationID = requestID
			log.Printf("[Gateway] Agent run fallback to requestID request=%s cursorConvID=%s",
				requestID, state.CursorConversationID)
		}

		if text := g.textFromConversationAction(runReq.GetAction(), state.WorkspaceRoot); text != "" {
			state.Messages = append(state.Messages, chatMessage{Role: "user", Content: text})
		}
	}
	if action := msg.GetConversationAction(); action != nil {
		state.AgentMode = agentModeFromConversationAction(action)
		state.WorkspaceRoot = workspaceRootFromConversationAction(action)
		if text := g.textFromConversationAction(action, state.WorkspaceRoot); text != "" {
			state.Messages = append(state.Messages, chatMessage{Role: "user", Content: text})
		}
	}
	return state, nil
}

func modelFromAgentRunRequest(req *agentv1.AgentRunRequest) (string, int, string) {
	if req == nil {
		return "", 0, ""
	}
	if requested := req.GetRequestedModel(); requested != nil {
		level := 0
		effort := ""
		if requested.GetMaxMode() {
			level = 3
			effort = "max"
		}
		for _, param := range requested.GetParameters() {
			if param == nil {
				continue
			}
			if strings.Contains(strings.ToLower(param.GetId()), "thinking") {
				value := strings.ToLower(strings.TrimSpace(param.GetValue()))
				switch value {
				case "medium":
					level = 1
					effort = value
				case "high":
					level = 2
					effort = value
				case "xhigh", "x_high", "x-high", "very_high", "max":
					level = 3
					effort = value
				}
			}
		}
		if requested.GetModelId() != "" {
			return requested.GetModelId(), level, effort
		}
	}
	if details := req.GetModelDetails(); details != nil {
		level := 0
		effort := ""
		if details.GetMaxMode() {
			level = 3
			effort = "max"
		}
		if details.GetModelId() != "" {
			return details.GetModelId(), level, effort
		}
		if details.GetDisplayModelId() != "" {
			return details.GetDisplayModelId(), level, effort
		}
	}
	return "", 0, ""
}

func textFromConversationAction(action *agentv1.ConversationAction, workspaceRoot string) string {
	return (*Gateway)(nil).textFromConversationAction(action, workspaceRoot)
}

func (g *Gateway) textFromConversationAction(action *agentv1.ConversationAction, workspaceRoot string) string {
	if action == nil {
		return ""
	}
	if uma := action.GetUserMessageAction(); uma != nil && uma.GetUserMessage() != nil {
		return g.textFromUserMessage(uma.GetUserMessage(), workspaceRoot)
	}
	if start := action.GetStartPlanAction(); start != nil {
		return g.textFromUserMessage(start.GetUserMessage(), workspaceRoot)
	}
	if exec := action.GetExecutePlanAction(); exec != nil {
		return textFromExecutePlanAction(exec)
	}
	return ""
}

func (g *Gateway) textFromUserMessage(msg *agentv1.UserMessage, workspaceRoot string) string {
	if msg == nil {
		return ""
	}
	contextText := g.selectedContextPrompt(msg.GetSelectedContext(), workspaceRoot)
	baseText := msg.GetText()
	if baseText == "" {
		baseText = msg.GetRichText()
	}
	if contextText != "" && baseText != "" {
		return contextText + "\n\n" + baseText
	}
	if contextText != "" {
		return contextText
	}
	return baseText
}

func textFromExecutePlanAction(action *agentv1.ExecutePlanAction) string {
	if action == nil {
		return ""
	}
	planText := strings.TrimSpace(action.GetPlan().GetPlan())
	if planText == "" {
		planText = strings.TrimSpace(action.GetPlanFileContent())
	}
	if planText == "" && strings.TrimSpace(action.GetPlanFileUri()) != "" {
		planText = "Plan file: " + strings.TrimSpace(action.GetPlanFileUri())
	}
	if planText == "" {
		return "Execute the approved plan now."
	}
	return "Execute the approved plan now.\n\n<plan>\n" + planText + "\n</plan>"
}

func selectedContextPrompt(ctx *agentv1.SelectedContext, workspaceRoot string) string {
	return (*Gateway)(nil).selectedContextPrompt(ctx, workspaceRoot)
}

func (g *Gateway) selectedContextPrompt(ctx *agentv1.SelectedContext, workspaceRoot string) string {
	if ctx == nil {
		return ""
	}
	sections := []string{}
	if userInfo := userInfoContextText(ctx, workspaceRoot); userInfo != "" {
		sections = append(sections, userInfo)
	}
	if visible := g.ideStateFilesContextText("visible_files", selectedVisibleFiles(ctx), workspaceRoot); visible != "" {
		sections = append(sections, visible)
	}
	if recent := g.ideStateFilesContextText("recently_viewed_files", selectedRecentFiles(ctx), workspaceRoot); recent != "" {
		sections = append(sections, recent)
	}
	if selected := selectedFilesContextText(ctx, workspaceRoot); selected != "" {
		sections = append(sections, selected)
	}
	if selections := codeSelectionsContextText(ctx, workspaceRoot); selections != "" {
		sections = append(sections, selections)
	}
	return strings.Join(sections, "\n\n")
}

func userInfoContextText(ctx *agentv1.SelectedContext, workspaceRoot string) string {
	if workspaceRoot == "" {
		return ""
	}
	focused := firstFocusedVisibleFile(ctx)
	lines := []string{"<user_info>", "Workspace Path: " + workspaceRoot}
	if focused != "" {
		lines = append(lines, "Current File: "+focused)
	}
	lines = append(lines, "</user_info>")
	return strings.Join(lines, "\n")
}

func selectedVisibleFiles(ctx *agentv1.SelectedContext) []*agentv1.InvocationContext_IdeState_File {
	if ctx == nil || ctx.GetInvocationContext() == nil || ctx.GetInvocationContext().GetIdeState() == nil {
		return nil
	}
	return ctx.GetInvocationContext().GetIdeState().GetVisibleFiles()
}

func selectedRecentFiles(ctx *agentv1.SelectedContext) []*agentv1.InvocationContext_IdeState_File {
	if ctx == nil || ctx.GetInvocationContext() == nil || ctx.GetInvocationContext().GetIdeState() == nil {
		return nil
	}
	return ctx.GetInvocationContext().GetIdeState().GetRecentlyViewedFiles()
}

func firstFocusedVisibleFile(ctx *agentv1.SelectedContext) string {
	files := selectedVisibleFiles(ctx)
	if len(files) == 0 {
		files = selectedRecentFiles(ctx)
	}
	if len(files) == 0 {
		return ""
	}
	return files[0].GetPath()
}

func ideStateFilesContextText(tag string, files []*agentv1.InvocationContext_IdeState_File, workspaceRoot string) string {
	return (*Gateway)(nil).ideStateFilesContextText(tag, files, workspaceRoot)
}

func (g *Gateway) ideStateFilesContextText(tag string, files []*agentv1.InvocationContext_IdeState_File, workspaceRoot string) string {
	if len(files) == 0 {
		return ""
	}
	lines := []string{"<" + tag + ">"}
	for _, file := range files {
		if file == nil || file.GetPath() == "" {
			continue
		}
		lines = append(lines, formatContextFileLine(file.GetPath(), file.GetRelativePath(), file.GetTotalLines(), cursorLine(file), workspaceRoot))
		if content := g.ideStateFileCachedContent(file, workspaceRoot); content != "" {
			lines = append(lines, truncateAgentContextText(content, 6000))
		}
	}
	if len(lines) == 1 {
		return ""
	}
	lines = append(lines, "</"+tag+">")
	return strings.Join(lines, "\n")
}

func (g *Gateway) ideStateFileCachedContent(file *agentv1.InvocationContext_IdeState_File, workspaceRoot string) string {
	if g == nil || file == nil {
		return ""
	}
	candidates := []string{
		file.GetRelativePath(),
		relativePathForContext(file.GetPath(), workspaceRoot),
		file.GetPath(),
		relativeFileSyncPath(file.GetPath(), workspaceRoot),
	}
	for _, candidate := range candidates {
		if content, ok := g.lookupFileSyncContent("", candidate); ok {
			return content
		}
	}
	return ""
}

func selectedFilesContextText(ctx *agentv1.SelectedContext, workspaceRoot string) string {
	if ctx == nil || len(ctx.GetFiles()) == 0 {
		return ""
	}
	lines := []string{"<selected_files>"}
	for _, file := range ctx.GetFiles() {
		if file == nil || file.GetPath() == "" {
			continue
		}
		lines = append(lines, formatContextFileLine(file.GetPath(), file.GetRelativePath(), 0, 0, workspaceRoot))
		if content := strings.TrimSpace(file.GetContent()); content != "" {
			lines = append(lines, truncateAgentContextText(content, 6000))
		}
	}
	if len(lines) == 1 {
		return ""
	}
	lines = append(lines, "</selected_files>")
	return strings.Join(lines, "\n")
}

func codeSelectionsContextText(ctx *agentv1.SelectedContext, workspaceRoot string) string {
	if ctx == nil || len(ctx.GetCodeSelections()) == 0 {
		return ""
	}
	lines := []string{"<code_selections>"}
	for _, selection := range ctx.GetCodeSelections() {
		if selection == nil || selection.GetPath() == "" {
			continue
		}
		lines = append(lines, formatContextFileLine(selection.GetPath(), selection.GetRelativePath(), 0, rangeStartLine(selection.GetRange()), workspaceRoot))
		if content := strings.TrimSpace(selection.GetContent()); content != "" {
			lines = append(lines, truncateAgentContextText(content, 6000))
		}
	}
	if len(lines) == 1 {
		return ""
	}
	lines = append(lines, "</code_selections>")
	return strings.Join(lines, "\n")
}

func formatContextFileLine(path string, relativePath string, totalLines int32, cursorLine int32, workspaceRoot string) string {
	if relativePath == "" {
		relativePath = relativePathForContext(path, workspaceRoot)
	}
	parts := []string{fmt.Sprintf("path=%q", path)}
	if relativePath != "" {
		parts = append(parts, fmt.Sprintf("relative_path=%q", filepath.ToSlash(relativePath)))
	}
	if cursorLine > 0 {
		parts = append(parts, fmt.Sprintf("cursor_line=%q", fmt.Sprint(cursorLine)))
	}
	if totalLines > 0 {
		parts = append(parts, fmt.Sprintf("total_lines=%q", fmt.Sprint(totalLines)))
	}
	return "<file " + strings.Join(parts, " ") + " />"
}

func relativePathForContext(path string, workspaceRoot string) string {
	if path == "" || workspaceRoot == "" {
		return ""
	}
	rel, err := filepath.Rel(workspaceRoot, path)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return ""
	}
	return rel
}

func cursorLine(file *agentv1.InvocationContext_IdeState_File) int32 {
	if file == nil || file.GetCursorPosition() == nil {
		return 0
	}
	return file.GetCursorPosition().GetLine()
}

func rangeStartLine(r *agentv1.Range) int32 {
	if r == nil || r.GetStart() == nil {
		return 0
	}
	return int32(r.GetStart().GetLine())
}

func truncateAgentContextText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "\n...[context truncated]"
}

func (g *Gateway) ensureAgentSession(requestID string) *agentSessionState {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.agentSessions == nil {
		g.agentSessions = make(map[string]*agentSessionState)
	}
	state := g.agentSessions[requestID]
	if state == nil {
		state = &agentSessionState{RequestID: requestID, Ready: make(chan struct{}), UpdatedAt: time.Now()}
		g.agentSessions[requestID] = state
	}
	return state
}

func (g *Gateway) mergeAgentSession(update *agentSessionState) {
	if update == nil || update.RequestID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.agentSessions == nil {
		g.agentSessions = make(map[string]*agentSessionState)
	}
	state := g.agentSessions[update.RequestID]
	if state == nil {
		state = &agentSessionState{RequestID: update.RequestID, Ready: make(chan struct{})}
		g.agentSessions[update.RequestID] = state
	}
	if update.ModelName != "" {
		state.ModelName = update.ModelName
	}
	if update.AgentMode != "" {
		state.AgentMode = update.AgentMode
	}
	if update.WorkspaceRoot != "" {
		state.WorkspaceRoot = normalizeWorkspaceRoot(update.WorkspaceRoot)
	}
	if update.CursorConversationID != "" {
		state.CursorConversationID = update.CursorConversationID
	}
	if update.ThinkingLevel > 0 {
		state.ThinkingLevel = update.ThinkingLevel
	}
	if update.ThinkingEffort != "" {
		state.ThinkingEffort = update.ThinkingEffort
	}
	if len(update.Messages) > 0 {
		state.Messages = dedupeAdjacentChatMessages(append(state.Messages, update.Messages...))
	}
	if update.Conversation != nil {
		if cloned, ok := proto.Clone(update.Conversation).(*agentv1.ConversationStateStructure); ok {
			state.Conversation = cloned
		}
	}
	if update.PriorUsedTokens > 0 {
		state.PriorUsedTokens = update.PriorUsedTokens
	}
	state.UpdatedAt = update.UpdatedAt
	if len(state.Messages) > 0 && !state.readyClosed {
		close(state.Ready)
		state.readyClosed = true
	}
	cutoff := time.Now().Add(-30 * time.Minute)
	for id, session := range g.agentSessions {
		if session != nil && !session.UpdatedAt.IsZero() && session.UpdatedAt.Before(cutoff) {
			delete(g.agentSessions, id)
		}
	}
}

func (g *Gateway) agentChatRequest(requestID string) (unifiedChatRequest, *ModelAdapter) {
	g.mu.RLock()
	state := g.agentSessions[requestID]
	var model string
	mode := cursorAgentModeAgent
	modeSet := false
	var workspaceRoot string
	var cursorConversationID string
	var level int
	var effort string
	var priorUsedTokens int
	var conversation *agentv1.ConversationStateStructure
	messages := []chatMessage{}
	if state != nil {
		model = state.ModelName
		if state.AgentMode != "" {
			mode = state.AgentMode
			modeSet = true
		}
		workspaceRoot = normalizeWorkspaceRoot(state.WorkspaceRoot)
		cursorConversationID = state.CursorConversationID
		level = state.ThinkingLevel
		effort = state.ThinkingEffort
		messages = latestAgentUserMessages(state.Messages)
		priorUsedTokens = state.PriorUsedTokens
		if state.Conversation != nil {
			if cloned, ok := proto.Clone(state.Conversation).(*agentv1.ConversationStateStructure); ok {
				conversation = cloned
				if !modeSet {
					if conversationMode := cursorAgentModeFromProto(conversation.GetMode()); conversationMode != "" {
						mode = conversationMode
						modeSet = true
					}
				}
			}
		}
	}
	g.mu.RUnlock()
	if model == "" {
		model = g.defaultAdapterName()
	}
	if len(messages) == 0 {
		messages = append(messages, chatMessage{Role: "user", Content: ""})
	}
	currentUserText := lastUserMessageContent(messages)
	return unifiedChatRequest{
		RequestID:            requestID,
		CursorConversationID: cursorConversationID,
		ModelName:            model,
		Messages:             messages,
		Mode:                 agentRunSSEPath,
		AgentMode:            mode,
		WorkspaceRoot:        workspaceRoot,
		ThinkingLevel:        level,
		ThinkingEffort:       effort,
		CurrentUserText:      currentUserText,
		PriorUsedTokens:      priorUsedTokens,
		Conversation:         conversation,
		ParameterValues:      map[string]string{},
	}, g.findAdapterByCursorName(model)
}

func (g *Gateway) defaultAdapterName() string {
	models := g.byokModels()
	if len(models) == 0 || models[0] == nil {
		return ""
	}
	return models[0].CursorModelName()
}

func (g *Gateway) streamAgentBYOK(ctx context.Context, writer io.Writer, req unifiedChatRequest, adapter *ModelAdapter, turnID int64, conversationID string) error {
	if adapter == nil {
		return fmt.Errorf("model adapter not found")
	}
	req.WorkspaceRoot = normalizeWorkspaceRoot(req.WorkspaceRoot)
	if adapter.Type != "openai" && adapter.Type != "anthropic" {
		return fmt.Errorf("provider %s is not supported", adapter.Type)
	}
	req.AgentMode = defaultCursorAgentMode(req.AgentMode)
	g.emit(Event{Type: EventBYOKRouted, Model: req.ModelName})
	log.Printf("[Gateway] Agent BYOK start model=%s provider=%s mode=%s messages=%d workspace=%q thinking=%q", req.ModelName, adapter.Type, req.AgentMode.displayName(), len(req.Messages), req.WorkspaceRoot, req.ThinkingEffort)
	if ctx == nil {
		ctx = context.Background()
	}
	agentCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	heartbeatWriter := newHeartbeatWriter(writer)
	stopHeartbeat := g.startAgentHeartbeat(agentCtx.Done(), heartbeatWriter)
	defer close(stopHeartbeat)
	writer = heartbeatWriter

	adapter.Normalize()
	endpoint := adapter.Endpoint
	if restored, stats := g.restoreAgentConversationMessages(agentCtx, writer, req.Conversation, req.WorkspaceRoot); len(restored) > 0 {
		req.Messages = mergeAgentRestoredMessages(restored, req.Messages)
		log.Printf("[Gateway] Agent restored conversation messages request=%s turns=%d messages=%d missing=%d totalMessages=%d", req.RequestID, stats.Turns, stats.Messages, stats.MissingBlobs, len(req.Messages))
	} else if req.PriorUsedTokens > 0 && req.Conversation != nil {
		log.Printf("[Gateway] Agent conversation restore empty request=%s turns=%d missing=%d priorUsedTokens=%d", req.RequestID, len(req.Conversation.GetTurns()), stats.MissingBlobs, req.PriorUsedTokens)
	}
	startedAt := time.Now()
	if err := writeAgentStepStartedFrame(writer); err != nil {
		return err
	}
	needsEdit := req.AgentMode == cursorAgentModeAgent && agentRequestNeedsEdit(req.Messages)
	req.Messages = withAgentToolSystemMessage(req.Messages, req.WorkspaceRoot, req.AgentMode)
	promptTokens := agentPromptTokenEstimate(req.Messages, adapter, endpoint, req.AgentMode)
	completionTokens := 0
	cacheReadTokens := 0
	cacheWriteTokens := 0
	contextWindow := agentContextWindow(adapter)
	if err := writeAgentConversationCheckpointFrame(writer, promptTokens, contextWindow, req.WorkspaceRoot, req.AgentMode); err != nil {
		return err
	}
	toolExecutions := 0
	editAttempts := 0
	editSucceeded := false
	editReminderCount := 0
	textChars := 0
	finalTextAfterTools := 0
	finalAnswerFailed := false
	var assistantText strings.Builder
	readPaths := []string{}
	for turn := 0; turn < maxAgentToolTurns; turn++ {
		log.Printf("[Gateway] Agent BYOK turn=%d model=%s messages=%d", turn+1, req.ModelName, len(req.Messages))
		turnResult, err := g.streamAgentProviderTurn(agentCtx, writer, req, adapter, endpoint)
		if err != nil {
			return err
		}
		if turnResult.PromptTokens > 0 {
			promptTokens = turnResult.PromptTokens
		}
		if turnResult.CompletionTokens > 0 {
			completionTokens += turnResult.CompletionTokens
		}
		cacheReadTokens += turnResult.CacheReadTokens
		cacheWriteTokens += turnResult.CacheWriteTokens
		if err := writeAgentConversationCheckpointFrame(writer, agentContextUsedTokensWithPrior(req.PriorUsedTokens, promptTokens, completionTokens, req.Messages), contextWindow, req.WorkspaceRoot, req.AgentMode); err != nil {
			return err
		}
		if turnResult.Text != "" {
			assistantText.WriteString(turnResult.Text)
		}
		textChars += turnResult.TextChars
		if len(turnResult.ToolCalls) == 0 {
			if toolExecutions > 0 {
				finalTextAfterTools += turnResult.TextChars
			}
			if needsEdit && !editSucceeded && toolExecutions > 0 && editReminderCount < agentMaxEditReminders && turn+1 < maxAgentToolTurns {
				editReminderCount++
				finalTextAfterTools = 0
				log.Printf("[Gateway] Agent BYOK continuing because edit is required model=%s tools=%d reminders=%d", req.ModelName, toolExecutions, editReminderCount)
				req.Messages = appendAgentEditRequiredInstruction(req.Messages)
				continue
			}
			log.Printf("[Gateway] Agent BYOK turn=%d completed without tool calls", turn+1)
			break
		}
		log.Printf("[Gateway] Agent BYOK turn=%d toolCalls=%d", turn+1, len(turnResult.ToolCalls))
		for _, toolCall := range turnResult.ToolCalls {
			if toolExecutions >= agentMaxToolExecutions {
				log.Printf("[Gateway] Agent BYOK reached tool execution limit model=%s tools=%d", req.ModelName, toolExecutions)
				break
			}
			if !isAllowedAgentToolNameForMode(toolCall.Name, req.AgentMode) {
				log.Printf("[Gateway] Agent BYOK skipped unsupported tool name=%s callID=%s mode=%s", toolCall.Name, toolCall.ID, req.AgentMode.displayName())
				continue
			}
			toolExecutions++
			args := parseToolArgs(toolCall.Arguments)
			args = normalizeAgentToolArgs(toolCall.Name, args, req.WorkspaceRoot)

			// Save tool call to database
			var toolCallID int64
			if g.db != nil && turnID > 0 {
				dbToolCall := &database.ToolCall{
					TurnID:         turnID,
					ConversationID: conversationID,
					ToolCallID:     toolCall.ID,
					ToolName:       toolCall.Name,
					ToolArgs:       toolCall.Arguments,
					Status:         "running",
				}
				if err := g.db.CreateToolCall(dbToolCall); err != nil {
					log.Printf("[Gateway] Failed to save tool call: %v", err)
				} else {
					toolCallID = dbToolCall.ID
					log.Printf("[Gateway] Saved tool call: turn=%d tool=%s id=%d", turnID, toolCall.Name, toolCallID)
				}
			}
			readPaths = append(readPaths, agentCheckpointReadPaths(toolCall.Name, args, req.WorkspaceRoot)...)
			startedCall := agentToolCallProto(toolCall.Name, args, nil)
			argsDelta := formatAgentToolArgs(args)
			if err := writeAgentPartialToolCallFrame(writer, toolCall.ID, toolCall.ID, startedCall, argsDelta); err != nil {
				return err
			}
			if err := writeAgentToolCallStartedFrame(writer, toolCall.ID, toolCall.ID, startedCall); err != nil {
				return err
			}
			if agentToolChangesFiles(toolCall.Name) {
				editAttempts++
			}
			execResult := g.executeAgentTool(agentCtx, writer, toolCall, req.WorkspaceRoot)
			if agentToolResultLooksSuccessfulEdit(execResult) {
				editSucceeded = true
			}
			if execResult.StartedCall == nil {
				execResult.StartedCall = startedCall
			}
			if err := writeAgentToolCallCompletedFrame(writer, toolCall.ID, toolCall.ID, execResult.CompletedCall); err != nil {
				return err
			}

			// Update tool call result in database
			if g.db != nil && toolCallID > 0 {
				resultText := execResult.ResultText
				status := "completed"
				errorMsg := ""
				// Check if result indicates error
				if isToolResultError(resultText) {
					status = "failed"
					errorMsg = resultText
				}
				if err := g.db.UpdateToolCallResult(toolCall.ID, resultText, status, errorMsg); err != nil {
					log.Printf("[Gateway] Failed to update tool call result: %v", err)
				} else {
					log.Printf("[Gateway] Updated tool call result: id=%d status=%s", toolCallID, status)
				}
			}

			req.Messages = appendAgentToolResultMessage(req.Messages, execResult)
		}
		if toolExecutions >= agentMaxToolExecutions {
			break
		}
		if needsEdit && !editSucceeded && toolExecutions >= agentEditReminderToolThreshold*(editReminderCount+1) && editReminderCount < agentMaxEditReminders && turn+1 < maxAgentToolTurns {
			editReminderCount++
			log.Printf("[Gateway] Agent BYOK steering toward edit model=%s tools=%d editAttempts=%d reminders=%d", req.ModelName, toolExecutions, editAttempts, editReminderCount)
			req.Messages = appendAgentEditRequiredInstruction(req.Messages)
		}
	}
	if needsEdit && !editSucceeded && toolExecutions > 0 {
		log.Printf("[Gateway] Agent BYOK ended without successful edit model=%s tools=%d editAttempts=%d reminders=%d", req.ModelName, toolExecutions, editAttempts, editReminderCount)
	}
	if toolExecutions > 0 && finalTextAfterTools == 0 {
		log.Printf("[Gateway] Agent BYOK forcing final answer model=%s tools=%d", req.ModelName, toolExecutions)
		finalReq := req
		finalReq.Messages = appendAgentFinalAnswerInstruction(compactAgentMessagesForFinal(req.Messages))
		finalReq.DisableTools = true
		turnResult, err := g.streamAgentProviderTurnWithTimeout(agentCtx, writer, finalReq, adapter, endpoint, agentFinalAnswerTimeout)
		if err != nil {
			log.Printf("[Gateway] Agent BYOK final answer failed model=%s tools=%d error=%v", req.ModelName, toolExecutions, err)
			g.emit(Event{Type: EventBYOKFailure, Model: req.ModelName, Error: err.Error()})
			finalAnswerFailed = true
			fallback := "已完成工具检查，但模型在生成最终回答时超时或断开。Cursor 工具调用已经执行完成，请缩小问题范围后重试；如果只是让它优化代码，建议先指定具体文件或函数。"
			if err := writeAgentServerFrame(writer, fallback); err != nil {
				return err
			}
			fallbackTokens := estimateTokens(fallback)
			if err := writeAgentTokenDeltaFrame(writer, fallbackTokens); err != nil {
				return err
			}
			completionTokens += fallbackTokens
			assistantText.WriteString(fallback)
			if err := writeAgentConversationCheckpointFrame(writer, agentContextUsedTokensWithPrior(req.PriorUsedTokens, promptTokens, completionTokens, req.Messages), contextWindow, req.WorkspaceRoot, req.AgentMode); err != nil {
				return err
			}
		} else {
			if turnResult.PromptTokens > 0 {
				promptTokens = turnResult.PromptTokens
			}
			if turnResult.CompletionTokens > 0 {
				completionTokens += turnResult.CompletionTokens
			}
			cacheReadTokens += turnResult.CacheReadTokens
			cacheWriteTokens += turnResult.CacheWriteTokens
			if err := writeAgentConversationCheckpointFrame(writer, agentContextUsedTokensWithPrior(req.PriorUsedTokens, promptTokens, completionTokens, req.Messages), contextWindow, req.WorkspaceRoot, req.AgentMode); err != nil {
				return err
			}
			if turnResult.Text != "" {
				assistantText.WriteString(turnResult.Text)
			}
			textChars += turnResult.TextChars
			if turnResult.TextChars == 0 {
				fallback := "已完成项目检查，但模型没有返回最终总结。请重试一次，或缩小分析范围后再发送。"
				if err := writeAgentServerFrame(writer, fallback); err != nil {
					return err
				}
				fallbackTokens := estimateTokens(fallback)
				if err := writeAgentTokenDeltaFrame(writer, fallbackTokens); err != nil {
					return err
				}
				completionTokens += fallbackTokens
				assistantText.WriteString(fallback)
				if err := writeAgentConversationCheckpointFrame(writer, agentContextUsedTokensWithPrior(req.PriorUsedTokens, promptTokens, completionTokens, req.Messages), contextWindow, req.WorkspaceRoot, req.AgentMode); err != nil {
					return err
				}
			}
		}
	}
	g.writeReferenceAgentConversationCheckpointFrame(agentCtx, writer, req, assistantText.String(), readPaths, agentContextUsedTokensWithPrior(req.PriorUsedTokens, promptTokens, completionTokens, req.Messages), contextWindow)
	if err := writeAgentStepCompletedFrame(writer, time.Since(startedAt)); err != nil {
		return err
	}
	if err := writeAgentTurnEndedFrame(writer); err != nil {
		return err
	}
	if err := writeAgentEndStreamFrame(writer); err != nil {
		return err
	}
	if !finalAnswerFailed {
		g.emit(Event{Type: EventBYOKSuccess, Model: req.ModelName})
	}
	g.emit(Event{Type: EventTokens, Model: req.ModelName, PromptTokens: promptTokens, CompletionTokens: completionTokens, CacheReadTokens: cacheReadTokens, CacheWriteTokens: cacheWriteTokens, EstimatedCost: estimateCost(adapter, promptTokens, completionTokens)})

	// Save token details to database
	if g.db != nil {
		if turnID > 0 {
			g.saveTokenDetails(conversationID, turnID, promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens)
		} else {
			log.Printf("[Gateway] Skipping token details save: turnID is 0")
		}
	}

	// Save assistant reply to database
	if g.db != nil && turnID > 0 {
		assistantReply := assistantText.String()
		if assistantReply != "" {
			// Get the current message count for this turn to determine MessageSeq
			messageSeq := len(req.Messages) // User messages count
			dbMsg := &database.Message{
				TurnID:         turnID,
				ConversationID: conversationID,
				MessageSeq:     messageSeq,
				Role:           "assistant",
				Content:        assistantReply,
			}
			if err := g.db.CreateMessage(dbMsg); err != nil {
				log.Printf("[Gateway] Failed to save assistant reply: %v", err)
			} else {
				log.Printf("[Gateway] Saved assistant reply: turn=%d seq=%d length=%d", turnID, messageSeq, len(assistantReply))
			}
		}
	}

	// Update turn status based on finalAnswerFailed
	if g.db != nil && turnID > 0 {
		if finalAnswerFailed {
			// If final answer failed, mark turn as failed
			if err := g.db.UpdateTurnStatus(turnID, "failed", "Final answer generation failed or timed out"); err != nil {
				log.Printf("[Gateway] Failed to update turn status to failed: %v", err)
			} else {
				log.Printf("[Gateway] Turn failed due to final answer failure: turn=%d", turnID)
			}
		} else {
			// Normal completion
			if err := g.db.UpdateTurnStatus(turnID, "completed", ""); err != nil {
				log.Printf("[Gateway] Failed to update turn status: %v", err)
			} else {
				log.Printf("[Gateway] Turn completed: turn=%d", turnID)
			}
		}
	}

	log.Printf("[Gateway] Agent BYOK done model=%s failed=%t promptTokens=%d completionTokens=%d cacheRead=%d cacheWrite=%d durationMs=%d", req.ModelName, finalAnswerFailed, promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens, time.Since(startedAt).Milliseconds())
	return nil
}

// isToolResultError checks if a tool result indicates an error
func isToolResultError(resultText string) bool {
	if resultText == "" {
		return false
	}
	lowerResult := strings.ToLower(resultText)
	return strings.Contains(lowerResult, "error") ||
		strings.Contains(lowerResult, "failed") ||
		strings.Contains(lowerResult, "failure")
}

func writeAgentServerFrame(w io.Writer, text string) error {
	return writeAgentInteractionUpdateFrame(w, &agentv1.InteractionUpdate{Message: &agentv1.InteractionUpdate_TextDelta{TextDelta: &agentv1.TextDeltaUpdate{Text: text}}})
}

func writeAgentTokenDeltaFrame(w io.Writer, tokens int) error {
	if tokens <= 0 {
		tokens = 1
	}
	return writeAgentInteractionUpdateFrame(w, &agentv1.InteractionUpdate{Message: &agentv1.InteractionUpdate_TokenDelta{TokenDelta: &agentv1.TokenDeltaUpdate{Tokens: int32(tokens)}}})
}

func writeAgentStepStartedFrame(w io.Writer) error {
	return writeAgentInteractionUpdateFrame(w, &agentv1.InteractionUpdate{Message: &agentv1.InteractionUpdate_StepStarted{StepStarted: &agentv1.StepStartedUpdate{StepId: 1}}})
}

func writeAgentStepCompletedFrame(w io.Writer, duration time.Duration) error {
	return writeAgentInteractionUpdateFrame(w, &agentv1.InteractionUpdate{Message: &agentv1.InteractionUpdate_StepCompleted{StepCompleted: &agentv1.StepCompletedUpdate{StepId: 1, StepDurationMs: duration.Milliseconds()}}})
}

func writeAgentTurnEndedFrame(w io.Writer) error {
	return writeAgentInteractionUpdateFrame(w, &agentv1.InteractionUpdate{Message: &agentv1.InteractionUpdate_TurnEnded{TurnEnded: &agentv1.TurnEndedUpdate{}}})
}

func agentContextWindow(adapter *ModelAdapter) int {
	if adapter != nil && adapter.ContextWindow > 0 {
		return adapter.ContextWindow
	}
	return localContextDefaultTokenLimit
}

func agentContextUsedTokens(promptTokens int, completionTokens int) int {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	return promptTokens + completionTokens
}

func agentContextUsedTokensWithPrior(priorUsedTokens int, promptTokens int, completionTokens int, messages []chatMessage) int {
	used := agentContextUsedTokens(promptTokens, completionTokens)
	if priorUsedTokens <= 0 || used >= priorUsedTokens {
		return used
	}
	latestUserTokens := estimateTokens(lastUserMessageContent(messages))
	return priorUsedTokens + latestUserTokens + maxInt(completionTokens, 0)
}

func agentPromptTokenEstimate(messages []chatMessage, adapter *ModelAdapter, endpoint string, modes ...cursorAgentMode) int {
	total := estimateMessagesTokens(messages)
	mode := cursorAgentModeAgent
	if len(modes) > 0 {
		mode = defaultCursorAgentMode(modes[0])
	}
	if adapter != nil {
		if tools := agentProviderTools(adapter.Type, endpoint, mode); len(tools) > 0 {
			if data, err := json.Marshal(tools); err == nil {
				total += estimateTokens(string(data))
			}
		}
	}
	return total
}

func workspaceURI(workspaceRoot string) string {
	root := filepath.ToSlash(normalizeWorkspaceRoot(workspaceRoot))
	if root == "" {
		return ""
	}
	if strings.HasPrefix(root, "/") {
		return "file://" + strings.ReplaceAll(root, " ", "%20")
	}
	return "file:///" + strings.ReplaceAll(root, " ", "%20")
}

func writeAgentConversationCheckpointFrame(w io.Writer, usedTokens int, maxTokens int, workspaceRoot string, mode cursorAgentMode) error {
	if !agentIntermediateConversationCheckpointMode {
		return nil
	}
	frame, err := buildAgentConversationCheckpointFrame(usedTokens, maxTokens, workspaceRoot, mode)
	if err != nil {
		return err
	}
	_, err = w.Write(frame)
	return err
}

func writeFinalAgentConversationCheckpointFrame(w io.Writer, usedTokens int, maxTokens int, workspaceRoot string) {
	frame, err := buildAgentConversationCheckpointFrame(usedTokens, maxTokens, workspaceRoot)
	if err != nil {
		log.Printf("[Gateway] Agent conversation checkpoint build failed usedTokens=%d maxTokens=%d error=%v", usedTokens, maxTokens, err)
		return
	}
	if _, err := w.Write(frame); err != nil {
		log.Printf("[Gateway] Agent conversation checkpoint write failed usedTokens=%d maxTokens=%d error=%v", usedTokens, maxTokens, err)
		return
	}
	log.Printf("[Gateway] Agent conversation checkpoint sent usedTokens=%d maxTokens=%d", usedTokens, maxTokens)
}

type agentKVBlob struct {
	Label string
	ID    []byte
	Data  []byte
}

type agentStoredKVBlob struct {
	Label     string
	Data      []byte
	UpdatedAt time.Time
}

type agentConversationRestoreStats struct {
	Turns        int
	Messages     int
	MissingBlobs int
}

type agentConversationCheckpointPayload struct {
	State *agentv1.ConversationStateStructure
	Blobs []agentKVBlob
}

func (g *Gateway) writeReferenceAgentConversationCheckpointFrame(ctx context.Context, w io.Writer, req unifiedChatRequest, assistantText string, readPaths []string, usedTokens int, maxTokens int) {
	payload, err := g.buildReferenceAgentConversationCheckpointPayload(req, assistantText, readPaths, usedTokens, maxTokens)
	if err != nil {
		log.Printf("[Gateway] Agent reference checkpoint build failed usedTokens=%d maxTokens=%d error=%v", usedTokens, maxTokens, err)
		return
	}
	for _, blob := range payload.Blobs {
		if ctx != nil {
			select {
			case <-ctx.Done():
				log.Printf("[Gateway] Agent reference checkpoint canceled before KV set label=%s error=%v", blob.Label, ctx.Err())
				return
			default:
			}
		}
		if err := g.writeAgentKVSetBlobFrame(w, blob); err != nil {
			log.Printf("[Gateway] Agent KV set blob write failed label=%s bytes=%d error=%v", blob.Label, len(blob.Data), err)
			return
		}
	}
	frame, err := buildAgentConversationCheckpointFrameFromState(payload.State)
	if err != nil {
		log.Printf("[Gateway] Agent reference checkpoint marshal failed usedTokens=%d maxTokens=%d error=%v", usedTokens, maxTokens, err)
		return
	}
	if _, err := w.Write(frame); err != nil {
		log.Printf("[Gateway] Agent reference checkpoint write failed usedTokens=%d maxTokens=%d error=%v", usedTokens, maxTokens, err)
		return
	}
	g.storeAgentConversationState(req.RequestID, payload.State)
	log.Printf("[Gateway] Agent reference checkpoint sent blobs=%d rootRefs=%d turns=%d readPaths=%d usedTokens=%d maxTokens=%d", len(payload.Blobs), len(payload.State.GetRootPromptMessagesJson()), len(payload.State.GetTurns()), len(payload.State.GetReadPaths()), usedTokens, maxTokens)
}

func (g *Gateway) buildReferenceAgentConversationCheckpointPayload(req unifiedChatRequest, assistantText string, readPaths []string, usedTokens int, maxTokens int) (*agentConversationCheckpointPayload, error) {
	if usedTokens < 0 {
		usedTokens = 0
	}
	if maxTokens <= 0 {
		maxTokens = localContextDefaultTokenLimit
	}
	state := g.agentConversationState(req.RequestID)
	if state == nil {
		state = &agentv1.ConversationStateStructure{}
	}
	mode := defaultCursorAgentMode(req.AgentMode).protoAgentMode()
	state.TokenDetails = &agentv1.ConversationTokenDetails{UsedTokens: uint32(usedTokens), MaxTokens: uint32(maxTokens)}
	state.Mode = &mode
	if workspace := workspaceURI(req.WorkspaceRoot); workspace != "" {
		state.PreviousWorkspaceUris = appendUniqueStrings(state.PreviousWorkspaceUris, workspace)
	}
	state.ReadPaths = appendUniqueStrings(state.ReadPaths, readPaths...)

	payload := &agentConversationCheckpointPayload{State: state}
	addBlob := func(label string, data []byte) []byte {
		if len(data) == 0 {
			return nil
		}
		id := agentKVBlobID(req.RequestID, label, data)
		payload.Blobs = append(payload.Blobs, agentKVBlob{Label: label, ID: id, Data: data})
		return id
	}

	if systemText := firstMessageContentByRole(req.Messages, "system"); systemText != "" {
		data, err := json.Marshal([]map[string]string{{"role": "system", "content": systemText}})
		if err != nil {
			return nil, err
		}
		if id := addBlob("root_prompt_messages_json", data); len(id) > 0 {
			state.RootPromptMessagesJson = appendUniqueBytes(state.RootPromptMessagesJson, id)
		}
	}

	userBlobID := []byte(nil)
	userText := strings.TrimSpace(req.CurrentUserText)
	if userText == "" {
		userText = lastUserMessageContent(req.Messages)
	}
	if userText != "" {
		user := &agentv1.UserMessage{
			Text:      userText,
			MessageId: agentCheckpointMessageID(req.RequestID, "user"),
			Mode:      mode,
		}
		data, err := proto.Marshal(user)
		if err != nil {
			return nil, err
		}
		userBlobID = addBlob("user_message", data)
	}

	stepBlobIDs := [][]byte{}
	if assistantText = strings.TrimSpace(assistantText); assistantText != "" {
		step := &agentv1.ConversationStep{
			Message: &agentv1.ConversationStep_AssistantMessage{
				AssistantMessage: &agentv1.AssistantMessage{Text: assistantText},
			},
		}
		data, err := proto.Marshal(step)
		if err != nil {
			return nil, err
		}
		if id := addBlob("assistant_step", data); len(id) > 0 {
			stepBlobIDs = append(stepBlobIDs, id)
		}
	}

	if len(userBlobID) > 0 || len(stepBlobIDs) > 0 {
		turn := &agentv1.AgentConversationTurnStructure{UserMessage: userBlobID, Steps: stepBlobIDs}
		if req.RequestID != "" {
			turn.RequestId = &req.RequestID
		}
		turnStructure := &agentv1.ConversationTurnStructure{
			Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
				AgentConversationTurn: turn,
			},
		}
		data, err := proto.Marshal(turnStructure)
		if err != nil {
			return nil, err
		}
		if id := addBlob("conversation_turn", data); len(id) > 0 {
			state.Turns = appendUniqueBytes(state.Turns, id)
		}
	}

	return payload, nil
}

func (g *Gateway) agentConversationState(requestID string) *agentv1.ConversationStateStructure {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if state := g.agentSessions[requestID]; state != nil && state.Conversation != nil {
		if cloned, ok := proto.Clone(state.Conversation).(*agentv1.ConversationStateStructure); ok {
			return cloned
		}
	}
	if state := g.readAgentConversationState(requestID); state != nil {
		return state
	}
	return nil
}

func (g *Gateway) restoreAgentConversationMessages(ctx context.Context, w io.Writer, state *agentv1.ConversationStateStructure, workspaceRoot string) ([]chatMessage, agentConversationRestoreStats) {
	stats := agentConversationRestoreStats{}
	if state == nil || len(state.GetTurns()) == 0 {
		return nil, stats
	}
	restoreCtx := ctx
	cancel := func() {}
	if restoreCtx == nil {
		restoreCtx = context.Background()
	}
	restoreCtx, cancel = context.WithTimeout(restoreCtx, agentKVFetchTimeout)
	defer cancel()

	messages := make([]chatMessage, 0, len(state.GetTurns())*2)
	for _, turnID := range state.GetTurns() {
		turnData, ok := g.resolveAgentKVBlob(restoreCtx, w, turnID)
		if !ok {
			stats.MissingBlobs++
			continue
		}
		var turn agentv1.ConversationTurnStructure
		if err := proto.Unmarshal(turnData, &turn); err != nil {
			log.Printf("[Gateway] Agent restore turn decode failed blob=%s error=%v", shortBlobID(turnID), err)
			continue
		}
		agentTurn := turn.GetAgentConversationTurn()
		if agentTurn == nil {
			continue
		}
		stats.Turns++
		if userText, ok := g.restoreAgentUserMessage(restoreCtx, w, agentTurn.GetUserMessage(), workspaceRoot); ok && strings.TrimSpace(userText) != "" {
			messages = append(messages, chatMessage{Role: "user", Content: userText})
			stats.Messages++
		} else if len(agentTurn.GetUserMessage()) > 0 {
			stats.MissingBlobs++
		}
		for _, stepID := range agentTurn.GetSteps() {
			stepText, ok := g.restoreAgentAssistantStep(restoreCtx, w, stepID)
			if !ok {
				stats.MissingBlobs++
				continue
			}
			if strings.TrimSpace(stepText) == "" {
				continue
			}
			messages = append(messages, chatMessage{Role: "assistant", Content: stepText})
			stats.Messages++
		}
	}
	return dedupeAdjacentChatMessages(messages), stats
}

func (g *Gateway) restoreAgentUserMessage(ctx context.Context, w io.Writer, blobID []byte, workspaceRoot string) (string, bool) {
	if len(blobID) == 0 {
		return "", false
	}
	data, ok := g.resolveAgentKVBlob(ctx, w, blobID)
	if !ok {
		return "", false
	}
	var user agentv1.UserMessage
	if err := proto.Unmarshal(data, &user); err != nil {
		log.Printf("[Gateway] Agent restore user message decode failed blob=%s error=%v", shortBlobID(blobID), err)
		return "", false
	}
	baseText := user.GetText()
	if baseText == "" {
		baseText = user.GetRichText()
	}
	contextText := g.selectedContextPrompt(user.GetSelectedContext(), workspaceRoot)
	if contextText != "" && baseText != "" {
		return contextText + "\n\n" + baseText, true
	}
	if contextText != "" {
		return contextText, true
	}
	return baseText, true
}

func (g *Gateway) restoreAgentAssistantStep(ctx context.Context, w io.Writer, blobID []byte) (string, bool) {
	if len(blobID) == 0 {
		return "", false
	}
	data, ok := g.resolveAgentKVBlob(ctx, w, blobID)
	if !ok {
		return "", false
	}
	var step agentv1.ConversationStep
	if err := proto.Unmarshal(data, &step); err != nil {
		log.Printf("[Gateway] Agent restore assistant step decode failed blob=%s error=%v", shortBlobID(blobID), err)
		return "", false
	}
	if assistant := step.GetAssistantMessage(); assistant != nil {
		return assistant.GetText(), true
	}
	return "", true
}

func (g *Gateway) resolveAgentKVBlob(ctx context.Context, w io.Writer, blobID []byte) ([]byte, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if data, ok := g.agentKVBlobData(blobID); ok {
		return data, true
	}
	if w == nil {
		return nil, false
	}
	id, ch, err := g.writeAgentKVGetBlobFrame(w, blobID)
	if err != nil {
		log.Printf("[Gateway] Agent KV get blob write failed blob=%s error=%v", shortBlobID(blobID), err)
		return nil, false
	}
	select {
	case data := <-ch:
		if len(data) == 0 {
			return nil, false
		}
		g.storeAgentKVBlob(agentKVBlob{Label: "client_blob", ID: blobID, Data: data})
		return data, true
	case <-ctx.Done():
		g.unregisterAgentKVGet(id)
		return nil, false
	}
}

func mergeAgentRestoredMessages(restored []chatMessage, current []chatMessage) []chatMessage {
	out := make([]chatMessage, 0, len(restored)+len(current))
	out = append(out, restored...)
	current = latestAgentUserMessages(current)
	for _, msg := range current {
		if strings.TrimSpace(msg.Content) == "" && msg.ToolResult == nil {
			continue
		}
		if chatMessagesContain(out, msg) {
			continue
		}
		out = append(out, msg)
	}
	return dedupeAdjacentChatMessages(out)
}

func latestAgentUserMessages(messages []chatMessage) []chatMessage {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.ToolResult == nil && msg.Role == "user" && strings.TrimSpace(msg.Content) != "" {
			return []chatMessage{msg}
		}
	}
	return nil
}

func dedupeAdjacentChatMessages(messages []chatMessage) []chatMessage {
	out := make([]chatMessage, 0, len(messages))
	for _, msg := range messages {
		if len(out) > 0 && chatMessagesEqual(out[len(out)-1], msg) {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func chatMessagesContain(messages []chatMessage, target chatMessage) bool {
	for _, msg := range messages {
		if chatMessagesEqual(msg, target) {
			return true
		}
	}
	return false
}

func chatMessagesEqual(a chatMessage, b chatMessage) bool {
	if a.Role != b.Role || strings.TrimSpace(a.Content) != strings.TrimSpace(b.Content) {
		return false
	}
	if a.ToolResult == nil || b.ToolResult == nil {
		return a.ToolResult == nil && b.ToolResult == nil
	}
	return a.ToolResult.ID == b.ToolResult.ID && a.ToolResult.Name == b.ToolResult.Name && a.ToolResult.Arguments == b.ToolResult.Arguments && a.ToolResult.Output == b.ToolResult.Output
}

func shortBlobID(blobID []byte) string {
	if len(blobID) == 0 {
		return ""
	}
	return hex.EncodeToString(blobID[:minInt(len(blobID), 8)])
}

func (g *Gateway) writeAgentKVSetBlobFrame(w io.Writer, blob agentKVBlob) error {
	if len(blob.ID) == 0 || len(blob.Data) == 0 {
		return nil
	}
	g.storeAgentKVBlob(blob)
	id := g.nextAgentKVMessageID()
	msg := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_KvServerMessage{
			KvServerMessage: &agentv1.KvServerMessage{
				Id: id,
				Message: &agentv1.KvServerMessage_SetBlobArgs{
					SetBlobArgs: &agentv1.SetBlobArgs{BlobId: blob.ID, BlobData: blob.Data},
				},
			},
		},
	}
	payload, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := w.Write(encodeFrame(payload)); err != nil {
		return err
	}
	log.Printf("[Gateway] Agent KV set blob sent id=%d label=%s blob=%s bytes=%d", id, blob.Label, hex.EncodeToString(blob.ID[:minInt(len(blob.ID), 8)]), len(blob.Data))
	return nil
}

func (g *Gateway) writeAgentKVGetBlobFrame(w io.Writer, blobID []byte) (uint32, <-chan []byte, error) {
	if len(blobID) == 0 {
		return 0, nil, fmt.Errorf("empty blob id")
	}
	id := g.nextAgentKVMessageID()
	ch := g.registerAgentKVGet(id)
	msg := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_KvServerMessage{
			KvServerMessage: &agentv1.KvServerMessage{
				Id: id,
				Message: &agentv1.KvServerMessage_GetBlobArgs{
					GetBlobArgs: &agentv1.GetBlobArgs{BlobId: blobID},
				},
			},
		},
	}
	payload, err := proto.Marshal(msg)
	if err != nil {
		g.unregisterAgentKVGet(id)
		return 0, nil, err
	}
	if _, err := w.Write(encodeFrame(payload)); err != nil {
		g.unregisterAgentKVGet(id)
		return 0, nil, err
	}
	log.Printf("[Gateway] Agent KV get blob sent id=%d blob=%s", id, hex.EncodeToString(blobID[:minInt(len(blobID), 8)]))
	return id, ch, nil
}

func (g *Gateway) handleAgentKVClientMessage(msg *agentv1.KvClientMessage) {
	if msg == nil {
		return
	}
	switch m := msg.GetMessage().(type) {
	case *agentv1.KvClientMessage_SetBlobResult:
		if m.SetBlobResult != nil && m.SetBlobResult.GetError() != nil {
			log.Printf("[Gateway] Agent KV set blob result id=%d error=%s", msg.GetId(), protojson.Format(m.SetBlobResult.GetError()))
			return
		}
		log.Printf("[Gateway] Agent KV set blob result id=%d ok", msg.GetId())
	case *agentv1.KvClientMessage_GetBlobResult:
		size := 0
		var data []byte
		if m.GetBlobResult != nil {
			data = m.GetBlobResult.GetBlobData()
			size = len(data)
		}
		g.completeAgentKVGet(msg.GetId(), data)
		log.Printf("[Gateway] Agent KV get blob result id=%d bytes=%d", msg.GetId(), size)
	default:
		log.Printf("[Gateway] Agent KV client message id=%d kind=unknown", msg.GetId())
	}
}

func (g *Gateway) nextAgentKVMessageID() uint32 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.agentKVSeq++
	return g.agentKVSeq
}

func (g *Gateway) registerAgentKVGet(id uint32) chan []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.agentKVGets == nil {
		g.agentKVGets = make(map[uint32]chan []byte)
	}
	ch := make(chan []byte, 1)
	g.agentKVGets[id] = ch
	return ch
}

func (g *Gateway) unregisterAgentKVGet(id uint32) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.agentKVGets, id)
}

func (g *Gateway) completeAgentKVGet(id uint32, data []byte) {
	g.mu.Lock()
	ch := g.agentKVGets[id]
	delete(g.agentKVGets, id)
	g.mu.Unlock()
	if ch == nil {
		return
	}
	copied := append([]byte(nil), data...)
	ch <- copied
	close(ch)
}

func (g *Gateway) storeAgentKVBlob(blob agentKVBlob) {
	if len(blob.ID) == 0 || len(blob.Data) == 0 {
		return
	}
	key := agentKVBlobKey(blob.ID)
	data := append([]byte(nil), blob.Data...)
	g.mu.Lock()
	if g.agentKVBlobs == nil {
		g.agentKVBlobs = make(map[string]agentStoredKVBlob)
	}
	g.agentKVBlobs[key] = agentStoredKVBlob{Label: blob.Label, Data: data, UpdatedAt: time.Now()}
	if len(g.agentKVBlobs) > agentKVBlobMaxEntries {
		var oldestKey string
		var oldest time.Time
		for key, stored := range g.agentKVBlobs {
			if oldestKey == "" || stored.UpdatedAt.Before(oldest) {
				oldestKey = key
				oldest = stored.UpdatedAt
			}
		}
		if oldestKey != "" {
			delete(g.agentKVBlobs, oldestKey)
		}
	}
	g.mu.Unlock()
	g.persistAgentKVBlob(blob)
}

func (g *Gateway) agentKVBlobData(blobID []byte) ([]byte, bool) {
	if len(blobID) == 0 {
		return nil, false
	}
	key := agentKVBlobKey(blobID)
	g.mu.RLock()
	stored, ok := g.agentKVBlobs[key]
	g.mu.RUnlock()
	if !ok || len(stored.Data) == 0 {
		if data, ok := g.readAgentKVBlobFromDisk(blobID); ok {
			g.storeAgentKVBlob(agentKVBlob{Label: "disk_blob", ID: blobID, Data: data})
			return data, true
		}
		return nil, false
	}
	return append([]byte(nil), stored.Data...), true
}

func agentKVBlobKey(blobID []byte) string {
	return hex.EncodeToString(blobID)
}

func buildAgentConversationCheckpointFrameFromState(state *agentv1.ConversationStateStructure) ([]byte, error) {
	payload, err := proto.Marshal(&agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ConversationCheckpointUpdate{
			ConversationCheckpointUpdate: state,
		},
	})
	if err != nil {
		return nil, err
	}
	return encodeFrame(payload), nil
}

func agentKVBlobID(requestID string, label string, data []byte) []byte {
	h := sha256.New()
	_, _ = h.Write([]byte("cursor-assistant-byok-agent-kv-v1"))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(requestID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(label))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(data)
	sum := h.Sum(nil)
	out := make([]byte, len(sum))
	copy(out, sum)
	return out
}

func agentCheckpointMessageID(requestID string, suffix string) string {
	requestID = strings.TrimSpace(requestID)
	suffix = strings.TrimSpace(suffix)
	if requestID == "" {
		if suffix == "" {
			return "byok"
		}
		return "byok-" + suffix
	}
	if suffix == "" {
		return requestID
	}
	return requestID + "-" + suffix
}

func firstMessageContentByRole(messages []chatMessage, role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	for _, msg := range messages {
		if strings.ToLower(strings.TrimSpace(msg.Role)) == role && strings.TrimSpace(msg.Content) != "" {
			return msg.Content
		}
	}
	return ""
}

func agentCheckpointReadPaths(toolName string, args map[string]any, workspaceRoot string) []string {
	switch normalizeAgentToolName(toolName) {
	case "Read", "ReadLints":
	default:
		return nil
	}
	candidates := []string{}
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if resolved, err := resolveToolPath(path, workspaceRoot); err == nil && resolved != "" {
			candidates = append(candidates, resolved)
			return
		}
		candidates = append(candidates, path)
	}
	add(argString(args, "path"))
	add(argString(args, "file"))
	if raw := args["paths"]; raw != nil {
		switch values := raw.(type) {
		case []any:
			for _, value := range values {
				add(fmt.Sprint(value))
			}
		case []string:
			for _, value := range values {
				add(value)
			}
		case string:
			for _, value := range strings.Split(values, ",") {
				add(value)
			}
		}
	}
	return dedupeStrings(candidates)
}

func appendUniqueStrings(base []string, values ...string) []string {
	out := append([]string{}, base...)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		exists := false
		for _, current := range out {
			if current == value {
				exists = true
				break
			}
		}
		if !exists {
			out = append(out, value)
		}
	}
	return out
}

func appendUniqueBytes(base [][]byte, values ...[]byte) [][]byte {
	out := append([][]byte{}, base...)
	for _, value := range values {
		if len(value) == 0 {
			continue
		}
		exists := false
		for _, current := range out {
			if bytes.Equal(current, value) {
				exists = true
				break
			}
		}
		if !exists {
			copied := make([]byte, len(value))
			copy(copied, value)
			out = append(out, copied)
		}
	}
	return out
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func buildAgentConversationCheckpointFrame(usedTokens int, maxTokens int, workspaceRoot string, modes ...cursorAgentMode) ([]byte, error) {
	if usedTokens < 0 {
		usedTokens = 0
	}
	if maxTokens <= 0 {
		maxTokens = localContextDefaultTokenLimit
	}
	agentMode := cursorAgentModeAgent
	if len(modes) > 0 {
		agentMode = defaultCursorAgentMode(modes[0])
	}
	mode := agentMode.protoAgentMode()
	state := &agentv1.ConversationStateStructure{
		TokenDetails: &agentv1.ConversationTokenDetails{
			UsedTokens: uint32(usedTokens),
			MaxTokens:  uint32(maxTokens),
		},
		Mode: &mode,
	}
	if workspaceRoot = normalizeWorkspaceRoot(workspaceRoot); workspaceRoot != "" {
		state.PreviousWorkspaceUris = []string{workspaceURI(workspaceRoot)}
	}
	payload, err := proto.Marshal(&agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ConversationCheckpointUpdate{
			ConversationCheckpointUpdate: state,
		},
	})
	if err != nil {
		return nil, err
	}
	return encodeFrame(payload), nil
}

func writeAgentInteractionUpdateFrame(w io.Writer, update *agentv1.InteractionUpdate) error {
	msg := &agentv1.AgentServerMessage{Message: &agentv1.AgentServerMessage_InteractionUpdate{InteractionUpdate: update}}
	payload, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(encodeFrame(payload))
	return err
}

func writeAgentEndStreamFrame(w io.Writer) error {
	payload, err := proto.Marshal(&agentv1.AgentServerMessage{})
	if err != nil {
		return err
	}
	if _, err := w.Write(encodeFrame(payload)); err != nil {
		return err
	}
	_, err = w.Write(encodeFrameWithFlag(0x02, []byte("{}")))
	return err
}

func writeAgentExecServerFrame(w io.Writer, msg *agentv1.ExecServerMessage) error {
	payload, err := proto.Marshal(&agentv1.AgentServerMessage{Message: &agentv1.AgentServerMessage_ExecServerMessage{ExecServerMessage: msg}})
	if err != nil {
		return err
	}
	_, err = w.Write(encodeFrame(payload))
	return err
}

func writeAgentHeartbeatFrame(w io.Writer) error {
	return writeAgentInteractionUpdateFrame(w, &agentv1.InteractionUpdate{Message: &agentv1.InteractionUpdate_Heartbeat{Heartbeat: &agentv1.HeartbeatUpdate{}}})
}

type heartbeatWriter struct {
	io.Writer
	mu        sync.Mutex
	lastWrite atomic.Int64
}

func newHeartbeatWriter(writer io.Writer) *heartbeatWriter {
	out := &heartbeatWriter{Writer: writer}
	out.touch()
	return out
}

func (w *heartbeatWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.Writer.Write(data)
	if n > 0 {
		w.touch()
	}
	return n, err
}

func (w *heartbeatWriter) touch() {
	w.lastWrite.Store(time.Now().UnixNano())
}

func (w *heartbeatWriter) lastWriteTime() time.Time {
	ns := w.lastWrite.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func (g *Gateway) startAgentHeartbeat(ctxDone <-chan struct{}, writer *heartbeatWriter) chan struct{} {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if time.Since(writer.lastWriteTime()) < 8*time.Second {
					continue
				}
				if err := writeAgentHeartbeatFrame(writer); err != nil {
					log.Printf("[Gateway] Agent heartbeat failed: %v", err)
					return
				}
			case <-ctxDone:
				return
			case <-stop:
				return
			}
		}
	}()
	return stop
}

// buildCursorConversationID creates a stable conversation ID from explicit Cursor conversation_id
// Uses hash of (workspace | mode | conversation_id) to create internal ID
func buildCursorConversationID(explicitConvID string, workspace string, mode cursorAgentMode) string {
	if explicitConvID == "" {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(workspace))
	h.Write([]byte("|"))
	h.Write([]byte(mode))
	h.Write([]byte("|"))
	h.Write([]byte(explicitConvID))
	hash := hex.EncodeToString(h.Sum(nil))
	return "cursor-conv-" + hash[:32]
}

// extractConversationIDFromState extracts a stable conversation ID from ConversationStateStructure
// Strategy: Use root prompt messages hash + workspace + mode as stable anchor
// Only used when no explicit conversation_id is provided
// Returns empty string if no stable anchor is available (triggers requestID fallback)
func extractConversationIDFromState(conversation *agentv1.ConversationStateStructure, workspace string, mode cursorAgentMode) string {
	if conversation == nil {
		return ""
	}

	// Use root prompt messages as stable anchor
	// These represent the initial conversation context and remain stable across turns
	rootRefs := conversation.GetRootPromptMessagesJson()
	if len(rootRefs) > 0 {
		// Hash root prompt messages to create stable ID
		h := sha256.New()
		for _, ref := range rootRefs {
			h.Write(ref) // ref is []byte
		}
		h.Write([]byte(workspace))
		h.Write([]byte(mode))
		hash := hex.EncodeToString(h.Sum(nil))
		return "conv-root-" + hash[:32]
	}

	// No stable anchor available - return empty to trigger requestID fallback
	// Do NOT use workspace+mode as fallback - that would incorrectly merge different conversations
	return ""
}

// agentConversationID returns a stable conversation ID for the agent session
// Priority strategy:
// 1. Use explicit CursorConversationID from state (from AgentRunRequest.conversation_id or ConversationStateStructure anchor)
// 2. Use explicit CursorConversationID from chatReq
// 3. Fallback to requestID (allows first-turn bootstrap, each request creates separate conversation)
func agentConversationID(requestID string, chatReq unifiedChatRequest, state *agentSessionState) string {
	// Priority 1: Use explicit Cursor conversation ID from state
	if state != nil && state.CursorConversationID != "" {
		return state.CursorConversationID
	}

	// Priority 2: Use explicit Cursor conversation ID from chatReq
	if chatReq.CursorConversationID != "" {
		return chatReq.CursorConversationID
	}

	// Priority 3: Fallback to requestID
	// Each request without explicit conversation ID creates a separate conversation
	return requestID
}

// createConversationAndTurn creates conversation and turn records in the database
// Returns the turn ID for later use (e.g., associating TokenDetails)
func (g *Gateway) createConversationAndTurn(requestID string, chatReq unifiedChatRequest, state *agentSessionState) int64 {
	if g.db == nil {
		return 0
	}

	// Get stable conversation ID
	conversationID := agentConversationID(requestID, chatReq, state)

	// Get or create conversation (avoid duplicate creation errors)
	conv, err := g.db.GetOrCreateConversation(conversationID, chatReq.WorkspaceRoot, chatReq.ModelName, string(chatReq.AgentMode))
	if err != nil {
		log.Printf("[Gateway] Failed to get or create conversation: %v", err)
		return 0
	}

	// Calculate next turn_seq based on existing turns in DB (starting from 1)
	nextTurnSeq := 1
	if existingTurns, err := g.db.GetTurnsByConversation(conversationID); err == nil && len(existingTurns) > 0 {
		// Find max turn_seq and increment
		maxSeq := 0
		for _, turn := range existingTurns {
			if turn.TurnSeq > maxSeq {
				maxSeq = turn.TurnSeq
			}
		}
		nextTurnSeq = maxSeq + 1
	}

	// Create turn
	turn := &database.Turn{
		ConversationID: conversationID,
		TurnSeq:        nextTurnSeq,
		RequestID:      requestID,
		ModelName:      chatReq.ModelName,
		ThinkingEffort: chatReq.ThinkingEffort,
		Status:         "running",
	}

	if err := g.db.CreateTurn(turn); err != nil {
		log.Printf("[Gateway] Failed to create turn: %v", err)
		return 0
	}

	// Create messages
	for i, msg := range chatReq.Messages {
		dbMsg := &database.Message{
			TurnID:         turn.ID,
			ConversationID: conversationID,
			MessageSeq:     i,
			Role:           msg.Role,
			Content:        msg.Content,
		}

		if err := g.db.CreateMessage(dbMsg); err != nil {
			log.Printf("[Gateway] Failed to create message %d: %v", i, err)
		}
	}

	log.Printf("[Gateway] Created conversation and turn: conv=%s turn=%d messages=%d", conv.ID, turn.ID, len(chatReq.Messages))
	return turn.ID
}

// saveTokenDetails saves token usage details to the database
func (g *Gateway) saveTokenDetails(requestID string, turnID int64, promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int) {
	if g.db == nil {
		return
	}

	tokenDetail := &database.TokenDetails{
		TurnID:           turnID,
		ConversationID:   requestID,
		ProviderCallSeq:  1, // This should be incremented for multiple provider calls
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		IsEstimated:      false,
	}

	if err := g.db.CreateTokenDetails(tokenDetail); err != nil {
		log.Printf("[Gateway] Failed to save token details: %v", err)
		return
	}

	log.Printf("[Gateway] Saved token details: conv=%s turn=%d prompt=%d completion=%d cacheRead=%d cacheWrite=%d",
		requestID, turnID, promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens)
}
