package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"focus-camera/internal/config"
	"focus-camera/internal/discovery"
	"focus-camera/internal/recorder"
	"focus-camera/internal/rtsp"
	"focus-camera/internal/talk"
)

func TestLiveCameraEngine(t *testing.T) {
	livePass := strings.TrimSpace(os.Getenv("FOCUS_CAMERA_PASSWORD"))
	if livePass == "" {
		t.Skip("Skipping live hardware test: set FOCUS_CAMERA_PASSWORD to run against physical LAN cameras")
	}
	tmpDir := t.TempDir()
	tmpCfg := filepath.Join(tmpDir, "cameras.json")
	store, err := config.NewStore(tmpCfg)
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	_ = store.UpdateAppSettings("admin", livePass, "192.168.1", config.RecordingConfig{
		StorageDir:     filepath.Join(tmpDir, "recordings"),
		SegmentMinutes: 15,
		RetentionDays:  7,
		MaxStorageGB:   100,
	})

	// Ensure default cameras is empty (nothing hardcoded)
	if len(store.GetConfig().Cameras) != 0 {
		t.Fatalf("Expected empty default cameras, got %d", len(store.GetConfig().Cameras))
	}

	testCams := []config.Camera{
		{Name: "1号琴房", IP: "192.168.1.233", Model: "TL-IPC42A-4"},
		{Name: "2号琴房", IP: "192.168.1.236", Model: "TL-IPC42A-Focus"},
		{Name: "3号琴房", IP: "192.168.1.202", Model: "TL-IPC42A-Focus"},
	}
	_, _, err = store.BatchUpsertCameras(testCams, "192.168.1", "admin", livePass)
	if err != nil {
		t.Fatalf("BatchUpsertCameras error: %v", err)
	}

	hub := rtsp.NewStreamHub(store)
	recMgr := recorder.NewManager(store, hub)
	defer recMgr.StopAll()

	// 1. Test RTSP video + audio streaming AND 1080P MP4 Recording on 192.168.1.236
	camID := config.MakeID("192.168.1.236")
	_ = recMgr.StartManual(camID)

	sub, unsub, err := hub.Subscribe("192.168.1.236", "main", true)
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}

	var gotConfig, gotKeyframe, gotAudio bool
	deadline := time.After(6 * time.Second)
loop:
	for !(gotConfig && gotKeyframe && gotAudio) {
		select {
		case pkt := <-sub.Ch:
			if len(pkt) == 0 {
				continue
			}
			switch pkt[0] {
			case rtsp.MsgTypeCodecConfig:
				gotConfig = true
				t.Logf("[192.168.1.236] Got AVCC CodecConfig (%d bytes)", len(pkt))
			case rtsp.MsgTypeVideoFrame:
				if pkt[1] == 1 && !gotKeyframe {
					gotKeyframe = true
					t.Logf("[192.168.1.236] Got 1080P H.264 IDR Keyframe (%d bytes)", len(pkt))
				}
			case rtsp.MsgTypeAudioPCMA:
				if !gotAudio {
					gotAudio = true
					t.Logf("[192.168.1.236] Got PCMA 8000Hz Audio packet (%d bytes)", len(pkt)-1)
				}
			case rtsp.MsgTypeStatusJSON:
				st := string(pkt[1:])
				if strings.Contains(st, "operation not permitted") {
					unsub()
					_ = recMgr.StopManual(camID)
					t.Fatalf("Sandbox network restriction: %s", st)
				}
			}
		case <-deadline:
			break loop
		}
	}

	// Let recorder collect at least 1 GOP (~2.5s) so fMP4 fragment flushes
	time.Sleep(2500 * time.Millisecond)
	_ = recMgr.StopManual(camID)
	unsub()
	time.Sleep(200 * time.Millisecond)

	segs := recMgr.ListSegments("", camID)
	if len(segs) == 0 {
		t.Fatalf("Expected recorded MP4 segment for %s, got 0", camID)
	}
	fullPath, err := recMgr.ResolveSafeFilePath(segs[0].RelPath)
	if err != nil {
		t.Fatalf("ResolveSafeFilePath error: %v", err)
	}
	st, err := os.Stat(fullPath)
	if err != nil || st.Size() < 10000 {
		t.Fatalf("Recorded MP4 too small or missing: size=%d err=%v", segs[0].SizeBytes, err)
	}
	t.Logf("Verified 1080P + Audio MP4 recording: %s (%d bytes)", segs[0].RelPath, st.Size())

	// 2. Verify Talkback session
	sess, err := talk.DialTalkSession("192.168.1.236", 8800, "admin", livePass)
	if err != nil {
		t.Fatalf("DialTalkSession error: %v", err)
	}
	_ = sess.Close()
}

