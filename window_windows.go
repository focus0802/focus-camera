//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

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
				Title:  "Focus Camera · 29路教室高清监控与双向对讲系统",
				Width:  1640,
				Height: 960,
				Center: true,
			},
		})
		if w != nil {
			defer w.Destroy()
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
