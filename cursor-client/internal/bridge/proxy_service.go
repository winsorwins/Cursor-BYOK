package bridge

import (
	"bufio"
	"bytes"
	"cursor-client/internal/certs"
	appconfig "cursor-client/internal/config"
	"cursor-client/internal/cursor"
	"cursor-client/internal/database"
	"cursor-client/internal/mitm"
	"cursor-client/internal/relay"
	appruntime "cursor-client/internal/runtime"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const localProxyURL = "http://127.0.0.1:18080"

const requestLogLimit = 100

// ProxyService provides proxy control to the frontend
type ProxyService struct {
	mu sync.RWMutex

	proxy   *mitm.ProxyServer
	gateway *relay.Gateway
	ca      *certs.CA
	paths   *appruntime.Paths
	cursor  *cursor.SettingsManager
	db      *database.DB

	config *appconfig.UserConfig

	// Request statistics
	stats       RuntimeStats
	requestLogs []RequestLogEntry
	trustStatus certs.TrustStatus

	// Tray manager (Windows only)
	trayMgr interface {
		IsCreated() bool
		IsAvailable() bool
		IsFailed() bool
		GetLastError() string
		UpdateProxyState(running bool)
	}

	// Window visible state getter
	getWindowVisible func() bool
}

// ProxyState represents the current proxy state
type ProxyState struct {
	Running      bool              `json:"running"`
	Address      string            `json:"address"`
	BaseURL      string            `json:"baseURL"`
	RequestCount int               `json:"requestCount"`
	LastRequest  string            `json:"lastRequest"` // ISO 8601 format
	DataDir      string            `json:"dataDir"`
	CursorPath   string            `json:"cursorPath"`
	LastError    string            `json:"lastError"`
	Stats        RuntimeStats      `json:"stats"`
	Trust        certs.TrustStatus `json:"trust"`
}

// RuntimeStats holds lightweight in-memory statistics for the dashboard.
type RuntimeStats struct {
	TotalRequests       int     `json:"totalRequests"`
	BYOKRequests        int     `json:"byokRequests"`
	SuccessfulDialogs   int     `json:"successfulDialogs"`
	FailedDialogs       int     `json:"failedDialogs"`
	FailedRequests      int     `json:"failedRequests"`
	AvailableModelPatch int     `json:"availableModelPatch"`
	CacheHits           int     `json:"cacheHits"`
	CacheMisses         int     `json:"cacheMisses"`
	CacheReadTokens     int     `json:"cacheReadTokens"`
	CacheWriteTokens    int     `json:"cacheWriteTokens"`
	PromptTokens        int     `json:"promptTokens"`
	CompletionTokens    int     `json:"completionTokens"`
	TotalTokens         int     `json:"totalTokens"`
	EstimatedCost       float64 `json:"estimatedCost"`
	LastModel           string  `json:"lastModel"`
	LastError           string  `json:"lastError"`
	LastRequest         string  `json:"lastRequest"` // ISO 8601 format
}

// RequestLogEntry is a compact record used for live Cursor integration debugging.
type RequestLogEntry struct {
	ID         int64  `json:"id"`
	Time       string `json:"time"` // ISO 8601 format
	Method     string `json:"method"`
	Host       string `json:"host"`
	Path       string `json:"path"`
	Route      string `json:"route"`
	Model      string `json:"model"`
	StatusCode int    `json:"statusCode"`
	DurationMs int64  `json:"durationMs"`
	Handled    bool   `json:"handled"`
	BYOK       bool   `json:"byok"`
	Error      string `json:"error"`
}

// NewProxyService creates a new proxy service
func NewProxyService() *ProxyService {
	paths, err := appruntime.ResolvePaths()
	if err != nil {
		log.Printf("[Bridge] Failed to resolve paths: %v", err)
	}

	cfg := appconfig.DefaultUserConfig()
	if paths != nil {
		loaded, err := appconfig.Load(paths.ConfigPath)
		if err != nil {
			log.Printf("[Bridge] Failed to load config: %v", err)
		} else {
			cfg = loaded
		}
		if err := appconfig.Save(paths.ConfigPath, cfg); err != nil {
			log.Printf("[Bridge] Failed to persist normalized config: %v", err)
		}
	}

	var cursorSettings *cursor.SettingsManager
	if paths != nil {
		cursorSettings, err = cursor.NewSettingsManager(paths.CursorBackup)
		if err != nil {
			log.Printf("[Bridge] Failed to initialize Cursor settings manager: %v", err)
		}
	}

	// Initialize database
	var db *database.DB
	if paths != nil {
		dbPath := filepath.Join(paths.DataDir, "conversations.db")
		db, err = database.Open(database.Config{Path: dbPath})
		if err != nil {
			log.Printf("[Bridge] Failed to open database: %v", err)
		} else {
			log.Printf("[Bridge] Database initialized at %s", dbPath)
		}
	}

	service := &ProxyService{
		paths:  paths,
		cursor: cursorSettings,
		config: cfg,
		db:     db,
	}
	service.restoreRuntimeState()
	return service
}

// EnsureCATrustOnStartup installs the local CA trust in the background during
// a real app startup. It is intentionally not called from NewProxyService so
// Wails binding generation and builds do not trigger OS certificate prompts.
func (s *ProxyService) EnsureCATrustOnStartup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureCATrustLocked()
}

