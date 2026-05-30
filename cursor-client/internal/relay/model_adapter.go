package relay

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// ModelAdapter represents a BYOK model configuration
type ModelAdapter struct {
	DisplayName        string  `json:"displayName"`
	Type               string  `json:"type"` // "openai" or "anthropic"
	BaseURL            string  `json:"baseURL"`
	APIKey             string  `json:"apiKey"`
	ModelID            string  `json:"modelID"`
	CatalogID          string  `json:"catalogID,omitempty"`
	CursorModelID      string  `json:"cursorModelID"`
	Endpoint           string  `json:"endpoint"`
	MaxTokens          int     `json:"maxTokens"`
	ContextWindow      int     `json:"contextWindow"`
	Temperature        float64 `json:"temperature"`
	InputPricePer1M    float64 `json:"inputPricePer1M"`
	OutputPricePer1M   float64 `json:"outputPricePer1M"`
	SupportsThinking   bool    `json:"supportsThinking"`
	SupportsImages     bool    `json:"supportsImages"`
	SupportsCmdK       bool    `json:"supportsCmdK"`
	SupportsSandboxing bool    `json:"supportsSandboxing"`
	Note               string  `json:"note,omitempty"`
	ThinkingLevel      string  `json:"thinkingLevel,omitempty"`
	ExtraParamsEnabled bool    `json:"extraParamsEnabled,omitempty"`
	ExtraParamsJSON    string  `json:"extraParamsJSON,omitempty"`
}

// EnsureCatalogID assigns the stable Cursor-facing model id used by the local
// catalog. It deliberately avoids a byok/ prefix because Cursor treats the
// AvailableModels catalog as opaque internal ids.
func (m *ModelAdapter) EnsureCatalogID() {
	if m == nil {
		return
	}
	if strings.TrimSpace(m.CatalogID) != "" {
		m.CatalogID = normalizeCursorModelID(m.CatalogID)
		return
	}
	m.CatalogID = m.computeCatalogID()
}

// CursorModelName returns the model id exposed to Cursor.
func (m *ModelAdapter) CursorModelName() string {
	if m == nil {
		return ""
	}
	if strings.TrimSpace(m.CatalogID) != "" {
		return normalizeCursorModelID(m.CatalogID)
	}
	return m.computeCatalogID()
}

// LegacyCursorModelNames returns old BYOK/raw names accepted for routing. This
// keeps previously selected Cursor models usable after migrating to catalog ids.
func (m *ModelAdapter) LegacyCursorModelNames() []string {
	if m == nil {
		return nil
	}
	seen := map[string]bool{}
	names := []string{}
	add := func(value string) {
		value = normalizeCursorModelID(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		names = append(names, value)
	}
	add(m.CursorModelName())
	if catalog := m.CursorModelName(); catalog != "" {
		add(catalog + "-max")
	}
	add(m.CursorModelID)
	add(m.ModelID)
	if normalized := normalizeCursorModelID(m.CursorModelID); normalized != "" {
		add("byok/" + normalized)
	}
	if normalized := normalizeCursorModelID(m.ModelID); normalized != "" {
		add("byok/" + normalized)
	}
	return names
}

func (m *ModelAdapter) matchesCursorModelName(cursorModel string) bool {
	cursorModel = normalizeCursorModelID(cursorModel)
	if cursorModel == "" {
		return false
	}
	for _, name := range m.LegacyCursorModelNames() {
		if name == cursorModel {
			return true
		}
	}
	return false
}

func (m *ModelAdapter) computeCatalogID() string {
	seed := strings.Join([]string{
		strings.TrimSpace(m.Type),
		strings.TrimRight(strings.TrimSpace(m.BaseURL), "/"),
		normalizeCursorModelID(m.ModelID),
		strings.TrimSpace(m.DisplayName),
		normalizeCursorModelID(m.CursorModelID),
	}, "\x00")
	if strings.Trim(seed, "\x00") == "" {
		seed = "cursor-local-assistant-model"
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:8])
}

func normalizeCursorModelID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "byok/")
	return value
}

