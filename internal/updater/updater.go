package updater

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"focus-camera/internal/config"
)

// CurrentVersion is the current application version (can be overridden via -ldflags).
var CurrentVersion = "v2.4.1"

// DefaultUpdateURL points to the official GitHub Releases endpoint for FocusCamera.
const DefaultUpdateURL = "https://api.github.com/repos/focus0802/focus-camera/releases/latest"

var githubMirrorPrefixes = []string{
	"https://ghfast.top/",
	"https://gh-proxy.com/",
}

type AssetInfo struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// Manifest represents the remote or LAN version.json payload.
type Manifest struct {
	Version     string               `json:"version"`
	PublishedAt string               `json:"published_at,omitempty"`
	Notes       string               `json:"notes,omitempty"`
	Assets      map[string]AssetInfo `json:"assets"`
}

// Status represents the current state of the controlled self-updater.
type Status struct {
	CurrentVersion  string `json:"current_version"`
	OSArch          string `json:"os_arch"`
	ExecutablePath  string `json:"executable_path"`
	UpdateURL       string `json:"update_url"`
	State           string `json:"state"` // "idle" | "checking" | "available" | "up_to_date" | "downloading" | "verifying" | "applying" | "restarting" | "error"
	HasUpdate       bool   `json:"has_update"`
	LatestVersion   string `json:"latest_version,omitempty"`
	PublishedAt     string `json:"published_at,omitempty"`
	ReleaseNotes    string `json:"release_notes,omitempty"`
	DownloadURL     string `json:"download_url,omitempty"`
	ExpectedSHA256  string `json:"expected_sha256,omitempty"`
	DownloadedBytes int64  `json:"downloaded_bytes"`
	TotalBytes      int64  `json:"total_bytes"`
	ProgressPct     int    `json:"progress_pct"`
	LastCheckedAt   string `json:"last_checked_at,omitempty"`
	Message         string `json:"message,omitempty"`
	Error           string `json:"error,omitempty"`
}

type Manager struct {
	mu            sync.RWMutex
	store         *config.Store
	status        Status
	minBinarySize int64
}

func NewManager(store *config.Store) *Manager {
	exePath := resolveExecutablePath()
	CleanupOldExecutable(exePath)

	cfg := store.GetConfig()
	initialURL := strings.TrimSpace(cfg.UpdateURL)
	if initialURL == "" {
		initialURL = DefaultUpdateURL
	}
	m := &Manager{
		store:         store,
		minBinarySize: 512 * 1024, // 512 KB minimum sanity check for production binaries
		status: Status{
			CurrentVersion: CurrentVersion,
			OSArch:         runtime.GOOS + "/" + runtime.GOARCH,
			ExecutablePath: exePath,
			UpdateURL:      initialURL,
			State:          "idle",
		},
	}
	return m
}

func resolveExecutablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// CleanupOldExecutable removes any leftover .old or .new.tmp files from a previous upgrade.
func CleanupOldExecutable(exePath string) {
	if exePath == "" {
		return
	}
	_ = os.Remove(exePath + ".old")
	_ = os.Remove(exePath + ".new.tmp")
}

func (m *Manager) GetStatus() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := m.status
	if cfgURL := strings.TrimSpace(m.store.GetConfig().UpdateURL); cfgURL != "" {
		st.UpdateURL = cfgURL
	} else if st.UpdateURL == "" {
		st.UpdateURL = DefaultUpdateURL
	}
	return st
}

// CompareVersions compares two semantic version strings (e.g. "v2.1.0" vs "v2.0.0").
// Returns 1 if v1 > v2, -1 if v1 < v2, and 0 if equal.
func CompareVersions(v1, v2 string) int {
	p1 := parseSemVer(v1)
	p2 := parseSemVer(v2)
	for i := 0; i < 3; i++ {
		if p1[i] > p2[i] {
			return 1
		}
		if p1[i] < p2[i] {
			return -1
		}
	}
	return 0
}

func parseSemVer(v string) [3]int {
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if idx := strings.IndexAny(s, "-+"); idx >= 0 {
		s = s[:idx]
	}
	parts := strings.Split(s, ".")
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		if n, err := strconv.Atoi(strings.TrimSpace(parts[i])); err == nil {
			out[i] = n
		}
	}
	return out
}

