//go:build windows

package webviewloader

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WebView2 release channel client GUIDs in priority order (Stable Evergreen -> Beta -> Dev -> Canary).
var webview2ClientGUIDs = []string{
	"{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}", // Microsoft Edge WebView2 Runtime (Stable Evergreen)
	"{2CD8A007-E189-409D-A2C8-9AF4EF3C72AA}", // Microsoft Edge Beta
	"{0D50BFEC-CD6A-4F9A-964C-C7416E3ACB10}", // Microsoft Edge Dev
	"{65C35B14-6C1D-4122-AC46-7148CC9D6497}", // Microsoft Edge Canary
}

func archSubfolders() []string {
	switch runtime.GOARCH {
	case "arm64":
		return []string{"arm64", "x64"}
	case "386":
		return []string{"x86"}
	default:
		return []string{"x64", "arm64"}
	}
}

// CompareBrowserVersions compares two dotted version strings and returns:
// -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2.
func CompareBrowserVersions(v1 string, v2 string) (int, error) {
	p1 := parseVersionParts(v1)
	p2 := parseVersionParts(v2)
	if len(p1) == 0 || len(p2) == 0 {
		return 0, fmt.Errorf("invalid version string: %q vs %q", v1, v2)
	}
	maxLen := len(p1)
	if len(p2) > maxLen {
		maxLen = len(p2)
	}
	for i := 0; i < maxLen; i++ {
		var a, b int
		if i < len(p1) {
			a = p1[i]
		}
		if i < len(p2) {
			b = p2[i]
		}
		if a < b {
			return -1, nil
		}
		if a > b {
			return 1, nil
		}
	}
	return 0, nil
}

