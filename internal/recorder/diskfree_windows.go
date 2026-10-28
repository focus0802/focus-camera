//go:build windows

package recorder

import "golang.org/x/sys/windows"

func getFreeDiskBytes(path string) (uint64, error) {
	free, _, err := getDiskSpaceBytes(path)
	return free, err
}

func getDiskSpaceBytes(path string) (freeBytes uint64, totalBytes uint64, err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var free, total uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, nil); err != nil {
		return 0, 0, err
	}
	return free, total, nil
}
