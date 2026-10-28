package recorder

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"focus-camera/internal/config"
	"focus-camera/internal/rtsp"
)

const minFreeDiskBytes uint64 = 5 * 1024 * 1024 * 1024 // 5 GB emergency free-space floor

func effectiveStorageDir(raw string) string {
	dir := strings.TrimSpace(raw)
	if dir == "" {
		return "./recordings"
	}
	if runtime.GOOS != "windows" && strings.HasPrefix(dir, `\\`) {
		return "./recordings"
	}
	return dir
}

type CameraRecordState struct {
	CameraID     string `json:"camera_id"`
	CameraName   string `json:"camera_name"`
	Active       bool   `json:"active"`
	Recording    bool   `json:"recording"`
	ManualActive bool   `json:"manual_active"`
	AutoEnabled  bool   `json:"auto_enabled"`
	Mode         string `json:"mode"` // "manual" | "auto" | "idle"
	CurrentFile  string `json:"current_file,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	DurationSec  int    `json:"duration_sec"`
	BytesWritten int64  `json:"bytes_written"`
	LastError    string `json:"last_error,omitempty"`
}

type SegmentItem struct {
	ID          string `json:"id"`
	RelPath     string `json:"rel_path"`
	Date        string `json:"date"`
	CameraID    string `json:"camera_id"`
	CameraName  string `json:"camera_name"`
	Mode        string `json:"mode"` // "manual" | "auto"
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time"`
	StartSec    int    `json:"start_sec"`
	StartSecDay int    `json:"start_sec_day"`
	DurationSec int    `json:"duration_sec"`
	SizeBytes   int64  `json:"size_bytes"`
	Active      bool   `json:"active"`
}

type StorageSummary struct {
	StorageDir     string   `json:"storage_dir"`
	TotalBytes     int64    `json:"total_bytes"`
	TotalFiles     int      `json:"total_files"`
	MaxStorageGB   int      `json:"max_storage_gb"`
	RetentionDays  int      `json:"retention_days"`
	AvailableDates []string `json:"available_dates"`
}

type DiskSpaceInfo struct {
	Path               string  `json:"path"`
	FreeBytes          uint64  `json:"free_bytes"`
	TotalBytes         uint64  `json:"total_bytes"`
	UsedByRecBytes     int64   `json:"used_by_rec_bytes"`
	FreeGB             float64 `json:"free_gb"`
	TotalGB            float64 `json:"total_gb"`
	UsedByRecGB        float64 `json:"used_by_rec_gb"`
	MaxQuotaGB         int     `json:"max_quota_gb"`
	RecommendedQuotaGB int     `json:"recommended_quota_gb"`
}

func InspectDiskSpace(rawDir string) DiskSpaceInfo {
	targetDir := strings.TrimSpace(rawDir)
	if targetDir == "" {
		targetDir = "./recordings"
	}
	if !strings.HasPrefix(targetDir, `\\`) {
		if abs, err := filepath.Abs(targetDir); err == nil {
			targetDir = abs
		}
	}

	var usedRecBytes int64
	if entries, err := os.ReadDir(targetDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || len(e.Name()) != 10 {
				continue
			}
			_ = filepath.WalkDir(filepath.Join(targetDir, e.Name()), func(_ string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".mp4") {
					if info, err := d.Info(); err == nil {
						usedRecBytes += info.Size()
					}
				}
				return nil
			})
		}
	}

	probePath := targetDir
	for i := 0; i < 16; i++ {
		if _, err := os.Stat(probePath); err == nil {
			break
		}
		parent := filepath.Dir(probePath)
		if parent == probePath || parent == "" {
			break
		}
		probePath = parent
	}

	const gb = 1024 * 1024 * 1024
	freeBytes, totalBytes, err := getDiskSpaceBytes(probePath)
	if err != nil || totalBytes == 0 {
		return DiskSpaceInfo{
			Path:               targetDir,
			UsedByRecBytes:     usedRecBytes,
			UsedByRecGB:        float64(usedRecBytes) / float64(gb),
			MaxQuotaGB:         500,
			RecommendedQuotaGB: 200,
		}
	}

	maxQuotaGB := int(freeBytes / gb)
	if maxQuotaGB < 1 {
		maxQuotaGB = 1
	}

	recQuotaGB := int(float64(maxQuotaGB) * 0.85)
	if recQuotaGB > 20 {
		recQuotaGB = (recQuotaGB / 5) * 5
	}
	if recQuotaGB < 1 {
		recQuotaGB = 1
	}
	if recQuotaGB >= maxQuotaGB && maxQuotaGB > 1 {
		recQuotaGB = maxQuotaGB - 1
	}

	return DiskSpaceInfo{
		Path:               targetDir,
		FreeBytes:          freeBytes,
		TotalBytes:         totalBytes,
		UsedByRecBytes:     usedRecBytes,
		FreeGB:             float64(freeBytes) / float64(gb),
		TotalGB:            float64(totalBytes) / float64(gb),
		UsedByRecGB:        float64(usedRecBytes) / float64(gb),
		MaxQuotaGB:         maxQuotaGB,
		RecommendedQuotaGB: recQuotaGB,
	}
}

