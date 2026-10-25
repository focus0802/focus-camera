//go:build !darwin && !windows

package main

import "os/exec"

func runNativeDesktopWindow(appURL string) {
	_ = exec.Command("xdg-open", appURL).Start()
	select {}
}