// Normalize fills defaults for user-facing model configuration. The UI keeps
// model creation intentionally small, so backend routing must own these values.
func (m *ModelAdapter) Normalize() {
	if m == nil {
		return
	}
	m.Type = strings.ToLower(strings.TrimSpace(m.Type))
	if m.Type == "" {
		m.Type = "openai"
	}
	m.DisplayName = strings.TrimSpace(m.DisplayName)
	m.ModelID = strings.TrimSpace(m.ModelID)
	m.CursorModelID = strings.TrimSpace(m.CursorModelID)
	m.BaseURL = strings.TrimRight(strings.TrimSpace(m.BaseURL), "/")
	if strings.HasSuffix(strings.ToLower(m.BaseURL), "/v1") {
		m.BaseURL = strings.TrimRight(m.BaseURL[:len(m.BaseURL)-3], "/")
	}
	m.APIKey = strings.TrimSpace(m.APIKey)
	m.Endpoint = normalizeProviderEndpoint(m.Type, m.Endpoint)
	m.ExtraParamsJSON = strings.TrimSpace(m.ExtraParamsJSON)

	switch m.Type {
	case "anthropic":
		m.ThinkingLevel = normalizeThinkingLevel(m.Type, m.ThinkingLevel)
		if m.BaseURL == "" {
			m.BaseURL = "https://api.anthropic.com"
		}
		if m.Endpoint == "" {
			m.Endpoint = "/v1/messages"
		}
		if m.MaxTokens <= 0 {
			m.MaxTokens = 4096
		}
	case "openai":
		fallthrough
	default:
		m.Type = "openai"
		m.ThinkingLevel = normalizeThinkingLevel(m.Type, m.ThinkingLevel)
		if m.BaseURL == "" {
			m.BaseURL = "https://api.openai.com"
		}
		if m.Endpoint == "" {
			m.Endpoint = "/v1/responses"
		}
	}
	if m.ContextWindow <= 0 {
		m.ContextWindow = 200000
	}
	if m.Temperature == 0 {
		m.Temperature = 0.7
	}
	if m.DisplayName == "" {
		m.DisplayName = m.ModelID
	}
	m.SupportsCmdK = true
	m.EnsureCatalogID()
}

func normalizeProviderEndpoint(provider string, endpoint string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		if provider == "anthropic" {
			return "/v1/messages"
		}
		return "/v1/responses"
	}
	clean := "/" + strings.TrimLeft(endpoint, "/")
	key := strings.ToLower(clean)
	switch provider {
	case "anthropic":
		switch key {
		case "/messages", "/v1/messages":
			return "/v1/messages"
		default:
			return clean
		}
	default:
		switch key {
		case "/responses", "/v1/responses":
			return "/v1/responses"
		case "/chat/completions", "/v1/chat/completions":
			return "/v1/chat/completions"
		default:
			return clean
		}
	}
}

func normalizeThinkingLevel(provider string, level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "off", "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(level))
	case "max":
		if provider == "anthropic" {
			return "max"
		}
		return "xhigh"
	case "xhigh", "x_high", "x-high", "very_high", "极高", "very high":
		if provider == "anthropic" {
			return "max"
		}
		return "xhigh"
	default:
		return "medium"
	}
}

func (m *ModelAdapter) APIURL() string {
	if m == nil {
		return ""
	}
	baseURL := strings.TrimRight(strings.TrimSpace(m.BaseURL), "/")
	endpoint := normalizeProviderEndpoint(m.Type, m.Endpoint)
	if baseURL == "" {
		return endpoint
	}
	if strings.HasSuffix(strings.ToLower(baseURL), "/v1") && strings.HasPrefix(strings.ToLower(endpoint), "/v1/") {
		endpoint = strings.TrimPrefix(endpoint, "/v1")
	}
	return baseURL + "/" + strings.TrimLeft(endpoint, "/")
}

func (m *ModelAdapter) ApplyExtraParams(target map[string]any) error {
	if m == nil || !m.ExtraParamsEnabled || strings.TrimSpace(m.ExtraParamsJSON) == "" {
		return nil
	}
	var extra map[string]any
	if err := json.Unmarshal([]byte(m.ExtraParamsJSON), &extra); err != nil {
		return fmt.Errorf("invalid extra params JSON: %w", err)
	}
	for key, value := range extra {
		target[key] = value
	}
	return nil
}

