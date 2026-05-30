package relay

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	agentv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/agent/v1"
)

var byokChatMethods = map[string]bool{
	"/aiserver.v1.ChatService/StreamUnifiedChat":                    true,
	"/aiserver.v1.ChatService/StreamUnifiedChatWithTools":           true,
	"/aiserver.v1.ChatService/StreamUnifiedChatWithToolsIdempotent": true,
}

// InterceptRequest is called by the MITM layer before forwarding to Cursor.
// It handles local Cursor RPCs before they reach the official upstream.
func (g *Gateway) InterceptRequest(req *http.Request) (*http.Response, bool) {
	if req == nil || req.URL == nil {
		return nil, false
	}
	g.beginHTTPRequest(req)
	if resp, handled := g.tryHandleLocalRPC(req); handled {
		return resp, true
	}
	if resp, handled := g.tryHandleAgentBidi(req); handled {
		return resp, true
	}
	if !byokChatMethods[req.URL.Path] {
		return nil, false
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		g.completeHTTPRequest(req, http.StatusBadRequest, "byok/local", true, true, "", "failed to read request")
		return textHTTPResponse(req, http.StatusBadRequest, "failed to read request"), true
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))

	parsed, err := parseUnifiedChatRequest(body, req.Header.Get("Content-Type"), req.URL.Path)
	if err != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		return nil, false
	}

	adapter := g.findAdapterByCursorName(parsed.ModelName)
	if adapter == nil && !strings.HasPrefix(parsed.ModelName, "byok/") {
		req.Body = io.NopCloser(bytes.NewReader(body))
		return nil, false
	}
	if adapter == nil {
		g.emit(Event{Type: EventBYOKFailure, Model: parsed.ModelName, Error: "BYOK model adapter not found"})
		g.completeHTTPRequest(req, http.StatusNotFound, "byok/local", true, true, parsed.ModelName, "BYOK model adapter not found")
		return textHTTPResponse(req, http.StatusNotFound, "BYOK model adapter not found"), true
	}

	resp := g.streamBYOKChatHTTPResponse(req, parsed, adapter)
	errText := ""
	if resp.StatusCode >= 400 {
		errText = resp.Status
	}
	route := "byok/local"
	if resp.Header.Get("X-Cursor-Assistant-Cache") == "hit" {
		route = "byok/cache"
	}
	g.completeHTTPRequest(req, resp.StatusCode, route, true, true, parsed.ModelName, errText)
	return resp, true
}

func isCursorRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	host := strings.ToLower(req.Host)
	if host == "" {
		host = strings.ToLower(req.URL.Host)
	}
	return strings.Contains(host, "cursor.sh") || strings.Contains(host, "cursor.com") || strings.HasPrefix(req.URL.Path, "/aiserver.") || strings.HasPrefix(req.URL.Path, "/agent.") || strings.HasPrefix(req.URL.Path, "/auth/")
}

func textHTTPResponse(req *http.Request, status int, text string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:        http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
		Body:          io.NopCloser(strings.NewReader(text)),
		ContentLength: int64(len(text)),
		Request:       req,
	}
}

func (g *Gateway) streamBYOKChatHTTPResponse(r *http.Request, chatReq unifiedChatRequest, adapter *ModelAdapter) *http.Response {
	reader, writer := io.Pipe()
	responseWriter := newPipeResponseWriter(writer)
	done := make(chan error, 1)

	go func() {
		err := g.streamBYOKChat(responseWriter, r, chatReq, adapter)
		if err != nil {
			log.Printf("[Gateway] BYOK chat error: %v", err)
			if !responseWriter.started() {
				_ = writer.Close()
				done <- err
				return
			}
			_ = writer.CloseWithError(err)
			done <- err
			return
		}

		if !responseWriter.started() {
			responseWriter.WriteHeader(http.StatusNoContent)
		}
		_ = writer.Close()
		done <- nil
	}()

	select {
	case <-responseWriter.ready:
		return responseWriter.response(r, reader)
	case err := <-done:
		if err != nil {
			_ = reader.Close()
			return textHTTPResponse(r, http.StatusBadGateway, err.Error())
		}
		return responseWriter.response(r, reader)
	}
}

type pipeResponseWriter struct {
	mu        sync.Mutex
	header    http.Header
	writer    *io.PipeWriter
	status    int
	ready     chan struct{}
	readyOnce sync.Once
}

