package relay

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"

	aiserverv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/aiserver/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	localContextDefaultTokenLimit = 200000
	localContextSystemTokens      = 3200
	localContextToolTokens        = 14500
)

type localContextTokenDetail struct {
	path        string
	text        string
	startLine   int32
	lineCount   int32
	selection   bool
	lastMessage bool
}

func (g *Gateway) buildCountTokensPayload(req *http.Request) []byte {
	countReq := &aiserverv1.CountTokensRequest{}
	if err := parseLocalProtoMessage(req, countReq); err != nil {
		log.Printf("[Gateway] CountTokens decode warning: %v", err)
	}

	total := 0
	tokenDetails := []*aiserverv1.ContextItemTokenDetail{}
	for _, item := range countReq.GetContextItems() {
		detail := contextItemTokenDetail(item)
		tokens := estimateTokens(detail.text)
		if tokens == 0 && detail.text != "" {
			tokens = 1
		}
		total += tokens
		if detail.path != "" || detail.lineCount > 0 {
			tokenDetails = append(tokenDetails, &aiserverv1.ContextItemTokenDetail{
				RelativeWorkspacePath: detail.path,
				Count:                 clampInt32(tokens),
				LineCount:             detail.lineCount,
			})
		}
	}

	resp := &aiserverv1.CountTokensResponse{
		Count:        clampInt32(total),
		TokenDetails: tokenDetails,
	}
	payload, err := proto.Marshal(resp)
	if err != nil {
		log.Printf("[Gateway] CountTokens encode failed: %v", err)
		return nil
	}
	return payload
}

func (g *Gateway) buildPromptDryRunPayload(req *http.Request) []byte {
	chatReq := &aiserverv1.StreamUnifiedChatRequest{}
	if err := parseLocalProtoMessage(req, chatReq); err != nil {
		log.Printf("[Gateway] GetPromptDryRun decode warning: %v", err)
	}

	limit := g.promptDryRunTokenLimit(chatReq)
	userTokens, conversationTokens, details := g.promptDryRunTokenDetails(chatReq)
	fullTokens := localContextSystemTokens + localContextToolTokens + conversationTokens
	overflow := fullTokens > limit

	resp := &aiserverv1.GetPromptDryRunResponse{
		UserMessageTokenLimit:      clampInt32(limit),
		UserMessageTokenCount:      promptDryRunTokenCount(userTokens, userTokens > limit),
		FullConversationTokenCount: promptDryRunTokenCount(fullTokens, overflow),
		BarFraction:                promptDryRunBarFraction(fullTokens, limit),
		DidBarOverflow:             overflow,
		ShouldShowNewChatHint:      overflow,
	}
	for _, detail := range details {
		tokens := estimateTokens(detail.text)
		if tokens <= 0 || detail.path == "" {
			continue
		}
		startLine := detail.startLine
		if startLine <= 0 {
			startLine = 1
		}
		lineCount := detail.lineCount
		if lineCount <= 0 {
			lineCount = int32(countLines(detail.text))
		}
		endLine := startLine + maxInt32(lineCount, 1) - 1
		intent := aiserverv1.CodeChunkContextInclusionInfo_INTENT_FILE
		intentV2 := aiserverv1.CodeChunkContextInclusionInfoV2_INTENT_FILE
		if detail.selection {
			intent = aiserverv1.CodeChunkContextInclusionInfo_INTENT_SELECTION
			intentV2 = aiserverv1.CodeChunkContextInclusionInfoV2_INTENT_SELECTION
		}
		resp.CodeChunks = append(resp.CodeChunks, &aiserverv1.CodeChunkContextInclusionInfo{
			RelativeWorkspacePath:      detail.path,
			StartLineNumber:            startLine,
			EndLineNumberInclusive:     endLine,
			InclusionType:              aiserverv1.CodeChunkContextInclusionInfo_INCLUSION_TYPE_FULL,
			FullFileTokenCount:         clampInt32(tokens),
			PromptTokenCount:           clampInt32(tokens),
			Intent:                     intent,
			ChunkIsFromLastUserMessage: detail.lastMessage,
		})
		resp.CodeChunksV2 = append(resp.CodeChunksV2, &aiserverv1.CodeChunkContextInclusionInfoV2{
			RelativeWorkspacePath:  detail.path,
			StartLineNumber:        startLine,
			EndLineNumberInclusive: endLine,
			Intent:                 intentV2,
			InclusionType:          aiserverv1.CodeChunkContextInclusionInfoV2_INCLUSION_TYPE_FULL,
		})
	}

	payload, err := proto.Marshal(resp)
	if err != nil {
		log.Printf("[Gateway] GetPromptDryRun encode failed: %v", err)
		return nil
	}
	return payload
}