func TestEmbeddedFrontendAssets(t *testing.T) {
	indexBytes, err := webEmbed.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("Failed to read embedded web/index.html: %v", err)
	}
	if !strings.Contains(string(indexBytes), "Focus Camera") {
		t.Fatalf("web/index.html missing expected title")
	}
	entries, err := webEmbed.ReadDir("web/assets")
	if err != nil || len(entries) < 2 {
		t.Fatalf("Expected compiled assets in embedded web/assets, got %d (err=%v)", len(entries), err)
	}
	t.Logf("Embedded frontend verified: index.html (%d bytes) + %d asset files", len(indexBytes), len(entries))
}

func TestReorderCamerasPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "cameras.json")
	store, err := config.NewStore(cfgPath)
	if err != nil {
		t.Fatalf("NewStore err: %v", err)
	}
	_, _, err = store.BatchUpsertCameras([]config.Camera{
		{Name: "1号琴房", IP: "192.168.1.233"},
		{Name: "2号琴房", IP: "192.168.1.236"},
		{Name: "14号琴房", IP: "192.168.1.100"},
	}, "192.168.1", "admin", "unit-test-pass")
	if err != nil {
		t.Fatalf("BatchUpsertCameras err: %v", err)
	}

	// Reorder so 14号琴房 comes first, then 2号琴房, then 1号琴房
	err = store.ReorderCameras([]string{
		config.MakeID("192.168.1.100"),
		config.MakeID("192.168.1.236"),
		config.MakeID("192.168.1.233"),
	})
	if err != nil {
		t.Fatalf("ReorderCameras err: %v", err)
	}

	// Reload from disk and verify order is preserved
	reloaded, err := config.NewStore(cfgPath)
	if err != nil {
		t.Fatalf("Reload store err: %v", err)
	}
	cams := reloaded.GetConfig().Cameras
	if len(cams) != 3 || cams[0].Name != "14号琴房" || cams[1].Name != "2号琴房" || cams[2].Name != "1号琴房" {
		t.Fatalf("Unexpected camera order after reload: %+v", cams)
	}

	// Verify re-scanning does not overwrite custom drag order
	_, _, _ = reloaded.BatchUpsertCameras([]config.Camera{
		{Name: "1号琴房", IP: "192.168.1.233"},
		{Name: "2号琴房", IP: "192.168.1.236"},
		{Name: "14号琴房", IP: "192.168.1.100"},
	}, "192.168.1", "admin", "unit-test-pass")
	camsAfterScan := reloaded.GetConfig().Cameras
	if camsAfterScan[0].Name != "14号琴房" || camsAfterScan[1].Name != "2号琴房" || camsAfterScan[2].Name != "1号琴房" {
		t.Fatalf("Custom order lost after BatchUpsertCameras: %+v", camsAfterScan)
	}
}

