//go:build windows
// +build windows

package webview2

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"log"
	"math"
	"reflect"
	"strconv"
	"sync"
	"unsafe"

	"github.com/jchv/go-webview2/internal/w32"
	"github.com/jchv/go-webview2/pkg/edge"

	"golang.org/x/sys/windows"
)

var (
	windowContext     = map[uintptr]interface{}{}
	windowContextSync sync.RWMutex
)

func getWindowContext(wnd uintptr) interface{} {
	windowContextSync.RLock()
	defer windowContextSync.RUnlock()
	return windowContext[wnd]
}

func setWindowContext(wnd uintptr, data interface{}) {
	windowContextSync.Lock()
	defer windowContextSync.Unlock()
	windowContext[wnd] = data
}

type browser interface {
	Embed(hwnd uintptr) bool
	Resize()
	Navigate(url string)
	NavigateToString(htmlContent string)
	Init(script string)
	Eval(script string)
	NotifyParentWindowPositionChanged() error
	Focus()
}

type webview struct {
	hwnd       uintptr
	mainthread uintptr
	browser    browser
	autofocus  bool
	maxsz      w32.Point
	minsz      w32.Point
	icon       uintptr
	trayAdded  bool
	m          sync.Mutex
	bindings   map[string]interface{}
	dispatchq  []func()
}

type WindowOptions struct {
	Title  string
	Width  uint
	Height uint
	IconId uint
	Center bool
}

type WebViewOptions struct {
	Window unsafe.Pointer
	Debug  bool

	// DataPath specifies the datapath for the WebView2 runtime to use for the
	// browser instance.
	DataPath string

	// AutoFocus will try to keep the WebView2 widget focused when the window
	// is focused.
	AutoFocus bool

	// WindowOptions customizes the window that is created to embed the
	// WebView2 widget.
	WindowOptions WindowOptions
}

// New creates a new webview in a new window.
func New(debug bool) WebView { return NewWithOptions(WebViewOptions{Debug: debug}) }

// NewWindow creates a new webview using an existing window.
//
// Deprecated: Use NewWithOptions.
func NewWindow(debug bool, window unsafe.Pointer) WebView {
	return NewWithOptions(WebViewOptions{Debug: debug, Window: window})
}

// NewWithOptions creates a new webview using the provided options.
func NewWithOptions(options WebViewOptions) WebView {
	w := &webview{}
	w.bindings = map[string]interface{}{}
	w.autofocus = options.AutoFocus

	chromium := edge.NewChromium()
	chromium.MessageCallback = w.msgcb
	chromium.DataPath = options.DataPath
	chromium.SetPermission(edge.CoreWebView2PermissionKindClipboardRead, edge.CoreWebView2PermissionStateAllow)
	chromium.SetPermission(edge.CoreWebView2PermissionKindMicrophone, edge.CoreWebView2PermissionStateAllow)
	chromium.SetGlobalPermission(edge.CoreWebView2PermissionStateAllow)

	w.browser = chromium
	w.mainthread, _, _ = w32.Kernel32GetCurrentThreadID.Call()
	if !w.CreateWithOptions(options.WindowOptions) {
		return nil
	}

	settings, err := chromium.GetSettings()
	if err != nil {
		log.Fatal(err)
	}
	// disable context menu
	err = settings.PutAreDefaultContextMenusEnabled(options.Debug)
	if err != nil {
		log.Fatal(err)
	}
	// disable developer tools
	err = settings.PutAreDevToolsEnabled(options.Debug)
	if err != nil {
		log.Fatal(err)
	}

	return w
}

