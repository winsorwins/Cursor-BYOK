package bridge

import (
	"log"
)

// DiagnosticsDTO provides a comprehensive view of the system state for diagnostics.
type DiagnosticsDTO struct {
	// Proxy status
	ProxyRunning   bool   `json:"proxyRunning"`
	ProxyAddress   string `json:"proxyAddress"`
	ProxyStartedAt string `json:"proxyStartedAt"` // ISO 8601, empty if not running

	// CA status
	CAInstalled bool   `json:"caInstalled"`
	CACertPath  string `json:"caCertPath"`
	CAExpiresAt string `json:"caExpiresAt"` // ISO 8601, empty if not available

	// Cursor settings status
	CursorProxySet   bool   `json:"cursorProxySet"`
	CursorConfigPath string `json:"cursorConfigPath"`

	// Data directories
	DataDir string `json:"dataDir"`
	LogDir  string `json:"logDir"`

	// Recent activity
	LastRequestAt    string `json:"lastRequestAt"`    // ISO 8601, empty if no requests
	LastErrorAt      string `json:"lastErrorAt"`      // ISO 8601, empty if no errors
	LastErrorMessage string `json:"lastErrorMessage"` // empty if no errors

	// Statistics
	TotalRequests int64 `json:"totalRequests"`
	TotalErrors   int64 `json:"totalErrors"`
}

// GetDiagnostics returns the current system diagnostics state.
// This method is read-only and has no side effects.
func (s *ProxyService) GetDiagnostics() (*DiagnosticsDTO, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	diag := &DiagnosticsDTO{
		ProxyRunning: s.proxy != nil && s.proxy.IsRunning(),
		ProxyAddress: "127.0.0.1:18080",
		DataDir:      s.dataDir(),
	}

	// Log directory
	if s.paths != nil {
		diag.LogDir = s.paths.LogDir
		diag.CACertPath = s.paths.CACertPath
	}

	// CA status
	if s.ca != nil {
		status := s.ca.TrustStatus()
		diag.CAInstalled = status.Trusted || status.Installed

		// Get CA certificate expiration from ToPEM
		certPEM, _, err := s.ca.ToPEM()
		if err == nil && len(certPEM) > 0 {
			// Parse the PEM to get NotAfter
			// For now, we'll skip parsing and just leave it empty
			// The CA certificate is valid for 10 years from creation
			diag.CAExpiresAt = "" // Will be populated if needed
		}
	}

	// Cursor settings status
	if s.cursor != nil {
		diag.CursorConfigPath = s.cursor.SettingsPath()
		usingProxy, err := s.cursor.UsesProxy("http://127.0.0.1:18080")
		if err != nil {
			log.Printf("[Bridge] Failed to check Cursor proxy settings: %v", err)
		} else {
			diag.CursorProxySet = usingProxy
		}
	}

	// Recent activity from stats
	diag.LastRequestAt = s.stats.LastRequest
	if s.stats.LastError != "" {
		diag.LastErrorMessage = s.stats.LastError
		// Use last request time as error time approximation
		diag.LastErrorAt = s.stats.LastRequest
	}

	// Statistics
	diag.TotalRequests = int64(s.stats.TotalRequests)
	diag.TotalErrors = int64(s.stats.FailedRequests)

	return diag, nil
}