type activeWorker struct {
	camID     string
	camName   string
	mode      string
	stopCh    chan struct{}
	doneCh    chan struct{}
	mu        sync.Mutex
	relPath   string
	startedAt time.Time
	writer    *FMP4Writer
	lastErr   string
}

type Manager struct {
	mu           sync.Mutex
	store        *config.Store
	hub          *rtsp.StreamHub
	manualActive map[string]bool
	workers      map[string]*activeWorker
	stopCh       chan struct{}
	stopOnce     sync.Once
	stopped      bool
}

func NewManager(store *config.Store, hub *rtsp.StreamHub) *Manager {
	m := &Manager{
		store:        store,
		hub:          hub,
		manualActive: make(map[string]bool),
		workers:      make(map[string]*activeWorker),
		stopCh:       make(chan struct{}),
	}
	go m.supervisorLoop()
	go m.cleanupLoop()
	return m
}

// StopAll gracefully flushes and closes all active recording MP4 files before shutdown.
func (m *Manager) StopAll() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})

	m.mu.Lock()
	m.stopped = true
	doneChs := make([]chan struct{}, 0, len(m.workers))
	for camID, w := range m.workers {
		close(w.stopCh)
		doneChs = append(doneChs, w.doneCh)
		delete(m.workers, camID)
	}
	m.mu.Unlock()

	if len(doneChs) == 0 {
		return
	}
	deadline := time.After(2500 * time.Millisecond)
	for _, ch := range doneChs {
		select {
		case <-ch:
		case <-deadline:
			return
		}
	}
}

func (m *Manager) ActiveRecordingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, w := range m.workers {
		w.mu.Lock()
		if w.writer != nil {
			count++
		}
		w.mu.Unlock()
	}
	return count
}

func (m *Manager) StartManual(camID string) error {
	m.mu.Lock()
	m.manualActive[camID] = true
	m.mu.Unlock()
	m.reconcile()
	return nil
}

func (m *Manager) StopManual(camID string) error {
	m.mu.Lock()
	delete(m.manualActive, camID)
	m.mu.Unlock()
	m.reconcile()
	return nil
}

func (m *Manager) SyncNow() {
	m.reconcile()
}

// StopCamera gracefully flushes and stops an active recording worker for a single camera ID.
func (m *Manager) StopCamera(camID string) {
	if camID == "" {
		return
	}
	m.mu.Lock()
	w, ok := m.workers[camID]
	if ok {
		close(w.stopCh)
		delete(m.workers, camID)
	}
	m.mu.Unlock()

	if ok {
		select {
		case <-w.doneCh:
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

// MigrateCameraID flushes active recordings for oldCamID, migrates all historical date/<oldCamID>
// recording folders on disk into date/<newCamID>, and resumes recording on newCamID.
func (m *Manager) MigrateCameraID(oldCamID, newCamID string) {
	oldCamID = strings.TrimSpace(oldCamID)
	newCamID = strings.TrimSpace(newCamID)
	if oldCamID == "" || newCamID == "" || oldCamID == newCamID {
		return
	}

	m.mu.Lock()
	if m.manualActive[oldCamID] {
		delete(m.manualActive, oldCamID)
		m.manualActive[newCamID] = true
	}
	w, hasWorker := m.workers[oldCamID]
	if hasWorker {
		close(w.stopCh)
		delete(m.workers, oldCamID)
	}
	m.mu.Unlock()

	if hasWorker {
		select {
		case <-w.doneCh:
		case <-time.After(1500 * time.Millisecond):
		}
	}

	cfg := m.store.GetConfig()
	storageDir := effectiveStorageDir(cfg.Recording.StorageDir)
	if entries, err := os.ReadDir(storageDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || len(e.Name()) != 10 {
				continue
			}
			dateDir := filepath.Join(storageDir, e.Name())
			oldDir := filepath.Join(dateDir, oldCamID)
			newDir := filepath.Join(dateDir, newCamID)
			if st, err := os.Stat(oldDir); err != nil || !st.IsDir() {
				continue
			}
			if _, err := os.Stat(newDir); os.IsNotExist(err) {
				_ = os.Rename(oldDir, newDir)
				continue
			}
			// Merge files from oldDir into newDir if newDir already exists
			_ = os.MkdirAll(newDir, 0o755)
			if files, err := os.ReadDir(oldDir); err == nil {
				for _, f := range files {
					if f.IsDir() {
						continue
					}
					src := filepath.Join(oldDir, f.Name())
					dst := filepath.Join(newDir, f.Name())
					if _, err := os.Stat(dst); os.IsNotExist(err) {
						_ = os.Rename(src, dst)
					}
				}
			}
			_ = os.Remove(oldDir)
		}
	}

	m.reconcile()
}

func (m *Manager) supervisorLoop() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.reconcile()
		}
	}
}