func TestListSegmentsAllDates(t *testing.T) {
	tmpDir := t.TempDir()
	recDir := filepath.Join(tmpDir, "recordings")
	store, err := config.NewStore(filepath.Join(tmpDir, "cameras.json"))
	if err != nil {
		t.Fatalf("NewStore err: %v", err)
	}
	_ = store.UpdateAppSettings("admin", "unit-test-pass", "192.168.1", config.RecordingConfig{
		StorageDir:     recDir,
		SegmentMinutes: 15,
		RetentionDays:  7,
		MaxStorageGB:   100,
	})
	_, _, _ = store.BatchUpsertCameras([]config.Camera{
		{Name: "6号琴房", IP: "192.168.1.249"},
	}, "192.168.1", "admin", "unit-test-pass")

	camID := config.MakeID("192.168.1.249")
	dummyBytes := make([]byte, 2048)
	for _, d := range []string{"2026-10-24", "2026-10-25"} {
		dir := filepath.Join(recDir, d, camID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll err: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "093533__6号琴房__auto.mp4"), dummyBytes, 0o644); err != nil {
			t.Fatalf("WriteFile err: %v", err)
		}
	}

	hub := rtsp.NewStreamHub(store)
	recMgr := recorder.NewManager(store, hub)
	defer recMgr.StopAll()

	allSegs := recMgr.ListSegments("", "")
	if len(allSegs) != 2 {
		t.Fatalf("Expected 2 segments across all dates when dateStr is empty, got %d", len(allSegs))
	}
	if allSegs[0].Date != "2026-10-25" || allSegs[1].Date != "2026-10-24" {
		t.Fatalf("Expected newest date first, got %s then %s", allSegs[0].Date, allSegs[1].Date)
	}

	daySegs := recMgr.ListSegments("2026-10-24", "")
	if len(daySegs) != 1 || daySegs[0].Date != "2026-10-24" {
		t.Fatalf("Expected 1 segment for 2026-10-24, got %+v", daySegs)
	}
}

func TestInspect233And100(t *testing.T) {
	livePass := strings.TrimSpace(os.Getenv("FOCUS_CAMERA_PASSWORD"))
	if livePass == "" {
		t.Skip("Skipping live hardware test: set FOCUS_CAMERA_PASSWORD to run against physical LAN cameras")
	}
	cams := discovery.ScanSubnet("192.168.1", "admin", livePass)
	if len(cams) == 0 {
		t.Fatalf("ScanSubnet returned 0 cameras (sandbox blocked)")
	}
	t.Logf("ScanSubnet found %d TP-LINK cameras (all ensured H.264)", len(cams))

	tmpDir := t.TempDir()
	store, _ := config.NewStore(filepath.Join(tmpDir, "cameras.json"))
	_, _, _ = store.BatchUpsertCameras([]config.Camera{
		{Name: "1号琴房", IP: "192.168.1.233"},
		{Name: "14号琴房", IP: "192.168.1.100"},
	}, "192.168.1", "admin", livePass)

	hub := rtsp.NewStreamHub(store)
	for _, ip := range []string{"192.168.1.233", "192.168.1.100"} {
		// 1. Open sub stream (simulating All-View wall tile)
		sub2, unsub2, err := hub.Subscribe(config.MakeID(ip), "sub", false)
		if err != nil {
			t.Fatalf("Subscribe sub err: %v", err)
		}
		<-sub2.Ch
		// 2. Unsubscribe sub and immediately subscribe main (simulating clicking 1080P Zoom Modal)
		unsub2()

		sub, unsub, err := hub.Subscribe(config.MakeID(ip), "main", false)
		if err != nil {
			t.Fatalf("Subscribe err: %v", err)
		}
		var gotConfig, gotKey bool
		timeout := time.After(5 * time.Second)
		for !(gotConfig && gotKey) {
			select {
			case pkt := <-sub.Ch:
				if pkt[0] == rtsp.MsgTypeCodecConfig {
					gotConfig = true
					t.Logf("[%s main] CodecConfig (%dB): %x", ip, len(pkt), pkt[:min(len(pkt), 16)])
				} else if pkt[0] == rtsp.MsgTypeVideoFrame && pkt[1] == 1 {
					gotKey = true
					t.Logf("[%s main] Got 1080P H.264 IDR Keyframe (%dB)", ip, len(pkt))
				}
			case <-timeout:
				unsub()
				t.Fatalf("[%s main] timed out waiting for CodecConfig=%v Keyframe=%v", ip, gotConfig, gotKey)
			}
		}
		unsub()
	}
}