// handleSelfImplementedSSE handles SSE requests using BYOK models
func (g *Gateway) handleSelfImplementedSSE(w http.ResponseWriter, r *http.Request, body []byte, modelID string) {
	adapter := g.findAdapterByCursorName(modelID)
	if adapter == nil {
		http.Error(w, "Model adapter not found", http.StatusNotFound)
		return
	}

	// Parse request
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Route to appropriate provider
	switch adapter.Type {
	case "openai":
		g.handleOpenAISSE(w, r, req, adapter)
	case "anthropic":
		g.handleAnthropicSSE(w, r, req, adapter)
	default:
		http.Error(w, "Unsupported provider type", http.StatusBadRequest)
	}
}

// handleOpenAISSE handles OpenAI API requests
func (g *Gateway) handleOpenAISSE(w http.ResponseWriter, r *http.Request, req map[string]interface{}, adapter *ModelAdapter) {
	// Convert to OpenAI format
	messages, _ := req["messages"].([]interface{})

	openAIReq := map[string]interface{}{
		"model":       adapter.ModelID,
		"messages":    messages,
		"stream":      true,
		"temperature": adapter.Temperature,
	}

	if adapter.MaxTokens > 0 {
		openAIReq["max_tokens"] = adapter.MaxTokens
	}

	// Marshal request
	reqBody, err := json.Marshal(openAIReq)
	if err != nil {
		http.Error(w, "Failed to marshal request", http.StatusInternalServerError)
		return
	}

	// Create request to OpenAI
	endpoint := adapter.Endpoint
	if endpoint == "" {
		endpoint = "/chat/completions"
	}
	apiURL := strings.TrimRight(adapter.BaseURL, "/") + "/" + strings.TrimLeft(endpoint, "/")
	apiReq, err := http.NewRequest("POST", apiURL, bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}

	apiReq.Header.Set("Content-Type", "application/json")
	apiReq.Header.Set("Authorization", "Bearer "+adapter.APIKey)

	// Send request
	client := &http.Client{}
	resp, err := client.Do(apiReq)
	if err != nil {
		log.Printf("[Gateway] OpenAI API error: %v", err)
		http.Error(w, "API request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Stream response
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Read and forward SSE stream
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// Forward SSE data
		fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()

		// Check for stream end
		if strings.HasPrefix(line, "data: [DONE]") {
			break
		}
	}
}

// handleAnthropicSSE handles Anthropic API requests
func (g *Gateway) handleAnthropicSSE(w http.ResponseWriter, r *http.Request, req map[string]interface{}, adapter *ModelAdapter) {
	// Convert to Anthropic format
	messages, _ := req["messages"].([]interface{})

	anthropicReq := map[string]interface{}{
		"model":       adapter.ModelID,
		"messages":    messages,
		"stream":      true,
		"max_tokens":  adapter.MaxTokens,
		"temperature": adapter.Temperature,
	}

	// Marshal request
	reqBody, err := json.Marshal(anthropicReq)
	if err != nil {
		http.Error(w, "Failed to marshal request", http.StatusInternalServerError)
		return
	}

	// Create request to Anthropic
	apiURL := adapter.BaseURL + "/v1/messages"
	apiReq, err := http.NewRequest("POST", apiURL, bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}

	apiReq.Header.Set("Content-Type", "application/json")
	apiReq.Header.Set("x-api-key", adapter.APIKey)
	apiReq.Header.Set("anthropic-version", "2023-06-01")

	// Send request
	client := &http.Client{}
	resp, err := client.Do(apiReq)
	if err != nil {
		log.Printf("[Gateway] Anthropic API error: %v", err)
		http.Error(w, "API request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Stream response
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Read and forward SSE stream
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// Forward SSE data
		fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()

		// Check for stream end
		if strings.HasPrefix(line, "event: message_stop") {
			break
		}
	}
}
