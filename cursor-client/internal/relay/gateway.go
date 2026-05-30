package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"cursor-client/internal/database"

	agentv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/agent/v1"
)

// RouteMode defines how requests are handled
type RouteMode string

const (
	RouteDirect          RouteMode = "direct"           // Forward directly to Cursor
	RouteRelay           RouteMode = "relay"            // Through relay gateway
	RouteSelfImplemented RouteMode = "self-implemented" // Use BYOK models
)

// Gateway handles request routing and forwarding
type Gateway struct {
	mu sync.RWMutex

	// Configuration
	baseURL         string // Upstream Cursor server URL
	modelAdapters   map[string]*ModelAdapter
	defaultMode     RouteMode
	onEvent         func(Event)
	requestSeq      int64
	pendingRequests map[*http.Request]pendingRequest
	agentSessions   map[string]*agentSessionState
	agentExecSeq    uint32
	agentKVSeq      uint32
	agentExecs      map[string]chan *agentv1.ExecClientMessage
	agentKVGets     map[uint32]chan []byte
	agentKVBlobs    map[string]agentStoredKVBlob
	fileSyncCache   map[string]fileSyncCacheEntry
	agentStateDir   string
	db              *database.DB // SQLite database for persistence
	contextStore    *ContextStore // Unified context management
}

// Config holds gateway configuration
type Config struct {
	BaseURL       string
	ModelAdapters []*ModelAdapter
	DefaultMode   RouteMode
	OnEvent       func(Event)
	StateDir      string
	Database      *database.DB // Optional database for persistence
}

// NewGateway creates a new relay gateway
func NewGateway(cfg Config) *Gateway {
	adapters := make(map[string]*ModelAdapter)
	for _, adapter := range cfg.ModelAdapters {
		if adapter == nil || adapter.ModelID == "" {
			continue
		}
		adapter.Normalize()
		adapters[adapter.ModelID] = adapter
	}

	if cfg.DefaultMode == "" {
		cfg.DefaultMode = RouteDirect
	}

	return &Gateway{
		baseURL:         cfg.BaseURL,
		modelAdapters:   adapters,
		defaultMode:     cfg.DefaultMode,
		onEvent:         cfg.OnEvent,
		pendingRequests: make(map[*http.Request]pendingRequest),
		agentSessions:   make(map[string]*agentSessionState),
		agentExecs:      make(map[string]chan *agentv1.ExecClientMessage),
		agentKVGets:     make(map[uint32]chan []byte),
		agentKVBlobs:    make(map[string]agentStoredKVBlob),
		fileSyncCache:   make(map[string]fileSyncCacheEntry),
		agentStateDir:   cfg.StateDir,
		db:              cfg.Database,
		contextStore:    NewContextStore(""), // Workspace root is set dynamically per request
	}
}

// EventType identifies a gateway event used by the UI statistics layer.
type EventType string

const (
	EventRequest        EventType = "request"
	EventBYOKRouted     EventType = "byok_routed"
	EventBYOKCacheHit   EventType = "byok_cache_hit"
	EventBYOKFailure    EventType = "byok_failure"
	EventBYOKSuccess    EventType = "byok_success"
	EventAvailablePatch EventType = "available_models_patch"
	EventTokens         EventType = "tokens"
	EventHTTPExchange   EventType = "http_exchange"
)

// Event is a compact telemetry event emitted by the relay gateway.
type Event struct {
	Type             EventType
	ID               int64
	Method           string
	Host             string
	Path             string
	Route            string
	Model            string
	StatusCode       int
	DurationMs       int64
	Handled          bool
	BYOK             bool
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	EstimatedCost    float64
	Error            string
	At               time.Time
}

type pendingRequest struct {
	ID      int64
	Started time.Time
	Method  string
	Host    string
	Path    string
	Route   string
	Model   string
}

func (g *Gateway) emit(event Event) {
	g.mu.RLock()
	handler := g.onEvent
	g.mu.RUnlock()
	if handler == nil {
		return
	}
	if event.At.IsZero() {
		event.At = time.Now()
	}
	handler(event)
}

func (g *Gateway) beginHTTPRequest(req *http.Request) bool {
	if !isCursorRequest(req) {
		return false
	}
	now := time.Now()

	g.mu.Lock()
	g.requestSeq++
	pending := pendingRequest{
		ID:      g.requestSeq,
		Started: now,
		Method:  req.Method,
		Host:    requestHost(req),
		Path:    requestPath(req),
		Route:   "official",
	}
	g.pendingRequests[req] = pending
	g.mu.Unlock()

	g.emit(Event{Type: EventRequest, At: now})
	return true
}

func (g *Gateway) completeHTTPRequest(req *http.Request, statusCode int, route string, handled bool, byok bool, model string, errText string) {
	if req == nil {
		return
	}
	now := time.Now()

	g.mu.Lock()
	pending, ok := g.pendingRequests[req]
	if ok {
		delete(g.pendingRequests, req)
	}
	g.mu.Unlock()
	if !ok {
		return
	}

	if route == "" {
		route = pending.Route
	}
	if model == "" {
		model = pending.Model
	}

	g.emit(Event{
		Type:       EventHTTPExchange,
		ID:         pending.ID,
		Method:     pending.Method,
		Host:       pending.Host,
		Path:       pending.Path,
		Route:      route,
		Model:      model,
		StatusCode: statusCode,
		DurationMs: now.Sub(pending.Started).Milliseconds(),
		Handled:    handled,
		BYOK:       byok,
		Error:      errText,
		At:         now,
	})
}

