package relay

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/agent/v1"
	aiserverv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/aiserver/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestAppendAvailableModelsAddsByokModel(t *testing.T) {
	adapter := &ModelAdapter{
		DisplayName:      "GPT Test",
		Type:             "openai",
		ModelID:          "gpt-test",
		ContextWindow:    200000,
		SupportsThinking: true,
		SupportsCmdK:     true,
	}
	adapter.EnsureCatalogID()
	payload := appendAvailableModels(nil, []*ModelAdapter{adapter})

	if len(payload) == 0 {
		t.Fatal("expected encoded payload")
	}

	if containsBytes(payload, []byte("byok/gpt-test")) {
		t.Fatalf("expected payload to avoid byok model name, got %q", payload)
	}
	if !containsBytes(payload, []byte(adapter.CursorModelName())) {
		t.Fatalf("expected payload to contain catalog model name %q, got %q", adapter.CursorModelName(), payload)
	}
	if !containsBytes(payload, []byte("GPT Test")) {
		t.Fatalf("expected payload to contain display name, got %q", payload)
	}
}

func TestFallbackAvailableModelsResponse(t *testing.T) {
	adapter := &ModelAdapter{
		DisplayName: "GPT 5.5",
		Type:        "openai",
		ModelID:     "gpt-5.5",
	}
	g := NewGateway(Config{ModelAdapters: []*ModelAdapter{adapter}})
	req, err := http.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.AiService/AvailableModels", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/connect+proto")
	g.beginHTTPRequest(req)

	resp := g.fallbackAvailableModelsResponse(req, &http.Response{Body: io.NopCloser(strings.NewReader("{}"))}, availableModelsKindAI, "test")
	if resp == nil {
		t.Fatal("expected fallback response")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !containsBytes(body, []byte(adapter.CursorModelName())) {
		t.Fatalf("expected catalog model in fallback body, got %q", body)
	}
}

func TestFallbackAvailableCppModelsResponse(t *testing.T) {
	adapter := &ModelAdapter{
		DisplayName: "GPT 5.5",
		Type:        "openai",
		ModelID:     "gpt-5.5",
	}
	g := NewGateway(Config{ModelAdapters: []*ModelAdapter{adapter}})
	req, err := http.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.CppService/AvailableModels", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/connect+proto")
	g.beginHTTPRequest(req)

	resp := g.fallbackAvailableModelsResponse(req, &http.Response{Body: io.NopCloser(strings.NewReader("{}"))}, availableModelsKindCpp, "test")
	if resp == nil {
		t.Fatal("expected fallback response")
	}
	body, _ := io.ReadAll(resp.Body)
	if !containsBytes(body, []byte(adapter.CursorModelName())) {
		t.Fatalf("expected catalog cpp model in fallback body, got %q", body)
	}
	payload, err := firstPayload(body, resp.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	if got := readFirstStringField(payload, 2); got != adapter.CursorModelName() {
		t.Fatalf("default model = %q", got)
	}
}

func TestCursorModelNameUsesStableCatalogID(t *testing.T) {
	adapter := &ModelAdapter{DisplayName: "GPT 5.5", Type: "openai", BaseURL: "https://example.test/v1", ModelID: "gpt-5.5"}
	adapter.EnsureCatalogID()
	if adapter.CursorModelName() == "" {
		t.Fatal("expected catalog id")
	}
	if strings.HasPrefix(adapter.CursorModelName(), "byok/") {
		t.Fatalf("expected catalog id without byok prefix, got %q", adapter.CursorModelName())
	}
	if adapter.CursorModelName() != adapter.CatalogID {
		t.Fatalf("cursor model = %q catalog = %q", adapter.CursorModelName(), adapter.CatalogID)
	}
	if !adapter.matchesCursorModelName("byok/gpt-5.5") {
		t.Fatal("expected legacy byok name to still route")
	}
}

