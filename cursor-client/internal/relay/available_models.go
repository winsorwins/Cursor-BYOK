package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const (
	aiAvailableModelsPath  = "/aiserver.v1.AiService/AvailableModels"
	cppAvailableModelsPath = "/aiserver.v1.CppService/AvailableModels"
)

type availableModelsKind string

const (
	availableModelsKindAI  availableModelsKind = "ai"
	availableModelsKindCpp availableModelsKind = "cpp"
)

// ModifyResponse lets the MITM layer delegate response patching to the relay.
func (g *Gateway) ModifyResponse(resp *http.Response, req *http.Request) *http.Response {
	if resp == nil || req == nil || req.URL == nil {
		return resp
	}
	if resp.Header.Get("X-Cursor-Assistant-Local") == "1" {
		return resp
	}
	kind, isAvailableModels := availableModelsKindForPath(req.URL.Path)
	defer func() {
		if !isAvailableModels {
			g.completeHTTPRequest(req, resp.StatusCode, "official/upstream", false, false, "", "")
		}
	}()
	if !isAvailableModels {
		return resp
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if fallback := g.fallbackAvailableModelsResponse(req, resp, kind, "official error"); fallback != nil {
			return fallback
		}
		g.completeHTTPRequest(req, resp.StatusCode, "official/upstream", false, false, "", "")
		return resp
	}
	if len(g.byokModels()) == 0 {
		g.completeHTTPRequest(req, resp.StatusCode, "official/upstream", false, false, "", "")
		return resp
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[Gateway] Failed to read AvailableModels response: %v", err)
		if fallback := g.fallbackAvailableModelsResponse(req, resp, kind, err.Error()); fallback != nil {
			return fallback
		}
		g.completeHTTPRequest(req, resp.StatusCode, "official/upstream", false, false, "", err.Error())
		return resp
	}
	_ = resp.Body.Close()
	if reason := availableModelsFallbackReason(body, resp); reason != "" {
		if fallback := g.fallbackAvailableModelsResponse(req, resp, kind, reason); fallback != nil {
			return fallback
		}
	}

	patched, err := g.patchAvailableModelsBody(kind, body, resp.Header.Get("Content-Type"))
	if err != nil {
		log.Printf("[Gateway] Failed to patch AvailableModels response: %v", err)
		if fallback := g.fallbackAvailableModelsResponse(req, resp, kind, err.Error()); fallback != nil {
			return fallback
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		g.completeHTTPRequest(req, resp.StatusCode, "official/upstream", false, false, "", err.Error())
		return resp
	}

	resp.Body = io.NopCloser(bytes.NewReader(patched))
	resp.ContentLength = int64(len(patched))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(patched)))
	resp.Header.Del("Content-Encoding")
	g.emit(Event{Type: EventAvailablePatch})
	g.completeHTTPRequest(req, resp.StatusCode, "official/available_models_patch", true, false, "", "")
	log.Printf("[Gateway] Injected %d BYOK models into %s AvailableModels", len(g.byokModels()), kind)
	return resp
}

func availableModelsKindForPath(path string) (availableModelsKind, bool) {
	switch path {
	case aiAvailableModelsPath:
		return availableModelsKindAI, true
	case cppAvailableModelsPath:
		return availableModelsKindCpp, true
	default:
		return "", false
	}
}

func (g *Gateway) fallbackAvailableModelsResponse(req *http.Request, resp *http.Response, kind availableModelsKind, reason string) *http.Response {
	models := g.byokModels()
	if len(models) == 0 {
		return nil
	}

	payload := buildAvailableModelsFallbackPayload(kind, models)
	body, contentType := encodeLocalProtoHTTPBody(payload, req.Header.Get("Content-Type"))

	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	status := http.StatusOK
	out := &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:        http.Header{},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
	out.Header.Set("Content-Type", contentType)
	out.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	out.Header.Set("Cache-Control", "no-cache")
	if contentType == "application/grpc" {
		out.Header.Set("Grpc-Status", "0")
	}
	g.emit(Event{Type: EventAvailablePatch})
	g.completeHTTPRequest(req, status, "local/available_models_fallback", true, false, "", reason)
	log.Printf("[Gateway] Returned %d local BYOK models for %s AvailableModels fallback: %s", len(models), kind, reason)
	return out
}

