package bridge

import (
	"testing"
)

// fakeTrayManager implements the tray manager interface for testing.
type fakeTrayManager struct {
	created   bool
	available bool
	failed    bool
	lastError string
}

func (f *fakeTrayManager) IsCreated() bool {
	return f.created
}

func (f *fakeTrayManager) IsAvailable() bool {
	return f.available
}

func (f *fakeTrayManager) IsFailed() bool {
	return f.failed
}

func (f *fakeTrayManager) GetLastError() string {
	return f.lastError
}

func (f *fakeTrayManager) UpdateProxyState(running bool) {
	// No-op for testing
}

func TestGetTrayState_NoManager(t *testing.T) {
	service := NewProxyService()

	state := service.GetTrayState()

	if state.Created {
		t.Errorf("Expected Created=false when no manager, got true")
	}
	if state.Available {
		t.Errorf("Expected Available=false when no manager, got true")
	}
	if state.Failed {
		t.Errorf("Expected Failed=false when no manager, got true")
	}
	if state.WindowVisible {
		t.Errorf("Expected WindowVisible=false when no getter, got true")
	}
	if state.ProxyRunning {
		t.Errorf("Expected ProxyRunning=false when proxy not started, got true")
	}
	if state.LastError != "" {
		t.Errorf("Expected empty LastError when no manager, got %q", state.LastError)
	}
}

func TestGetTrayState_CreatedButNotAvailable(t *testing.T) {
	service := NewProxyService()
	fakeMgr := &fakeTrayManager{
		created:   true,
		available: false,
		failed:    false,
		lastError: "",
	}
	InjectTrayManager(service, fakeMgr)

	state := service.GetTrayState()

	if !state.Created {
		t.Errorf("Expected Created=true, got false")
	}
	if state.Available {
		t.Errorf("Expected Available=false when not initialized, got true")
	}
	if state.Failed {
		t.Errorf("Expected Failed=false, got true")
	}
}

func TestGetTrayState_InitializationFailed(t *testing.T) {
	service := NewProxyService()
	fakeMgr := &fakeTrayManager{
		created:   true,
		available: false,
		failed:    true,
		lastError: "Tray initialization timeout",
	}
	InjectTrayManager(service, fakeMgr)

	state := service.GetTrayState()

	if !state.Created {
		t.Errorf("Expected Created=true, got false")
	}
	if state.Available {
		t.Errorf("Expected Available=false when failed, got true")
	}
	if !state.Failed {
		t.Errorf("Expected Failed=true, got false")
	}
	if state.LastError != "Tray initialization timeout" {
		t.Errorf("Expected LastError=%q, got %q", "Tray initialization timeout", state.LastError)
	}
}

func TestGetTrayState_AvailableAndRunning(t *testing.T) {
	service := NewProxyService()
	fakeMgr := &fakeTrayManager{
		created:   true,
		available: true,
		failed:    false,
		lastError: "",
	}
	InjectTrayManager(service, fakeMgr)

	state := service.GetTrayState()

	if !state.Created {
		t.Errorf("Expected Created=true, got false")
	}
	if !state.Available {
		t.Errorf("Expected Available=true, got false")
	}
	if state.Failed {
		t.Errorf("Expected Failed=false, got true")
	}
	if state.LastError != "" {
		t.Errorf("Expected empty LastError, got %q", state.LastError)
	}
}

func TestGetTrayState_WindowVisible(t *testing.T) {
	service := NewProxyService()
	fakeMgr := &fakeTrayManager{
		created:   true,
		available: true,
		failed:    false,
		lastError: "",
	}
	InjectTrayManager(service, fakeMgr)

	// Test window visible
	InjectWindowVisibleGetter(service, func() bool { return true })
	state := service.GetTrayState()
	if !state.WindowVisible {
		t.Errorf("Expected WindowVisible=true, got false")
	}

	// Test window hidden
	InjectWindowVisibleGetter(service, func() bool { return false })
	state = service.GetTrayState()
	if state.WindowVisible {
		t.Errorf("Expected WindowVisible=false, got true")
	}
}

func TestGetTrayState_WithLastError(t *testing.T) {
	service := NewProxyService()
	fakeMgr := &fakeTrayManager{
		created:   true,
		available: true,
		failed:    false,
		lastError: "Failed to read icon: file not found",
	}
	InjectTrayManager(service, fakeMgr)

	state := service.GetTrayState()

	if state.LastError != "Failed to read icon: file not found" {
		t.Errorf("Expected LastError=%q, got %q", "Failed to read icon: file not found", state.LastError)
	}
}

func TestInjectWindowVisibleGetter(t *testing.T) {
	service := NewProxyService()

	// Initially no getter
	state := service.GetTrayState()
	if state.WindowVisible {
		t.Errorf("Expected WindowVisible=false before injection, got true")
	}

	// Inject getter returning true
	InjectWindowVisibleGetter(service, func() bool { return true })
	state = service.GetTrayState()
	if !state.WindowVisible {
		t.Errorf("Expected WindowVisible=true after injection, got false")
	}

	// Update getter to return false
	InjectWindowVisibleGetter(service, func() bool { return false })
	state = service.GetTrayState()
	if state.WindowVisible {
		t.Errorf("Expected WindowVisible=false after update, got true")
	}
}
