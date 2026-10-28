//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const darwinLaunchAgentLabel = "com.focus.camera"

var plistFirstArgRe = regexp.MustCompile(`(?s)<key>ProgramArguments</key>\s*<array>\s*<string>(.*?)</string>`)

func darwinLaunchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("无法获取当前用户主目录: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", darwinLaunchAgentLabel+".plist"), nil
}

func xmlEscapeString(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

func xmlUnescapeString(s string) string {
	r := strings.NewReplacer(
		"&apos;", "'",
		"&quot;", "\"",
		"&gt;", ">",
		"&lt;", "<",
		"&amp;", "&",
	)
	return r.Replace(s)
}

func buildDarwinLaunchAgentPlist(exePath, workDir string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>--autostart</string>
	</array>
	<key>WorkingDirectory</key>
	<string>%s</string>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`, darwinLaunchAgentLabel, xmlEscapeString(exePath), xmlEscapeString(workDir))
}

func parseDarwinLaunchAgentExecutable(plistContent string) string {
	m := plistFirstArgRe.FindStringSubmatch(plistContent)
	if len(m) > 1 {
		return strings.TrimSpace(xmlUnescapeString(m[1]))
	}
	return ""
}

func queryOSAutoStart() (supported bool, registered bool, registeredCmd string) {
	plistPath, err := darwinLaunchAgentPath()
	if err != nil {
		return true, false, ""
	}
	data, err := os.ReadFile(plistPath)
	if err != nil {
		return true, false, ""
	}
	cmd := parseDarwinLaunchAgentExecutable(string(data))
	if cmd == "" {
		return true, false, ""
	}
	return true, true, cmd
}

func enableOSAutoStart(exePath, workDir string) error {
	plistPath, err := darwinLaunchAgentPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("无法创建 LaunchAgents 目录: %w", err)
	}
	content := buildDarwinLaunchAgentPlist(exePath, workDir)
	if err := os.WriteFile(plistPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("无法写入 macOS 登录启动项 (%s): %w", plistPath, err)
	}
	return nil
}

func disableOSAutoStart() error {
	plistPath, err := darwinLaunchAgentPath()
	if err != nil {
		return err
	}
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("无法移除 macOS 登录启动项 (%s): %w", plistPath, err)
	}
	return nil
}