type rpcMessage struct {
	ID     int               `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

func jsString(v interface{}) string { b, _ := json.Marshal(v); return string(b) }

func (w *webview) msgcb(msg string) {
	d := rpcMessage{}
	if err := json.Unmarshal([]byte(msg), &d); err != nil {
		log.Printf("invalid RPC message: %v", err)
		return
	}

	if d.ID <= 0 {
		_, _ = w.callbinding(d)
		return
	}

	id := strconv.Itoa(d.ID)
	if res, err := w.callbinding(d); err != nil {
		w.Dispatch(func() {
			w.Eval("window._rpc[" + id + "].reject(" + jsString(err.Error()) + "); window._rpc[" + id + "] = undefined")
		})
	} else if b, err := json.Marshal(res); err != nil {
		w.Dispatch(func() {
			w.Eval("window._rpc[" + id + "].reject(" + jsString(err.Error()) + "); window._rpc[" + id + "] = undefined")
		})
	} else {
		w.Dispatch(func() {
			w.Eval("window._rpc[" + id + "].resolve(" + string(b) + "); window._rpc[" + id + "] = undefined")
		})
	}
}

func (w *webview) callbinding(d rpcMessage) (interface{}, error) {
	w.m.Lock()
	f, ok := w.bindings[d.Method]
	w.m.Unlock()
	if !ok {
		return nil, nil
	}

	v := reflect.ValueOf(f)
	isVariadic := v.Type().IsVariadic()
	numIn := v.Type().NumIn()
	if (isVariadic && len(d.Params) < numIn-1) || (!isVariadic && len(d.Params) != numIn) {
		return nil, errors.New("function arguments mismatch")
	}
	args := []reflect.Value{}
	for i := range d.Params {
		var arg reflect.Value
		if isVariadic && i >= numIn-1 {
			arg = reflect.New(v.Type().In(numIn - 1).Elem())
		} else {
			arg = reflect.New(v.Type().In(i))
		}
		if err := json.Unmarshal(d.Params[i], arg.Interface()); err != nil {
			return nil, err
		}
		args = append(args, arg.Elem())
	}

	errorType := reflect.TypeOf((*error)(nil)).Elem()
	res := v.Call(args)
	switch len(res) {
	case 0:
		// No results from the function, just return nil
		return nil, nil

	case 1:
		// One result may be a value, or an error
		if res[0].Type().Implements(errorType) {
			if res[0].Interface() != nil {
				return nil, res[0].Interface().(error)
			}
			return nil, nil
		}
		return res[0].Interface(), nil

	case 2:
		// Two results: first one is value, second is error
		if !res[1].Type().Implements(errorType) {
			return nil, errors.New("second return value must be an error")
		}
		if res[1].Interface() == nil {
			return res[0].Interface(), nil
		}
		return res[0].Interface(), res[1].Interface().(error)

	default:
		return nil, errors.New("unexpected number of return values")
	}
}

func wndproc(hwnd, msg, wp, lp uintptr) uintptr {
	if msg == w32.WMNCCalcSize && lp != 0 {
		rgrc := (*w32.Rect)(unsafe.Pointer(lp))
		origTop := rgrc.Top
		r, _, _ := w32.User32DefWindowProcW.Call(hwnd, msg, wp, lp)
		zoomed, _, _ := w32.User32IsZoomed.Call(hwnd)
		if zoomed != 0 {
			frameY, _, _ := w32.User32GetSystemMetrics.Call(w32.SM_CYSIZEFRAME)
			padY, _, _ := w32.User32GetSystemMetrics.Call(w32.SM_CXPADDEDBORDER)
			rgrc.Top = origTop + int32(frameY+padY)
		} else {
			rgrc.Top = origTop
		}
		return r
	}
	if w, ok := getWindowContext(hwnd).(*webview); ok {
		switch msg {
		case w32.WMMove, w32.WMMoving:
			_ = w.browser.NotifyParentWindowPositionChanged()
		case w32.WMNCLButtonDown:
			_, _, _ = w32.User32SetFocus.Call(w.hwnd)
			r, _, _ := w32.User32DefWindowProcW.Call(hwnd, msg, wp, lp)
			return r
		case 0x0084 /* WM_NCHITTEST */:
			r, _, _ := w32.User32DefWindowProcW.Call(hwnd, msg, wp, lp)
			if r >= 10 && r <= 17 /* HTLEFT..HTBOTTOMRIGHT */ {
				return 1 /* HTCLIENT */
			}
			return r
		case 0x0112 /* WM_SYSCOMMAND */:
			cmd := wp & 0xFFF0
			if cmd == 0xF000 /* SC_SIZE */ || cmd == 0xF030 /* SC_MAXIMIZE */ {
				return 0
			}
			r, _, _ := w32.User32DefWindowProcW.Call(hwnd, msg, wp, lp)
			return r
		case w32.WMSize:
			w.browser.Resize()
			if wp == 2 {
				w.browser.Eval("document.documentElement.dataset.windowMaximized = 'true';")
			} else if wp == 0 {
				w.browser.Eval("document.documentElement.dataset.windowMaximized = 'false';")
			}
		case w32.WMActivate:
			if wp == w32.WAInactive {
				break
			}
			if w.autofocus {
				w.browser.Focus()
			}
		case w32.WMClose:
			w.browser.Eval("window.dispatchEvent(new CustomEvent('focus-request-close'));")
			return 0
		case w32.WMTrayIcon:
			switch lp {
			case w32.WMLButtonUp, w32.WMLButtonDblClk, w32.NINBalloonClick:
				w.RestoreFromTray()
			case w32.WMRButtonUp:
				w.showTrayContextMenu()
			}
			return 0
		case w32.WMDestroy:
			w.RemoveTrayIcon()
			w.Terminate()
		case w32.WMGetMinMaxInfo:
			lpmmi := (*w32.MinMaxInfo)(unsafe.Pointer(lp))
			if w.maxsz.X > 0 && w.maxsz.Y > 0 {
				lpmmi.PtMaxSize = w.maxsz
				lpmmi.PtMaxTrackSize = w.maxsz
			}
			if w.minsz.X > 0 && w.minsz.Y > 0 {
				lpmmi.PtMinTrackSize = w.minsz
			}
		default:
			r, _, _ := w32.User32DefWindowProcW.Call(hwnd, msg, wp, lp)
			return r
		}
		return 0
	}
	r, _, _ := w32.User32DefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func (w *webview) Create(debug bool, window unsafe.Pointer) bool {
	// This function signature stopped making sense a long time ago.
	// It is but legacy cruft at this point.
	return w.CreateWithOptions(WindowOptions{})
}

func (w *webview) CreateWithOptions(opts WindowOptions) bool {
	var hinstance windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hinstance)

	var icon uintptr
	if opts.IconId != 0 {
		icon, _, _ = w32.User32LoadImageW.Call(uintptr(hinstance), uintptr(opts.IconId), 1, 0, 0, w32.LR_DEFAULTSIZE|w32.LR_SHARED)
	}
	if icon == 0 {
		icon, _, _ = w32.User32LoadImageW.Call(uintptr(hinstance), 1, 1, 0, 0, w32.LR_DEFAULTSIZE|w32.LR_SHARED)
	}
	if icon == 0 {
		icon = createFocusCameraIconHandle()
	}
	if icon == 0 {
		icow, _, _ := w32.User32GetSystemMetrics.Call(w32.SystemMetricsCxIcon)
		icoh, _, _ := w32.User32GetSystemMetrics.Call(w32.SystemMetricsCyIcon)
		icon, _, _ = w32.User32LoadImageW.Call(uintptr(hinstance), 32512, icow, icoh, 0)
	}
	w.icon = icon

	className, _ := windows.UTF16PtrFromString("webview")
	wc := w32.WndClassExW{
		CbSize:        uint32(unsafe.Sizeof(w32.WndClassExW{})),
		HInstance:     hinstance,
		LpszClassName: className,
		HIcon:         windows.Handle(icon),
		HIconSm:       windows.Handle(icon),
		LpfnWndProc:   windows.NewCallback(wndproc),
	}
	_, _, _ = w32.User32RegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	windowName, _ := windows.UTF16PtrFromString(opts.Title)

	windowWidth := opts.Width
	if windowWidth == 0 {
		windowWidth = 640
	}
	windowHeight := opts.Height
	if windowHeight == 0 {
		windowHeight = 480
	}

	var workRect w32.Rect
	spiOk, _, _ := w32.User32SystemParametersInfoW.Call(
		0x0030, // SPI_GETWORKAREA
		0,
		uintptr(unsafe.Pointer(&workRect)),
		0,
	)
	frameX, _, _ := w32.User32GetSystemMetrics.Call(w32.SM_CXSIZEFRAME)
	frameY, _, _ := w32.User32GetSystemMetrics.Call(w32.SM_CYSIZEFRAME)
	pad, _, _ := w32.User32GetSystemMetrics.Call(w32.SM_CXPADDEDBORDER)
	borderX := int32(frameX + pad)
	borderY := int32(frameY + pad)

	var posX, posY, winW, winH int32
	if spiOk != 0 && workRect.Right > workRect.Left && workRect.Bottom > workRect.Top {
		posX = workRect.Left - borderX
		posY = workRect.Top
		winW = (workRect.Right - workRect.Left) + borderX*2
		winH = (workRect.Bottom - workRect.Top) + borderY
	} else {
		screenWidth, _, _ := w32.User32GetSystemMetrics.Call(w32.SM_CXSCREEN)
		screenHeight, _, _ := w32.User32GetSystemMetrics.Call(w32.SM_CYSCREEN)
		winW = int32(windowWidth)
		winH = int32(windowHeight)
		if int32(screenWidth) > winW {
			posX = (int32(screenWidth) - winW) / 2
		}
		if int32(screenHeight) > winH {
			posY = (int32(screenHeight) - winH) / 2
		}
	}
	w.minsz = w32.Point{X: winW, Y: winH}
	w.maxsz = w32.Point{X: winW, Y: winH}

	w.hwnd, _, _ = w32.User32CreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		0xCF0000, // WS_OVERLAPPEDWINDOW
		uintptr(posX),
		uintptr(posY),
		uintptr(winW),
		uintptr(winH),
		0,
		0,
		uintptr(hinstance),
		0,
	)
	setWindowContext(w.hwnd, w)
	if w.icon != 0 {
		_, _, _ = w32.User32SendMessageW.Call(w.hwnd, w32.WMSetIcon, 1 /* ICON_BIG */, w.icon)
		_, _, _ = w32.User32SendMessageW.Call(w.hwnd, w32.WMSetIcon, 0 /* ICON_SMALL */, w.icon)
	}
	_, _, _ = w32.User32SetWindowPos.Call(
		w.hwnd, 0, uintptr(posX), uintptr(posY), uintptr(winW), uintptr(winH),
		w32.SWPNoZOrder|w32.SWPNoActivate|w32.SWPFrameChanged,
	)
	w.initTrayIcon()

	_, _, _ = w32.User32ShowWindow.Call(w.hwnd, w32.SWShow)
	_, _, _ = w32.User32UpdateWindow.Call(w.hwnd)
	_, _, _ = w32.User32SetFocus.Call(w.hwnd)

	if !w.browser.Embed(w.hwnd) {
		setWindowContext(w.hwnd, nil)
		_, _, _ = w32.User32DestroyWindow.Call(w.hwnd)
		return false
	}
	w.browser.Resize()
	return true
}

func (w *webview) Destroy() {
	_, _, _ = w32.User32PostMessageW.Call(w.hwnd, w32.WMClose, 0, 0)
}

func (w *webview) Run() {
	var msg w32.Msg
	for {
		_, _, _ = w32.User32GetMessageW.Call(
			uintptr(unsafe.Pointer(&msg)),
			0,
			0,
			0,
		)
		if msg.Message == w32.WMApp {
			w.m.Lock()
			q := append([]func(){}, w.dispatchq...)
			w.dispatchq = []func(){}
			w.m.Unlock()
			for _, v := range q {
				v()
			}
		} else if msg.Message == w32.WMQuit {
			return
		}
		r, _, _ := w32.User32GetAncestor.Call(uintptr(msg.Hwnd), w32.GARoot)
		r, _, _ = w32.User32IsDialogMessage.Call(r, uintptr(unsafe.Pointer(&msg)))
		if r != 0 {
			continue
		}
		_, _, _ = w32.User32TranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = w32.User32DispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func (w *webview) Terminate() {
	_, _, _ = w32.User32PostQuitMessage.Call(0)
}

func (w *webview) Window() unsafe.Pointer {
	return unsafe.Pointer(w.hwnd)
}

func (w *webview) Navigate(url string) {
	w.browser.Navigate(url)
}

func (w *webview) SetHtml(html string) {
	w.browser.NavigateToString(html)
}

func (w *webview) SetTitle(title string) {
	_title, err := windows.UTF16FromString(title)
	if err != nil {
		_title, _ = windows.UTF16FromString("")
	}
	_, _, _ = w32.User32SetWindowTextW.Call(w.hwnd, uintptr(unsafe.Pointer(&_title[0])))
}

func (w *webview) SetSize(width int, height int, hints Hint) {
	index := w32.GWLStyle
	style := w32.GetWindowLong(w.hwnd, index)
	if hints == HintFixed {
		style &^= (w32.WSThickFrame | w32.WSMaximizeBox)
	} else {
		style |= (w32.WSThickFrame | w32.WSMaximizeBox)
	}
	w32.SetWindowLong(w.hwnd, index, style)

	if hints == HintMax {
		w.maxsz.X = int32(width)
		w.maxsz.Y = int32(height)
	} else if hints == HintMin {
		w.minsz.X = int32(width)
		w.minsz.Y = int32(height)
	} else {
		_, _, _ = w32.User32SetWindowPos.Call(
			w.hwnd, 0, 0, 0, uintptr(width), uintptr(height),
			w32.SWPNoZOrder|w32.SWPNoActivate|w32.SWPNoMove|w32.SWPFrameChanged)
		w.browser.Resize()
	}
}

func (w *webview) Init(js string) {
	w.browser.Init(js)
}

func (w *webview) Eval(js string) {
	w.browser.Eval(js)
}

func (w *webview) Dispatch(f func()) {
	w.m.Lock()
	w.dispatchq = append(w.dispatchq, f)
	w.m.Unlock()
	_, _, _ = w32.User32PostThreadMessageW.Call(w.mainthread, w32.WMApp, 0, 0)
}

func (w *webview) Bind(name string, f interface{}) error {
	v := reflect.ValueOf(f)
	if v.Kind() != reflect.Func {
		return errors.New("only functions can be bound")
	}
	if n := v.Type().NumOut(); n > 2 {
		return errors.New("function may only return a value or a value+error")
	}
	w.m.Lock()
	w.bindings[name] = f
	w.m.Unlock()

	w.Init("(function() { var name = " + jsString(name) + ";" + `
		var RPC = window._rpc = (window._rpc || {nextSeq: 1});
		window[name] = function() {
		  var seq = RPC.nextSeq++;
		  var promise = new Promise(function(resolve, reject) {
			RPC[seq] = {
			  resolve: resolve,
			  reject: reject,
			};
		  });
		  window.external.invoke(JSON.stringify({
			id: seq,
			method: name,
			params: Array.prototype.slice.call(arguments),
		  }));
		  return promise;
		}
	})()`)

	return nil
}

func copyUTF16(dst []uint16, s string) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return
	}
	copy(dst, u)
}

func (w *webview) initTrayIcon() {
	if w.hwnd == 0 || w.trayAdded {
		return
	}
	var nid w32.NotifyIconDataW
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.Hwnd = w.hwnd
	nid.UID = 1
	nid.UFlags = w32.NIFMessage | w32.NIFIcon | w32.NIFTip
	nid.UCallbackMessage = w32.WMTrayIcon
	nid.HIcon = w.icon
	copyUTF16(nid.SzTip[:], "FocusCamera · 高清监控与 NVR 中心（后台运行中）")
	ret, _, _ := w32.Shell32ShellNotifyIconW.Call(w32.NIMAdd, uintptr(unsafe.Pointer(&nid)))
	if ret != 0 {
		w.trayAdded = true
	}
}

func (w *webview) MinimizeToTray() {
	if w.hwnd == 0 {
		return
	}
	if !w.trayAdded {
		w.initTrayIcon()
	}
	_, _, _ = w32.User32ShowWindow.Call(w.hwnd, w32.SWHide)
	if w.trayAdded {
		var nid w32.NotifyIconDataW
		nid.CbSize = uint32(unsafe.Sizeof(nid))
		nid.Hwnd = w.hwnd
		nid.UID = 1
		nid.UFlags = w32.NIFInfo
		nid.DwInfoFlags = w32.NIIFInfo
		copyUTF16(nid.SzInfoTitle[:], "FocusCamera 已转入系统托盘运行")
		copyUTF16(nid.SzInfo[:], "后台 1080P 自动录像仍在持续进行，点击右下角托盘图标即可恢复主窗口。")
		_, _, _ = w32.Shell32ShellNotifyIconW.Call(w32.NIMModify, uintptr(unsafe.Pointer(&nid)))
	}
}

func (w *webview) RestoreFromTray() {
	if w.hwnd == 0 {
		return
	}
	_, _, _ = w32.User32ShowWindow.Call(w.hwnd, w32.SWShow)
	_, _, _ = w32.User32ShowWindow.Call(w.hwnd, w32.SWRestore)
	_, _, _ = w32.User32SetForegroundWindow.Call(w.hwnd)
	_, _, _ = w32.User32SetFocus.Call(w.hwnd)
	w.browser.Resize()
	w.browser.Focus()
}

func (w *webview) RemoveTrayIcon() {
	if w.hwnd == 0 || !w.trayAdded {
		return
	}
	var nid w32.NotifyIconDataW
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.Hwnd = w.hwnd
	nid.UID = 1
	_, _, _ = w32.Shell32ShellNotifyIconW.Call(w32.NIMDelete, uintptr(unsafe.Pointer(&nid)))
	w.trayAdded = false
}

func (w *webview) showTrayContextMenu() {
	hMenu, _, _ := w32.User32CreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer w32.User32DestroyMenu.Call(hMenu)

	openLabel, _ := windows.UTF16PtrFromString("打开 FocusCamera 监控主界面")
	statusLabel, _ := windows.UTF16PtrFromString("状态：后台 1080P 自动录像运行中")
	quitLabel, _ := windows.UTF16PtrFromString("彻底退出并停止录像...")

	_, _, _ = w32.User32AppendMenuW.Call(hMenu, 0x0000 /* MF_STRING */, 1001, uintptr(unsafe.Pointer(openLabel)))
	_, _, _ = w32.User32AppendMenuW.Call(hMenu, 0x0800 /* MF_SEPARATOR */, 0, 0)
	_, _, _ = w32.User32AppendMenuW.Call(hMenu, 0x0001 /* MF_GRAYED */, 0, uintptr(unsafe.Pointer(statusLabel)))
	_, _, _ = w32.User32AppendMenuW.Call(hMenu, 0x0800 /* MF_SEPARATOR */, 0, 0)
	_, _, _ = w32.User32AppendMenuW.Call(hMenu, 0x0000 /* MF_STRING */, 1002, uintptr(unsafe.Pointer(quitLabel)))

	var pt w32.Point
	_, _, _ = w32.User32GetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	_, _, _ = w32.User32SetForegroundWindow.Call(w.hwnd)
	// TPM_RETURNCMD (0x0100) | TPM_NONOTIFY (0x0080) | TPM_RIGHTBUTTON (0x0002)
	cmd, _, _ := w32.User32TrackPopupMenu.Call(hMenu, 0x0182, uintptr(pt.X), uintptr(pt.Y), 0, w.hwnd, 0)
	if cmd == 1001 {
		w.RestoreFromTray()
	} else if cmd == 1002 {
		w.RestoreFromTray()
		w.browser.Eval("window.dispatchEvent(new CustomEvent('focus-request-close'));")
	}
}

func createFocusCameraIconHandle() uintptr {
	const size = 64
	const headerSize = 40
	const xorSize = size * size * 4
	const andSize = size * (size / 8)
	buf := make([]byte, headerSize+xorSize+andSize)

	// BITMAPINFOHEADER
	binary.LittleEndian.PutUint32(buf[0:4], headerSize)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(size))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(size*2))
	binary.LittleEndian.PutUint16(buf[12:14], 1)
	binary.LittleEndian.PutUint16(buf[14:16], 32)
	binary.LittleEndian.PutUint32(buf[20:24], uint32(xorSize))

	setPixel := func(x, y int, r, g, b, a uint8) {
		// DIB is bottom-up
		row := (size - 1 - y)
		off := headerSize + (row*size+x)*4
		buf[off+0] = b
		buf[off+1] = g
		buf[off+2] = r
		buf[off+3] = a
	}

	roundedRectDist := func(px, py, cx, cy, halfW, halfH, rad float64) float64 {
		dx := math.Abs(px-cx) - (halfW - rad)
		dy := math.Abs(py-cy) - (halfH - rad)
		ox := math.Max(dx, 0)
		oy := math.Max(dy, 0)
		return math.Hypot(ox, oy) + math.Min(math.Max(dx, dy), 0) - rad
	}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx := float64(x) + 0.5
			fy := float64(y) + 0.5

			// Squircle background
			bgDist := roundedRectDist(fx, fy, 32.0, 32.0, 28.0, 28.0, 14.0)
			if bgDist > 0.7 {
				continue
			}
			alpha := 1.0
			if bgDist > -0.7 {
				alpha = (0.7 - bgDist) / 1.4
			}

			r, g, b := 16.0, 23.0, 38.0
			if bgDist > -2.0 {
				// Subtle slate border
				r, g, b = 46.0, 68.0, 104.0
			}

			// Camera lens center distance
			lensDist := math.Hypot(fx-32.0, fy-32.0)
			if lensDist <= 18.5 {
				if lensDist >= 14.5 {
					// Azure outer lens ring
					r, g, b = 61.0, 140.0, 245.0
				} else if lensDist >= 11.5 {
					r, g, b = 26.0, 42.0, 68.0
				} else {
					// Inner glass iris
					r, g, b = 34.0, 96.0, 204.0
					glintDist := math.Hypot(fx-27.5, fy-27.5)
					if glintDist <= 3.5 {
						r, g, b = 215.0, 236.0, 255.0
					}
				}
			}

			// Top-right REC indicator dot
			recDist := math.Hypot(fx-48.5, fy-15.5)
			if recDist <= 5.5 {
				r, g, b = 242.0, 61.0, 71.0
			}

			setPixel(x, y, uint8(r), uint8(g), uint8(b), uint8(alpha*255.0))
		}
	}

	hIcon, _, _ := w32.User32CreateIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		1,
		0x00030000,
		size,
		size,
		0,
	)
	return hIcon
}