func (g *Gateway) patchAvailableModelsBody(kind availableModelsKind, body []byte, contentType string) ([]byte, error) {
	models := g.byokModels()
	if len(models) == 0 {
		return body, nil
	}

	if looksFramed(body, contentType) {
		return patchFirstProtoFrame(body, func(payload []byte) []byte {
			return appendAvailableModelsPayload(kind, payload, models)
		})
	}

	return appendAvailableModelsPayload(kind, body, models), nil
}

func (g *Gateway) byokModels() []*ModelAdapter {
	g.mu.RLock()
	defer g.mu.RUnlock()

	models := make([]*ModelAdapter, 0, len(g.modelAdapters))
	for _, adapter := range g.modelAdapters {
		if adapter != nil && adapter.ModelID != "" {
			models = append(models, adapter)
		}
	}
	return models
}

func buildAvailableModelsFallbackPayload(kind availableModelsKind, adapters []*ModelAdapter) []byte {
	switch kind {
	case availableModelsKindCpp:
		return appendAvailableCppModels(nil, adapters, true)
	default:
		payload := appendAvailableModels(nil, adapters)
		if len(adapters) > 0 {
			payload = appendAvailableModelFeatureDefaults(payload, adapters[0].CursorModelName())
		}
		return payload
	}
}

func appendAvailableModelsPayload(kind availableModelsKind, payload []byte, adapters []*ModelAdapter) []byte {
	switch kind {
	case availableModelsKindCpp:
		return appendAvailableCppModels(payload, adapters, false)
	default:
		return appendAvailableModels(payload, adapters)
	}
}

func appendAvailableModels(payload []byte, adapters []*ModelAdapter) []byte {
	out := make([]byte, 0, len(payload)+len(adapters)*256)
	out = append(out, payload...)

	for _, adapter := range adapters {
		name := adapter.CursorModelName()
		out = appendStringField(out, 1, name) // model_names
		out = appendBytesField(out, 2, encodeAvailableModel(adapter))
	}

	return out
}

func appendAvailableCppModels(payload []byte, adapters []*ModelAdapter, forceDefault bool) []byte {
	out := make([]byte, 0, len(payload)+len(adapters)*64)
	out = append(out, payload...)

	for _, adapter := range adapters {
		out = appendStringField(out, 1, adapter.CursorModelName())
	}
	if forceDefault && len(adapters) > 0 && !hasProtoField(payload, 2) {
		out = appendStringField(out, 2, adapters[0].CursorModelName())
	}
	return out
}

func appendAvailableModelFeatureDefaults(payload []byte, defaultModel string) []byte {
	if defaultModel == "" {
		return payload
	}
	config := encodeFeatureModelConfig(defaultModel)
	for _, fieldNumber := range []int{4, 5, 6, 7, 8, 9, 10} {
		payload = appendBytesField(payload, fieldNumber, config)
	}
	return appendVarintField(payload, 11, 0)
}

func encodeFeatureModelConfig(defaultModel string) []byte {
	msg := appendStringField(nil, 1, defaultModel)
	msg = appendStringField(msg, 2, defaultModel)
	msg = appendStringField(msg, 3, defaultModel)
	return msg
}

