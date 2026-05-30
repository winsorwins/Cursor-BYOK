// +build windows

package tray

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getlantern/systray"
)

// Manager manages the system tray lifecycle on Windows.
type Manager struct {
	mu              sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	iconPath        string
	onShow          func()
	onHide          func()
	onStartProxy    func() error
	onStopProxy     func() error
	onRestoreCursor func() error
	onQuit          func()

	created     atomic.Bool
	available   atomic.Bool
	failed      atomic.Bool
	lastError   atomic.Value
	menuShow    *systray.MenuItem
	menuHide    *systray.MenuItem
	menuStart   *systray.MenuItem
	menuStop    *systray.MenuItem
	menuRestore *systray.MenuItem
	menuQuit    *systray.MenuItem
}

// NewManager creates a new tray manager for Windows.
func NewManager(iconPath string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		ctx:      ctx,
		cancel:   cancel,
		iconPath: iconPath,
	}
	m.created.Store(true)
	m.available.Store(false)
	m.failed.Store(false)
	return m
}

// SetCallbacks sets the action callbacks.
func (m *Manager) SetCallbacks(
	onShow, onHide func(),
	onStartProxy, onStopProxy, onRestoreCursor func() error,
	onQuit func(),
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onShow = onShow
	m.onHide = onHide
	m.onStartProxy = onStartProxy
	m.onStopProxy = onStopProxy
	m.onRestoreCursor = onRestoreCursor
	m.onQuit = onQuit
}

// Start initializes and runs the system tray. Blocks until tray exits.
func (m *Manager) Start() error {
	iconData, err := os.ReadFile(m.iconPath)
	if err != nil {
		m.failed.Store(true)
		m.setError(fmt.Sprintf("Failed to read icon: %v", err))
		return err
	}

	readyCh := make(chan struct{})
	timeoutCtx, timeoutCancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer timeoutCancel()

	go func() {
		systray.Run(func() {
			// Check if we already timed out
			select {
			case <-timeoutCtx.Done():
				// Timeout occurred, exit immediately without initializing
				log.Println("[Tray] Initialization aborted due to timeout")
				systray.Quit()
				return
			default:
			}
			m.onReady(iconData)
			close(readyCh)
		}, m.onExit)
	}()

	select {
	case <-readyCh:
		m.available.Store(true)
		m.failed.Store(false)
		log.Println("[Tray] System tray initialized")
		return nil
	case <-timeoutCtx.Done():
		m.available.Store(false)
		m.failed.Store(true)
		m.setError("Tray initialization timeout")
		// Attempt to quit the tray to prevent late initialization
		systray.Quit()
		return fmt.Errorf("tray initialization timeout")
	case <-m.ctx.Done():
		m.available.Store(false)
		return m.ctx.Err()
	}
}

func (m *Manager) onReady(iconData []byte) {
	systray.SetIcon(iconData)
	systray.SetTitle("Cursor助手")
	systray.SetTooltip("Cursor助手 - 代理未运行")

	m.menuShow = systray.AddMenuItem("显示窗口", "显示主窗口")
	m.menuHide = systray.AddMenuItem("隐藏窗口", "隐藏主窗口")
	systray.AddSeparator()
	m.menuStart = systray.AddMenuItem("启动代理", "启动本地代理服务")
	m.menuStop = systray.AddMenuItem("停止代理", "停止本地代理服务")
	m.menuStop.Disable()
	systray.AddSeparator()
	m.menuRestore = systray.AddMenuItem("恢复官方通道", "恢复 Cursor 官方设置")
	systray.AddSeparator()
	m.menuQuit = systray.AddMenuItem("退出应用", "退出 Cursor助手")

	go m.handleMenuEvents()
}

func (m *Manager) onExit() {
	log.Println("[Tray] System tray exited")
	m.available.Store(false)
}