func newPipeResponseWriter(writer *io.PipeWriter) *pipeResponseWriter {
	return &pipeResponseWriter{
		header: http.Header{},
		writer: writer,
		ready:  make(chan struct{}),
	}
}

func (w *pipeResponseWriter) Header() http.Header {
	return w.header
}

func (w *pipeResponseWriter) WriteHeader(status int) {
	w.mu.Lock()
	if w.status != 0 {
		w.mu.Unlock()
		return
	}
	w.status = status
	w.mu.Unlock()
	w.readyOnce.Do(func() { close(w.ready) })
}

func (w *pipeResponseWriter) Write(data []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.writer.Write(data)
}

func (w *pipeResponseWriter) Flush() {}

func (w *pipeResponseWriter) started() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status != 0
}

func (w *pipeResponseWriter) response(req *http.Request, body io.ReadCloser) *http.Response {
	w.mu.Lock()
	status := w.status
	w.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     w.header.Clone(),
		Body:       body,
		Request:    req,
	}
}

type unifiedChatRequest struct {
	RequestID              string
	CursorConversationID   string // Stable conversation ID from Cursor client
	ModelName              string
	Messages               []chatMessage
	Mode                   string
	AgentMode              cursorAgentMode
	WorkspaceRoot          string
	ThinkingLevel          int
	ThinkingEffort         string
	CurrentUserText        string
	PriorUsedTokens        int
	Conversation           *agentv1.ConversationStateStructure
	DisableTools           bool
	ParameterValues        map[string]string
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	ToolResult *chatToolResult `json:"-"`
}

type chatToolResult struct {
	ID        string
	Name      string
	Arguments string
	Output    string
}