func encodeAvailableModel(adapter *ModelAdapter) []byte {
	baseName := adapter.CursorModelName()
	name := baseName
	displayName := adapter.DisplayName
	if displayName == "" {
		displayName = adapter.ModelID
	}
	contextLimit := int64(adapter.ContextWindow)
	if contextLimit <= 0 {
		contextLimit = 200000
	}

	tooltip := []byte{}
	tooltip = appendStringField(tooltip, 1, displayName)
	tooltip = appendStringField(tooltip, 2, adapter.Type)
	tooltip = appendStringField(tooltip, 7, "**"+displayName+"**<br />BYOK model via local proxy")

	msg := []byte{}
	msg = appendStringField(msg, 1, name)
	msg = appendVarintField(msg, 2, 1) // default_on
	msg = appendVarintField(msg, 5, 1) // supports_agent
	msg = appendVarintField(msg, 6, 0) // degradation_status
	msg = appendBytesField(msg, 8, tooltip)
	msg = appendVarintField(msg, 9, boolToVarint(adapter.SupportsThinking))
	msg = appendVarintField(msg, 10, boolToVarint(adapter.SupportsImages))
	msg = appendVarintField(msg, 14, 1) // supports_max_mode
	msg = appendVarintField(msg, 15, uint64(contextLimit))
	msg = appendVarintField(msg, 16, uint64(contextLimit))
	msg = appendStringField(msg, 17, displayName)
	msg = appendStringField(msg, 18, name)
	msg = appendVarintField(msg, 19, 1) // supports_non_max_mode
	msg = appendVarintField(msg, 22, 1) // supports_plan_mode
	msg = appendVarintField(msg, 23, 1) // is_user_added
	msg = appendStringField(msg, 24, displayName)
	msg = appendVarintField(msg, 25, boolToVarint(adapter.SupportsSandboxing))
	msg = appendVarintField(msg, 26, boolToVarint(adapter.SupportsCmdK))
	msg = appendVarintField(msg, 27, 0) // only_supports_cmd_k
	msg = appendVarintField(msg, 28, 0) // background_composer_sort_order
	for _, alias := range adapter.LegacyCursorModelNames() {
		if alias != name {
			msg = appendStringField(msg, 36, alias) // legacy_slugs
			msg = appendStringField(msg, 37, alias) // id_aliases
		}
	}
	msg = appendVarintField(msg, 38, 0) // named_model_section_index
	msg = appendStringField(msg, 39, "Local BYOK")
	msg = appendStringField(msg, 41, adapter.Type)
	if adapter.SupportsThinking {
		maxEffort := "xhigh"
		if adapter.Type == "anthropic" {
			maxEffort = "max"
		}
		msg = appendBytesField(msg, 29, encodeThinkingEffortParameterDefinition(adapter.Type))
		msg = appendBytesField(msg, 30, encodeModelVariantConfig("medium", displayName, false, true))
		msg = appendBytesField(msg, 30, encodeModelVariantConfig(maxEffort, displayName+" Max", true, false))
	}
	return msg
}

func encodeThinkingEffortParameterDefinition(provider string) []byte {
	maxEffort := "xhigh"
	maxLabel := "Extra High"
	if provider == "anthropic" {
		maxEffort = "max"
		maxLabel = "Max"
	}
	enumParam := []byte{}
	enumParam = appendBytesField(enumParam, 1, encodeEnumParameterValue("medium", "Balanced"))
	enumParam = appendBytesField(enumParam, 1, encodeEnumParameterValue("high", "High"))
	enumParam = appendBytesField(enumParam, 1, encodeEnumParameterValue(maxEffort, maxLabel))
	paramType := appendBytesField(nil, 2, enumParam)
	msg := appendStringField(nil, 1, "thinking_effort")
	msg = appendStringField(msg, 2, "Thinking effort")
	msg = appendStringField(msg, 3, "Controls reasoning budget for compatible models.")
	msg = appendBytesField(msg, 4, paramType)
	return msg
}

func encodeEnumParameterValue(value string, displayName string) []byte {
	msg := appendStringField(nil, 1, value)
	msg = appendStringField(msg, 2, displayName)
	return msg
}

func encodeModelVariantConfig(effort string, displayName string, isMax bool, isDefaultNonMax bool) []byte {
	parameterValue := appendStringField(nil, 1, "thinking_effort")
	parameterValue = appendStringField(parameterValue, 2, effort)
	msg := appendBytesField(nil, 1, parameterValue)
	msg = appendStringField(msg, 2, displayName)
	msg = appendVarintField(msg, 3, boolToVarint(isMax))
	msg = appendVarintField(msg, 4, boolToVarint(isMax))
	msg = appendVarintField(msg, 5, boolToVarint(isDefaultNonMax))
	return msg
}

func looksFramed(body []byte, contentType string) bool {
	if len(body) < 5 {
		return false
	}
	ct := strings.ToLower(contentType)
	if !(strings.Contains(ct, "grpc") || strings.Contains(ct, "connect")) {
		return false
	}
	length := int(binary.BigEndian.Uint32(body[1:5]))
	return length >= 0 && length <= len(body)-5
}

func availableModelsFallbackReason(body []byte, resp *http.Response) string {
	if resp != nil {
		if status := resp.Header.Get("Grpc-Status"); status != "" && status != "0" {
			return "grpc status " + status
		}
	}
	contentType := ""
	if resp != nil {
		contentType = resp.Header.Get("Content-Type")
	}
	if reason := connectErrorFrameReason(body, contentType); reason != "" {
		return reason
	}
	lowerBody := bytes.ToLower(body)
	for _, needle := range [][]byte{
		[]byte("unauthenticated"),
		[]byte("error_not_logged_in"),
		[]byte("authentication error"),
		[]byte(`"code":16`),
	} {
		if bytes.Contains(lowerBody, needle) {
			return "official authentication error"
		}
	}
	return ""
}