// CheckSilent performs a non-intrusive startup check; if offline or no release exists yet,
// it quietly resets state to "idle" instead of displaying a startup error banner.
func (m *Manager) CheckSilent() {
	st, err := m.Check("")
	if err != nil && !st.HasUpdate {
		m.mu.Lock()
		m.status.State = "idle"
		m.status.Error = ""
		m.status.Message = ""
		m.mu.Unlock()
	}
}

// Check queries the configured (or overridden) update manifest URL or LAN path.
func (m *Manager) Check(overrideURL string) (Status, error) {
	m.mu.Lock()
	if m.status.State == "downloading" || m.status.State == "applying" || m.status.State == "restarting" {
		st := m.status
		m.mu.Unlock()
		return st, errors.New("正在执行升级任务，请稍候")
	}
	targetURL := strings.TrimSpace(overrideURL)
	if targetURL != "" {
		_ = m.store.SetUpdateURL(targetURL)
	} else {
		targetURL = strings.TrimSpace(m.store.GetConfig().UpdateURL)
	}
	if targetURL == "" {
		// Check if a local ./updates/version.json exists next to the config file first
		localCandidate := filepath.Join("updates", "version.json")
		if _, err := os.Stat(localCandidate); err == nil {
			targetURL = localCandidate
		} else {
			targetURL = DefaultUpdateURL
		}
	}

	m.status.UpdateURL = targetURL
	m.status.State = "checking"
	m.status.Error = ""
	m.status.Message = "正在连接更新源检查新版本…"
	m.mu.Unlock()

	manifest, baseRef, err := fetchManifest(targetURL)
	nowStr := time.Now().Format("2006-01-02 15:04:05")

	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.LastCheckedAt = nowStr

	if err != nil {
		m.status.State = "error"
		m.status.Error = fmt.Sprintf("检查更新失败: %v", err)
		m.status.Message = ""
		return m.status, err
	}

	asset, ok := matchPlatformAsset(manifest.Assets, runtime.GOOS, runtime.GOARCH)
	resolvedAssetURL := ""
	if ok && asset.URL != "" {
		resolvedAssetURL = resolveRelativeAsset(baseRef, asset.URL)
	}

	cmp := CompareVersions(manifest.Version, CurrentVersion)
	m.status.LatestVersion = manifest.Version
	m.status.PublishedAt = manifest.PublishedAt
	m.status.ReleaseNotes = manifest.Notes
	m.status.DownloadURL = resolvedAssetURL
	m.status.ExpectedSHA256 = strings.ToLower(strings.TrimSpace(asset.SHA256))
	m.status.TotalBytes = asset.Size
	m.status.DownloadedBytes = 0
	m.status.ProgressPct = 0

	if cmp > 0 {
		if resolvedAssetURL == "" {
			m.status.State = "error"
			m.status.HasUpdate = false
			m.status.Error = fmt.Sprintf("发现新版本 %s，但更新源中未包含当前系统 (%s/%s) 的安装包", manifest.Version, runtime.GOOS, runtime.GOARCH)
			return m.status, errors.New(m.status.Error)
		}
		m.status.State = "available"
		m.status.HasUpdate = true
		m.status.Message = fmt.Sprintf("发现新版本 %s（当前版本 %s），可随时一键安全升级", manifest.Version, CurrentVersion)
	} else {
		m.status.State = "up_to_date"
		m.status.HasUpdate = false
		m.status.Message = fmt.Sprintf("当前已是最新版本 (%s)", CurrentVersion)
	}
	return m.status, nil
}

func buildCandidateURLs(rawURL string) []string {
	u := strings.TrimSpace(rawURL)
	candidates := []string{u}
	if strings.HasPrefix(u, "https://github.com/") || strings.HasPrefix(u, "https://raw.githubusercontent.com/") {
		for _, prefix := range githubMirrorPrefixes {
			candidates = append(candidates, prefix+u)
		}
	}
	return candidates
}

func fetchHTTPBytesWithFallback(rawURL string, timeout time.Duration) ([]byte, error) {
	candidates := buildCandidateURLs(rawURL)
	var lastErr error
	for _, candidate := range candidates {
		client := &http.Client{Timeout: timeout}
		req, err := http.NewRequest(http.MethodGet, candidate, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "FocusCamera-Updater/"+CurrentVersion)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d (%s)", resp.StatusCode, resp.Status)
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return body, nil
	}
	return nil, lastErr
}