func parseLocalProtoMessage(req *http.Request, msg proto.Message) error {
	if req == nil || req.Body == nil {
		return nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) == 0 {
		return nil
	}
	payload, err := firstPayloadFromHeaders(body, req.Header)
	if err != nil {
		return err
	}
	if looksJSONPayload(payload, req.Header.Get("Content-Type")) {
		return protojson.Unmarshal(payload, msg)
	}
	return proto.Unmarshal(payload, msg)
}

func (g *Gateway) promptDryRunTokenLimit(req *aiserverv1.StreamUnifiedChatRequest) int {
	modelName := ""
	if req != nil && req.GetModelDetails() != nil {
		modelName = req.GetModelDetails().GetModelName()
	}
	if modelName != "" {
		if adapter := g.findAdapterByCursorName(modelName); adapter != nil && adapter.ContextWindow > 0 {
			return adapter.ContextWindow
		}
		if strings.HasSuffix(modelName, "-max") {
			if adapter := g.findAdapterByCursorName(strings.TrimSuffix(modelName, "-max")); adapter != nil && adapter.ContextWindow > 0 {
				return adapter.ContextWindow
			}
		}
	}
	for _, adapter := range g.byokModels() {
		if adapter != nil && adapter.ContextWindow > 0 {
			return adapter.ContextWindow
		}
	}
	return localContextDefaultTokenLimit
}

func (g *Gateway) promptDryRunTokenDetails(req *aiserverv1.StreamUnifiedChatRequest) (int, int, []localContextTokenDetail) {
	if req == nil {
		return 0, 0, nil
	}
	details := []localContextTokenDetail{}
	total := 0
	userTokens := 0
	conversation := req.GetConversation()
	lastHuman := lastHumanMessageIndex(conversation)
	for i, msg := range conversation {
		msgDetails := conversationMessageTokenDetails(msg, i == lastHuman)
		msgTokens := tokenDetailsTotal(msgDetails)
		total += msgTokens
		if i == lastHuman {
			userTokens += msgTokens
		}
		details = append(details, msgDetails...)
	}
	if project := req.GetProjectContext(); project != nil {
		projectDetails := conversationMessageTokenDetails(project, false)
		total += tokenDetailsTotal(projectDetails)
		details = append(details, projectDetails...)
	}
	if current := req.GetCurrentFile(); current != nil {
		detail := currentFileTokenDetail(current)
		if detail.text == "" && current.GetRelyOnFilesync() {
			if cached, ok := g.lookupFileSyncContent("", current.GetRelativeWorkspacePath()); ok {
				detail.text = cached
				detail.lineCount = int32(countLines(cached))
			}
		}
		if detail.text != "" {
			total += estimateTokens(detail.text)
			userTokens += estimateTokens(detail.text)
			details = append(details, detail)
		}
	}
	if explicit := req.GetExplicitContext(); explicit != nil {
		for _, text := range []string{explicit.GetContext(), explicit.GetRepoContext(), explicit.GetModeSpecificContext()} {
			total += estimateTokens(text)
			userTokens += estimateTokens(text)
		}
		for _, rule := range explicit.GetRules() {
			total += estimateTokens(cursorRuleText(rule))
		}
		for _, instruction := range explicit.GetMcpInstructions() {
			total += estimateTokens(instruction.String())
		}
	}
	for _, ranked := range req.GetAdditionalRankedContext() {
		if ranked.GetContext() == nil {
			continue
		}
		ctx := ranked.GetContext()
		detail := localContextTokenDetail{
			path:      ctx.GetRelativeWorkspacePath(),
			text:      firstNonEmpty(ctx.GetContents(), codeBlockText(ctx.GetCodeBlock())),
			startLine: 1,
		}
		if detail.text == "" {
			continue
		}
		detail.lineCount = int32(countLines(detail.text))
		total += estimateTokens(detail.text)
		details = append(details, detail)
	}
	return userTokens, total, details
}

