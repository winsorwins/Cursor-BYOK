package bridge

// TrayState represents the system tray state.
type TrayState struct {
	Created       bool   `json:"created"`       // Whether tray manager was created
	Available     bool   `json:"available"`     // Whether tray is initialized and ready
	Failed        bool   `json:"failed"`        // Whether tray initialization failed
	WindowVisible bool   `json:"windowVisible"` // Whether main window is currently visible
	ProxyRunning  bool   `json:"proxyRunning"`  // Whether proxy is currently running
	LastError     string `json:"lastError"`     // Last error message from tray operations
}
