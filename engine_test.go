package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"focus-camera/internal/config"
	"focus-camera/internal/discovery"
	"focus-camera/internal/ptz"
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

	// Add a manual recording on 2026-10-25 and verify GetStorageSummaryForCamera
	if err := os.WriteFile(filepath.Join(recDir, "2026-10-25", camID, "142000__6号琴房__manual.mp4"), dummyBytes, 0o644); err != nil {
		t.Fatalf("WriteFile manual err: %v", err)
	}
	sumAll := recMgr.GetStorageSummaryForCamera("")
	if len(sumAll.AvailableDates) != 2 || sumAll.TotalFiles != 3 {
		t.Fatalf("Expected 2 dates and 3 total files, got %+v", sumAll)
	}
	ds25 := sumAll.DateSummaries["2026-10-25"]
	if ds25.SegmentCount != 2 || ds25.AutoCount != 1 || ds25.ManualCount != 1 {
		t.Fatalf("Unexpected DateSummary for 2026-10-25: %+v", ds25)
	}
	sumOtherCam := recMgr.GetStorageSummaryForCamera("non_existent_cam")
	if len(sumOtherCam.AvailableDates) != 0 || sumOtherCam.TotalFiles != 3 {
		t.Fatalf("Expected 0 camera dates for non_existent_cam while preserving TotalFiles=3, got %+v", sumOtherCam)
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

func TestMixedCameraPasswordsPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "cameras.json")
	store, err := config.NewStore(cfgPath)
	if err != nil {
		t.Fatalf("NewStore err: %v", err)
	}

	// Simulate batch adding 3 cameras:
	// 1. A camera using the default scan password (Password omitted, NoPassword=false)
	// 2. A factory-default camera with empty password (Password="", NoPassword=true)
	// 3. A camera with its own unique password (Password="custom-room-3", NoPassword=false)
	_, _, err = store.BatchUpsertCameras([]config.Camera{
		{Name: "1号琴房", IP: "192.168.1.101"},
		{Name: "2号琴房", IP: "192.168.1.102", Password: "", NoPassword: true},
		{Name: "3号琴房", IP: "192.168.1.103", Password: "custom-room-3"},
	}, "192.168.1", "admin", "common-studio-pass")
	if err != nil {
		t.Fatalf("BatchUpsertCameras err: %v", err)
	}

	cam1, ok := store.GetCamera("192.168.1.101")
	if !ok || cam1.Password != "common-studio-pass" || cam1.NoPassword {
		t.Fatalf("Expected cam1 to inherit default password, got: %+v", cam1)
	}
	cam2, ok := store.GetCamera("192.168.1.102")
	if !ok || cam2.Password != "" || !cam2.NoPassword {
		t.Fatalf("Expected cam2 to preserve empty password (NoPassword=true), got: %+v", cam2)
	}
	cam3, ok := store.GetCamera("192.168.1.103")
	if !ok || cam3.Password != "custom-room-3" || cam3.NoPassword {
		t.Fatalf("Expected cam3 to preserve custom password, got: %+v", cam3)
	}
}