// RepairCursorBootstrapCacheOnStartup fixes Cursor's cached Statsig bootstrap
// before Cursor extensions read it during their early startup.
func (s *ProxyService) RepairCursorBootstrapCacheOnStartup() {
	statsigRepaired, adminRepaired, err := cursor.RepairStartupCaches()
	if err != nil {
		log.Printf("[Bridge] Failed to repair Cursor startup caches: %v", err)
		return
	}
	if statsigRepaired {
		log.Printf("[Bridge] Repaired Cursor Statsig bootstrap cache")
	}
	if adminRepaired {
		log.Printf("[Bridge] Repaired Cursor admin settings cache")
	}
}

// RestoreProxyOnStartup starts the local proxy when Cursor is already pointed at
// it, which can happen after a crash, rebuild, or forced process exit.
func (s *ProxyService) RestoreProxyOnStartup() {
	if s.cursor == nil {
		return
	}
	usingProxy, err := s.cursor.UsesProxy(localProxyURL)
	if err != nil {
		log.Printf("[Bridge] Failed to check Cursor proxy settings: %v", err)
		return
	}
	if !usingProxy {
		return
	}
	if _, err := s.StartProxy(); err != nil {
		log.Printf("[Bridge] Failed to restore proxy on startup: %v", err)
		return
	}
	// Update tray menu state after successful restore
	s.mu.Lock()
	if s.trayMgr != nil {
		s.trayMgr.UpdateProxyState(true)
	}
	s.mu.Unlock()
}

// StartProxy starts the proxy server
func (s *ProxyService) StartProxy() (*ProxyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.proxy != nil && s.proxy.IsRunning() {
		return s.getState(), nil
	}

	if statsigRepaired, adminRepaired, err := cursor.RepairStartupCaches(); err != nil {
		log.Printf("[Bridge] Failed to repair Cursor startup caches: %v", err)
	} else {
		if statsigRepaired {
			log.Printf("[Bridge] Repaired Cursor Statsig bootstrap cache")
		}
		if adminRepaired {
			log.Printf("[Bridge] Repaired Cursor admin settings cache")
		}
	}

	// Create gateway
	s.gateway = relay.NewGateway(relay.Config{
		BaseURL:       s.config.BaseURL,
		ModelAdapters: s.config.ModelAdapters,
		DefaultMode:   relay.RouteDirect,
		OnEvent:       s.handleGatewayEvent,
		StateDir:      s.dataDir(),
		Database:      s.db,
	})

	if s.ca == nil {
		ca, err := s.loadCA()
		if err != nil {
			return nil, err
		}
		s.ca = ca
	}

	// Create proxy
	proxy, err := mitm.NewProxyServer(mitm.Config{
		Addr:    "127.0.0.1:18080",
		Handler: s.gateway,
		CA:      s.ca,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create proxy: %w", err)
	}

	// Start proxy
	if err := proxy.Start(); err != nil {
		return nil, fmt.Errorf("failed to start proxy: %w", err)
	}

	s.proxy = proxy

	if s.cursor != nil {
		if err := s.cursor.ApplyProxy(localProxyURL); err != nil {
			log.Printf("[Bridge] Failed to apply Cursor proxy settings: %v", err)
		}
	}

	// Update tray menu state
	if s.trayMgr != nil {
		s.trayMgr.UpdateProxyState(true)
	}

	log.Println("[Bridge] Proxy started successfully")

	return s.getState(), nil
}

// StopProxy stops the proxy server
func (s *ProxyService) StopProxy() (*ProxyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.proxy == nil || !s.proxy.IsRunning() {
		return s.getState(), nil
	}

	if err := s.proxy.Stop(); err != nil {
		return nil, fmt.Errorf("failed to stop proxy: %w", err)
	}

	if s.cursor != nil {
		if err := s.cursor.RestoreProxy(); err != nil {
			return nil, fmt.Errorf("failed to restore Cursor settings: %w", err)
		}
	}

	// Update tray menu state
	if s.trayMgr != nil {
		s.trayMgr.UpdateProxyState(false)
	}

	// Note: Database connection is NOT closed here as it's a long-lived connection
	// that should be reused across proxy restarts. It will be closed when the
	// application exits (handled by the application lifecycle).

	log.Println("[Bridge] Proxy stopped")

	return s.getState(), nil
}

// GetState returns the current proxy state
func (s *ProxyService) GetState() *ProxyState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getState()
}

