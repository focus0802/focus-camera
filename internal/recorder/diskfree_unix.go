//go:build !windows

package recorder

import "syscall"

func getFreeDiskBytes(path string) (uint64, error) {
	free, _, err := getDiskSpaceBytes(path)
	return free, err
}

func getDiskSpaceBytes(path string) (freeBytes uint64, totalBytes uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), uint64(st.Blocks) * uint64(st.Bsize), nil
}
