//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"unsafe"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

var (
	dwmapi                           = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttr             = dwmapi.NewProc("DwmSetWindowAttribute")
	procDwmExtendFrameIntoClientArea = dwmapi.NewProc("DwmExtendFrameIntoClientArea")

	user32             = windows.NewLazySystemDLL("user32.dll")
	procShowWindow     = user32.NewProc("ShowWindow")
	procIsZoomed       = user32.NewProc("IsZoomed")
	procPostMessageW   = user32.NewProc("PostMessageW")
	procReleaseCapture = user32.NewProc("ReleaseCapture")
)

type dwmMargins struct {
	CxLeftWidth    int32
	CxRightWidth   int32
	CyTopHeight    int32
	CyBottomHeight int32
}

func applyWindowsImmersiveTheme(hwnd uintptr, dark bool) {
	if hwnd == 0 {
		return
	}
	if procDwmExtendFrameIntoClientArea.Find() == nil {
		margins := dwmMargins{CxLeftWidth: 0, CxRightWidth: 0, CyTopHeight: 1, CyBottomHeight: 0}
		_, _, _ = procDwmExtendFrameIntoClientArea.Call(hwnd, uintptr(unsafe.Pointer(&margins)))
	}
	if procDwmSetWindowAttr.Find() != nil {
		return
	}
	var darkMode int32
	var captionColor uint32 = 0x00FFFFFF // BGR: #FFFFFF
	var textColor uint32 = 0x001A1A1A    // BGR: #1A1A1A
	if dark {
		darkMode = 1
		captionColor = 0x001A1A1A // BGR: #1A1A1A matching --surface dark
		textColor = 0x00F2F2F2    // BGR: #F2F2F2
	}
	// DWMWA_USE_IMMERSIVE_DARK_MODE (20 for Win10 20H1+ & Win11, 19 for older Win10)
	_, _, _ = procDwmSetWindowAttr.Call(hwnd, 20, uintptr(unsafe.Pointer(&darkMode)), unsafe.Sizeof(darkMode))
	_, _, _ = procDwmSetWindowAttr.Call(hwnd, 19, uintptr(unsafe.Pointer(&darkMode)), unsafe.Sizeof(darkMode))
	// Windows 11 DWMWA_CAPTION_COLOR (35) & DWMWA_TEXT_COLOR (36)
	_, _, _ = procDwmSetWindowAttr.Call(hwnd, 35, uintptr(unsafe.Pointer(&captionColor)), unsafe.Sizeof(captionColor))
	_, _, _ = procDwmSetWindowAttr.Call(hwnd, 36, uintptr(unsafe.Pointer(&textColor)), unsafe.Sizeof(textColor))
}

func runNativeDesktopWindow(appURL string) {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		localAppData = os.TempDir()
	}
	dataPath := filepath.Join(localAppData, "FocusCameraWebView2")
	_ = os.MkdirAll(dataPath, 0o755)

	// Extract official signed WebView2Loader.dll to disk and register its directory
	// so Windows loads it via native OS LoadLibraryW (compatible with Windows 11 ARM64 VM & x64).
	if len(webviewloader.WebView2Loader) > 0 {
		dllPath := filepath.Join(dataPath, "WebView2Loader.dll")
		if info, err := os.Stat(dllPath); err != nil || info.Size() != int64(len(webviewloader.WebView2Loader)) {
			_ = os.WriteFile(dllPath, webviewloader.WebView2Loader, 0o755)
		}
		_ = windows.SetDllDirectory(dataPath)
	}

	// Configure WebView2 Chromium engine flags for zero-prompt microphone intercom,
	// unmuted audio monitoring, and unthrottled 29-channel live video decoding.
	_ = os.Setenv(
		"WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
		"--autoplay-policy=no-user-gesture-required --use-fake-ui-for-media-stream --disable-background-timer-throttling --disable-renderer-backgrounding --disable-backgrounding-occluded-windows",
	)

	if ver, err := webviewloader.GetInstalledVersion(); err == nil && ver != "" {
		log.Printf("检测到系统 WebView2 运行时 (%s)，正在启动原生 Win32 桌面窗口...", ver)
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			Debug:     true,
			AutoFocus: true,
			DataPath:  dataPath,
			WindowOptions: webview2.WindowOptions{
				Title:  "Focus Camera · 高清监控与 NVR 中心",
				Width:  1640,
				Height: 960,
				IconId: 1,
				Center: true,
			},
		})
		if w != nil {
			defer w.Destroy()
			hwnd := uintptr(w.Window())
			applyWindowsImmersiveTheme(hwnd, true)
			w.Init("window.__FOCUS_PLATFORM__ = 'windows';")
			_ = w.Bind("focusSyncWindowTheme", func(theme string) {
				w.Dispatch(func() {
					applyWindowsImmersiveTheme(hwnd, theme != "light")
				})
			})
			_ = w.Bind("focusWindowControl", func(action string) {
				w.Dispatch(func() {
					switch action {
					case "drag":
						_, _, _ = procReleaseCapture.Call()
						_, _, _ = procPostMessageW.Call(hwnd, 0x00A1 /* WM_NCLBUTTONDOWN */, 2 /* HTCAPTION */, 0)
					case "minimize":
						_, _, _ = procShowWindow.Call(hwnd, 6 /* SW_MINIMIZE */)
					case "tray":
						w.MinimizeToTray()
					case "remove-tray":
						w.RemoveTrayIcon()
					case "maximize":
						zoomed, _, _ := procIsZoomed.Call(hwnd)
						if zoomed != 0 {
							_, _, _ = procShowWindow.Call(hwnd, 9 /* SW_RESTORE */)
							w.Eval("document.documentElement.dataset.windowMaximized = 'false';")
						} else {
							_, _, _ = procShowWindow.Call(hwnd, 3 /* SW_MAXIMIZE */)
							w.Eval("document.documentElement.dataset.windowMaximized = 'true';")
						}
					case "close":
						w.Eval("window.dispatchEvent(new CustomEvent('focus-request-close'));")
					}
				})
			})
			w.SetSize(1640, 960, webview2.HintNone)
			w.Navigate(appURL)
			w.Run()
			return
		}
	} else {
		log.Printf("未检测到系统 WebView2 运行时 (err=%v)，回退至独立应用窗口模式", err)
	}

	launchFallbackWindow(appURL, dataPath)
	// Keep HTTP/WebSocket backend server alive! Never exit on launcher handoff.
	select {}
}

func launchFallbackWindow(appURL, profileDir string) {
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
	}
	for _, exe := range candidates {
		if exe != "" {
			if _, err := os.Stat(exe); err == nil {
				cmd := exec.Command(exe,
					"--app="+appURL,
					"--new-window",
					"--window-size=1640,960",
					"--no-first-run",
					"--no-default-browser-check",
					"--autoplay-policy=no-user-gesture-required",
					"--disable-background-timer-throttling",
					"--disable-renderer-backgrounding",
					"--disable-backgrounding-occluded-windows",
					"--user-data-dir="+profileDir,
				)
				if err := cmd.Start(); err == nil {
					return
				}
			}
		}
	}
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", appURL).Start()
}