func (s *ProxyService) getState() *ProxyState {
	running := s.proxy != nil && s.proxy.IsRunning()
	stats := s.stats
	stats.LastModel = s.displayModelName(stats.LastModel)

	lastRequest := ""
	if stats.LastRequest != "" {
		lastRequest = stats.LastRequest
	}

	return &ProxyState{
		Running:      running,
		Address:      "127.0.0.1:18080",
		BaseURL:      s.config.BaseURL,
		RequestCount: s.stats.TotalRequests,
		LastRequest:  lastRequest,
		DataDir:      s.dataDir(),
		CursorPath:   s.cursorPath(),
		LastError:    s.stats.LastError,
		Stats:        stats,
		Trust:        s.trustStatus,
	}
}

func (s *ProxyService) displayModelName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if s == nil || s.config == nil {
		return raw
	}

	normalizedRaw := normalizeDashboardModelName(raw)
	for _, adapter := range s.config.ModelAdapters {
		if adapter == nil {
			continue
		}
		names := []string{
			adapter.CursorModelName(),
			adapter.CatalogID,
			adapter.CursorModelID,
			adapter.ModelID,
		}
		for _, name := range names {
			if normalizeDashboardModelName(name) != normalizedRaw {
				continue
			}
			if displayName := strings.TrimSpace(adapter.DisplayName); displayName != "" {
				return displayName
			}
			if modelID := strings.TrimSpace(adapter.ModelID); modelID != "" {
				return modelID
			}
			return raw
		}
	}
	return raw
}

func normalizeDashboardModelName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "byok/")
	value = strings.TrimSuffix(value, "-max")
	return value
}

func (s *ProxyService) ensureCATrustLocked() {
	ca, err := s.loadCA()
	if err != nil {
		s.trustStatus = certs.TrustStatus{Store: "CurrentUser\\Root", Error: err.Error()}
		return
	}
	s.ca = ca
	s.trustStatus = ca.EnsureTrusted()
	if s.trustStatus.Error != "" {
		log.Printf("[Bridge] Failed to trust CA automatically: %s", s.trustStatus.Error)
	} else if s.trustStatus.Installed {
		log.Printf("[Bridge] CA installed into %s", s.trustStatus.Store)
	}
}

func (s *ProxyService) handleGatewayEvent(event relay.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if event.At.IsZero() {
		event.At = time.Now()
	}

	s.stats.LastRequest = event.At.Format(time.RFC3339)
	if event.Model != "" {
		s.stats.LastModel = s.displayModelName(event.Model)
	}

	switch event.Type {
	case relay.EventRequest:
		s.stats.TotalRequests++
	case relay.EventBYOKRouted:
		s.stats.BYOKRequests++
		s.stats.LastError = ""
	case relay.EventBYOKCacheHit:
		s.stats.BYOKRequests++
		s.stats.CacheHits++
		s.stats.LastError = ""
	case relay.EventBYOKFailure:
		s.stats.FailedRequests++
		s.stats.FailedDialogs++
		s.stats.LastError = event.Error
	case relay.EventBYOKSuccess:
		s.stats.SuccessfulDialogs++
		s.stats.LastError = ""
	case relay.EventAvailablePatch:
		s.stats.AvailableModelPatch++
	case relay.EventTokens:
		s.stats.PromptTokens += event.PromptTokens
		s.stats.CompletionTokens += event.CompletionTokens
		s.stats.CacheReadTokens += event.CacheReadTokens
		s.stats.CacheWriteTokens += event.CacheWriteTokens
		if event.PromptTokens > 0 {
			if event.CacheReadTokens > 0 {
				s.stats.CacheHits++
			} else {
				s.stats.CacheMisses++
			}
		}
		s.stats.TotalTokens = s.stats.PromptTokens + s.stats.CompletionTokens
		s.stats.EstimatedCost += event.EstimatedCost
	case relay.EventHTTPExchange:
		s.addRequestLogLocked(event)
	}
	s.saveRuntimeStatsLocked()
}

