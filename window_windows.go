//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

var (
	dwmapi                           = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttr             = dwmapi.NewProc("DwmSetWindowAttribute")
	procDwmExtendFrameIntoClientArea = dwmapi.NewProc("DwmExtendFrameIntoClientArea")

	user32               = windows.NewLazySystemDLL("user32.dll")
	procShowWindow       = user32.NewProc("ShowWindow")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procReleaseCapture   = user32.NewProc("ReleaseCapture")
	procSendMessageW     = user32.NewProc("SendMessageW")
	procGetDpiForSystem  = user32.NewProc("GetDpiForSystem")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")

	shell32                 = windows.NewLazySystemDLL("shell32.dll")
	procSHBrowseForFolderW  = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")

	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")

	comdlg32                 = windows.NewLazySystemDLL("comdlg32.dll")
	procGetSaveFileNameW     = comdlg32.NewProc("GetSaveFileNameW")
	procCommDlgExtendedError = comdlg32.NewProc("CommDlgExtendedError")

	winWindowMu    sync.Mutex
	activeWebView  webview2.WebView
	activeMainHWND uintptr
)

type openFileNameW struct {
	LStructSize       uint32
	HwndOwner         uintptr
	HInstance         uintptr
	LpstrFilter       *uint16
	LpstrCustomFilter *uint16
	NMaxCustFilter    uint32
	NFilterIndex      uint32
	LpstrFile         *uint16
	NMaxFile          uint32
	LpstrFileTitle    *uint16
	NMaxFileTitle     uint32
	LpstrInitialDir   *uint16
	LpstrTitle        *uint16
	Flags             uint32
	NFileOffset       uint16
	NFileExtension    uint16
	LpstrDefExt       *uint16
	LCustData         uintptr
	LpfnHook          uintptr
	LpTemplateName    *uint16
	PvReserved        uintptr
	DwReserved        uint32
	FlagsEx           uint32
}

type browseInfoW struct {
	HwndOwner      uintptr
	PidlRoot       uintptr
	PszDisplayName *uint16
	LpszTitle      *uint16
	UlFlags        uint32
	_              uint32
	Lpfn           uintptr
	LParam         uintptr
	IImage         int32
	_              int32
}

var browseFolderCallback = windows.NewCallback(func(hwnd uintptr, uMsg uint32, lParam, lpData uintptr) uintptr {
	if uMsg == 1 /* BFFM_INITIALIZED */ && lpData != 0 {
		// BFFM_SETSELECTIONW = WM_USER (0x0400) + 103 = 1127
		_, _, _ = procSendMessageW.Call(hwnd, 1127, 1, lpData)
	}
	return 0
})

func showWin32FolderPicker(ownerHWND uintptr, initialDir string) (string, bool, error) {
	var displayName [520]uint16
	titlePtr, _ := windows.UTF16PtrFromString("请选择 1080P 监控录像分片 (MP4) 的本地存储文件夹：")

	var initDirPtr *uint16
	cleanInit := strings.TrimSpace(initialDir)
	if cleanInit != "" && !strings.HasPrefix(cleanInit, `\\`) {
		if abs, err := filepath.Abs(cleanInit); err == nil {
			cleanInit = abs
		}
		if st, err := os.Stat(cleanInit); err == nil && st.IsDir() {
			initDirPtr, _ = windows.UTF16PtrFromString(cleanInit)
		}
	}

	bi := browseInfoW{
		HwndOwner:      ownerHWND,
		PszDisplayName: &displayName[0],
		LpszTitle:      titlePtr,
		// BIF_RETURNONLYFSDIRS (0x01) | BIF_EDITBOX (0x10) | BIF_NEWDIALOGSTYLE (0x40)
		UlFlags: 0x00000051,
	}
	if initDirPtr != nil {
		bi.Lpfn = browseFolderCallback
		bi.LParam = uintptr(unsafe.Pointer(initDirPtr))
	}

	pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return "", true, nil
	}
	defer procCoTaskMemFree.Call(pidl)

	var pathBuf [520]uint16
	ok, _, _ := procSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&pathBuf[0])))
	if ok == 0 {
		return "", true, nil
	}
	selected := strings.TrimSpace(windows.UTF16ToString(pathBuf[:]))
	if selected == "" {
		return "", true, nil
	}
	return selected, false, nil
}

