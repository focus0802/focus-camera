package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"focus-camera/internal/config"
)

type Status struct {
	Supported      bool   `json:"supported"`
	Enabled        bool   `json:"enabled"`
	OSRegistered   bool   `json:"os_registered"`
	Mode           string `json:"mode"`
	ExecutablePath string `json:"executable_path"`
	RegisteredCmd  string `json:"registered_cmd,omitempty"`
	IsNetworkPath  bool   `json:"is_network_path,omitempty"`
	Warning        string `json:"warning,omitempty"`
}

func ResolveExecutablePath() (exePath string, workDir string, err error) {
	p, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil && strings.TrimSpace(resolved) != "" {
		p = resolved
	}
	if abs, err := filepath.Abs(p); err == nil && strings.TrimSpace(abs) != "" {
		p = abs
	}
	dir := filepath.Dir(p)
	// When running inside FocusCamera.app/Contents/MacOS/FocusCamera, step out of the .app bundle for WorkingDirectory
	if strings.HasSuffix(filepath.ToSlash(dir), ".app/Contents/MacOS") {
		dir = filepath.Dir(filepath.Dir(filepath.Dir(dir)))
	}
	return p, dir, nil
}

func IsTempBuildBinary(exePath string) bool {
	s := filepath.ToSlash(strings.ToLower(exePath))
	return strings.Contains(s, "/go-build") || strings.HasSuffix(s, ".test") || strings.HasSuffix(s, ".test.exe")
}

func IsWindowsUNCPath(exePath string) bool {
	return runtime.GOOS == "windows" && strings.HasPrefix(strings.TrimSpace(exePath), `\\`)
}

func GetStatus(cfgEnabled bool, cfgMode string) Status {
	mode := config.NormalizeAutoStartMode(cfgMode)
	exePath, _, _ := ResolveExecutablePath()
	isUNC := IsWindowsUNCPath(exePath)

	supported, osReg, regCmd := queryOSAutoStart()
	return Status{
		Supported:      supported,
		Enabled:        cfgEnabled,
		OSRegistered:   osReg,
		Mode:           mode,
		ExecutablePath: exePath,
		RegisteredCmd:  regCmd,
		IsNetworkPath:  isUNC,
	}
}

func Apply(enabled bool, mode string) (Status, error) {
	normMode := config.NormalizeAutoStartMode(mode)
	exePath, workDir, err := ResolveExecutablePath()
	if err != nil {
		return GetStatus(enabled, normMode), fmt.Errorf("无法解析当前可执行文件路径: %w", err)
	}
	if enabled {
		if err := enableOSAutoStart(exePath, workDir); err != nil {
			return GetStatus(false, normMode), err
		}
	} else {
		if err := disableOSAutoStart(); err != nil {
			return GetStatus(enabled, normMode), err
		}
	}
	return GetStatus(enabled, normMode), nil
}

// SyncOnStartup checks whether auto-start is enabled in cameras.json and automatically
// updates the OS auto-start entry if the user moved the portable executable/app to a new folder.
func SyncOnStartup(cfgEnabled bool, cfgMode string) (healed bool, err error) {
	if !cfgEnabled {
		return false, nil
	}
	exePath, workDir, err := ResolveExecutablePath()
	if err != nil || exePath == "" {
		return false, err
	}
	if IsTempBuildBinary(exePath) {
		return false, nil
	}
	supported, osReg, regCmd := queryOSAutoStart()
	if !supported {
		return false, nil
	}
	if osReg && commandMatchesExecutable(regCmd, exePath) {
		return false, nil
	}
	if err := enableOSAutoStart(exePath, workDir); err != nil {
		return false, err
	}
	return true, nil
}

func commandMatchesExecutable(regCmd, exePath string) bool {
	c := strings.TrimSpace(regCmd)
	e := strings.TrimSpace(exePath)
	if c == "" || e == "" {
		return false
	}
	expectedWin := fmt.Sprintf(`"%s" --autostart`, e)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(c, expectedWin) || strings.EqualFold(c, e)
	}
	return c == e || c == expectedWin
}