func (s *ProxyService) addRequestLogLocked(event relay.Event) {
	entry := RequestLogEntry{
		ID:         event.ID,
		Time:       event.At.Format(time.RFC3339),
		Method:     event.Method,
		Host:       event.Host,
		Path:       event.Path,
		Route:      event.Route,
		Model:      s.displayModelName(event.Model),
		StatusCode: event.StatusCode,
		DurationMs: event.DurationMs,
		Handled:    event.Handled,
		BYOK:       event.BYOK,
		Error:      event.Error,
	}
	s.requestLogs = append([]RequestLogEntry{entry}, s.requestLogs...)
	if len(s.requestLogs) > requestLogLimit {
		s.requestLogs = s.requestLogs[:requestLogLimit]
	}
	s.appendRequestLogFileLocked(entry)
}

func (s *ProxyService) appendRequestLogFileLocked(entry RequestLogEntry) {
	if s.paths == nil || s.paths.LogDir == "" {
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	path := filepath.Join(s.paths.LogDir, "requests.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("[Bridge] Failed to open request log %s: %v", path, err)
		return
	}
	defer file.Close()
	_, _ = file.Write(append(data, '\n'))
}

// SetBaseURL updates the upstream base URL
func (s *ProxyService) SetBaseURL(url string) (*ProxyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.config.BaseURL = url

	if s.gateway != nil {
		s.gateway.SetBaseURL(url)
	}

	log.Printf("[Bridge] Base URL updated to: %s", url)

	return s.getState(), nil
}

// LoadUserConfig loads user configuration
func (s *ProxyService) LoadUserConfig() (*appconfig.UserConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.config, nil
}

// SaveUserConfig saves user configuration
func (s *ProxyService) SaveUserConfig(cfg *appconfig.UserConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.config = cfg
	if s.config.BaseURL == "" {
		s.config.BaseURL = "https://api2.cursor.sh"
	}
	if s.config.ModelAdapters == nil {
		s.config.ModelAdapters = []*relay.ModelAdapter{}
	}
	for _, adapter := range s.config.ModelAdapters {
		if adapter != nil {
			adapter.EnsureCatalogID()
		}
	}
	if err := s.saveConfigLocked(); err != nil {
		return err
	}

	// Update gateway if running
	if s.gateway != nil {
		s.gateway.SetBaseURL(cfg.BaseURL)
		s.gateway.UpdateModelAdapters(cfg.ModelAdapters)
	}

	log.Println("[Bridge] User config saved")

	return nil
}

// GetCACertificate returns the CA certificate in PEM format
func (s *ProxyService) GetCACertificate() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ca == nil {
		ca, err := s.loadCA()
		if err != nil {
			return "", err
		}
		s.ca = ca
	}

	certPEM, _, err := s.ca.ToPEM()
	if err != nil {
		return "", fmt.Errorf("failed to get CA certificate: %w", err)
	}

	return string(certPEM), nil
}

// EnsureCACertificateTrusted installs the local CA into the OS trust store.
func (s *ProxyService) EnsureCACertificateTrusted() (*certs.TrustStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ca == nil {
		ca, err := s.loadCA()
		if err != nil {
			return nil, err
		}
		s.ca = ca
	}
	status := s.ca.EnsureTrusted()
	s.trustStatus = status
	return &status, nil
}

// GetCACertificateTrustStatus returns the current CA trust status.
func (s *ProxyService) GetCACertificateTrustStatus() (*certs.TrustStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ca == nil {
		ca, err := s.loadCA()
		if err != nil {
			return nil, err
		}
		s.ca = ca
	}
	status := s.ca.TrustStatus()
	s.trustStatus = status
	return &status, nil
}