func selectNativeStorageFolder(initialDir string) (string, bool, error) {
	winWindowMu.Lock()
	w := activeWebView
	hwnd := activeMainHWND
	winWindowMu.Unlock()

	type pickResult struct {
		path      string
		cancelled bool
		err       error
	}

	if w != nil && hwnd != 0 {
		ch := make(chan pickResult, 1)
		w.Dispatch(func() {
			p, c, err := showWin32FolderPicker(hwnd, initialDir)
			ch <- pickResult{path: p, cancelled: c, err: err}
		})
		res := <-ch
		return res.path, res.cancelled, res.err
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_, _, _ = procCoInitializeEx.Call(0, 2 /* COINIT_APARTMENTTHREADED */)
	return showWin32FolderPicker(0, initialDir)
}

func sanitizeWindowsFileName(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			b.WriteRune('_')
		default:
			if r >= 32 {
				b.WriteRune(r)
			}
		}
	}
	res := strings.TrimSpace(strings.Trim(b.String(), ". "))
	if res == "" {
		return "recording.mp4"
	}
	return res
}

func showWin32SaveFilePicker(ownerHWND uintptr, defaultFileName string, initialDir string) (string, bool, error) {
	cleanName := sanitizeWindowsFileName(defaultFileName)
	if !strings.HasSuffix(strings.ToLower(cleanName), ".mp4") {
		cleanName += ".mp4"
	}

	var fileBuf [1024]uint16
	nameUTF16, err := windows.UTF16FromString(cleanName)
	if err == nil && len(nameUTF16) < len(fileBuf) {
		copy(fileBuf[:], nameUTF16)
	}

	filterUTF16 := []uint16{
		'M', 'P', '4', ' ', 0x89c6, 0x9891, 0x6587, 0x4ef6, ' ', '(', '*', '.', 'm', 'p', '4', ')', 0,
		'*', '.', 'm', 'p', '4', 0,
		0x6240, 0x6709, 0x6587, 0x4ef6, ' ', '(', '*', '.', '*', ')', 0,
		'*', '.', '*', 0,
		0,
	}
	titlePtr, _ := windows.UTF16PtrFromString("导出 1080P 监控录像分片 (MP4)")
	defExtPtr, _ := windows.UTF16PtrFromString("mp4")

	var initDirPtr *uint16
	cleanInit := strings.TrimSpace(initialDir)
	if cleanInit == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dl := filepath.Join(home, "Downloads")
			if st, err := os.Stat(dl); err == nil && st.IsDir() {
				cleanInit = dl
			} else {
				cleanInit = home
			}
		}
	}
	if cleanInit != "" {
		if abs, err := filepath.Abs(cleanInit); err == nil {
			if st, err := os.Stat(abs); err == nil && st.IsDir() {
				initDirPtr, _ = windows.UTF16PtrFromString(abs)
			}
		}
	}

	ofn := openFileNameW{
		LStructSize:     uint32(unsafe.Sizeof(openFileNameW{})),
		HwndOwner:       ownerHWND,
		LpstrFilter:     &filterUTF16[0],
		NFilterIndex:    1,
		LpstrFile:       &fileBuf[0],
		NMaxFile:        uint32(len(fileBuf)),
		LpstrInitialDir: initDirPtr,
		LpstrTitle:      titlePtr,
		// OFN_OVERWRITEPROMPT (0x02) | OFN_NOCHANGEDIR (0x08) | OFN_PATHMUSTEXIST (0x0800) | OFN_EXPLORER (0x080000)
		Flags:       0x0008080A,
		LpstrDefExt: defExtPtr,
	}

	ok, _, _ := procGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if ok == 0 {
		// Check if GetSaveFileNameW failed due to invalid initial filename/dir rather than user cancellation
		if extErr, _, _ := procCommDlgExtendedError.Call(); extErr != 0 {
			fileBuf = [1024]uint16{}
			if fallbackUTF16, err := windows.UTF16FromString("recording.mp4"); err == nil {
				copy(fileBuf[:], fallbackUTF16)
			}
			ofn.LpstrInitialDir = nil
			ok, _, _ = procGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
		}
		if ok == 0 {
			return "", true, nil
		}
	}
	selected := strings.TrimSpace(windows.UTF16ToString(fileBuf[:]))
	if selected == "" {
		return "", true, nil
	}
	if !strings.HasSuffix(strings.ToLower(selected), ".mp4") {
		selected += ".mp4"
	}
	return selected, false, nil
}