func (g *Gateway) tryHandleBYOKChat(w http.ResponseWriter, r *http.Request, body []byte) bool {
	if !byokChatMethods[r.URL.Path] {
		return false
	}

	req, err := parseUnifiedChatRequest(body, r.Header.Get("Content-Type"), r.URL.Path)
	if err != nil {
		return false
	}

	adapter := g.findAdapterByCursorName(req.ModelName)
	if adapter == nil && !strings.HasPrefix(req.ModelName, "byok/") {
		return false
	}
	if adapter == nil {
		g.emit(Event{Type: EventBYOKFailure, Model: req.ModelName, Error: "BYOK model adapter not found"})
		http.Error(w, "BYOK model adapter not found", http.StatusNotFound)
		return true
	}

	log.Printf("[Gateway] BYOK chat model=%s provider=%s messages=%d", req.ModelName, adapter.Type, len(req.Messages))
	tracked := &trackingResponseWriter{ResponseWriter: w}
	if err := g.streamBYOKChat(tracked, r, req, adapter); err != nil {
		log.Printf("[Gateway] BYOK chat error: %v", err)
		if !tracked.started {
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
		return true
	}
	return true
}

type trackingResponseWriter struct {
	http.ResponseWriter
	started bool
}

func (w *trackingResponseWriter) WriteHeader(status int) {
	w.started = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *trackingResponseWriter) Write(data []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(data)
}

func (w *trackingResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (g *Gateway) findAdapterByCursorName(cursorModel string) *ModelAdapter {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, adapter := range g.modelAdapters {
		if adapter.matchesCursorModelName(cursorModel) {
			return adapter
		}
	}
	return nil
}

func parseUnifiedChatRequest(body []byte, contentType, path string) (unifiedChatRequest, error) {
	payload, err := firstPayload(body, contentType)
	if err != nil {
		return unifiedChatRequest{}, err
	}

	if strings.HasSuffix(path, "StreamUnifiedChatWithTools") {
		payload = firstNestedMessage(payload, 1)
	}
	if strings.HasSuffix(path, "StreamUnifiedChatWithToolsIdempotent") {
		clientChunk := firstNestedMessage(payload, 1)
		payload = firstNestedMessage(clientChunk, 1)
	}

	modelDetails := firstNestedMessage(payload, 5)
	modelName := readFirstStringField(modelDetails, 1)
	maxMode := readFirstVarintField(modelDetails, 8) == 1
	if maxMode {
		modelName = normalizeCursorModelID(strings.TrimSuffix(modelName, "-max"))
	}
	thinkingLevel := int(readFirstVarintField(payload, 49))
	thinkingEffort := ""
	if maxMode && thinkingLevel == 0 {
		thinkingLevel = 3
		thinkingEffort = "max"
	}

	messages := []chatMessage{}
	for _, conversationMsg := range readRepeatedMessages(payload, 1) {
		text := readFirstStringField(conversationMsg, 1)
		if text == "" {
			text = readFirstStringField(conversationMsg, 57)
		}
		if text == "" {
			continue
		}
		role := "user"
		switch readFirstVarintField(conversationMsg, 2) {
		case 2:
			role = "assistant"
		case 1:
			role = "user"
		}
		messages = append(messages, chatMessage{Role: role, Content: text})
	}

	if len(messages) == 0 {
		messages = append(messages, chatMessage{Role: "user", Content: ""})
	}

	return unifiedChatRequest{
		ModelName:       modelName,
		Messages:        messages,
		Mode:            path,
		ThinkingLevel:   thinkingLevel,
		ThinkingEffort:  thinkingEffort,
		ParameterValues: map[string]string{},
	}, nil
}

func (g *Gateway) streamBYOKChat(w http.ResponseWriter, r *http.Request, req unifiedChatRequest, adapter *ModelAdapter) error {
	if adapter.Type != "openai" && adapter.Type != "anthropic" {
		err := fmt.Errorf("provider %s is not supported", adapter.Type)
		g.emit(Event{Type: EventBYOKFailure, Model: req.ModelName, Error: err.Error()})
		return err
	}

	g.emit(Event{Type: EventBYOKRouted, Model: req.ModelName})

	adapter.Normalize()
	endpoint := adapter.Endpoint
	apiURL := adapter.APIURL()

	providerReq := buildProviderRequest(req, adapter, endpoint)
	if err := adapter.ApplyExtraParams(providerReq); err != nil {
		g.emit(Event{Type: EventBYOKFailure, Model: req.ModelName, Error: err.Error()})
		return err
	}
	body, err := json.Marshal(providerReq)
	if err != nil {
		g.emit(Event{Type: EventBYOKFailure, Model: req.ModelName, Error: err.Error()})
		return err
	}

	apiReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		g.emit(Event{Type: EventBYOKFailure, Model: req.ModelName, Error: err.Error()})
		return err
	}
	apiReq.Header.Set("Content-Type", "application/json")
	if adapter.Type == "anthropic" {
		apiReq.Header.Set("x-api-key", adapter.APIKey)
		apiReq.Header.Set("anthropic-version", "2023-06-01")
	} else {
		apiReq.Header.Set("Authorization", "Bearer "+adapter.APIKey)
	}

	resp, err := http.DefaultClient.Do(apiReq)
	if err != nil {
		g.emit(Event{Type: EventBYOKFailure, Model: req.ModelName, Error: err.Error()})
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		err := fmt.Errorf("upstream returned %d: %s", resp.StatusCode, string(data))
		g.emit(Event{Type: EventBYOKFailure, Model: req.ModelName, Error: err.Error()})
		return err
	}

	contentType := responseContentType(r.Header.Get("Content-Type"))
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	writeFrame := func(payload []byte) error {
		if _, err := writeEncodedFrame(w, payload, contentType); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	if strings.Contains(req.Mode, "WithTools") {
		if err := writeFrame(encodeChatStreamStartPayload(req.Mode)); err != nil {
			g.emit(Event{Type: EventBYOKFailure, Model: req.ModelName, Error: err.Error()})
			return err
		}
	}

	promptTokens := estimateMessagesTokens(req.Messages)
	completionTokens := 0
	cacheReadTokens := 0
	cacheWriteTokens := 0
	parser := providerStreamParser(adapter, endpoint)
	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 4*1024*1024)
	eventName := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		text, done, usage := parser(eventName, data)
		if usage.PromptTokens > 0 {
			promptTokens = usage.PromptTokens
		}
		if usage.CompletionTokens > 0 {
			completionTokens = usage.CompletionTokens
		}
		if usage.CacheReadTokens > 0 {
			cacheReadTokens = usage.CacheReadTokens
		}
		if usage.CacheWriteTokens > 0 {
			cacheWriteTokens = usage.CacheWriteTokens
		}
		if done {
			break
		}
		if text == "" {
			continue
		}

		payload := encodeChatTextPayload(req.Mode, text)
		if err := writeFrame(payload); err != nil {
			g.emit(Event{Type: EventBYOKFailure, Model: req.ModelName, Error: err.Error()})
			return err
		}
		completionTokens += estimateTokens(text)
	}
	if err := scanner.Err(); err != nil {
		g.emit(Event{Type: EventBYOKFailure, Model: req.ModelName, Error: err.Error()})
		return err
	}
	if isGRPCWebContentType(contentType) {
		if _, err := writeEncodedRawFrame(w, encodeGRPCWebTrailerFrame(), contentType); err != nil {
			g.emit(Event{Type: EventBYOKFailure, Model: req.ModelName, Error: err.Error()})
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	cost := estimateCost(adapter, promptTokens, completionTokens)
	g.emit(Event{Type: EventBYOKSuccess, Model: req.ModelName})
	g.emit(Event{
		Type:             EventTokens,
		Model:            req.ModelName,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		EstimatedCost:    cost,
	})
	return nil
}

type streamUsage struct {
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
}

type streamParser func(eventName string, data string) (text string, done bool, usage streamUsage)

func buildProviderRequest(req unifiedChatRequest, adapter *ModelAdapter, endpoint string) map[string]any {
	thinkingEffort := effectiveThinkingEffort(adapter, req)
	if adapter.Type == "anthropic" {
		out := map[string]any{
			"model":       adapter.ModelID,
			"messages":    normalizeAnthropicMessages(req.Messages),
			"stream":      true,
			"temperature": adapter.Temperature,
		}
		if adapter.MaxTokens > 0 {
			out["max_tokens"] = adapter.MaxTokens
		} else {
			out["max_tokens"] = 4096
		}
		if thinkingEffort != "" {
			out["thinking"] = map[string]any{
				"type":          "enabled",
				"budget_tokens": thinkingBudgetTokens(thinkingEffort, adapter.MaxTokens),
			}
		}
		if req.Mode == agentRunSSEPath && !req.DisableTools {
			if tools := agentProviderTools(adapter.Type, endpoint, req.AgentMode); len(tools) > 0 {
				out["tools"] = tools
			}
		}
		return out
	}

	if strings.Contains(strings.ToLower(endpoint), "responses") {
		out := map[string]any{
			"model":       adapter.ModelID,
			"input":       openAIResponsesInput(req.Messages),
			"stream":      true,
			"temperature": adapter.Temperature,
		}
		if adapter.MaxTokens > 0 {
			out["max_output_tokens"] = adapter.MaxTokens
		}
		if thinkingEffort != "" {
			out["reasoning"] = map[string]any{"effort": thinkingEffort}
		}
		if req.Mode == agentRunSSEPath && !req.DisableTools {
			if tools := agentProviderTools(adapter.Type, endpoint, req.AgentMode); len(tools) > 0 {
				out["tools"] = tools
			}
		}
		return out
	}

	out := map[string]any{
		"model":       adapter.ModelID,
		"messages":    openAIChatMessages(req.Messages),
		"stream":      true,
		"temperature": adapter.Temperature,
	}
	if adapter.MaxTokens > 0 {
		out["max_tokens"] = adapter.MaxTokens
	}
	if thinkingEffort != "" {
		out["reasoning_effort"] = thinkingEffort
	}
	if req.Mode == agentRunSSEPath && !req.DisableTools {
		if tools := agentProviderTools(adapter.Type, endpoint, req.AgentMode); len(tools) > 0 {
			out["tools"] = tools
			out["tool_choice"] = "auto"
		}
	}
	return out
}

func normalizeAnthropicMessages(messages []chatMessage) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		if msg.ToolResult != nil {
			input := map[string]any{}
			_ = json.Unmarshal([]byte(msg.ToolResult.Arguments), &input)
			out = append(out, map[string]any{
				"role": "assistant",
				"content": []map[string]any{{
					"type":  "tool_use",
					"id":    msg.ToolResult.ID,
					"name":  msg.ToolResult.Name,
					"input": input,
				}},
			})
			out = append(out, map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": msg.ToolResult.ID,
					"content":     msg.ToolResult.Output,
				}},
			})
			continue
		}
		role := msg.Role
		if role == "system" {
			role = "user"
		}
		if role != "assistant" {
			role = "user"
		}
		out = append(out, map[string]any{"role": role, "content": msg.Content})
	}
	return out
}