// AddModelAdapter adds a new model adapter
func (s *ProxyService) AddModelAdapter(adapter *relay.ModelAdapter) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if adapter.ModelID == "" {
		return fmt.Errorf("modelID is required")
	}
	adapter.Normalize()

	replaced := false
	for i, existing := range s.config.ModelAdapters {
		if existing.ModelID == adapter.ModelID {
			s.config.ModelAdapters[i] = adapter
			replaced = true
			break
		}
	}
	if !replaced {
		s.config.ModelAdapters = append(s.config.ModelAdapters, adapter)
	}
	if err := s.saveConfigLocked(); err != nil {
		return err
	}

	if s.gateway != nil {
		s.gateway.UpdateModelAdapters(s.config.ModelAdapters)
	}

	log.Printf("[Bridge] Model adapter added: %s", adapter.DisplayName)

	return nil
}

// TestModelAdapter validates a model adapter against its upstream provider.
func (s *ProxyService) TestModelAdapter(adapter *relay.ModelAdapter) (string, error) {
	if adapter == nil {
		return "", fmt.Errorf("model configuration is required")
	}
	adapter.Normalize()
	if adapter.ModelID == "" {
		return "", fmt.Errorf("modelID is required")
	}
	if adapter.APIKey == "" {
		return "", fmt.Errorf("apiKey is required")
	}

	endpoint := adapter.Endpoint
	apiURL := adapter.APIURL()
	body := map[string]any{}
	if adapter.Type == "anthropic" {
		body = map[string]any{
			"model":      adapter.ModelID,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
			"max_tokens": 8,
			"stream":     false,
		}
	} else if strings.Contains(strings.ToLower(endpoint), "responses") {
		body = map[string]any{
			"model":             adapter.ModelID,
			"input":             "ping",
			"max_output_tokens": 8,
			"stream":            false,
		}
	} else {
		body = map[string]any{
			"model":      adapter.ModelID,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
			"max_tokens": 8,
			"stream":     false,
		}
	}
	if err := adapter.ApplyExtraParams(body); err != nil {
		return "", err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if adapter.Type == "anthropic" {
		req.Header.Set("x-api-key", adapter.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+adapter.APIKey)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("upstream returned %d: %s", resp.StatusCode, string(respBody))
	}
	return fmt.Sprintf("测试通过：%s 返回 %d", adapter.DisplayName, resp.StatusCode), nil
}

// RemoveModelAdapter removes a model adapter by ID
func (s *ProxyService) RemoveModelAdapter(modelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	newAdapters := []*relay.ModelAdapter{}
	for _, adapter := range s.config.ModelAdapters {
		if adapter.ModelID != modelID {
			newAdapters = append(newAdapters, adapter)
		}
	}

	s.config.ModelAdapters = newAdapters
	if err := s.saveConfigLocked(); err != nil {
		return err
	}

	if s.gateway != nil {
		s.gateway.UpdateModelAdapters(s.config.ModelAdapters)
	}

	log.Printf("[Bridge] Model adapter removed: %s", modelID)

	return nil
}

// RestoreCursorSettings restores Cursor settings from the local backup.
func (s *ProxyService) RestoreCursorSettings() (*ProxyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cursor == nil {
		return s.getState(), fmt.Errorf("Cursor settings manager is not initialized")
	}
	if err := s.cursor.RestoreProxy(); err != nil {
		return nil, err
	}
	return s.getState(), nil
}

// ListRequestLogs returns recent proxy request records for diagnostics.
func (s *ProxyService) ListRequestLogs() ([]RequestLogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	logs := make([]RequestLogEntry, len(s.requestLogs))
	copy(logs, s.requestLogs)
	for i := range logs {
		logs[i].Model = s.displayModelName(logs[i].Model)
	}
	return logs, nil
}

// ClearRequestLogs clears recent proxy request records.
func (s *ProxyService) ClearRequestLogs() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.requestLogs = nil
	if s.paths != nil && s.paths.LogDir != "" {
		path := filepath.Join(s.paths.LogDir, "requests.log")
		if err := os.WriteFile(path, nil, 0600); err != nil {
			return err
		}
	}
	return nil
}

func (s *ProxyService) restoreRuntimeState() {
	if s.paths == nil || s.paths.LogDir == "" {
		return
	}
	if stats, err := readRuntimeStats(s.runtimeStatsPath()); err == nil {
		s.stats = stats
	} else {
		s.stats = statsFromRequestLog(s.requestLogPath())
	}
	s.requestLogs = readRecentRequestLogs(s.requestLogPath(), requestLogLimit)
}

