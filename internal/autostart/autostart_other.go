//go:build !darwin && !windows

package autostart

import "fmt"

func queryOSAutoStart() (supported bool, registered bool, registeredCmd string) {
	return false, false, ""
}

func enableOSAutoStart(_, _ string) error {
	return fmt.Errorf("当前操作系统暂不支持自动配置开机自启")
}

func disableOSAutoStart() error {
	return nil
}