func openAIResponsesInput(messages []chatMessage) []map[string]any {
	out := make([]map[string]any, 0, len(messages)*2)
	for _, msg := range messages {
		if msg.ToolResult != nil {
			out = append(out, map[string]any{
				"type":      "function_call",
				"call_id":   msg.ToolResult.ID,
				"name":      msg.ToolResult.Name,
				"arguments": msg.ToolResult.Arguments,
				"status":    "completed",
			})
			out = append(out, map[string]any{
				"type":    "function_call_output",
				"call_id": msg.ToolResult.ID,
				"output":  msg.ToolResult.Output,
			})
			continue
		}
		out = append(out, map[string]any{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}
	return out
}

func openAIChatMessages(messages []chatMessage) []map[string]any {
	out := make([]map[string]any, 0, len(messages)*2)
	for _, msg := range messages {
		if msg.ToolResult != nil {
			out = append(out, map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []map[string]any{{
					"id":   msg.ToolResult.ID,
					"type": "function",
					"function": map[string]any{
						"name":      msg.ToolResult.Name,
						"arguments": msg.ToolResult.Arguments,
					},
				}},
			})
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": msg.ToolResult.ID,
				"content":      msg.ToolResult.Output,
			})
			continue
		}
		role := msg.Role
		if role == "" {
			role = "user"
		}
		out = append(out, map[string]any{"role": role, "content": msg.Content})
	}
	return out
}