func isWithinSchedule(rc config.RecordingConfig, now time.Time) bool {
	if !rc.ScheduleEnabled {
		return true
	}
	cur := now.Format("15:04")
	start := rc.ScheduleStartTime
	end := rc.ScheduleEndTime
	if start == "" || end == "" {
		return true
	}
	if start <= end {
		return cur >= start && cur <= end
	}
	// Overnight schedule (e.g. 22:00 to 06:00)
	return cur >= start || cur <= end
}

func (m *Manager) reconcile() {
	cfg := m.store.GetConfig()
	now := time.Now()
	inSched := isWithinSchedule(cfg.Recording, now)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return
	}

	desired := make(map[string]struct {
		cam  config.Camera
		mode string
	})

	for _, cam := range cfg.Cameras {
		if m.manualActive[cam.ID] {
			desired[cam.ID] = struct {
				cam  config.Camera
				mode string
			}{cam: cam, mode: "manual"}
		} else if cam.AutoRecord && inSched {
			desired[cam.ID] = struct {
				cam  config.Camera
				mode string
			}{cam: cam, mode: "auto"}
		}
	}

	// Clean up exited workers (auto-heal) or stop workers no longer desired
	for camID, w := range m.workers {
		select {
		case <-w.doneCh:
			delete(m.workers, camID)
			continue
		default:
		}
		d, ok := desired[camID]
		if !ok {
			close(w.stopCh)
			delete(m.workers, camID)
		} else if w.mode != d.mode {
			w.mu.Lock()
			w.mode = d.mode
			w.mu.Unlock()
		}
	}

	// Start new workers (or restart auto-healed workers)
	for camID, d := range desired {
		if _, exists := m.workers[camID]; !exists {
			w := &activeWorker{
				camID:   d.cam.ID,
				camName: d.cam.Name,
				mode:    d.mode,
				stopCh:  make(chan struct{}),
				doneCh:  make(chan struct{}),
			}
			m.workers[camID] = w
			go m.runWorker(w)
		}
	}
}

func sanitizeFilePart(s string) string {
	r := strings.NewReplacer(
		"/", "-", "\\", "-", ":", "-", "*", "-",
		"?", "-", "\"", "", "<", "", ">", "", "|", "-", "__", "_",
	)
	out := strings.TrimSpace(r.Replace(s))
	if out == "" {
		return "Room"
	}
	return out
}