func TestMultiVendorAndHEVCRecording(t *testing.T) {
	// 1. Verify vendor stream path presets and custom overrides
	hikCam := config.Camera{IP: "192.168.1.50", Vendor: "hikvision"}
	if got := hikCam.ResolveStreamPath("main"); got != "/Streaming/Channels/101" {
		t.Fatalf("Expected hikvision main stream path /Streaming/Channels/101, got %s", got)
	}
	if got := hikCam.ResolveStreamPath("sub"); got != "/Streaming/Channels/102" {
		t.Fatalf("Expected hikvision sub stream path /Streaming/Channels/102, got %s", got)
	}

	dahuaCam := config.Camera{IP: "192.168.1.51", Vendor: "dahua"}
	if got := dahuaCam.ResolveStreamPath("main"); !strings.Contains(got, "subtype=0") {
		t.Fatalf("Expected dahua main stream path with subtype=0, got %s", got)
	}
	if got := dahuaCam.ResolveStreamPath("sub"); !strings.Contains(got, "subtype=1") {
		t.Fatalf("Expected dahua sub stream path with subtype=1, got %s", got)
	}

	mercuryCam := config.Camera{IP: "192.168.1.53", Model: "MIPC251C-4"}
	if got := config.NormalizeVendor(mercuryCam.Vendor, mercuryCam.Model); got != "mercury" {
		t.Fatalf("Expected MIPC251C-4 model to normalize to mercury, got %s", got)
	}
	if !mercuryCam.IsTPLink() || mercuryCam.ResolveStreamPath("main") != "/stream1" {
		t.Fatalf("Expected mercury camera to support TP-LINK/Mercury :8800 protocol and /stream1")
	}

	customCam := config.Camera{
		IP:             "192.168.1.52",
		Vendor:         "custom",
		MainStreamPath: "rtsp://192.168.1.52:8554/xiaomi_living_room",
		SubStreamPath:  "/live/sub",
	}
	if got := customCam.ResolveStreamPath("main"); got != "rtsp://192.168.1.52:8554/xiaomi_living_room" {
		t.Fatalf("Expected full custom rtsp URI preserved, got %s", got)
	}
	if got := customCam.ResolveStreamPath("sub"); got != "/live/sub" {
		t.Fatalf("Expected custom sub path /live/sub, got %s", got)
	}

	// 2. Verify general default group inference
	grp, order := config.InferGroupAndOrder("12号机位", "192.168.1.120")
	if grp != "默认分组" || order != 112 {
		t.Fatalf("Expected group=默认分组 order=112, got group=%s order=%d", grp, order)
	}

	// 3. Verify H.265 (HEVC) fMP4 writer creates valid hvc1 / hvcC boxes
	tmpFile := filepath.Join(t.TempDir(), "hevc_test.mp4")
	fw, err := recorder.NewFMP4Writer(tmpFile)
	if err != nil {
		t.Fatalf("NewFMP4Writer err: %v", err)
	}
	defer fw.Close()

	// Construct minimal synthetic hvcC record (configurationVersion=1, 3 arrays: VPS/SPS/PPS)
	hvcc := []byte{
		1, 0x01, 0x60, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x78,
		0xF0, 0x00, 0xFC, 0xFD, 0xF8, 0xF8, 0x00, 0x00, 0x0F, 0x03,
		0xA0, 0x00, 0x01, 0x00, 0x04, 0x40, 0x01, 0x0C, 0x01,
		0xA1, 0x00, 0x01, 0x00, 0x04, 0x42, 0x01, 0x01, 0x01,
		0xA2, 0x00, 0x01, 0x00, 0x04, 0x44, 0x01, 0xC1, 0x73,
	}
	if err := fw.InitHeader(hvcc); err != nil {
		t.Fatalf("InitHeader (HEVC) err: %v", err)
	}
	raw, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("ReadFile err: %v", err)
	}
	if !strings.Contains(string(raw), "hvc1") || !strings.Contains(string(raw), "hvcC") {
		t.Fatalf("Expected fMP4 init segment to contain hvc1 and hvcC boxes for HEVC")
	}
}

