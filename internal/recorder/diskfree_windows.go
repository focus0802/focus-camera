//go:build windows

package recorder

import "golang.org/x/sys/windows"

func getFreeDiskBytes(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var freeBytes uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeBytes, nil, nil); err != nil {
		return 0, err
	}
	return freeBytes, nil
}