func (m *Manager) runWorker(w *activeWorker) {
	defer close(w.doneCh)

	// Always record 1080P main stream ("main") + Audio (wantAudio = true) per user requirement
	sub, unsub, err := m.hub.Subscribe(w.camID, "main", true)
	if err != nil {
		w.mu.Lock()
		w.lastErr = err.Error()
		w.mu.Unlock()
		return
	}
	defer unsub()

	var (
		avccConfig []byte
		segWriter  *FMP4Writer
		segStart   time.Time
	)

	closeSegment := func() {
		if segWriter != nil {
			_ = segWriter.Close()
			segWriter = nil
		}
		w.mu.Lock()
		w.writer = nil
		w.relPath = ""
		w.mu.Unlock()
	}
	defer closeSegment()

	openNewSegment := func(now time.Time) error {
		closeSegment()
		cfg := m.store.GetConfig()
		cam, ok := m.store.GetCamera(w.camID)
		camName := w.camName
		if ok && cam.Name != "" {
			camName = cam.Name
		}

		dateStr := now.Format("2006-01-02")
		timeStr := now.Format("150405")
		w.mu.Lock()
		modeStr := w.mode
		w.mu.Unlock()

		fileName := fmt.Sprintf("%s__%s__%s.mp4", timeStr, sanitizeFilePart(camName), modeStr)
		storageDir := effectiveStorageDir(cfg.Recording.StorageDir)
		dirPath := filepath.Join(storageDir, dateStr, w.camID)
		if err := os.MkdirAll(dirPath, 0o755); err != nil {
			return err
		}
		fullPath := filepath.Join(dirPath, fileName)
		fw, err := NewFMP4Writer(fullPath)
		if err != nil {
			return err
		}
		if err := fw.InitHeader(avccConfig); err != nil {
			_ = fw.Close()
			return err
		}

		segWriter = fw
		segStart = now
		rel := filepath.ToSlash(filepath.Join(dateStr, w.camID, fileName))
		w.mu.Lock()
		w.writer = fw
		w.relPath = rel
		w.startedAt = now
		w.lastErr = ""
		w.mu.Unlock()
		return nil
	}

	for {
		select {
		case <-w.stopCh:
			return
		case pkt, ok := <-sub.Ch:
			if !ok {
				return
			}
			if len(pkt) < 2 {
				continue
			}
			switch pkt[0] {
			case rtsp.MsgTypeCodecConfig:
				if len(pkt) > 4 {
					avccConfig = append([]byte(nil), pkt[4:]...)
				}
			case rtsp.MsgTypeVideoFrame:
				if len(pkt) <= 6 || len(avccConfig) == 0 {
					continue
				}
				isKey := pkt[1] == 1
				tsMs := binary.BigEndian.Uint32(pkt[2:6])
				nalus := pkt[6:]

				now := time.Now()
				segMins := m.store.GetConfig().Recording.SegmentMinutes
				if segMins <= 0 {
					segMins = 15
				}

				if isKey {
					needRotate := segWriter == nil ||
						now.Sub(segStart) >= time.Duration(segMins)*time.Minute ||
						now.Format("2006-01-02") != segStart.Format("2006-01-02")
					if needRotate {
						if err := openNewSegment(now); err != nil {
							w.mu.Lock()
							w.lastErr = err.Error()
							w.mu.Unlock()
							continue
						}
					}
				}
				if segWriter != nil {
					if err := segWriter.WriteVideo(nalus, tsMs, isKey); err != nil {
						w.mu.Lock()
						w.lastErr = err.Error()
						w.mu.Unlock()
						closeSegment()
					}
				}
			case rtsp.MsgTypeAudioPCMA:
				if segWriter != nil && len(pkt) > 1 {
					_ = segWriter.WriteAudioPCMA(pkt[1:])
				}
			case rtsp.MsgTypeStatusJSON:
				var st struct {
					State   string `json:"state"`
					Message string `json:"message"`
				}
				if err := json.Unmarshal(pkt[1:], &st); err == nil {
					w.mu.Lock()
					if st.State == "error" && st.Message != "" {
						w.lastErr = st.Message
					} else if st.State == "streaming" {
						w.lastErr = ""
					}
					w.mu.Unlock()
				}
			}
		}
	}
}

func (m *Manager) GetStatus() map[string]CameraRecordState {
	cfg := m.store.GetConfig()
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[string]CameraRecordState, len(cfg.Cameras))
	now := time.Now()
	for _, cam := range cfg.Cameras {
		st := CameraRecordState{
			CameraID:     cam.ID,
			CameraName:   cam.Name,
			AutoEnabled:  cam.AutoRecord,
			ManualActive: m.manualActive[cam.ID],
			Mode:         "idle",
		}
		if w, ok := m.workers[cam.ID]; ok {
			w.mu.Lock()
			st.Recording = w.writer != nil
			st.Active = st.Recording
			st.Mode = w.mode
			st.CurrentFile = w.relPath
			if !w.startedAt.IsZero() {
				st.StartedAt = w.startedAt.Format("15:04:05")
				st.DurationSec = int(now.Sub(w.startedAt).Seconds())
			}
			if w.writer != nil {
				st.BytesWritten = w.writer.BytesWritten()
			}
			st.LastError = w.lastErr
			w.mu.Unlock()
		}
		out[cam.ID] = st
	}
	return out
}