func connectErrorFrameReason(body []byte, contentType string) string {
	if !looksFramed(body, contentType) {
		return ""
	}
	dataFrames := 0
	for pos := 0; pos < len(body); {
		if len(body)-pos < 5 {
			return "truncated connect frame"
		}
		flag := body[pos]
		length := int(binary.BigEndian.Uint32(body[pos+1 : pos+5]))
		frameStart := pos + 5
		frameEnd := frameStart + length
		if length < 0 || frameEnd > len(body) {
			return "invalid connect frame"
		}
		if flag&0x02 != 0 {
			if dataFrames == 0 {
				return "connect end stream error"
			}
			return ""
		}
		if flag&0x80 == 0 && flag&0x01 == 0 {
			dataFrames++
		}
		pos = frameEnd
	}
	return ""
}

func encodeAvailableModelsResponseBody(payload []byte, contentType string) []byte {
	if !shouldFrameProtoContentType(contentType) {
		return payload
	}
	body := encodeFrame(payload)
	if isGRPCWebContentType(contentType) {
		body = append(body, encodeGRPCWebTrailerFrame()...)
	}
	if isGRPCWebTextContentType(contentType) {
		return []byte(base64.StdEncoding.EncodeToString(body))
	}
	return body
}

func encodeLocalProtoHTTPBody(payload []byte, requestContentType string) ([]byte, string) {
	contentType := responseContentType(requestContentType)
	return encodeAvailableModelsResponseBody(payload, contentType), contentType
}

func shouldFrameProtoContentType(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "grpc") || strings.Contains(ct, "connect")
}

func encodeGRPCWebTrailerFrame() []byte {
	return encodeFrameWithFlag(0x80, []byte("grpc-status: 0\r\n"))
}

func encodeFrameWithFlag(flag byte, payload []byte) []byte {
	out := make([]byte, 5, len(payload)+5)
	out[0] = flag
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	out = append(out, payload...)
	return out
}

func patchFirstProtoFrame(body []byte, patch func([]byte) []byte) ([]byte, error) {
	out := make([]byte, 0, len(body)+512)
	pos := 0
	patched := false

	for pos < len(body) {
		if len(body)-pos < 5 {
			return nil, fmt.Errorf("truncated frame header")
		}
		flag := body[pos]
		length := int(binary.BigEndian.Uint32(body[pos+1 : pos+5]))
		frameStart := pos + 5
		frameEnd := frameStart + length
		if length < 0 || frameEnd > len(body) {
			return nil, fmt.Errorf("invalid frame length")
		}

		payload := body[frameStart:frameEnd]
		if !patched && flag&0x80 == 0 && flag&0x01 == 0 && flag&0x02 == 0 {
			payload = patch(payload)
			patched = true
		}

		out = append(out, flag)
		var lengthBuf [4]byte
		binary.BigEndian.PutUint32(lengthBuf[:], uint32(len(payload)))
		out = append(out, lengthBuf[:]...)
		out = append(out, payload...)
		pos = frameEnd
	}

	if !patched {
		return nil, fmt.Errorf("no uncompressed proto frame found")
	}
	return out, nil
}

func appendStringField(dst []byte, fieldNumber int, value string) []byte {
	return appendBytesField(dst, fieldNumber, []byte(value))
}

func appendBytesField(dst []byte, fieldNumber int, value []byte) []byte {
	dst = appendVarint(dst, uint64(fieldNumber<<3|2))
	dst = appendVarint(dst, uint64(len(value)))
	dst = append(dst, value...)
	return dst
}

func appendVarintField(dst []byte, fieldNumber int, value uint64) []byte {
	dst = appendVarint(dst, uint64(fieldNumber<<3))
	dst = appendVarint(dst, value)
	return dst
}

func appendVarint(dst []byte, value uint64) []byte {
	for value >= 0x80 {
		dst = append(dst, byte(value)|0x80)
		value >>= 7
	}
	dst = append(dst, byte(value))
	return dst
}

func hasProtoField(payload []byte, fieldNumber int) bool {
	found := false
	walkProto(payload, func(num int, _ int, _ []byte) bool {
		if num == fieldNumber {
			found = true
			return false
		}
		return true
	})
	return found
}

func boolToVarint(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}