func (s *ProxyService) saveRuntimeStatsLocked() {
	if s.paths == nil || s.paths.LogDir == "" {
		return
	}
	data, err := json.MarshalIndent(s.stats, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(s.runtimeStatsPath(), data, 0600); err != nil {
		log.Printf("[Bridge] Failed to persist runtime stats: %v", err)
	}
}

func (s *ProxyService) requestLogPath() string {
	if s.paths == nil {
		return ""
	}
	return filepath.Join(s.paths.LogDir, "requests.log")
}

func (s *ProxyService) runtimeStatsPath() string {
	if s.paths == nil {
		return ""
	}
	return filepath.Join(s.paths.LogDir, "stats.json")
}

func readRuntimeStats(path string) (RuntimeStats, error) {
	var stats RuntimeStats
	if path == "" {
		return stats, os.ErrInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return stats, err
	}
	if err := json.Unmarshal(data, &stats); err != nil {
		return stats, err
	}
	return stats, nil
}

func statsFromRequestLog(path string) RuntimeStats {
	stats := RuntimeStats{}
	logs := readRequestLogChronological(path, 0)
	for _, entry := range logs {
		stats.TotalRequests++
		if entry.BYOK {
			stats.BYOKRequests++
		}
		if entry.StatusCode >= 400 || entry.Error != "" {
			stats.FailedRequests++
			if entry.BYOK {
				stats.FailedDialogs++
			}
			stats.LastError = entry.Error
		} else if entry.BYOK && entry.StatusCode > 0 {
			stats.SuccessfulDialogs++
		}
		if entry.Route == "local/available_models" || entry.Route == "local/available_models_fallback" || entry.Route == "official/available_models_patch" {
			stats.AvailableModelPatch++
		}
		if entry.Model != "" {
			stats.LastModel = entry.Model
		}
		if entry.Time != "" {
			stats.LastRequest = entry.Time
		}
	}
	return stats
}

func readRecentRequestLogs(path string, limit int) []RequestLogEntry {
	logs := readRequestLogChronological(path, limit)
	for left, right := 0, len(logs)-1; left < right; left, right = left+1, right-1 {
		logs[left], logs[right] = logs[right], logs[left]
	}
	return logs
}

func readRequestLogChronological(path string, limit int) []RequestLogEntry {
	if path == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	logs := []RequestLogEntry{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry RequestLogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		logs = append(logs, entry)
		if limit > 0 && len(logs) > limit {
			logs = logs[len(logs)-limit:]
		}
	}
	return logs
}

func (s *ProxyService) loadCA() (*certs.CA, error) {
	if s.paths == nil {
		return certs.NewCA()
	}
	return certs.LoadOrCreateCA(s.paths.CACertPath, s.paths.CAKeyPath)
}

func (s *ProxyService) saveConfigLocked() error {
	if s.paths == nil {
		return nil
	}
	return appconfig.Save(s.paths.ConfigPath, s.config)
}

func (s *ProxyService) dataDir() string {
	if s.paths == nil {
		return ""
	}
	return s.paths.DataDir
}

func (s *ProxyService) cursorPath() string {
	if s.cursor == nil {
		return ""
	}
	return s.cursor.SettingsPath()
}

// ListModelAdapters returns all configured model adapters
func (s *ProxyService) ListModelAdapters() ([]*relay.ModelAdapter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.config.ModelAdapters, nil
}

// FixOptions specifies which issues to fix
type FixOptions struct {
	FixCATrust        bool `json:"fixCATrust"`
	FixCursorProxy    bool `json:"fixCursorProxy"`
	ClearStatsigCache bool `json:"clearStatsigCache"`
	ClearAdminCache   bool `json:"clearAdminCache"`
	RestoreOfficial   bool `json:"restoreOfficial"`
}

// FixResult contains the result of a fix operation
type FixResult struct {
	Success      bool             `json:"success"`
	FixedIssues  []FixedIssue     `json:"fixedIssues"`
	FailedIssues []FixFailure     `json:"failedIssues"`
	BeforeState  DiagnosticsDTO   `json:"beforeState"`
	AfterState   DiagnosticsDTO   `json:"afterState"`
}

// FixedIssue represents a successfully fixed issue
type FixedIssue struct {
	Issue  string `json:"issue"`
	Status string `json:"status"` // "fixed", "already_ok", "skipped"
}

// FixFailure represents a failed fix attempt
type FixFailure struct {
	Issue string `json:"issue"`
	Error string `json:"error"`
}

// FixIssues attempts to fix common integration issues
func (s *ProxyService) FixIssues(options FixOptions) (*FixResult, error) {
	// Validate mutually exclusive options
	if options.FixCursorProxy && options.RestoreOfficial {
		return nil, fmt.Errorf("fixCursorProxy and restoreOfficial are mutually exclusive")
	}

	// Get before state (without holding lock)
	beforeState, err := s.GetDiagnostics()
	if err != nil {
		return nil, fmt.Errorf("failed to get diagnostics before fix: %w", err)
	}

	result := &FixResult{
		Success:      true,
		FixedIssues:  []FixedIssue{},
		FailedIssues: []FixFailure{},
		BeforeState:  *beforeState,
	}

	// Fix CA trust
	if options.FixCATrust {
		if err := s.fixCATrust(result); err != nil {
			log.Printf("[Bridge] CA trust fix failed: %v", err)
		}
	}

	// Fix Cursor proxy settings
	if options.FixCursorProxy {
		if err := s.fixCursorProxy(result); err != nil {
			log.Printf("[Bridge] Cursor proxy fix failed: %v", err)
		}
	}

	// Clear Statsig cache
	if options.ClearStatsigCache {
		s.clearStatsigCache(result)
	}

	// Clear Admin cache
	if options.ClearAdminCache {
		s.clearAdminCache(result)
	}

	// Restore official channel
	if options.RestoreOfficial {
		if err := s.restoreOfficial(result); err != nil {
			log.Printf("[Bridge] Restore official failed: %v", err)
		}
	}

	// Get after state (without holding lock)
	afterState, err := s.GetDiagnostics()
	if err != nil {
		log.Printf("[Bridge] Failed to get diagnostics after fix: %v", err)
		result.FailedIssues = append(result.FailedIssues, FixFailure{
			Issue: "get_after_state",
			Error: err.Error(),
		})
	} else {
		result.AfterState = *afterState
	}

	// Set success to false if any fixes failed
	if len(result.FailedIssues) > 0 {
		result.Success = false
	}

	return result, nil
}

// determineCATrustFixStatus determines the fix status based on before/after trust states.
// This is a pure function for testability.
func determineCATrustFixStatus(beforeTrusted, afterTrusted, afterInstalled bool) (status string, failed bool, errMsg string) {
	if beforeTrusted {
		return "already_ok", false, ""
	}
	if afterTrusted || afterInstalled {
		return "fixed", false, ""
	}
	return "", true, "CA trust status unchanged after fix attempt"
}

func (s *ProxyService) fixCATrust(result *FixResult) error {
	// Check if CA is already trusted before attempting to fix
	s.mu.RLock()
	ca := s.ca
	s.mu.RUnlock()

	if ca == nil {
		result.FailedIssues = append(result.FailedIssues, FixFailure{
			Issue: "ca_trust",
			Error: "CA is not initialized",
		})
		return fmt.Errorf("CA is not initialized")
	}

	// Get trust status before attempting to fix
	beforeStatus := ca.TrustStatus()

	// Attempt to ensure CA is trusted
	afterStatus, err := s.EnsureCACertificateTrusted()
	if err != nil {
		result.FailedIssues = append(result.FailedIssues, FixFailure{
			Issue: "ca_trust",
			Error: err.Error(),
		})
		return err
	}

	// Determine fix status using pure function
	status, failed, errMsg := determineCATrustFixStatus(
		beforeStatus.Trusted,
		afterStatus.Trusted,
		afterStatus.Installed,
	)

	if failed {
		result.FailedIssues = append(result.FailedIssues, FixFailure{
			Issue: "ca_trust",
			Error: errMsg,
		})
		return fmt.Errorf("%s", errMsg)
	}

	result.FixedIssues = append(result.FixedIssues, FixedIssue{
		Issue:  "ca_trust",
		Status: status,
	})
	return nil
}

func (s *ProxyService) fixCursorProxy(result *FixResult) error {
	// Check if proxy is running
	s.mu.RLock()
	running := s.proxy != nil && s.proxy.IsRunning()
	s.mu.RUnlock()

	if !running {
		result.FixedIssues = append(result.FixedIssues, FixedIssue{
			Issue:  "cursor_proxy",
			Status: "skipped",
		})
		return fmt.Errorf("proxy is not running")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cursor == nil {
		result.FailedIssues = append(result.FailedIssues, FixFailure{
			Issue: "cursor_proxy",
			Error: "Cursor settings manager is not initialized",
		})
		return fmt.Errorf("Cursor settings manager is not initialized")
	}

	// Check if already configured
	usesProxy, err := s.cursor.UsesProxy(localProxyURL)
	if err != nil {
		result.FailedIssues = append(result.FailedIssues, FixFailure{
			Issue: "cursor_proxy",
			Error: err.Error(),
		})
		return err
	}

	if usesProxy {
		result.FixedIssues = append(result.FixedIssues, FixedIssue{
			Issue:  "cursor_proxy",
			Status: "already_ok",
		})
		return nil
	}

	// Apply proxy settings
	if err := s.cursor.ApplyProxy(localProxyURL); err != nil {
		result.FailedIssues = append(result.FailedIssues, FixFailure{
			Issue: "cursor_proxy",
			Error: err.Error(),
		})
		return err
	}

	result.FixedIssues = append(result.FixedIssues, FixedIssue{
		Issue:  "cursor_proxy",
		Status: "fixed",
	})
	return nil
}

func (s *ProxyService) clearStatsigCache(result *FixResult) {
	repaired, err := cursor.RepairStatsigBootstrapCache()
	if err != nil {
		result.FailedIssues = append(result.FailedIssues, FixFailure{
			Issue: "statsig_cache",
			Error: err.Error(),
		})
		return
	}

	if repaired {
		result.FixedIssues = append(result.FixedIssues, FixedIssue{
			Issue:  "statsig_cache",
			Status: "fixed",
		})
	} else {
		result.FixedIssues = append(result.FixedIssues, FixedIssue{
			Issue:  "statsig_cache",
			Status: "already_ok",
		})
	}
}

func (s *ProxyService) clearAdminCache(result *FixResult) {
	repaired, err := cursor.RepairAdminSettingsCache()
	if err != nil {
		result.FailedIssues = append(result.FailedIssues, FixFailure{
			Issue: "admin_cache",
			Error: err.Error(),
		})
		return
	}

	if repaired {
		result.FixedIssues = append(result.FixedIssues, FixedIssue{
			Issue:  "admin_cache",
			Status: "fixed",
		})
	} else {
		result.FixedIssues = append(result.FixedIssues, FixedIssue{
			Issue:  "admin_cache",
			Status: "already_ok",
		})
	}
}

func (s *ProxyService) restoreOfficial(result *FixResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cursor == nil {
		result.FailedIssues = append(result.FailedIssues, FixFailure{
			Issue: "restore_official",
			Error: "Cursor settings manager is not initialized",
		})
		return fmt.Errorf("Cursor settings manager is not initialized")
	}

	if err := s.cursor.RestoreProxy(); err != nil {
		result.FailedIssues = append(result.FailedIssues, FixFailure{
			Issue: "restore_official",
			Error: err.Error(),
		})
		return err
	}

	result.FixedIssues = append(result.FixedIssues, FixedIssue{
		Issue:  "restore_official",
		Status: "fixed",
	})
	return nil
}

// setTrayManager sets the tray manager instance (internal use only, not exposed to frontend).
func (s *ProxyService) setTrayManager(mgr interface {
	IsCreated() bool
	IsAvailable() bool
	IsFailed() bool
	GetLastError() string
	UpdateProxyState(running bool)
}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trayMgr = mgr
}

// InjectTrayManager is a package-level function to inject tray manager without exposing to Wails bindings.
func InjectTrayManager(service *ProxyService, mgr interface {
	IsCreated() bool
	IsAvailable() bool
	IsFailed() bool
	GetLastError() string
	UpdateProxyState(running bool)
}) {
	service.setTrayManager(mgr)
}

// InjectWindowVisibleGetter injects a function to get window visible state.
func InjectWindowVisibleGetter(service *ProxyService, getter func() bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.getWindowVisible = getter
}

// GetTrayState returns the current tray state.
func (s *ProxyService) GetTrayState() TrayState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	proxyRunning := s.proxy != nil && s.proxy.IsRunning()
	windowVisible := false
	if s.getWindowVisible != nil {
		windowVisible = s.getWindowVisible()
	}

	if s.trayMgr == nil {
		return TrayState{
			Created:       false,
			Available:     false,
			Failed:        false,
			WindowVisible: windowVisible,
			ProxyRunning:  proxyRunning,
			LastError:     "",
		}
	}

	return TrayState{
		Created:       s.trayMgr.IsCreated(),
		Available:     s.trayMgr.IsAvailable(),
		Failed:        s.trayMgr.IsFailed(),
		WindowVisible: windowVisible,
		ProxyRunning:  proxyRunning,
		LastError:     s.trayMgr.GetLastError(),
	}
}