func (m *Manager) GetStorageSummary() StorageSummary {
	rc := m.store.GetConfig().Recording
	storageDir := effectiveStorageDir(rc.StorageDir)
	sum := StorageSummary{
		StorageDir:     storageDir,
		MaxStorageGB:   rc.MaxStorageGB,
		RetentionDays:  rc.RetentionDays,
		AvailableDates: []string{},
	}
	entries, err := os.ReadDir(storageDir)
	if err != nil {
		return sum
	}
	dateSet := make(map[string]bool)
	for _, e := range entries {
		if !e.IsDir() || len(e.Name()) != 10 {
			continue
		}
		dateDir := filepath.Join(storageDir, e.Name())
		hasMP4 := false
		_ = filepath.WalkDir(dateDir, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".mp4") {
				if info, err := d.Info(); err == nil && info.Size() > 512 {
					sum.TotalBytes += info.Size()
					sum.TotalFiles++
					hasMP4 = true
				}
			}
			return nil
		})
		if hasMP4 {
			dateSet[e.Name()] = true
		}
	}
	for d := range dateSet {
		sum.AvailableDates = append(sum.AvailableDates, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(sum.AvailableDates)))
	return sum
}

func (m *Manager) ListSegments(dateStr string, filterCamID string) []SegmentItem {
	cfg := m.store.GetConfig()
	camNames := make(map[string]string, len(cfg.Cameras))
	for _, c := range cfg.Cameras {
		camNames[c.ID] = c.Name
	}

	activePaths := make(map[string]bool)
	m.mu.Lock()
	for _, w := range m.workers {
		w.mu.Lock()
		if w.relPath != "" {
			activePaths[w.relPath] = true
		}
		w.mu.Unlock()
	}
	m.mu.Unlock()

	storageDir := effectiveStorageDir(cfg.Recording.StorageDir)
	var dateList []string
	if dateStr == "" || dateStr == "all" {
		entries, err := os.ReadDir(storageDir)
		if err != nil {
			return []SegmentItem{}
		}
		for _, e := range entries {
			if e.IsDir() && len(e.Name()) == 10 {
				dateList = append(dateList, e.Name())
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(dateList)))
	} else {
		dateList = []string{dateStr}
	}

	var items []SegmentItem
	for _, dStr := range dateList {
		dateDir := filepath.Join(storageDir, dStr)
		camDirs, err := os.ReadDir(dateDir)
		if err != nil {
			continue
		}

		for _, cd := range camDirs {
			if !cd.IsDir() {
				continue
			}
			camID := cd.Name()
			if filterCamID != "" && filterCamID != "all" && camID != filterCamID {
				continue
			}
			files, err := os.ReadDir(filepath.Join(dateDir, camID))
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(strings.ToLower(f.Name()), ".mp4") {
					continue
				}
				info, err := f.Info()
				if err != nil || info.Size() <= 512 {
					continue
				}

				base := strings.TrimSuffix(f.Name(), filepath.Ext(f.Name()))
				parts := strings.Split(base, "__")
				hmsStr := "000000"
				roomName := camNames[camID]
				mode := "auto"
				if len(parts) >= 1 && len(parts[0]) == 6 {
					hmsStr = parts[0]
				}
				if len(parts) >= 2 && roomName == "" {
					roomName = parts[1]
				}
				if len(parts) >= 3 {
					mode = parts[2]
				}
				if roomName == "" {
					roomName = camID
				}

				startT, err := time.ParseInLocation("2006-01-02 150405", dStr+" "+hmsStr, time.Local)
				if err != nil {
					startT = info.ModTime()
				}
				endT := info.ModTime()
				durSec := int(endT.Sub(startT).Seconds())
				if durSec < 1 {
					durSec = 1
				}
				startSecDay := startT.Hour()*3600 + startT.Minute()*60 + startT.Second()
				relPath := filepath.ToSlash(filepath.Join(dStr, camID, f.Name()))

				items = append(items, SegmentItem{
					ID:          relPath,
					RelPath:     relPath,
					Date:        dStr,
					CameraID:    camID,
					CameraName:  roomName,
					Mode:        mode,
					StartTime:   startT.Format("15:04:05"),
					EndTime:     endT.Format("15:04:05"),
					StartSec:    startSecDay,
					StartSecDay: startSecDay,
					DurationSec: durSec,
					SizeBytes:   info.Size(),
					Active:      activePaths[relPath],
				})
			}
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Date != items[j].Date {
			return items[i].Date > items[j].Date
		}
		if items[i].StartSecDay != items[j].StartSecDay {
			return items[i].StartSecDay > items[j].StartSecDay
		}
		return items[i].CameraName < items[j].CameraName
	})
	return items
}

