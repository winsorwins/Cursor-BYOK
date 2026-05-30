// +build !windows

package tray

import (
	"fmt"
)

// Manager is a stub for non-Windows platforms.
type Manager struct{}

// NewManager creates a stub manager.
func NewManager(iconPath string) *Manager {
	return &Manager{}
}

// SetCallbacks is a no-op on non-Windows platforms.
func (m *Manager) SetCallbacks(
	onShow, onHide func(),
	onStartProxy, onStopProxy, onRestoreCursor func() error,
	onQuit func(),
) {
}

// Start returns an error indicating tray is not supported.
func (m *Manager) Start() error {
	return fmt.Errorf("system tray not supported on this platform")
}

// Stop is a no-op.
func (m *Manager) Stop() {
}

// IsAvailable always returns false on non-Windows platforms.
func (m *Manager) IsAvailable() bool {
	return false
}

// GetLastError returns empty string.
func (m *Manager) GetLastError() string {
	return ""
}

// UpdateProxyState is a no-op.
func (m *Manager) UpdateProxyState(running bool) {
}