func fetchManifest(source string) (*Manifest, string, error) {
	src := strings.TrimSpace(source)
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		body, err := fetchHTTPBytesWithFallback(src, 10*time.Second)
		if err != nil {
			return nil, "", err
		}
		mf, err := parseManifestBytes(body)
		return mf, src, err
	}

	// Local path or LAN UNC share path
	cleanPath := src
	if info, err := os.Stat(cleanPath); err == nil && info.IsDir() {
		cleanPath = filepath.Join(cleanPath, "version.json")
	}
	body, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, "", err
	}
	mf, err := parseManifestBytes(body)
	return mf, cleanPath, err
}

func parseManifestBytes(data []byte) (*Manifest, error) {
	// 1. Try standard FocusCamera version.json format
	var mf Manifest
	if err := json.Unmarshal(data, &mf); err == nil && mf.Version != "" && len(mf.Assets) > 0 {
		return &mf, nil
	}

	// 2. Try GitHub Releases API format (/repos/{owner}/{repo}/releases/latest)
	var gh struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
		Assets      []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &gh); err == nil && gh.TagName != "" {
		out := &Manifest{
			Version:     gh.TagName,
			PublishedAt: gh.PublishedAt,
			Notes:       gh.Body,
			Assets:      make(map[string]AssetInfo),
		}
		var versionAssetURL string
		for _, a := range gh.Assets {
			lower := strings.ToLower(a.Name)
			if lower == "version.json" {
				versionAssetURL = a.BrowserDownloadURL
				continue
			}
			info := AssetInfo{
				URL:  a.BrowserDownloadURL,
				Size: a.Size,
			}
			switch {
			case strings.Contains(lower, "windows") && !strings.Contains(lower, "console"):
				out.Assets["windows_amd64"] = info
				out.Assets["windows"] = info
			case strings.Contains(lower, "mac") || strings.Contains(lower, "darwin"):
				out.Assets["darwin_arm64"] = info
				out.Assets["darwin_amd64"] = info
				out.Assets["darwin"] = info
			}
		}
		// Enrich with SHA-256 hashes from the release's attached version.json if present
		if versionAssetURL != "" {
			if vBytes, err := fetchHTTPBytesWithFallback(versionAssetURL, 6*time.Second); err == nil {
				var vMf Manifest
				if json.Unmarshal(vBytes, &vMf) == nil {
					for k, cur := range out.Assets {
						if rich, ok := vMf.Assets[k]; ok && rich.SHA256 != "" {
							cur.SHA256 = rich.SHA256
							out.Assets[k] = cur
						}
					}
				}
			}
		}
		return out, nil
	}

	return nil, errors.New("无法解析 version.json 清单格式（需包含 version 与 assets 字段）")
}

func matchPlatformAsset(assets map[string]AssetInfo, goos, goarch string) (AssetInfo, bool) {
	keys := []string{
		goos + "_" + goarch,
		goos + "-" + goarch,
		goos,
	}
	if goos == "darwin" {
		keys = append(keys, "macos", "mac", "darwin_universal")
	}
	if goos == "windows" && goarch == "arm64" {
		// Windows 11 ARM64 runs x64 binaries seamlessly
		keys = append(keys, "windows_amd64", "windows-amd64", "windows_x64")
	}
	for _, k := range keys {
		if a, ok := assets[k]; ok && strings.TrimSpace(a.URL) != "" {
			return a, true
		}
	}
	return AssetInfo{}, false
}

func resolveRelativeAsset(baseRef, assetURL string) string {
	u := strings.TrimSpace(assetURL)
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") || filepath.IsAbs(u) || strings.HasPrefix(u, `\\`) {
		return u
	}
	if strings.HasPrefix(baseRef, "http://") || strings.HasPrefix(baseRef, "https://") {
		baseU, err := url.Parse(baseRef)
		if err != nil {
			return u
		}
		relU, err := url.Parse(u)
		if err != nil {
			return u
		}
		return baseU.ResolveReference(relU).String()
	}
	return filepath.Join(filepath.Dir(baseRef), u)
}

