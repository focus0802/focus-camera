//go:build !darwin && !windows

package main

import "os/exec"

func runNativeDesktopWindow(appURL string, startInTray bool, preferredDisplayID string) {
	if !startInTray {
		_ = exec.Command("xdg-open", appURL).Start()
	}
	select {}
}

func listNativeDisplays() []DisplayInfo {
	return []DisplayInfo{
		{
			ID:      "default",
			Name:    "默认主显示器",
			Index:   1,
			Primary: true,
			Current: true,
			Width:   1920,
			Height:  1080,
			WorkX:   0,
			WorkY:   0,
			WorkW:   1920,
			WorkH:   1080,
		},
	}
}

func moveNativeWindowToDisplay(displayID string) (string, error) {
	return displayID, nil
}

func selectNativeStorageFolder(initialDir string) (string, bool, error) {
	return "", true, nil
}

func selectNativeExportFile(defaultFileName string, initialDir string) (string, bool, error) {
	return "", true, nil
}