func conversationMessageTokenDetails(msg *aiserverv1.ConversationMessage, lastMessage bool) []localContextTokenDetail {
	if msg == nil {
		return nil
	}
	details := []localContextTokenDetail{}
	messageText := firstNonEmpty(msg.GetRichText(), msg.GetText())
	if messageText != "" {
		details = append(details, localContextTokenDetail{text: messageText, lastMessage: lastMessage})
	}
	for _, chunk := range msg.GetAttachedCodeChunks() {
		details = append(details, conversationCodeChunkTokenDetail(chunk, lastMessage))
	}
	for _, chunk := range msg.GetCodebaseContextChunks() {
		details = append(details, codeBlockTokenDetail(chunk, lastMessage))
	}
	for _, chunk := range msg.GetRecentlyViewedFiles() {
		details = append(details, conversationCodeChunkTokenDetail(chunk, false))
	}
	for _, piece := range msg.GetContextPieces() {
		if piece.GetContent() == "" {
			continue
		}
		details = append(details, localContextTokenDetail{
			path:        piece.GetRelativeWorkspacePath(),
			text:        piece.GetContent(),
			startLine:   1,
			lineCount:   int32(countLines(piece.GetContent())),
			lastMessage: lastMessage,
		})
	}
	for _, rule := range msg.GetCursorRules() {
		if text := cursorRuleText(rule); text != "" {
			details = append(details, localContextTokenDetail{text: text, lastMessage: lastMessage})
		}
	}
	return details
}

func conversationCodeChunkTokenDetail(chunk *aiserverv1.ConversationMessage_CodeChunk, lastMessage bool) localContextTokenDetail {
	if chunk == nil {
		return localContextTokenDetail{}
	}
	text := strings.Join(chunk.GetLines(), "\n")
	intent := chunk.GetIntent()
	return localContextTokenDetail{
		path:        chunk.GetRelativeWorkspacePath(),
		text:        text,
		startLine:   chunk.GetStartLineNumber(),
		lineCount:   int32(len(chunk.GetLines())),
		selection:   intent == aiserverv1.ConversationMessage_CodeChunk_INTENT_CODE_SELECTION,
		lastMessage: lastMessage,
	}
}

func codeBlockTokenDetail(block *aiserverv1.CodeBlock, lastMessage bool) localContextTokenDetail {
	if block == nil {
		return localContextTokenDetail{}
	}
	text := firstNonEmpty(block.GetOverrideContents(), block.GetContents(), block.GetFileContents(), block.GetOriginalContents())
	return localContextTokenDetail{
		path:        block.GetRelativeWorkspacePath(),
		text:        text,
		startLine:   1,
		lineCount:   int32(countLines(text)),
		lastMessage: lastMessage,
	}
}

func currentFileTokenDetail(file *aiserverv1.CurrentFileInfo) localContextTokenDetail {
	if file == nil {
		return localContextTokenDetail{}
	}
	startLine := file.GetContentsStartAtLine()
	if startLine <= 0 {
		startLine = 1
	}
	lineCount := file.GetTotalNumberOfLines()
	if lineCount <= 0 {
		lineCount = int32(countLines(file.GetContents()))
	}
	return localContextTokenDetail{
		path:        file.GetRelativeWorkspacePath(),
		text:        file.GetContents(),
		startLine:   startLine,
		lineCount:   lineCount,
		lastMessage: true,
	}
}

