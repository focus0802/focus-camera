package updater

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"focus-camera/internal/config"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		v1   string
		v2   string
		want int
	}{
		{"v2.0.0", "v2.0.0", 0},
		{"v2.1.0", "v2.0.0", 1},
		{"2.0.10", "v2.0.2", 1},
		{"v1.9.9", "v2.0.0", -1},
		{"v2.0.1-beta", "v2.0.0", 1},
	}
	for _, tc := range cases {
		if got := CompareVersions(tc.v1, tc.v2); got != tc.want {
			t.Fatalf("CompareVersions(%q, %q) = %d, want %d", tc.v1, tc.v2, got, tc.want)
		}
	}
}

func TestParseGitHubManifest(t *testing.T) {
	ghJSON := `{
		"tag_name": "v2.3.0",
		"published_at": "2026-10-25T12:00:00Z",
		"body": "支持一键受控热更新与 SHA-256 校验",
		"assets": [
			{"name": "FocusCamera-Windows-x64.exe", "browser_download_url": "https://example.com/FocusCamera-Windows-x64.exe", "size": 18000000},
			{"name": "FocusCamera-Mac", "browser_download_url": "https://example.com/FocusCamera-Mac", "size": 19000000}
		]
	}`
	mf, err := parseManifestBytes([]byte(ghJSON))
	if err != nil {
		t.Fatalf("parseManifestBytes failed: %v", err)
	}
	if mf.Version != "v2.3.0" {
		t.Fatalf("expected version v2.3.0, got %s", mf.Version)
	}
	if _, ok := matchPlatformAsset(mf.Assets, "windows", "amd64"); !ok {
		t.Fatalf("expected windows_amd64 asset match")
	}
	if _, ok := matchPlatformAsset(mf.Assets, "darwin", "arm64"); !ok {
		t.Fatalf("expected darwin_arm64 asset match")
	}
}

func TestEndToEndControlledUpgrade(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := config.NewStore(filepath.Join(tmpDir, "cameras.json"))
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// Create a mock current executable
	currExe := filepath.Join(tmpDir, "FocusCameraCurrent")
	if err := os.WriteFile(currExe, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write currExe: %v", err)
	}

	// Create a mock update package with valid OS magic header + 64KB payload
	updateDir := filepath.Join(tmpDir, "updates")
	_ = os.MkdirAll(updateDir, 0o755)
	newBinName := "FocusCamera-NewBin"
	newBinPath := filepath.Join(updateDir, newBinName)

	payload := make([]byte, 64*1024)
	if runtime.GOOS == "windows" {
		payload[0] = 'M'
		payload[1] = 'Z'
	} else {
		binary.BigEndian.PutUint32(payload[:4], 0xCAFEBABE)
	}
	copy(payload[16:], []byte("FocusCamera-v2.1.0-Verified-Binary"))
	if err := os.WriteFile(newBinPath, payload, 0o755); err != nil {
		t.Fatalf("write newBinPath: %v", err)
	}

	sum := sha256.Sum256(payload)
	sumHex := hex.EncodeToString(sum[:])

	mf := Manifest{
		Version:     "v9.9.0",
		PublishedAt: "2026-10-25",
		Notes:       "1. 新增受控一键热更新\n2. 升级前自动刷盘所有 1080P MP4 录像",
		Assets: map[string]AssetInfo{
			runtime.GOOS + "_" + runtime.GOARCH: {
				URL:    newBinName, // relative path inside updateDir
				SHA256: sumHex,
				Size:   int64(len(payload)),
			},
		},
	}
	mfBytes, _ := json.MarshalIndent(mf, "", "  ")
	manifestPath := filepath.Join(updateDir, "version.json")
	if err := os.WriteFile(manifestPath, mfBytes, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	mgr := NewManager(store)
	mgr.minBinarySize = 1024
	mgr.status.ExecutablePath = currExe

	st, err := mgr.Check(manifestPath)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !st.HasUpdate || st.State != "available" || st.LatestVersion != "v9.9.0" {
		t.Fatalf("unexpected status after Check: %+v", st)
	}

	flushed := false
	if err := mgr.StartUpgrade(func() {
		flushed = true
	}, false); err != nil {
		t.Fatalf("StartUpgrade failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		cur := mgr.GetStatus()
		if cur.State == "restarting" {
			break
		}
		if cur.State == "error" {
			t.Fatalf("upgrade failed with error: %s", cur.Error)
		}
		time.Sleep(25 * time.Millisecond)
	}

	if !flushed {
		t.Fatalf("expected onBeforeReplace (recording flush) to be called before binary swap")
	}

	replacedBytes, err := os.ReadFile(currExe)
	if err != nil {
		t.Fatalf("read replaced currExe: %v", err)
	}
	if len(replacedBytes) != len(payload) {
		t.Fatalf("expected replaced binary size %d, got %d", len(payload), len(replacedBytes))
	}
	if _, err := os.Stat(currExe + ".old"); err != nil {
		t.Fatalf("expected backup .old binary to exist: %v", err)
	}
}