func (m *Manager) handleMenuEvents() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.menuShow.ClickedCh:
			m.handleShow()
		case <-m.menuHide.ClickedCh:
			m.handleHide()
		case <-m.menuStart.ClickedCh:
			m.handleStartProxy()
		case <-m.menuStop.ClickedCh:
			m.handleStopProxy()
		case <-m.menuRestore.ClickedCh:
			m.handleRestoreCursor()
		case <-m.menuQuit.ClickedCh:
			m.handleQuit()
		}
	}
}

func (m *Manager) handleShow() {
	m.mu.Lock()
	onShow := m.onShow
	m.mu.Unlock()
	if onShow != nil {
		onShow()
	}
}

func (m *Manager) handleHide() {
	m.mu.Lock()
	onHide := m.onHide
	m.mu.Unlock()
	if onHide != nil {
		onHide()
	}
}

func (m *Manager) handleStartProxy() {
	m.mu.Lock()
	onStartProxy := m.onStartProxy
	m.mu.Unlock()
	if onStartProxy != nil {
		if err := onStartProxy(); err != nil {
			m.setError(fmt.Sprintf("启动代理失败: %v", err))
			systray.SetTooltip(fmt.Sprintf("Cursor助手 - 启动失败: %v", err))
		} else {
			m.clearError()
			m.menuStart.Disable()
			m.menuStop.Enable()
			systray.SetTooltip("Cursor助手 - 代理运行中")
		}
	}
}

func (m *Manager) handleStopProxy() {
	m.mu.Lock()
	onStopProxy := m.onStopProxy
	m.mu.Unlock()
	if onStopProxy != nil {
		if err := onStopProxy(); err != nil {
			m.setError(fmt.Sprintf("停止代理失败: %v", err))
			systray.SetTooltip(fmt.Sprintf("Cursor助手 - 停止失败: %v", err))
		} else {
			m.clearError()
			m.menuStart.Enable()
			m.menuStop.Disable()
			systray.SetTooltip("Cursor助手 - 代理未运行")
		}
	}
}

func (m *Manager) handleRestoreCursor() {
	m.mu.Lock()
	onRestoreCursor := m.onRestoreCursor
	m.mu.Unlock()
	if onRestoreCursor != nil {
		if err := onRestoreCursor(); err != nil {
			m.setError(fmt.Sprintf("恢复官方通道失败: %v", err))
			systray.SetTooltip(fmt.Sprintf("Cursor助手 - 恢复失败: %v", err))
		} else {
			m.clearError()
			systray.SetTooltip("Cursor助手 - 已恢复官方通道")
		}
	}
}

func (m *Manager) handleQuit() {
	m.mu.Lock()
	onQuit := m.onQuit
	m.mu.Unlock()
	if onQuit != nil {
		onQuit()
	}
	systray.Quit()
}

// Stop stops the tray manager.
func (m *Manager) Stop() {
	m.cancel()
	systray.Quit()
}

// IsCreated returns whether the tray manager was created.
func (m *Manager) IsCreated() bool {
	return m.created.Load()
}

// IsAvailable returns whether the tray is available.
func (m *Manager) IsAvailable() bool {
	return m.available.Load()
}

// IsFailed returns whether the tray initialization failed.
func (m *Manager) IsFailed() bool {
	return m.failed.Load()
}

// GetLastError returns the last error message.
func (m *Manager) GetLastError() string {
	if v := m.lastError.Load(); v != nil {
		return v.(string)
	}
	return ""
}

func (m *Manager) setError(msg string) {
	m.lastError.Store(msg)
	log.Printf("[Tray] Error: %s", msg)
}

func (m *Manager) clearError() {
	m.lastError.Store("")
}

// UpdateProxyState updates the tray menu based on proxy state.
func (m *Manager) UpdateProxyState(running bool) {
	if !m.IsAvailable() {
		return
	}
	if running {
		m.menuStart.Disable()
		m.menuStop.Enable()
		systray.SetTooltip("Cursor助手 - 代理运行中")
	} else {
		m.menuStart.Enable()
		m.menuStop.Disable()
		systray.SetTooltip("Cursor助手 - 代理未运行")
	}
}
