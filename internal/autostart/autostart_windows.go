//go:build windows

package autostart

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	winRunSubKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
	winRunValueName   = "FocusCamera"
	regOptionNonVol   = 0x00000000
	regKeyQueryValue  = 0x0001
	regKeySetValue    = 0x0002
	regSZ             = 1
	errFileNotFound   = 2
)

var (
	advapi32            = windows.NewLazySystemDLL("advapi32.dll")
	procRegCreateKeyExW = advapi32.NewProc("RegCreateKeyExW")
	procRegSetValueExW  = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW = advapi32.NewProc("RegDeleteValueW")
)

func queryOSAutoStart() (supported bool, registered bool, registeredCmd string) {
	subKeyPtr, err := windows.UTF16PtrFromString(winRunSubKey)
	if err != nil {
		return true, false, ""
	}
	valNamePtr, err := windows.UTF16PtrFromString(winRunValueName)
	if err != nil {
		return true, false, ""
	}

	var hKey windows.Handle
	if err := windows.RegOpenKeyEx(windows.HKEY_CURRENT_USER, subKeyPtr, 0, regKeyQueryValue, &hKey); err != nil {
		return true, false, ""
	}
	defer windows.RegCloseKey(hKey)

	var valType uint32
	var bufLen uint32 = 2048
	buf := make([]uint16, bufLen/2)
	err = windows.RegQueryValueEx(hKey, valNamePtr, nil, &valType, (*byte)(unsafe.Pointer(&buf[0])), &bufLen)
	if err != nil {
		return true, false, ""
	}
	cmd := strings.TrimSpace(windows.UTF16ToString(buf))
	if cmd == "" {
		return true, false, ""
	}
	return true, true, cmd
}

func enableOSAutoStart(exePath, _ string) error {
	subKeyPtr, err := windows.UTF16PtrFromString(winRunSubKey)
	if err != nil {
		return err
	}
	valNamePtr, err := windows.UTF16PtrFromString(winRunValueName)
	if err != nil {
		return err
	}
	cmdStr := fmt.Sprintf(`"%s" --autostart`, exePath)
	cmdUTF16, err := windows.UTF16FromString(cmdStr)
	if err != nil {
		return err
	}

	var hKey windows.Handle
	var disposition uint32
	r0, _, _ := procRegCreateKeyExW.Call(
		uintptr(windows.HKEY_CURRENT_USER),
		uintptr(unsafe.Pointer(subKeyPtr)),
		0,
		0,
		regOptionNonVol,
		regKeySetValue|regKeyQueryValue,
		0,
		uintptr(unsafe.Pointer(&hKey)),
		uintptr(unsafe.Pointer(&disposition)),
	)
	if r0 != 0 {
		return fmt.Errorf("无法打开 Windows 注册表自启动项 (错误码 %d)", r0)
	}
	defer windows.RegCloseKey(hKey)

	byteLen := uint32(len(cmdUTF16) * 2)
	r1, _, _ := procRegSetValueExW.Call(
		uintptr(hKey),
		uintptr(unsafe.Pointer(valNamePtr)),
		0,
		regSZ,
		uintptr(unsafe.Pointer(&cmdUTF16[0])),
		uintptr(byteLen),
	)
	if r1 != 0 {
		return fmt.Errorf("无法写入 Windows 注册表自启动项 (错误码 %d)", r1)
	}
	return nil
}

func disableOSAutoStart() error {
	subKeyPtr, err := windows.UTF16PtrFromString(winRunSubKey)
	if err != nil {
		return err
	}
	valNamePtr, err := windows.UTF16PtrFromString(winRunValueName)
	if err != nil {
		return err
	}

	var hKey windows.Handle
	if err := windows.RegOpenKeyEx(windows.HKEY_CURRENT_USER, subKeyPtr, 0, regKeySetValue, &hKey); err != nil {
		if err == syscall.Errno(errFileNotFound) {
			return nil
		}
		return nil
	}
	defer windows.RegCloseKey(hKey)

	r0, _, _ := procRegDeleteValueW.Call(
		uintptr(hKey),
		uintptr(unsafe.Pointer(valNamePtr)),
	)
	if r0 != 0 && r0 != errFileNotFound {
		return fmt.Errorf("无法清理 Windows 注册表自启动项 (错误码 %d)", r0)
	}
	return nil
}