func effectiveThinkingEffort(adapter *ModelAdapter, req unifiedChatRequest) string {
	provider := ""
	configured := ""
	if adapter != nil {
		provider = adapter.Type
		configured = adapter.ThinkingLevel
	}
	requestEffort := normalizeThinkingEffort(provider, req.ThinkingEffort)
	if requestEffort == "" && req.ThinkingLevel > 0 {
		requestEffort = thinkingEffortForLevel(provider, req.ThinkingLevel)
	}
	configuredEffort := normalizeThinkingEffort(provider, configured)
	if thinkingEffortRank(configuredEffort) > thinkingEffortRank(requestEffort) {
		return configuredEffort
	}
	return requestEffort
}

func normalizeThinkingEffort(provider string, effort string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "off", "low", "":
		return ""
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh", "x_high", "x-high", "very_high", "very high", "极高":
		if provider == "anthropic" {
			return "max"
		}
		return "xhigh"
	case "max":
		if provider == "anthropic" {
			return "max"
		}
		return "xhigh"
	default:
		return ""
	}
}

func thinkingEffortForLevel(provider string, level int) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch level {
	case 1:
		return "medium"
	case 2:
		return "high"
	case 3:
		if provider == "anthropic" {
			return "max"
		}
		return "xhigh"
	default:
		return ""
	}
}

func thinkingEffortRank(effort string) int {
	switch effort {
	case "medium":
		return 1
	case "high":
		return 2
	case "xhigh", "max":
		return 3
	default:
		return 0
	}
}

func thinkingBudgetTokens(effort string, maxTokens int) int {
	switch effort {
	case "xhigh", "max":
		if maxTokens > 0 && maxTokens < 16384 {
			return maxTokens / 2
		}
		return 16384
	case "high":
		if maxTokens > 0 && maxTokens < 8192 {
			return maxTokens / 2
		}
		return 8192
	case "medium":
		if maxTokens > 0 && maxTokens < 4096 {
			return maxTokens / 2
		}
		return 4096
	default:
		return 1024
	}
}

func providerStreamParser(adapter *ModelAdapter, endpoint string) streamParser {
	if adapter.Type == "anthropic" {
		return anthropicStreamText
	}
	if strings.Contains(strings.ToLower(endpoint), "responses") {
		return openAIResponsesStreamText
	}
	return openAIChatStreamText
}

func openAIChatStreamText(_ string, data string) (string, bool, streamUsage) {
	var event struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return "", false, streamUsage{}
	}
	usage := streamUsage{PromptTokens: event.Usage.PromptTokens, CompletionTokens: event.Usage.CompletionTokens, CacheReadTokens: event.Usage.PromptTokensDetails.CachedTokens}
	if len(event.Choices) == 0 {
		return "", false, usage
	}
	return event.Choices[0].Delta.Content, false, usage
}