func requestHost(req *http.Request) string {
	if req == nil {
		return ""
	}
	if req.Host != "" {
		return req.Host
	}
	if req.URL != nil {
		return req.URL.Host
	}
	return ""
}

func requestPath(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	path := req.URL.Path
	if req.URL.RawQuery != "" {
		path += "?" + req.URL.RawQuery
	}
	if len(path) > 240 {
		return path[:240] + "..."
	}
	return path
}

// ServeHTTP implements http.Handler
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("[Gateway] %s %s", r.Method, r.URL.Path)
	g.beginHTTPRequest(r)
	if resp, handled := g.tryHandleLocalRPC(r); handled {
		writeHTTPResponse(w, resp)
		return
	}
	if resp, handled := g.tryHandleAgentBidi(r); handled {
		writeHTTPResponse(w, resp)
		return
	}

	if byokChatMethods[r.URL.Path] {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			g.completeHTTPRequest(r, http.StatusBadRequest, "byok/local", true, true, "", "failed to read request")
			http.Error(w, "failed to read request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
		if g.tryHandleBYOKChat(w, r, body) {
			return
		}
		g.forwardToUpstream(w, r, body)
		return
	}

	switch {
	case strings.HasPrefix(r.URL.Path, "/api/models"):
		g.handleListModels(w, r)
	default:
		g.handleDirect(w, r)
	}
}

func writeHTTPResponse(w http.ResponseWriter, resp *http.Response) {
	if resp == nil {
		http.Error(w, "empty response", http.StatusInternalServerError)
		return
	}
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	status := resp.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if resp.Body == nil {
		return
	}
	defer resp.Body.Close()
	copyHTTPResponseBody(w, resp)
}

func copyHTTPResponseBody(w http.ResponseWriter, resp *http.Response) {
	if shouldFlushHTTPResponse(resp) {
		copyHTTPResponseBodyFlush(w, resp.Body)
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

func shouldFlushHTTPResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		return true
	}
	if strings.Contains(contentType, "grpc") || strings.Contains(contentType, "connect") {
		return true
	}
	for _, value := range resp.Header.Values("Transfer-Encoding") {
		if strings.Contains(strings.ToLower(value), "chunked") {
			return true
		}
	}
	return resp.ContentLength < 0
}

func copyHTTPResponseBodyFlush(w http.ResponseWriter, body io.Reader) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

// handleListModels returns available models
func (g *Gateway) handleListModels(w http.ResponseWriter, r *http.Request) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	models := []map[string]interface{}{}

	// Add BYOK models
	for id, adapter := range g.modelAdapters {
		models = append(models, map[string]interface{}{
			"id":         adapter.CursorModelName(),
			"name":       adapter.DisplayName,
			"provider":   adapter.Type,
			"max_tokens": adapter.MaxTokens,
			"target":     id,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"models": models,
	})
}

// handleDirect forwards request directly to upstream
func (g *Gateway) handleDirect(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close()

	g.forwardToUpstream(w, r, body)
}

// forwardToUpstream forwards request to upstream Cursor server
func (g *Gateway) forwardToUpstream(w http.ResponseWriter, r *http.Request, body []byte) {
	// Build upstream URL
	upstreamURL := g.baseURL + r.URL.Path
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	// Create new request
	req, err := http.NewRequest(r.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		g.completeHTTPRequest(r, http.StatusInternalServerError, "official/upstream", false, false, "", err.Error())
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}

	// Copy headers
	for k, v := range r.Header {
		req.Header[k] = v
	}

	// Send request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[Gateway] Upstream error: %v", err)
		g.completeHTTPRequest(r, http.StatusBadGateway, "official/upstream", false, false, "", err.Error())
		http.Error(w, "Upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	g.completeHTTPRequest(r, resp.StatusCode, "official/upstream", false, false, "", "")

	// Copy response headers
	for k, v := range resp.Header {
		w.Header()[k] = v
	}

	// Copy status code
	w.WriteHeader(resp.StatusCode)

	// Copy response body
	io.Copy(w, resp.Body)
}

// getRouteMode determines routing mode for a model
func (g *Gateway) getRouteMode(modelID string) RouteMode {
	g.mu.RLock()
	defer g.mu.RUnlock()

	for _, adapter := range g.modelAdapters {
		if adapter.matchesCursorModelName(modelID) {
			return RouteSelfImplemented
		}
	}

	return g.defaultMode
}

// UpdateModelAdapters updates the model adapter configuration
func (g *Gateway) UpdateModelAdapters(adapters []*ModelAdapter) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.modelAdapters = make(map[string]*ModelAdapter)
	for _, adapter := range adapters {
		if adapter == nil || adapter.ModelID == "" {
			continue
		}
		adapter.Normalize()
		g.modelAdapters[adapter.ModelID] = adapter
	}
}

// SetBaseURL updates the upstream base URL
func (g *Gateway) SetBaseURL(url string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.baseURL = url
}