func (m *Manager) ResolveSafeFilePath(relPath string) (string, error) {
	rc := m.store.GetConfig().Recording
	storageDir := effectiveStorageDir(rc.StorageDir)
	cleanRel := filepath.Clean(filepath.FromSlash(relPath))
	if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
		return "", fmt.Errorf("invalid recording path")
	}
	absBase, err := filepath.Abs(storageDir)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(filepath.Join(absBase, cleanRel))
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(absTarget, absBase) {
		return "", fmt.Errorf("path outside storage directory")
	}
	return absTarget, nil
}

func (m *Manager) cleanupLoop() {
	m.runRetentionCleanup()
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.runRetentionCleanup()
		}
	}
}

func (m *Manager) runRetentionCleanup() {
	rc := m.store.GetConfig().Recording
	storageDir := effectiveStorageDir(rc.StorageDir)
	entries, err := os.ReadDir(storageDir)
	if err != nil {
		return
	}

	activeAbs := make(map[string]bool)
	m.mu.Lock()
	for _, w := range m.workers {
		w.mu.Lock()
		if w.relPath != "" {
			if abs, err := filepath.Abs(filepath.Join(storageDir, filepath.FromSlash(w.relPath))); err == nil {
				activeAbs[abs] = true
			}
		}
		w.mu.Unlock()
	}
	m.mu.Unlock()

	type fileRec struct {
		path    string
		modTime time.Time
		size    int64
	}
	var allFiles []fileRec
	var totalBytes int64

	for _, e := range entries {
		if !e.IsDir() || len(e.Name()) != 10 {
			continue
		}
		dateDir := filepath.Join(storageDir, e.Name())
		_ = filepath.WalkDir(dateDir, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".mp4") {
				if info, err := d.Info(); err == nil {
					totalBytes += info.Size()
					if abs, err := filepath.Abs(path); err == nil && activeAbs[abs] {
						return nil
					}
					allFiles = append(allFiles, fileRec{
						path:    path,
						modTime: info.ModTime(),
						size:    info.Size(),
					})
				}
			}
			return nil
		})
	}

	maxBytes := int64(rc.MaxStorageGB) * 1024 * 1024 * 1024
	freeBytes, freeErr := getFreeDiskBytes(storageDir)
	needFreeCleanup := freeErr == nil && freeBytes > 0 && freeBytes < minFreeDiskBytes
	needQuotaCleanup := maxBytes > 0 && totalBytes > maxBytes

	if needQuotaCleanup || needFreeCleanup {
		sort.Slice(allFiles, func(i, j int) bool {
			return allFiles[i].modTime.Before(allFiles[j].modTime)
		})
		var freedBytes int64
		var removedCount int
		affectedDirs := make(map[string]bool)
		for _, f := range allFiles {
			overQuota := maxBytes > 0 && totalBytes > maxBytes
			lowDisk := needFreeCleanup && (freeBytes+uint64(freedBytes) < minFreeDiskBytes)
			if !overQuota && !lowDisk {
				break
			}
			if os.Remove(f.path) == nil {
				totalBytes -= f.size
				freedBytes += f.size
				removedCount++
				affectedDirs[filepath.Dir(f.path)] = true
			}
		}
		// Prune empty camera and date directories left behind after FIFO cyclic deletion
		dateDirsToCheck := make(map[string]bool)
		for camDir := range affectedDirs {
			if sub, err := os.ReadDir(camDir); err == nil && len(sub) == 0 {
				_ = os.Remove(camDir)
			}
			dateDirsToCheck[filepath.Dir(camDir)] = true
		}
		for dateDir := range dateDirsToCheck {
			if sub, err := os.ReadDir(dateDir); err == nil && len(sub) == 0 {
				_ = os.Remove(dateDir)
			}
		}
		if removedCount > 0 {
			log.Printf("[存储巡检] 满额自动循环覆盖：已淘汰 %d 个最早录像分片，释放空间 %.1f MB", removedCount, float64(freedBytes)/(1024*1024))
		}
	}
}