func openAIResponsesStreamText(eventName string, data string) (string, bool, streamUsage) {
	var event struct {
		Type     string `json:"type"`
		Delta    string `json:"delta"`
		Text     string `json:"text"`
		Response struct {
			Usage struct {
				InputTokens        int `json:"input_tokens"`
				OutputTokens       int `json:"output_tokens"`
				InputTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return "", false, streamUsage{}
	}
	typeName := event.Type
	if typeName == "" {
		typeName = eventName
	}
	usage := streamUsage{PromptTokens: event.Response.Usage.InputTokens, CompletionTokens: event.Response.Usage.OutputTokens, CacheReadTokens: event.Response.Usage.InputTokensDetails.CachedTokens}
	switch typeName {
	case "response.output_text.delta", "response.refusal.delta":
		return event.Delta, false, usage
	case "response.output_text.done":
		return "", false, usage
	case "response.completed", "response.done":
		return "", true, usage
	default:
		return "", false, usage
	}
}

func anthropicStreamText(eventName string, data string) (string, bool, streamUsage) {
	var event struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return "", false, streamUsage{}
	}
	typeName := event.Type
	if typeName == "" {
		typeName = eventName
	}
	usage := streamUsage{PromptTokens: event.Usage.InputTokens, CompletionTokens: event.Usage.OutputTokens, CacheReadTokens: event.Usage.CacheReadInputTokens, CacheWriteTokens: event.Usage.CacheCreationInputTokens}
	switch typeName {
	case "content_block_delta":
		if event.Delta.Type == "text_delta" {
			return event.Delta.Text, false, usage
		}
	case "message_delta":
		return "", false, usage
	case "message_stop":
		return "", true, usage
	}
	return "", false, usage
}

func encodeChatTextPayload(mode, text string) []byte {
	response := appendStringField(nil, 1, text) // StreamUnifiedChatResponse.text
	if strings.Contains(mode, "WithToolsIdempotent") {
		withTools := appendBytesField(nil, 2, response) // stream_unified_chat_response
		return appendBytesField(nil, 1, withTools)      // server_chunk
	}
	if strings.Contains(mode, "WithTools") {
		return appendBytesField(nil, 2, response) // stream_unified_chat_response
	}
	return response
}

func encodeChatStreamStartPayload(mode string) []byte {
	withTools := encodeChatWithToolsStreamStart()
	if strings.Contains(mode, "WithToolsIdempotent") {
		return appendBytesField(nil, 1, withTools)
	}
	return withTools
}

func encodeChatWithToolsStreamStart() []byte {
	streamStart := appendStringField(nil, 1, "")
	return appendBytesField(nil, 5, streamStart)
}

func estimateMessagesTokens(messages []chatMessage) int {
	total := 0
	for _, message := range messages {
		if message.ToolResult != nil {
			total += estimateTokens(message.ToolResult.Name)
			total += estimateTokens(message.ToolResult.Arguments)
			total += estimateTokens(message.ToolResult.Output)
			continue
		}
		total += estimateTokens(message.Content)
	}
	return total
}

func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	ascii := 0
	nonASCII := 0
	for _, r := range text {
		if r < 128 {
			ascii++
		} else {
			nonASCII++
		}
	}
	return (ascii+3)/4 + nonASCII
}

func estimateCost(adapter *ModelAdapter, promptTokens int, completionTokens int) float64 {
	if adapter == nil {
		return 0
	}
	return float64(promptTokens)/1_000_000*adapter.InputPricePer1M + float64(completionTokens)/1_000_000*adapter.OutputPricePer1M
}

func firstPayload(body []byte, contentType string) ([]byte, error) {
	if isGRPCWebTextContentType(contentType) {
		decoded, err := decodeGRPCWebTextBody(body)
		if err != nil {
			return nil, err
		}
		body = decoded
	}
	if looksFramed(body, contentType) {
		for pos := 0; pos < len(body); {
			if len(body)-pos < 5 {
				return nil, fmt.Errorf("missing frame header")
			}
			flag := body[pos]
			length := int(binary.BigEndian.Uint32(body[pos+1 : pos+5]))
			frameStart := pos + 5
			frameEnd := frameStart + length
			if length < 0 || frameEnd > len(body) {
				return nil, fmt.Errorf("invalid frame length")
			}
			if flag&0x80 != 0 || flag&0x02 != 0 {
				pos = frameEnd
				continue
			}
			if flag&0x01 != 0 {
				return nil, fmt.Errorf("compressed request frames are not supported yet")
			}
			return body[frameStart:frameEnd], nil
		}
		return nil, fmt.Errorf("no proto payload frame found")
	}
	return body, nil
}