// ValidateExecutableHeader checks that the downloaded file starts with a valid
// Mach-O header (on darwin) or PE 'MZ' header (on windows) so an HTML 404 page
// or truncated file can never overwrite the application binary.
func ValidateExecutableHeader(filePath string, targetOS string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	var hdr [4]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return fmt.Errorf("读取可执行文件头失败: %w", err)
	}

	switch targetOS {
	case "windows":
		if hdr[0] != 'M' || hdr[1] != 'Z' {
			return errors.New("安装包文件头校验失败：不是有效的 Windows PE (.exe) 可执行程序")
		}
	case "darwin":
		magicBE := binary.BigEndian.Uint32(hdr[:])
		magicLE := binary.LittleEndian.Uint32(hdr[:])
		validMachO := magicBE == 0xCAFEBABE || // Universal binary
			magicBE == 0xFEEDFACF || magicLE == 0xFEEDFACF || // 64-bit Mach-O
			magicBE == 0xFEEDFACE || magicLE == 0xFEEDFACE
		if !validMachO {
			return errors.New("安装包文件头校验失败：不是有效的 macOS Mach-O 可执行程序")
		}
	}
	return nil
}

// StartUpgrade downloads the verified update binary, flushes all recordings via onBeforeReplace,
// performs an atomic rename (.old backup), and spawns the new process.
func (m *Manager) StartUpgrade(onBeforeReplace func(), autoRestart bool) error {
	m.mu.Lock()
	if m.status.State == "downloading" || m.status.State == "verifying" || m.status.State == "applying" || m.status.State == "restarting" {
		m.mu.Unlock()
		return errors.New("升级任务已在运行中")
	}
	dlURL := m.status.DownloadURL
	expectedHash := m.status.ExpectedSHA256
	exePath := m.status.ExecutablePath
	latestVer := m.status.LatestVersion
	if dlURL == "" {
		m.mu.Unlock()
		return errors.New("请先检查更新并确认存在可用的新版本安装包")
	}
	if exePath == "" {
		m.mu.Unlock()
		return errors.New("无法定位当前可执行文件路径")
	}

	m.status.State = "downloading"
	m.status.Error = ""
	m.status.DownloadedBytes = 0
	m.status.ProgressPct = 0
	m.status.Message = fmt.Sprintf("正在下载 %s 安装包…", latestVer)
	m.mu.Unlock()

	go m.runUpgradeWorker(dlURL, expectedHash, exePath, onBeforeReplace, autoRestart)
	return nil
}

func (m *Manager) runUpgradeWorker(dlURL, expectedHash, exePath string, onBeforeReplace func(), autoRestart bool) {
	tmpPath := exePath + ".new.tmp"
	oldPath := exePath + ".old"
	_ = os.Remove(tmpPath)

	actualHash, written, err := m.downloadToFile(dlURL, tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		m.failUpgrade(fmt.Sprintf("下载新版本失败: %v", err))
		return
	}

	m.mu.Lock()
	m.status.State = "verifying"
	m.status.ProgressPct = 100
	m.status.Message = "正在校验安装包完整性与 SHA-256 签名…"
	minSize := m.minBinarySize
	m.mu.Unlock()

	if written < minSize {
		_ = os.Remove(tmpPath)
		m.failUpgrade(fmt.Sprintf("安装包体积异常 (%d 字节)，已中止替换以保护系统", written))
		return
	}

	if err := ValidateExecutableHeader(tmpPath, runtime.GOOS); err != nil {
		_ = os.Remove(tmpPath)
		m.failUpgrade(err.Error())
		return
	}

	if expectedHash != "" && !strings.EqualFold(actualHash, expectedHash) {
		_ = os.Remove(tmpPath)
		m.failUpgrade(fmt.Sprintf("SHA-256 哈希校验不匹配 (期望 %s，实际 %s)", expectedHash, actualHash))
		return
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		_ = os.Remove(tmpPath)
		m.failUpgrade(fmt.Sprintf("设置可执行权限失败: %v", err))
		return
	}

	m.mu.Lock()
	m.status.State = "applying"
	m.status.Message = "正在安全落盘所有 1080P 录像分片并原子替换主程序…"
	m.mu.Unlock()

	if onBeforeReplace != nil {
		onBeforeReplace()
	}

	if err := ReplaceExecutableAtomically(exePath, tmpPath, oldPath); err != nil {
		_ = os.Remove(tmpPath)
		m.failUpgrade(fmt.Sprintf("替换主程序失败: %v", err))
		return
	}

	if runtime.GOOS == "darwin" {
		_ = exec.Command("/usr/bin/codesign", "--force", "--sign", "-", exePath).Run()
	}

	m.mu.Lock()
	m.status.State = "restarting"
	m.status.CurrentVersion = m.status.LatestVersion
	m.status.HasUpdate = false
	m.status.Message = "升级已完成，正在重新启动新版本客户端…"
	m.mu.Unlock()

	log.Printf("[Updater] 已成功升级至 %s (%s)，准备重启进程...", m.status.LatestVersion, exePath)

	if autoRestart {
		time.Sleep(700 * time.Millisecond)
		cmd := exec.Command(exePath, os.Args[1:]...)
		cmd.Env = os.Environ()
		if err := cmd.Start(); err != nil {
			log.Printf("[Updater] 自动拉起新进程失败 (%v)，请手动重新打开客户端", err)
			return
		}
		os.Exit(0)
	}
}