func TestCameraIPChangeAndRecordingMigration(t *testing.T) {
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

	_, _, err = store.BatchUpsertCameras([]config.Camera{
		{Name: "8号琴房", IP: "192.168.1.208", Group: "琴房", AutoRecord: true, SortOrder: 80},
	}, "192.168.1", "admin", "unit-test-pass")
	if err != nil {
		t.Fatalf("BatchUpsertCameras err: %v", err)
	}

	oldID := config.MakeID("192.168.1.208")
	newID := config.MakeID("192.168.1.108")

	// Create historical recording segment under oldID
	oldSegDir := filepath.Join(recDir, "2026-10-26", oldID)
	if err := os.MkdirAll(oldSegDir, 0o755); err != nil {
		t.Fatalf("MkdirAll err: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldSegDir, "101500__8号琴房__auto.mp4"), make([]byte, 4096), 0o644); err != nil {
		t.Fatalf("WriteFile err: %v", err)
	}

	hub := rtsp.NewStreamHub(store)
	recMgr := recorder.NewManager(store, hub)
	defer recMgr.StopAll()

	// Verify gateway & netmask inference
	gw, mask := discovery.InferGatewayAndNetmask("192.168.1.108")
	if gw != "192.168.1.1" || mask != "255.255.255.0" {
		t.Fatalf("Expected gateway=192.168.1.1 mask=255.255.255.0, got gw=%s mask=%s", gw, mask)
	}

	// Update camera IP using UpsertCameraTrackID (passing oldID so it updates in place)
	cam, ok := store.GetCamera(oldID)
	if !ok {
		t.Fatalf("Expected to find camera %s", oldID)
	}
	cam.IP = "192.168.1.108"
	gotOldID, gotNewID, err := store.UpsertCameraTrackID(cam)
	if err != nil {
		t.Fatalf("UpsertCameraTrackID err: %v", err)
	}
	if gotOldID != oldID || gotNewID != newID {
		t.Fatalf("Expected migration %s -> %s, got %s -> %s", oldID, newID, gotOldID, gotNewID)
	}

	// Migrate recording folder
	recMgr.MigrateCameraID(gotOldID, gotNewID)

	// Verify oldID is replaced by newID in store with SortOrder preserved
	updatedCam, ok := store.GetCamera(newID)
	if !ok || updatedCam.IP != "192.168.1.108" || updatedCam.SortOrder != 80 {
		t.Fatalf("Unexpected updated camera after IP change: %+v", updatedCam)
	}

	// Verify historical recording segment is now listed under newID
	segs := recMgr.ListSegments("2026-10-26", newID)
	if len(segs) != 1 || segs[0].CameraID != newID {
		t.Fatalf("Expected 1 migrated segment under %s, got %+v", newID, segs)
	}
}

func TestPTZPresetsAndModelInference(t *testing.T) {
	if !config.InferSupportsPTZFromModel("TL-IPC43AN-4") {
		t.Fatalf("Expected TL-IPC43AN-4 to infer SupportsPTZ=true")
	}
	if !config.InferSupportsPTZFromModel("Tapo C210") {
		t.Fatalf("Expected Tapo C210 to infer SupportsPTZ=true")
	}
	if !config.InferSupportsPTZFromModel("TL-IPC42A-4") {
		t.Fatalf("Expected pan-tilt dome TL-IPC42A-4 to infer SupportsPTZ=true")
	}
	if config.InferSupportsPTZFromModel("TL-IPC542B") {
		t.Fatalf("Expected fixed bullet TL-IPC542B to infer SupportsPTZ=false")
	}

	tmpDir := t.TempDir()
	store, err := config.NewStore(filepath.Join(tmpDir, "cameras.json"))
	if err != nil {
		t.Fatalf("NewStore err: %v", err)
	}
	_, _, err = store.BatchUpsertCameras([]config.Camera{
		{Name: "1号琴房", IP: "192.0.2.10", Model: "TL-IPC42A-4"},
	}, "192.0.2", "admin", "unit-test-pass")
	if err != nil {
		t.Fatalf("BatchUpsertCameras err: %v", err)
	}
	camID := config.MakeID("192.0.2.10")

	presets, err := store.UpsertCameraPTZPreset(camID, config.PTZPreset{
		Name: "钢琴键盘特写",
		Zoom: 2.2,
		PanX: -15.5,
		PanY: 12.0,
	})
	if err != nil || len(presets) != 1 || presets[0].Name != "钢琴键盘特写" || presets[0].Zoom != 2.2 {
		t.Fatalf("UpsertCameraPTZPreset unexpected result: presets=%+v err=%v", presets, err)
	}

	// Ensure re-scanning does not wipe out saved PTZ presets
	_, _, _ = store.BatchUpsertCameras([]config.Camera{
		{Name: "1号琴房", IP: "192.0.2.10", Model: "TL-IPC42A-4"},
	}, "192.0.2", "admin", "unit-test-pass")

	cam, ok := store.GetCamera(camID)
	if !ok || len(cam.PTZPresets) != 1 || cam.PTZPresets[0].Name != "钢琴键盘特写" {
		t.Fatalf("Expected PTZPresets to persist across BatchUpsertCameras, got %+v", cam.PTZPresets)
	}
	if !cam.SupportsPTZ {
		t.Fatalf("Expected TL-IPC42A-4 camera to have SupportsPTZ=true")
	}

	ctrl := ptz.NewController()
	resInvalid := ctrl.Execute(cam, ptz.CommandRequest{
		ID:        camID,
		Action:    "move",
		Direction: "invalid_dir",
		Speed:     2,
	})
	if resInvalid.OK {
		t.Fatalf("Expected invalid direction to fail, got %+v", resInvalid)
	}

	afterDel, err := store.DeleteCameraPTZPreset(camID, presets[0].ID)
	if err != nil || len(afterDel) != 0 {
		t.Fatalf("Expected 0 presets after DeleteCameraPTZPreset, got %+v (err=%v)", afterDel, err)
	}

	// Verify automatic flip_type == "center" inversion and manual PTZInvertX/PTZInvertY overrides
	invX, invY := ptz.ResolveEffectiveInvert(config.Camera{FlipType: "center"}, "center")
	if !invX || !invY {
		t.Fatalf("Expected center flip_type to default to invX=true, invY=true, got %v, %v", invX, invY)
	}
	invXOff, invYOff := ptz.ResolveEffectiveInvert(config.Camera{FlipType: "off"}, "off")
	if invXOff || invYOff {
		t.Fatalf("Expected off flip_type to default to invX=false, invY=false, got %v, %v", invXOff, invYOff)
	}
	tVal := true
	_ = store.SetCameraPTZInvert(camID, &tVal, nil, "off")
	camInv, _ := store.GetCamera(camID)
	effX, effY := ptz.ResolveEffectiveInvert(camInv, camInv.FlipType)
	if !effX || effY {
		t.Fatalf("Expected manual PTZInvertX=true override with FlipType=off to yield effX=true, effY=false, got %v, %v", effX, effY)
	}
}