func parseVersionParts(v string) []int {
	s := strings.TrimSpace(v)
	if s == "" {
		return nil
	}
	fields := strings.Fields(s)
	if len(fields) > 0 {
		s = fields[0]
	}
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// GetInstalledVersion returns the installed version of the Microsoft Edge WebView2 runtime.
// If no runtime is installed, an empty string is returned.
func GetInstalledVersion() (string, error) {
	_, ver, err := findEmbeddedBrowserWebView("")
	if err != nil {
		return "", err
	}
	return ver, nil
}

// CreateCoreWebView2EnvironmentWithOptions locates the system-installed Microsoft Edge WebView2
// Runtime (EmbeddedBrowserWebView.dll) and invokes CreateWebViewEnvironmentWithOptionsInternal
// directly via standard Windows LoadLibraryW without any in-memory reflective loader or embedded DLL.
func CreateCoreWebView2EnvironmentWithOptions(browserExecutableFolder, userDataFolder *uint16, environmentOptions uintptr, environmentCompletedHandle uintptr) (uintptr, error) {
	customDir := ""
	runtimeType := uintptr(0)
	if browserExecutableFolder != nil {
		customDir = windows.UTF16PtrToString(browserExecutableFolder)
		if customDir != "" {
			runtimeType = 1
		}
	}
	if customDir == "" {
		if envDir := strings.TrimSpace(os.Getenv("WEBVIEW2_BROWSER_EXECUTABLE_FOLDER")); envDir != "" {
			customDir = envDir
			runtimeType = 1
		}
	}

	dllPath, _, err := findEmbeddedBrowserWebView(customDir)
	if err != nil || dllPath == "" {
		if err == nil {
			err = fmt.Errorf("WebView2 runtime (EmbeddedBrowserWebView.dll) not found")
		}
		return uintptr(windows.ERROR_FILE_NOT_FOUND), err
	}

	dll := windows.NewLazyDLL(dllPath)
	if err := dll.Load(); err != nil {
		return uintptr(windows.ERROR_FILE_NOT_FOUND), fmt.Errorf("failed to load %s: %w", dllPath, err)
	}
	proc := dll.NewProc("CreateWebViewEnvironmentWithOptionsInternal")
	if err := proc.Find(); err != nil {
		return uintptr(windows.ERROR_PROC_NOT_FOUND), fmt.Errorf("missing CreateWebViewEnvironmentWithOptionsInternal in %s: %w", dllPath, err)
	}

	hr, _, _ := proc.Call(
		1, // checkRunningInstance = true
		runtimeType,
		uintptr(unsafe.Pointer(userDataFolder)),
		environmentOptions,
		environmentCompletedHandle,
	)
	return hr, nil
}

func resolveDLLInBaseDir(baseDir string) string {
	for _, arch := range archSubfolders() {
		candidate := filepath.Join(baseDir, "EBWebView", arch, "EmbeddedBrowserWebView.dll")
		if fileExists(candidate) {
			return candidate
		}
	}
	directCandidate := filepath.Join(baseDir, "EmbeddedBrowserWebView.dll")
	if fileExists(directCandidate) {
		return directCandidate
	}
	return ""
}

func findEmbeddedBrowserWebView(customDir string) (dllPath string, version string, err error) {
	if customDir != "" {
		if found := resolveDLLInBaseDir(customDir); found != "" {
			return found, filepath.Base(customDir), nil
		}
		return "", "", fmt.Errorf("EmbeddedBrowserWebView.dll not found under %s", customDir)
	}

	// 1. Query official EdgeUpdate ClientState registry keys (HKLM WOW6432Node, HKLM, HKCU)
	type regRoot struct {
		root   windows.Handle
		prefix string
	}
	roots := []regRoot{
		{windows.HKEY_LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\ClientState\`},
		{windows.HKEY_LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\ClientState\`},
		{windows.HKEY_CURRENT_USER, `Software\Microsoft\EdgeUpdate\ClientState\`},
	}

	for _, guid := range webview2ClientGUIDs {
		for _, r := range roots {
			ebDir, pv := queryClientStateRegistry(r.root, r.prefix+guid)
			if ebDir == "" || pv == "" || pv == "0.0.0.0" {
				continue
			}
			if found := resolveDLLInBaseDir(ebDir); found != "" {
				return found, pv, nil
			}
		}
	}

	// 2. Fallback: scan standard Microsoft EdgeWebView & EdgeCore Application directories on disk
	appDirs := []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "EdgeWebView", "Application"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "EdgeWebView", "Application"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "EdgeCore"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "EdgeCore"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "EdgeWebView", "Application"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "Edge", "Application"),
	}
	bestVer := ""
	bestDLL := ""
	for _, base := range appDirs {
		if base == "" {
			continue
		}
		entries, readErr := os.ReadDir(base)
		if readErr != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			vName := entry.Name()
			if len(parseVersionParts(vName)) < 2 {
				continue
			}
			found := resolveDLLInBaseDir(filepath.Join(base, vName))
			if found == "" {
				continue
			}
			if bestVer == "" {
				bestVer = vName
				bestDLL = found
			} else if cmp, cmpErr := CompareBrowserVersions(vName, bestVer); cmpErr == nil && cmp > 0 {
				bestVer = vName
				bestDLL = found
			}
		}
	}
	if bestDLL != "" {
		return bestDLL, bestVer, nil
	}

	return "", "", nil
}

func queryClientStateRegistry(root windows.Handle, subkey string) (ebWebView string, pv string) {
	subkeyPtr, err := windows.UTF16PtrFromString(subkey)
	if err != nil {
		return "", ""
	}
	accessFlags := []uint32{
		windows.KEY_READ,
		windows.KEY_READ | 0x0200, // KEY_WOW64_32KEY
		windows.KEY_READ | 0x0100, // KEY_WOW64_64KEY
	}
	for _, access := range accessFlags {
		var hKey windows.Handle
		if err := windows.RegOpenKeyEx(root, subkeyPtr, 0, access, &hKey); err != nil {
			continue
		}
		ebWebView = readRegistryString(hKey, "EBWebView")
		pv = readRegistryString(hKey, "pv")
		_ = windows.RegCloseKey(hKey)
		if ebWebView != "" && pv != "" {
			return ebWebView, pv
		}
	}
	return "", ""
}

func readRegistryString(hKey windows.Handle, valueName string) string {
	namePtr, err := windows.UTF16PtrFromString(valueName)
	if err != nil {
		return ""
	}
	var valType uint32
	var bufLen uint32
	if err := windows.RegQueryValueEx(hKey, namePtr, nil, &valType, nil, &bufLen); err != nil || bufLen == 0 {
		return ""
	}
	buf := make([]uint16, bufLen/2+1)
	if err := windows.RegQueryValueEx(hKey, namePtr, nil, &valType, (*byte)(unsafe.Pointer(&buf[0])), &bufLen); err != nil {
		return ""
	}
	return strings.TrimSpace(windows.UTF16ToString(buf))
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