func contextItemTokenDetail(item *aiserverv1.ContextItem) localContextTokenDetail {
	if item == nil {
		return localContextTokenDetail{}
	}
	if chunk := item.GetFileChunk(); chunk != nil {
		text := chunk.GetChunkContents()
		return localContextTokenDetail{path: chunk.GetRelativeWorkspacePath(), text: text, startLine: chunk.GetStartLineNumber(), lineCount: int32(countLines(text))}
	}
	if chunk := item.GetOutlineChunk(); chunk != nil {
		text := chunk.GetContents()
		return localContextTokenDetail{path: chunk.GetRelativeWorkspacePath(), text: text, startLine: 1, lineCount: int32(countLines(text))}
	}
	if selection := item.GetCmdKSelection(); selection != nil {
		return localContextTokenDetail{text: strings.Join(selection.GetLines(), "\n"), startLine: selection.GetStartLineNumber(), lineCount: int32(len(selection.GetLines())), selection: true}
	}
	if immediate := item.GetCmdKImmediateContext(); immediate != nil {
		lines := []string{}
		for _, line := range immediate.GetLines() {
			lines = append(lines, line.GetLine())
		}
		return localContextTokenDetail{path: immediate.GetRelativeWorkspacePath(), text: strings.Join(lines, "\n"), startLine: firstImmediateLineNumber(immediate.GetLines()), lineCount: int32(len(lines))}
	}
	if sparse := item.GetSparseFileChunk(); sparse != nil {
		lines := []string{}
		for _, line := range sparse.GetLines() {
			lines = append(lines, line.GetLine())
		}
		return localContextTokenDetail{path: sparse.GetRelativeWorkspacePath(), text: strings.Join(lines, "\n"), startLine: firstSparseLineNumber(sparse.GetLines()), lineCount: int32(len(lines))}
	}
	if custom := item.GetCustomInstructions(); custom != nil {
		return localContextTokenDetail{text: custom.GetInstructions()}
	}
	if doc := item.GetDocumentationChunk(); doc != nil {
		return localContextTokenDetail{path: doc.GetDocName(), text: doc.GetDocumentationChunk(), lineCount: int32(countLines(doc.GetDocumentationChunk()))}
	}
	if history := item.GetChatHistory(); history != nil {
		text := strings.TrimSpace(history.GetUserMessage() + "\n" + history.GetAssistantResponse() + "\n" + chatHistoryText(history.GetChatHistory()))
		return localContextTokenDetail{text: text, lineCount: int32(countLines(text))}
	}
	if rule := item.GetProjectRule(); rule != nil {
		return localContextTokenDetail{path: rule.GetFullPath(), text: cursorRuleText(rule), lineCount: int32(countLines(rule.GetBody()))}
	}
	payload, err := protojson.Marshal(item)
	if err != nil {
		return localContextTokenDetail{}
	}
	return localContextTokenDetail{text: string(payload), lineCount: int32(countLines(string(payload)))}
}

func promptDryRunTokenCount(tokens int, overLimit bool) *aiserverv1.GetPromptDryRunResponse_TokenCount {
	n := clampInt32(tokens)
	return &aiserverv1.GetPromptDryRunResponse_TokenCount{
		IsOverTokenLimit: overLimit,
		NumTokens:        &n,
	}
}

func promptDryRunBarFraction(tokens int, limit int) float32 {
	if limit <= 0 || tokens <= 0 {
		return 0
	}
	fraction := float32(tokens) / float32(limit)
	if fraction > 1 {
		return 1
	}
	return fraction
}

func tokenDetailsTotal(details []localContextTokenDetail) int {
	total := 0
	for _, detail := range details {
		total += estimateTokens(detail.text)
	}
	return total
}

func lastHumanMessageIndex(messages []*aiserverv1.ConversationMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i] != nil && messages[i].GetType() == aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN {
			return i
		}
	}
	if len(messages) > 0 {
		return len(messages) - 1
	}
	return -1
}

func codeBlockText(block *aiserverv1.CodeBlock) string {
	if block == nil {
		return ""
	}
	return firstNonEmpty(block.GetOverrideContents(), block.GetContents(), block.GetFileContents(), block.GetOriginalContents())
}

func cursorRuleText(rule *aiserverv1.CursorRule) string {
	if rule == nil {
		return ""
	}
	return strings.TrimSpace(strings.Join([]string{rule.GetName(), rule.GetDescription(), rule.GetBody()}, "\n"))
}

func chatHistoryText(history *aiserverv1.ContextItem_ChatHistory) string {
	if history == nil {
		return ""
	}
	return strings.TrimSpace(history.GetUserMessage() + "\n" + history.GetAssistantResponse() + "\n" + chatHistoryText(history.GetChatHistory()))
}

func firstImmediateLineNumber(lines []*aiserverv1.ContextItem_CmdKImmediateContext_Line) int32 {
	for _, line := range lines {
		if line != nil && line.GetLineNumber() > 0 {
			return line.GetLineNumber()
		}
	}
	return 1
}

func firstSparseLineNumber(lines []*aiserverv1.ContextItem_SparseFileChunk_Line) int32 {
	for _, line := range lines {
		if line != nil && line.GetLineNumber() > 0 {
			return line.GetLineNumber()
		}
	}
	return 1
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func maxInt32(a int32, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func clampInt32(value int) int32 {
	if value > 2147483647 {
		return 2147483647
	}
	if value < -2147483648 {
		return -2147483648
	}
	return int32(value)
}