func TestAvailableModelsFallbackReasonDetectsConnectErrorFrame(t *testing.T) {
	errFrame := append([]byte{0x02, 0, 0, 0, 0}, []byte{}...)
	resp := &http.Response{Header: http.Header{"Content-Type": []string{"application/connect+proto"}}}
	if reason := availableModelsFallbackReason(errFrame, resp); reason == "" {
		t.Fatal("expected fallback reason for connect error frame")
	}
}

func TestFallbackAvailableModelsPreservesGRPCWebContentType(t *testing.T) {
	adapter := &ModelAdapter{
		DisplayName: "GPT 5.5",
		Type:        "openai",
		ModelID:     "gpt-5.5",
	}
	g := NewGateway(Config{ModelAdapters: []*ModelAdapter{adapter}})
	req, err := http.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.AiService/AvailableModels", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	g.beginHTTPRequest(req)

	resp := g.fallbackAvailableModelsResponse(req, &http.Response{Body: io.NopCloser(strings.NewReader("{}"))}, availableModelsKindAI, "test")
	if resp.Header.Get("Content-Type") != "application/grpc-web+proto" {
		t.Fatalf("content type = %q", resp.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(resp.Body)
	if !containsBytes(body, []byte("grpc-status: 0\r\n")) {
		t.Fatalf("expected grpc-web trailer, got %q", body)
	}
}

func TestParseUnifiedChatRequest(t *testing.T) {
	modelDetails := appendStringField(nil, 1, "byok/gpt-test")
	message := []byte{}
	message = appendStringField(message, 1, "hello")
	message = appendVarintField(message, 2, 1)

	payload := []byte{}
	payload = appendBytesField(payload, 1, message)
	payload = appendBytesField(payload, 5, modelDetails)

	req, err := parseUnifiedChatRequest(payload, "application/proto", "/aiserver.v1.ChatService/StreamUnifiedChat")
	if err != nil {
		t.Fatal(err)
	}
	if req.ModelName != "byok/gpt-test" {
		t.Fatalf("model name = %q", req.ModelName)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Content != "hello" {
		t.Fatalf("unexpected messages: %#v", req.Messages)
	}
}

func TestParseBidiAppendRequestBodySupportsGzipConnectProto(t *testing.T) {
	userMessage := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ConversationAction{
			ConversationAction: &agentv1.ConversationAction{
				Action: &agentv1.ConversationAction_UserMessageAction{
					UserMessageAction: &agentv1.UserMessageAction{
						UserMessage: &agentv1.UserMessage{Text: "hello gzip"},
					},
				},
			},
		},
	}
	userPayload, err := proto.Marshal(userMessage)
	if err != nil {
		t.Fatal(err)
	}
	appendReq := &aiserverv1.BidiAppendRequest{
		Data:        bytesToHex(userPayload),
		RequestId:   &aiserverv1.BidiRequestId{RequestId: "req-gzip"},
		AppendSeqno: 1,
	}
	payload, err := proto.Marshal(appendReq)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(encodeFrame(payload)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	header := http.Header{}
	header.Set("Content-Type", "application/connect+proto")
	header.Set("Content-Encoding", "gzip")
	parsed, err := parseBidiAppendRequestBody(compressed.Bytes(), header)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.GetRequestId().GetRequestId() != "req-gzip" {
		t.Fatalf("request id = %q", parsed.GetRequestId().GetRequestId())
	}
}

func TestParseAgentRunRequestBodyIgnoresUncompressedConnectEncoding(t *testing.T) {
	runReq := &agentv1.BidiRequestId{RequestId: "req-connect-plain"}
	payload, err := proto.Marshal(runReq)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	header.Set("Content-Type", "application/connect+proto")
	header.Set("Connect-Content-Encoding", "gzip")

	parsed, err := parseAgentRunRequestBody(encodeFrame(payload), header)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.GetRequestId() != "req-connect-plain" {
		t.Fatalf("request id = %q", parsed.GetRequestId())
	}
}

type flushRecorder struct {
	header  http.Header
	body    bytes.Buffer
	status  int
	flushes int
}

func (r *flushRecorder) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}

func (r *flushRecorder) WriteHeader(status int) {
	r.status = status
}

func (r *flushRecorder) Write(data []byte) (int, error) {
	return r.body.Write(data)
}

func (r *flushRecorder) Flush() {
	r.flushes++
}

func TestWriteHTTPResponseFlushesStreamingBodies(t *testing.T) {
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:          io.NopCloser(strings.NewReader("one\ntwo\n")),
		ContentLength: -1,
	}
	rec := &flushRecorder{}

	writeHTTPResponse(rec, resp)

	if rec.status != http.StatusOK {
		t.Fatalf("status = %d", rec.status)
	}
	if rec.body.String() != "one\ntwo\n" {
		t.Fatalf("body = %q", rec.body.String())
	}
	if rec.flushes == 0 {
		t.Fatal("expected streaming response to flush")
	}
}

func TestBidiAppendDecodeFailureStillReturnsOK(t *testing.T) {
	g := NewGateway(Config{})
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend", strings.NewReader("not proto"))
	req.Header.Set("Content-Type", "application/connect+proto")
	g.beginHTTPRequest(req)

	resp := g.handleLocalBidiAppend(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestParseBidiAppendRequestBodySupportsJSON(t *testing.T) {
	data, err := protojson.Marshal(&aiserverv1.BidiAppendRequest{
		RequestId: &aiserverv1.BidiRequestId{RequestId: "req-json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	parsed, err := parseBidiAppendRequestBody(data, header)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.GetRequestId().GetRequestId() != "req-json" {
		t.Fatalf("request id = %q", parsed.GetRequestId().GetRequestId())
	}
}

func TestParseBidiAppendRequestBodySupportsGRPCWebText(t *testing.T) {
	appendReq := &aiserverv1.BidiAppendRequest{
		RequestId: &aiserverv1.BidiRequestId{RequestId: "req-web-text"},
	}
	payload, err := proto.Marshal(appendReq)
	if err != nil {
		t.Fatal(err)
	}
	body := base64.StdEncoding.EncodeToString(encodeFrame(payload))
	header := http.Header{}
	header.Set("Content-Type", "application/grpc-web-text+proto")
	parsed, err := parseBidiAppendRequestBody([]byte(body), header)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.GetRequestId().GetRequestId() != "req-web-text" {
		t.Fatalf("request id = %q", parsed.GetRequestId().GetRequestId())
	}
}

func TestEncodeChatTextPayload(t *testing.T) {
	payload := encodeChatTextPayload("/aiserver.v1.ChatService/StreamUnifiedChatWithTools", "hello")
	outer := firstNestedMessage(payload, 2)
	if got := readFirstStringField(outer, 1); got != "hello" {
		t.Fatalf("text = %q", got)
	}
}

func TestEncodeChatStreamStartPayloadForIdempotent(t *testing.T) {
	payload := encodeChatStreamStartPayload("/aiserver.v1.ChatService/StreamUnifiedChatWithToolsIdempotent")
	serverChunk := firstNestedMessage(payload, 1)
	streamStart := firstNestedMessage(serverChunk, 5)
	if streamStart == nil {
		t.Fatal("expected wrapped stream start payload")
	}
}

func TestEstimateCost(t *testing.T) {
	adapter := &ModelAdapter{InputPricePer1M: 2, OutputPricePer1M: 10}
	cost := estimateCost(adapter, 1000, 2000)
	if cost != 0.022 {
		t.Fatalf("cost = %f", cost)
	}
}

func TestModelAdapterAPIURLNormalizesProviderEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		adapter  ModelAdapter
		expected string
	}{
		{
			name:     "openai domain with responses",
			adapter:  ModelAdapter{Type: "openai", BaseURL: "https://api.openai.com", Endpoint: "/v1/responses"},
			expected: "https://api.openai.com/v1/responses",
		},
		{
			name:     "openai legacy v1 base with responses",
			adapter:  ModelAdapter{Type: "openai", BaseURL: "https://api.openai.com/v1", Endpoint: "/v1/responses"},
			expected: "https://api.openai.com/v1/responses",
		},
		{
			name:     "openai old responses endpoint",
			adapter:  ModelAdapter{Type: "openai", BaseURL: "https://relay.test", Endpoint: "/responses"},
			expected: "https://relay.test/v1/responses",
		},
		{
			name:     "openai chat completions",
			adapter:  ModelAdapter{Type: "openai", BaseURL: "https://relay.test", Endpoint: "/v1/chat/completions"},
			expected: "https://relay.test/v1/chat/completions",
		},
		{
			name:     "anthropic domain with messages",
			adapter:  ModelAdapter{Type: "anthropic", BaseURL: "https://api.anthropic.com", Endpoint: "/v1/messages"},
			expected: "https://api.anthropic.com/v1/messages",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := tc.adapter
			adapter.Normalize()
			if strings.HasSuffix(strings.ToLower(adapter.BaseURL), "/v1") {
				t.Fatalf("base url should be domain-only after normalize, got %q", adapter.BaseURL)
			}
			if got := adapter.APIURL(); got != tc.expected {
				t.Fatalf("api url = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestNormalizeThinkingLevelUsesLowercaseXHigh(t *testing.T) {
	adapter := &ModelAdapter{Type: "openai", ModelID: "gpt-test", ThinkingLevel: "very_high"}
	adapter.Normalize()
	if adapter.ThinkingLevel != "xhigh" {
		t.Fatalf("thinking level = %q, want xhigh", adapter.ThinkingLevel)
	}
}

func TestNormalizeThinkingLevelKeepsAnthropicMax(t *testing.T) {
	adapter := &ModelAdapter{Type: "anthropic", ModelID: "claude-test", ThinkingLevel: "max"}
	adapter.Normalize()
	if adapter.ThinkingLevel != "max" {
		t.Fatalf("thinking level = %q, want max", adapter.ThinkingLevel)
	}
}

func TestNormalizeThinkingLevelMigratesAnthropicXHighToMax(t *testing.T) {
	adapter := &ModelAdapter{Type: "anthropic", ModelID: "claude-test", ThinkingLevel: "xhigh"}
	adapter.Normalize()
	if adapter.ThinkingLevel != "max" {
		t.Fatalf("thinking level = %q, want max", adapter.ThinkingLevel)
	}
}

func TestBuildProviderRequestUsesLowercaseXHighThinkingEffort(t *testing.T) {
	adapter := &ModelAdapter{Type: "openai", ModelID: "gpt-test", Endpoint: "/v1/responses", ThinkingLevel: "xhigh"}
	adapter.Normalize()
	req := unifiedChatRequest{Messages: []chatMessage{{Role: "user", Content: "hello"}}}
	body := buildProviderRequest(req, adapter, adapter.Endpoint)
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("expected reasoning object, got %#v", body["reasoning"])
	}
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("effort = %#v, want xhigh", reasoning["effort"])
	}
}

func TestBuildProviderRequestKeepsAnthropicMaxThinkingEffort(t *testing.T) {
	adapter := &ModelAdapter{Type: "anthropic", ModelID: "claude-test", Endpoint: "/v1/messages", ThinkingLevel: "max"}
	adapter.Normalize()
	req := unifiedChatRequest{Messages: []chatMessage{{Role: "user", Content: "hello"}}}
	body := buildProviderRequest(req, adapter, adapter.Endpoint)
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking object, got %#v", body["thinking"])
	}
	if thinking["budget_tokens"] != 2048 {
		t.Fatalf("budget_tokens = %#v, want 2048", thinking["budget_tokens"])
	}
	if effort := effectiveThinkingEffort(adapter, req); effort != "max" {
		t.Fatalf("effort = %q, want max", effort)
	}
}

func TestAvailableModelsUsesAnthropicMaxParameterValue(t *testing.T) {
	adapter := &ModelAdapter{Type: "anthropic", ModelID: "claude-test", DisplayName: "Claude Test", SupportsThinking: true, ThinkingLevel: "max"}
	adapter.Normalize()
	payload := appendAvailableModels(nil, []*ModelAdapter{adapter})
	if !containsBytes(payload, []byte("max")) {
		t.Fatalf("expected available model payload to contain max parameter value, got %q", payload)
	}
	if containsBytes(payload, []byte("xhigh")) {
		t.Fatalf("expected anthropic available model payload to avoid xhigh, got %q", payload)
	}
}

func TestAvailableModelsUsesOpenAIXHighParameterValue(t *testing.T) {
	adapter := &ModelAdapter{Type: "openai", ModelID: "gpt-test", DisplayName: "GPT Test", SupportsThinking: true, ThinkingLevel: "xhigh"}
	adapter.Normalize()
	payload := appendAvailableModels(nil, []*ModelAdapter{adapter})
	if !containsBytes(payload, []byte("xhigh")) {
		t.Fatalf("expected available model payload to contain xhigh parameter value, got %q", payload)
	}
}

func TestBuildProviderRequestOpenAIMaxNormalizesToXHigh(t *testing.T) {
	adapter := &ModelAdapter{Type: "openai", ModelID: "gpt-test", Endpoint: "/v1/responses", ThinkingLevel: "max"}
	adapter.Normalize()
	if adapter.ThinkingLevel != "xhigh" {
		t.Fatalf("thinking level = %q, want xhigh", adapter.ThinkingLevel)
	}
	req := unifiedChatRequest{Messages: []chatMessage{{Role: "user", Content: "hello"}}}
	body := buildProviderRequest(req, adapter, adapter.Endpoint)
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("expected reasoning object, got %#v", body["reasoning"])
	}
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("effort = %#v, want xhigh", reasoning["effort"])
	}
}

func TestBYOKChatDoesNotReplayLocalResponseCache(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\n")
		_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","delta":"OK"}`+"\n\n")
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed"}`+"\n\n")
	}))
	defer upstream.Close()

	adapter := &ModelAdapter{
		DisplayName: "GPT Cache",
		Type:        "openai",
		BaseURL:     upstream.URL,
		ModelID:     "gpt-cache",
		Endpoint:    "/responses",
	}
	adapter.EnsureCatalogID()
	g := NewGateway(Config{ModelAdapters: []*ModelAdapter{adapter}})
	chatReq := unifiedChatRequest{
		ModelName: adapter.CursorModelName(),
		Mode:      "/aiserver.v1.ChatService/StreamUnifiedChat",
		Messages:  []chatMessage{{Role: "user", Content: "same prompt"}},
	}

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.ChatService/StreamUnifiedChat", nil)
	firstReq.Header.Set("Content-Type", "application/proto")
	if err := g.streamBYOKChat(first, firstReq, chatReq, adapter); err != nil {
		t.Fatal(err)
	}
	if !containsBytes(first.Body.Bytes(), []byte("OK")) {
		t.Fatalf("expected first response text, got %q", first.Body.Bytes())
	}

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.ChatService/StreamUnifiedChat", nil)
	secondReq.Header.Set("Content-Type", "application/proto")
	if err := g.streamBYOKChat(second, secondReq, chatReq, adapter); err != nil {
		t.Fatal(err)
	}
	if !containsBytes(second.Body.Bytes(), []byte("OK")) {
		t.Fatalf("expected second response text, got %q", second.Body.Bytes())
	}
	if upstreamCalls != 2 {
		t.Fatalf("upstream calls = %d, want 2", upstreamCalls)
	}
}

func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		matched := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func bytesToHex(data []byte) string {
	const alphabet = "0123456789abcdef"
	out := make([]byte, len(data)*2)
	for i, b := range data {
		out[i*2] = alphabet[b>>4]
		out[i*2+1] = alphabet[b&0x0f]
	}
	return string(out)
}
