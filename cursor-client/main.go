package main

import (
	"context"
	"embed"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"

	"cursor-client/internal/bridge"
	appruntime "cursor-client/internal/runtime"
	"cursor-client/internal/tray"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

var (
	appCtx        context.Context
	shouldQuit    atomic.Bool
	hideOnClose   atomic.Bool
	windowVisible atomic.Bool
)

func main() {
	closeLog := setupAppLog()
	defer closeLog()

	// Create proxy service
	proxyService := bridge.NewProxyService()

	// Initialize tray manager
	var trayMgr *tray.Manager
	if runtime.GOOS == "windows" {
		iconPath := "build/windows/icon.ico"
		if _, err := os.Stat(iconPath); err != nil {
			log.Printf("[Main] Icon not found at %s, tray disabled", iconPath)
		} else {
			trayMgr = tray.NewManager(iconPath)
			hideOnClose.Store(true)
		}
	}

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "Cursor助手 | 自定义模型 API",
		Width:  1200,
		Height: 800,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 17, G: 24, B: 39, A: 255},
		OnStartup: func(ctx context.Context) {
			appCtx = ctx
			shouldQuit.Store(false)
			windowVisible.Store(true) // Window starts visible

			// Set up tray callbacks
			if trayMgr != nil {
				trayMgr.SetCallbacks(
					func() {
						wailsruntime.WindowShow(ctx)
						windowVisible.Store(true)
					},
					func() {
						wailsruntime.WindowHide(ctx)
						windowVisible.Store(false)
					},
					func() error {
						_, err := proxyService.StartProxy()
						if err == nil && trayMgr != nil {
							trayMgr.UpdateProxyState(true)
						}
						return err
					},
					func() error {
						_, err := proxyService.StopProxy()
						if err == nil && trayMgr != nil {
							trayMgr.UpdateProxyState(false)
						}
						return err
					},
					func() error {
						_, err := proxyService.RestoreCursorSettings()
						return err
					},
					func() {
						shouldQuit.Store(true)
						state := proxyService.GetState()
						if state.Running {
							if _, err := proxyService.StopProxy(); err != nil {
								log.Printf("[Main] Failed to stop proxy on quit: %v", err)
							}
						}
						wailsruntime.Quit(ctx)
					},
				)
				bridge.InjectTrayManager(proxyService, trayMgr)
				bridge.InjectWindowVisibleGetter(proxyService, func() bool {
					return windowVisible.Load()
				})

				// Start tray in background
				go func() {
					if err := trayMgr.Start(); err != nil {
						log.Printf("[Main] Tray initialization failed: %v", err)
						hideOnClose.Store(false)
					}
				}()
			}

			go proxyService.RepairCursorBootstrapCacheOnStartup()
			go proxyService.EnsureCATrustOnStartup()
			go proxyService.RestoreProxyOnStartup()
		},
		OnBeforeClose: func(ctx context.Context) bool {
			if shouldQuit.Load() {
				return false
			}
			if hideOnClose.Load() && trayMgr != nil && trayMgr.IsAvailable() {
				wailsruntime.WindowHide(ctx)
				windowVisible.Store(false)
				return true
			}
			shouldQuit.Store(true)
			state := proxyService.GetState()
			if state.Running {
				if _, err := proxyService.StopProxy(); err != nil {
					log.Printf("[Main] Failed to stop proxy on close: %v", err)
				}
			}
			return false
		},
		OnShutdown: func(ctx context.Context) {
			if trayMgr != nil {
				trayMgr.Stop()
			}
		},
		Bind: []interface{}{
			proxyService,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
		},
	})

	if err != nil {
		log.Fatal(err)
	}
}

func setupAppLog() func() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	paths, err := appruntime.ResolvePaths()
	if err != nil {
		log.Printf("[Main] Failed to resolve log path: %v", err)
		return func() {}
	}
	path := filepath.Join(paths.LogDir, "app.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("[Main] Failed to open log file %s: %v", path, err)
		return func() {}
	}
	log.SetOutput(io.MultiWriter(file, os.Stderr))
	log.Printf("[Main] Logging to %s", path)
	return func() { _ = file.Close() }
}