// ReplaceExecutableAtomically moves currentExe -> oldExe, then newTmpExe -> currentExe,
// rolling back automatically if the second rename fails.
func ReplaceExecutableAtomically(currentExe, newTmpExe, oldExe string) error {
	_ = os.Remove(oldExe)
	if err := os.Rename(currentExe, oldExe); err != nil {
		return fmt.Errorf("备份当前程序失败: %w", err)
	}
	if err := os.Rename(newTmpExe, currentExe); err != nil {
		// Rollback so the user is never left without a working binary
		_ = os.Rename(oldExe, currentExe)
		return fmt.Errorf("写入新版本程序失败 (已自动回滚): %w", err)
	}
	return nil
}

func (m *Manager) downloadToFile(src string, dstPath string) (string, int64, error) {
	var reader io.ReadCloser
	var total int64

	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		candidates := buildCandidateURLs(src)
		var lastErr error
		for _, candidate := range candidates {
			client := &http.Client{Timeout: 5 * time.Minute}
			req, err := http.NewRequest(http.MethodGet, candidate, nil)
			if err != nil {
				lastErr = err
				continue
			}
			req.Header.Set("User-Agent", "FocusCamera-Updater/"+CurrentVersion)
			resp, err := client.Do(req)
			if err != nil {
				lastErr = err
				continue
			}
			if resp.StatusCode != http.StatusOK {
				_ = resp.Body.Close()
				lastErr = fmt.Errorf("HTTP %d (%s)", resp.StatusCode, resp.Status)
				continue
			}
			reader = resp.Body
			total = resp.ContentLength
			lastErr = nil
			break
		}
		if lastErr != nil {
			return "", 0, lastErr
		}
	} else {
		f, err := os.Open(src)
		if err != nil {
			return "", 0, err
		}
		if info, err := f.Stat(); err == nil {
			total = info.Size()
		}
		reader = f
	}
	defer reader.Close()

	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", 0, err
	}
	defer out.Close()

	m.mu.Lock()
	if total > 0 {
		m.status.TotalBytes = total
	} else {
		total = m.status.TotalBytes
	}
	m.mu.Unlock()

	hasher := sha256.New()
	buf := make([]byte, 64*1024)
	var written int64

	for {
		n, rErr := reader.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				return "", written, wErr
			}
			_, _ = hasher.Write(buf[:n])
			written += int64(n)

			m.mu.Lock()
			m.status.DownloadedBytes = written
			if total > 0 {
				pct := int((written * 100) / total)
				if pct > 99 {
					pct = 99
				}
				m.status.ProgressPct = pct
			}
			m.mu.Unlock()
		}
		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				break
			}
			return "", written, rErr
		}
	}

	if err := out.Sync(); err != nil {
		return "", written, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), written, nil
}

func (m *Manager) failUpgrade(errMsg string) {
	log.Printf("[Updater] 升级中止: %s", errMsg)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.State = "error"
	m.status.Error = errMsg
	m.status.Message = ""
}