func selectNativeExportFile(defaultFileName string, initialDir string) (string, bool, error) {
	winWindowMu.Lock()
	w := activeWebView
	hwnd := activeMainHWND
	winWindowMu.Unlock()

	type pickResult struct {
		path      string
		cancelled bool
		err       error
	}

	if w != nil && hwnd != 0 {
		ch := make(chan pickResult, 1)
		w.Dispatch(func() {
			p, c, err := showWin32SaveFilePicker(hwnd, defaultFileName, initialDir)
			ch <- pickResult{path: p, cancelled: c, err: err}
		})
		res := <-ch
		return res.path, res.cancelled, res.err
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_, _, _ = procCoInitializeEx.Call(0, 2 /* COINIT_APARTMENTTHREADED */)
	return showWin32SaveFilePicker(0, defaultFileName, initialDir)
}

func resolveInitialWindowBounds(logicalW, logicalH int) (width, height, minW, minH int) {
	dpi := 96
	if procGetDpiForSystem.Find() == nil {
		if r, _, _ := procGetDpiForSystem.Call(); r >= 96 && r <= 480 {
			dpi = int(r)
		}
	}
	scale := func(v int) int {
		return (v*dpi + 48) / 96
	}
	width = scale(logicalW)
	height = scale(logicalH)
	minW = scale(760)
	minH = scale(500)

	if procGetSystemMetrics.Find() == nil {
		screenW, _, _ := procGetSystemMetrics.Call(0 /* SM_CXSCREEN */)
		screenH, _, _ := procGetSystemMetrics.Call(1 /* SM_CYSCREEN */)
		if screenW > 0 && screenH > 0 {
			maxW := int(screenW) * 88 / 100
			maxH := int(screenH) * 86 / 100
			if width > maxW {
				width = maxW
			}
			if height > maxH {
				height = maxH
			}
			if minW > maxW {
				minW = maxW
			}
			if minH > maxH {
				minH = maxH
			}
		}
	}
	return width, height, minW, minH
}

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

func runNativeDesktopWindow(appURL string, startInTray bool) {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		localAppData = os.TempDir()
	}
	dataPath := filepath.Join(localAppData, "FocusCameraWebView2")
	_ = os.MkdirAll(dataPath, 0o755)

	// Configure WebView2 Chromium engine flags for zero-prompt microphone intercom,
	// unmuted audio monitoring, and unthrottled 29-channel live video decoding.
	_ = os.Setenv(
		"WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
		"--autoplay-policy=no-user-gesture-required --use-fake-ui-for-media-stream --disable-background-timer-throttling --disable-renderer-backgrounding --disable-backgrounding-occluded-windows",
	)

	if ver, err := webviewloader.GetInstalledVersion(); err == nil && ver != "" {
		log.Printf("检测到系统 WebView2 运行时 (%s)，正在启动原生 Win32 桌面窗口 (startInTray=%v)...", ver, startInTray)
		initW, initH, _, _ := resolveInitialWindowBounds(1640, 960)
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			Debug:     true,
			AutoFocus: !startInTray,
			DataPath:  dataPath,
			WindowOptions: webview2.WindowOptions{
				Title:  "Focus Camera · 高清监控与 NVR 中心",
				Width:  uint(initW),
				Height: uint(initH),
				IconId: 1,
				Center: true,
			},
		})
		if w != nil {
			hwnd := uintptr(w.Window())
			winWindowMu.Lock()
			activeWebView = w
			activeMainHWND = hwnd
			winWindowMu.Unlock()
			defer func() {
				winWindowMu.Lock()
				activeWebView = nil
				activeMainHWND = 0
				winWindowMu.Unlock()
				w.Destroy()
			}()
			applyWindowsImmersiveTheme(hwnd, true)
			if startInTray {
				w.MinimizeToTray()
			} else {
				_, _, _ = procShowWindow.Call(hwnd, 5 /* SW_SHOW */)
			}
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
					case "close":
						w.Eval("window.dispatchEvent(new CustomEvent('focus-request-close'));")
					}
				})
			})
			w.Navigate(appURL)
			w.Run()
			return
		}
	} else {
		log.Printf("未检测到系统 WebView2 运行时 (err=%v)，回退至独立应用窗口模式", err)
	}

	if !startInTray {
		launchFallbackWindow(appURL, dataPath)
	}
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
