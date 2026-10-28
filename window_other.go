//go:build !darwin && !windows

package main

import "os/exec"

func runNativeDesktopWindow(appURL string, startInTray bool) {
	if !startInTray {
		_ = exec.Command("xdg-open", appURL).Start()
	}
	select {}
}

func selectNativeStorageFolder(initialDir string) (string, bool, error) {
	return "", true, nil
}