func decodeGRPCWebTextBody(body []byte) ([]byte, error) {
	compact := make([]byte, 0, len(body))
	for _, b := range body {
		switch b {
		case ' ', '\n', '\r', '\t':
			continue
		default:
			compact = append(compact, b)
		}
	}
	if len(compact) == 0 {
		return compact, nil
	}
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(compact)))
	n, err := base64.StdEncoding.Decode(decoded, compact)
	if err == nil {
		return decoded[:n], nil
	}
	decoded = make([]byte, base64.RawStdEncoding.DecodedLen(len(compact)))
	n, rawErr := base64.RawStdEncoding.Decode(decoded, compact)
	if rawErr == nil {
		return decoded[:n], nil
	}
	return nil, fmt.Errorf("invalid grpc-web-text body: %w", err)
}

func encodeFrame(payload []byte) []byte {
	out := make([]byte, 5, len(payload)+5)
	out[0] = 0
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	out = append(out, payload...)
	return out
}

func writeEncodedFrame(w io.Writer, payload []byte, contentType string) (int, error) {
	return writeEncodedRawFrame(w, encodeFrame(payload), contentType)
}

func writeEncodedRawFrame(w io.Writer, frame []byte, contentType string) (int, error) {
	if isGRPCWebTextContentType(contentType) {
		encoded := make([]byte, base64.StdEncoding.EncodedLen(len(frame)))
		base64.StdEncoding.Encode(encoded, frame)
		return w.Write(encoded)
	}
	return w.Write(frame)
}

func firstNestedMessage(payload []byte, fieldNumber int) []byte {
	for _, msg := range readRepeatedMessages(payload, fieldNumber) {
		return msg
	}
	return nil
}

func readRepeatedMessages(payload []byte, fieldNumber int) [][]byte {
	fields := [][]byte{}
	walkProto(payload, func(num int, wire int, value []byte) bool {
		if num == fieldNumber && wire == 2 {
			fields = append(fields, append([]byte(nil), value...))
		}
		return true
	})
	return fields
}

func readFirstStringField(payload []byte, fieldNumber int) string {
	var result string
	walkProto(payload, func(num int, wire int, value []byte) bool {
		if num == fieldNumber && wire == 2 {
			result = string(value)
			return false
		}
		return true
	})
	return result
}

func readFirstVarintField(payload []byte, fieldNumber int) uint64 {
	var result uint64
	walkProto(payload, func(num int, wire int, value []byte) bool {
		if num == fieldNumber && wire == 0 {
			parsed, _, ok := readVarint(value, 0)
			if ok {
				result = parsed
			}
			return false
		}
		return true
	})
	return result
}

func walkProto(payload []byte, visit func(fieldNumber int, wireType int, value []byte) bool) {
	for pos := 0; pos < len(payload); {
		key, next, ok := readVarint(payload, pos)
		if !ok {
			return
		}
		pos = next
		fieldNumber := int(key >> 3)
		wireType := int(key & 0x7)

		switch wireType {
		case 0:
			start := pos
			_, next, ok := readVarint(payload, pos)
			if !ok {
				return
			}
			if !visit(fieldNumber, wireType, payload[start:next]) {
				return
			}
			pos = next
		case 1:
			if pos+8 > len(payload) {
				return
			}
			if !visit(fieldNumber, wireType, payload[pos:pos+8]) {
				return
			}
			pos += 8
		case 2:
			length, next, ok := readVarint(payload, pos)
			if !ok {
				return
			}
			pos = next
			end := pos + int(length)
			if end < pos || end > len(payload) {
				return
			}
			if !visit(fieldNumber, wireType, payload[pos:end]) {
				return
			}
			pos = end
		case 5:
			if pos+4 > len(payload) {
				return
			}
			if !visit(fieldNumber, wireType, payload[pos:pos+4]) {
				return
			}
			pos += 4
		default:
			return
		}
	}
}

func readVarint(data []byte, pos int) (uint64, int, bool) {
	var value uint64
	for shift := 0; shift < 64; shift += 7 {
		if pos >= len(data) {
			return 0, pos, false
		}
		b := data[pos]
		pos++
		value |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return value, pos, true
		}
	}
	return 0, pos, false
}
