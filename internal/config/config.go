package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type PTZPreset struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	HWToken  string  `json:"hw_token,omitempty"`
	Zoom     float64 `json:"zoom,omitempty"`
	PanX     float64 `json:"pan_x,omitempty"`
	PanY     float64 `json:"pan_y,omitempty"`
	Hardware bool    `json:"hardware,omitempty"`
}

type Camera struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Group          string      `json:"group"`
	IP             string      `json:"ip"`
	Username       string      `json:"username"`
	Password       string      `json:"password"`
	NoPassword     bool        `json:"no_password,omitempty"`
	RTSPPort       int         `json:"rtsp_port"`
	TalkPort       int         `json:"talk_port"`
	Vendor         string      `json:"vendor,omitempty"`
	MainStreamPath string      `json:"main_stream_path,omitempty"`
	SubStreamPath  string      `json:"sub_stream_path,omitempty"`
	Model          string      `json:"model"`
	SupportsTalk   bool        `json:"supports_talk"`
	SupportsPTZ    bool        `json:"supports_ptz,omitempty"`
	FlipType       string      `json:"flip_type,omitempty"`
	PTZInvertX     *bool       `json:"ptz_invert_x,omitempty"`
	PTZInvertY     *bool       `json:"ptz_invert_y,omitempty"`
	PTZPresets     []PTZPreset `json:"ptz_presets,omitempty"`
	AutoRecord     bool        `json:"auto_record"`
	SortOrder      int         `json:"sort_order"`
}

func NormalizeVendor(vendor, model string) string {
	v := strings.ToLower(strings.TrimSpace(vendor))
	m := strings.ToUpper(strings.TrimSpace(model))
	if strings.HasPrefix(m, "MIPC") || strings.Contains(m, "MERCURY") {
		return "mercury"
	}
	switch v {
	case "mercury", "mercurynet":
		return "mercury"
	case "tplink", "tp-link":
		return "tplink"
	case "hikvision", "hik", "ezviz":
		return "hikvision"
	case "dahua", "imou":
		return "dahua"
	case "tapo":
		return "tapo"
	case "uniview", "unv":
		return "uniview"
	case "reolink":
		return "reolink"
	case "onvif":
		return "onvif"
	case "custom", "rtsp":
		return "custom"
	}
	switch {
	case strings.HasPrefix(m, "DS-") || strings.Contains(m, "HIK") || strings.Contains(m, "EZVIZ"):
		return "hikvision"
	case strings.HasPrefix(m, "DH-") || strings.HasPrefix(m, "IPC-H") || strings.Contains(m, "DAHUA") || strings.Contains(m, "IMOU"):
		return "dahua"
	case strings.Contains(m, "TAPO"):
		return "tapo"
	case strings.Contains(m, "UNV") || strings.Contains(m, "UNIVIEW"):
		return "uniview"
	case strings.Contains(m, "REOLINK") || strings.HasPrefix(m, "RLC-"):
		return "reolink"
	case strings.HasPrefix(m, "TL-") || strings.Contains(m, "TP-LINK"):
		return "tplink"
	}
	if v != "" {
		return v
	}
	return "tplink"
}

func (c Camera) IsTPLink() bool {
	v := NormalizeVendor(c.Vendor, c.Model)
	return v == "tplink" || v == "mercury"
}

func DefaultStreamPathsForVendor(vendor string) (mainPath, subPath string) {
	switch NormalizeVendor(vendor, "") {
	case "hikvision":
		return "/Streaming/Channels/101", "/Streaming/Channels/102"
	case "dahua":
		return "/cam/realmonitor?channel=1&subtype=0", "/cam/realmonitor?channel=1&subtype=1"
	case "uniview":
		return "/media/video1", "/media/video2"
	case "reolink":
		return "/h264Preview_01_main", "/h264Preview_01_sub"
	default:
		// tplink, tapo, onvif, custom default fallback
		return "/stream1", "/stream2"
	}
}

func (c Camera) ResolveStreamPath(streamType string) string {
	defMain, defSub := DefaultStreamPathsForVendor(NormalizeVendor(c.Vendor, c.Model))
	mainP := strings.TrimSpace(c.MainStreamPath)
	subP := strings.TrimSpace(c.SubStreamPath)
	if mainP == "" {
		mainP = defMain
	}
	if subP == "" {
		if strings.TrimSpace(c.MainStreamPath) != "" {
			// If user explicitly provided only a custom MainStreamPath, use it as fallback for sub stream too
			subP = mainP
		} else {
			subP = defSub
		}
	}
	if streamType == "main" || streamType == "stream1" {
		return mainP
	}
	return subP
}

type RecordingConfig struct {
	StorageDir        string `json:"storage_dir"`
	SegmentMinutes    int    `json:"segment_minutes"`
	RetentionDays     int    `json:"retention_days"`
	MaxStorageGB      int    `json:"max_storage_gb"`
	ScheduleEnabled   bool   `json:"schedule_enabled"`
	ScheduleStartTime string `json:"schedule_start_time"`
	ScheduleEndTime   string `json:"schedule_end_time"`
}

type AppConfig struct {
	DefaultUsername  string          `json:"default_username"`
	DefaultPassword  string          `json:"default_password"`
	SubnetPrefix     string          `json:"subnet_prefix"`
	UpdateURL        string          `json:"update_url"`
	AutoStartEnabled bool            `json:"auto_start_enabled"`
	AutoStartMode    string          `json:"auto_start_mode"`
	Recording        RecordingConfig `json:"recording"`
	Cameras          []Camera        `json:"cameras"`
}

func NormalizeAutoStartMode(mode string) string {
	if strings.ToLower(strings.TrimSpace(mode)) == "tray" {
		return "tray"
	}
	return "window"
}

type Store struct {
	mu       sync.RWMutex
	filePath string
	cfg      AppConfig
}

var numRegex = regexp.MustCompile(`(\d+)`)

func InferGroupAndOrder(name string, ip string) (string, int) {
	n := strings.TrimSpace(name)
	if n != "" && !strings.HasPrefix(n, "摄像头 (") {
		if m := numRegex.FindStringSubmatch(n); len(m) > 1 {
			if v, err := strconv.Atoi(m[1]); err == nil {
				return "默认分组", 100 + v
			}
		}
	}
	parts := strings.Split(ip, ".")
	lastOctet := 999
	if len(parts) == 4 {
		if v, err := strconv.Atoi(parts[3]); err == nil {
			lastOctet = v
		}
	}
	return "默认分组", 700 + lastOctet
}

func MakeID(ip string) string {
	return "cam-" + strings.ReplaceAll(ip, ".", "-")
}

// DefaultCameras returns an empty slice so no camera IPs are hardcoded in the binary.
func DefaultCameras() []Camera {
	return []Camera{}
}

func DefaultRecordingConfig(baseDir string) RecordingConfig {
	recDir := filepath.Join(baseDir, "recordings")
	if runtime.GOOS == "windows" && strings.HasPrefix(strings.TrimSpace(baseDir), `\\`) {
		sysDrive := strings.TrimSpace(os.Getenv("SystemDrive"))
		if sysDrive == "" {
			sysDrive = "C:"
		}
		recDir = filepath.Join(sysDrive+`\`, "FocusCamera", "recordings")
	}
	return RecordingConfig{
		StorageDir:        recDir,
		SegmentMinutes:    15,
		RetentionDays:     7,
		MaxStorageGB:      500,
		ScheduleEnabled:   false,
		ScheduleStartTime: "08:30",
		ScheduleEndTime:   "22:00",
	}
}

func normalizeRecordingConfig(rc *RecordingConfig, baseDir string) {
	rawDir := strings.TrimSpace(rc.StorageDir)
	if rawDir == "" || (runtime.GOOS == "windows" && strings.HasPrefix(strings.ToLower(rawDir), `\\mac\`)) {
		if runtime.GOOS == "windows" && (strings.HasPrefix(strings.TrimSpace(baseDir), `\\`) || strings.HasPrefix(strings.ToLower(rawDir), `\\mac\`)) {
			sysDrive := strings.TrimSpace(os.Getenv("SystemDrive"))
			if sysDrive == "" {
				sysDrive = "C:"
			}
			rc.StorageDir = filepath.Join(sysDrive+`\`, "FocusCamera", "recordings")
		} else {
			rc.StorageDir = filepath.Join(baseDir, "recordings")
		}
	}
	if rc.SegmentMinutes < 1 || rc.SegmentMinutes > 120 {
		rc.SegmentMinutes = 15
	}
	if rc.RetentionDays < 1 || rc.RetentionDays > 365 {
		rc.RetentionDays = 7
	}
	if rc.MaxStorageGB < 1 {
		rc.MaxStorageGB = 500
	}
	if rc.ScheduleStartTime == "" {
		rc.ScheduleStartTime = "08:30"
	}
	if rc.ScheduleEndTime == "" {
		rc.ScheduleEndTime = "22:00"
	}
}

func SortCameras(cams []Camera) {
	sort.SliceStable(cams, func(i, j int) bool {
		if cams[i].SortOrder != cams[j].SortOrder {
			return cams[i].SortOrder < cams[j].SortOrder
		}
		return cams[i].Name < cams[j].Name
	})
}

func NewStore(filePath string) (*Store, error) {
	s := &Store{filePath: filePath}
	baseDir := filepath.Dir(filePath)
	if err := s.load(); err != nil {
		s.cfg = AppConfig{
			DefaultUsername:  "admin",
			DefaultPassword:  "",
			SubnetPrefix:     "192.168.1",
			AutoStartEnabled: false,
			AutoStartMode:    "window",
			Recording:        DefaultRecordingConfig(baseDir),
			Cameras:          DefaultCameras(),
		}
		_ = s.save()
	}
	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	if cfg.DefaultUsername == "" {
		cfg.DefaultUsername = "admin"
	}
	if cfg.SubnetPrefix == "" {
		cfg.SubnetPrefix = "192.168.1"
	}
	cfg.AutoStartMode = NormalizeAutoStartMode(cfg.AutoStartMode)
	normalizeRecordingConfig(&cfg.Recording, filepath.Dir(s.filePath))
	if cfg.Cameras == nil {
		cfg.Cameras = []Camera{}
	}
	for i := range cfg.Cameras {
		if cfg.Cameras[i].IsTPLink() {
			if cfg.Cameras[i].TalkPort == 0 {
				cfg.Cameras[i].TalkPort = 8800
			}
			if cfg.Cameras[i].TalkPort == 8800 {
				cfg.Cameras[i].SupportsTalk = true
			}
		}
		if !cfg.Cameras[i].SupportsPTZ && InferSupportsPTZFromModel(cfg.Cameras[i].Model) {
			cfg.Cameras[i].SupportsPTZ = true
		}
	}
	SortCameras(cfg.Cameras)
	s.cfg = cfg
	return nil
}

func (s *Store) save() error {
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := s.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err == nil {
		if err := os.Rename(tmpPath, s.filePath); err == nil {
			return nil
		}
		_ = os.Remove(tmpPath)
	}
	return os.WriteFile(s.filePath, data, 0o644)
}

func (s *Store) GetConfig() AppConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := s.cfg
	cp.Cameras = append([]Camera(nil), s.cfg.Cameras...)
	return cp
}

func (s *Store) GetCamera(id string) (Camera, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.cfg.Cameras {
		if c.ID == id || c.IP == id {
			return c, true
		}
	}
	return Camera{}, false
}

func (s *Store) normalizeCameraLocked(cam *Camera) {
	cam.IP = strings.TrimSpace(cam.IP)
	cam.Name = strings.TrimSpace(cam.Name)
	if cam.Name == "" && cam.IP != "" {
		cam.Name = "摄像头 (" + cam.IP + ")"
	}
	if cam.ID == "" {
		cam.ID = MakeID(cam.IP)
	}
	if cam.Username == "" {
		cam.Username = s.cfg.DefaultUsername
	}
	if cam.Password != "" {
		cam.NoPassword = false
	} else if !cam.NoPassword {
		cam.Password = s.cfg.DefaultPassword
	}
	if cam.RTSPPort == 0 {
		cam.RTSPPort = 554
	}
	cam.Vendor = NormalizeVendor(cam.Vendor, cam.Model)
	cam.MainStreamPath = strings.TrimSpace(cam.MainStreamPath)
	cam.SubStreamPath = strings.TrimSpace(cam.SubStreamPath)
	if cam.IsTPLink() {
		if cam.TalkPort == 0 {
			cam.TalkPort = 8800
		}
		if cam.TalkPort == 8800 {
			cam.SupportsTalk = true
		}
	}
	if !cam.SupportsPTZ && InferSupportsPTZFromModel(cam.Model) {
		cam.SupportsPTZ = true
	}
	grp, ord := InferGroupAndOrder(cam.Name, cam.IP)
	if strings.TrimSpace(cam.Group) == "" {
		cam.Group = grp
	}
	if cam.SortOrder == 0 {
		cam.SortOrder = ord
	}
}

func (s *Store) UpsertCamera(cam Camera) error {
	_, _, err := s.UpsertCameraTrackID(cam)
	return err
}

func (s *Store) UpsertCameraTrackID(cam Camera) (oldID string, newID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	origSort := cam.SortOrder
	s.normalizeCameraLocked(&cam)
	newID = MakeID(cam.IP)
	updated := false
	for i := range s.cfg.Cameras {
		if s.cfg.Cameras[i].ID == cam.ID || s.cfg.Cameras[i].IP == cam.IP {
			oldID = s.cfg.Cameras[i].ID
			if origSort == 0 && s.cfg.Cameras[i].SortOrder > 0 {
				cam.SortOrder = s.cfg.Cameras[i].SortOrder
			}
			if len(cam.PTZPresets) == 0 && len(s.cfg.Cameras[i].PTZPresets) > 0 {
				cam.PTZPresets = s.cfg.Cameras[i].PTZPresets
			}
			if !cam.SupportsPTZ && s.cfg.Cameras[i].SupportsPTZ {
				cam.SupportsPTZ = true
			}
			if cam.FlipType == "" && s.cfg.Cameras[i].FlipType != "" {
				cam.FlipType = s.cfg.Cameras[i].FlipType
			}
			if cam.PTZInvertX == nil && s.cfg.Cameras[i].PTZInvertX != nil {
				cam.PTZInvertX = s.cfg.Cameras[i].PTZInvertX
			}
			if cam.PTZInvertY == nil && s.cfg.Cameras[i].PTZInvertY != nil {
				cam.PTZInvertY = s.cfg.Cameras[i].PTZInvertY
			}
			cam.ID = newID
			s.cfg.Cameras[i] = cam
			updated = true
			break
		}
	}
	if !updated {
		cam.ID = newID
		s.cfg.Cameras = append(s.cfg.Cameras, cam)
	}
	SortCameras(s.cfg.Cameras)
	return oldID, newID, s.save()
}

func (s *Store) BatchUpsertCameras(cams []Camera, defaultSubnet, defaultUser, defaultPass string) (added int, updated int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if defaultSubnet != "" {
		s.cfg.SubnetPrefix = strings.TrimSuffix(strings.TrimSpace(defaultSubnet), ".")
	}
	if defaultUser != "" {
		s.cfg.DefaultUsername = strings.TrimSpace(defaultUser)
	}
	if defaultPass != "" {
		s.cfg.DefaultPassword = defaultPass
	}

	for _, cam := range cams {
		if strings.TrimSpace(cam.IP) == "" {
			continue
		}
		s.normalizeCameraLocked(&cam)

		found := false
		// 1. Reconcile by room name if a room's DHCP IP shifted
		for i := range s.cfg.Cameras {
			if cam.Name != "" && !strings.HasPrefix(cam.Name, "摄像头 (") && s.cfg.Cameras[i].Name == cam.Name {
				cam.AutoRecord = s.cfg.Cameras[i].AutoRecord
				if len(cam.PTZPresets) == 0 && len(s.cfg.Cameras[i].PTZPresets) > 0 {
					cam.PTZPresets = s.cfg.Cameras[i].PTZPresets
				}
				if !cam.SupportsPTZ && s.cfg.Cameras[i].SupportsPTZ {
					cam.SupportsPTZ = true
				}
				if cam.FlipType == "" && s.cfg.Cameras[i].FlipType != "" {
					cam.FlipType = s.cfg.Cameras[i].FlipType
				}
				if cam.PTZInvertX == nil && s.cfg.Cameras[i].PTZInvertX != nil {
					cam.PTZInvertX = s.cfg.Cameras[i].PTZInvertX
				}
				if cam.PTZInvertY == nil && s.cfg.Cameras[i].PTZInvertY != nil {
					cam.PTZInvertY = s.cfg.Cameras[i].PTZInvertY
				}
				if s.cfg.Cameras[i].SortOrder > 0 {
					cam.SortOrder = s.cfg.Cameras[i].SortOrder
				}
				cam.ID = MakeID(cam.IP)
				s.cfg.Cameras[i] = cam
				updated++
				found = true
				break
			}
		}
		if found {
			continue
		}
		// 2. Match by IP or ID
		for i := range s.cfg.Cameras {
			if s.cfg.Cameras[i].IP == cam.IP || s.cfg.Cameras[i].ID == cam.ID {
				cam.AutoRecord = s.cfg.Cameras[i].AutoRecord
				if len(cam.PTZPresets) == 0 && len(s.cfg.Cameras[i].PTZPresets) > 0 {
					cam.PTZPresets = s.cfg.Cameras[i].PTZPresets
				}
				if !cam.SupportsPTZ && s.cfg.Cameras[i].SupportsPTZ {
					cam.SupportsPTZ = true
				}
				if cam.FlipType == "" && s.cfg.Cameras[i].FlipType != "" {
					cam.FlipType = s.cfg.Cameras[i].FlipType
				}
				if cam.PTZInvertX == nil && s.cfg.Cameras[i].PTZInvertX != nil {
					cam.PTZInvertX = s.cfg.Cameras[i].PTZInvertX
				}
				if cam.PTZInvertY == nil && s.cfg.Cameras[i].PTZInvertY != nil {
					cam.PTZInvertY = s.cfg.Cameras[i].PTZInvertY
				}
				if s.cfg.Cameras[i].SortOrder > 0 && !strings.HasPrefix(s.cfg.Cameras[i].Name, "摄像头 (") {
					cam.SortOrder = s.cfg.Cameras[i].SortOrder
				}
				cam.ID = MakeID(cam.IP)
				s.cfg.Cameras[i] = cam
				updated++
				found = true
				break
			}
		}
		if !found {
			cam.ID = MakeID(cam.IP)
			s.cfg.Cameras = append(s.cfg.Cameras, cam)
			added++
		}
	}
	SortCameras(s.cfg.Cameras)
	return added, updated, s.save()
}

func (s *Store) DeleteCamera(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Camera, 0, len(s.cfg.Cameras))
	for _, c := range s.cfg.Cameras {
		if c.ID != id && c.IP != id {
			out = append(out, c)
		}
	}
	s.cfg.Cameras = out
	return s.save()
}

func (s *Store) ClearAllCameras() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Cameras = []Camera{}
	return s.save()
}

func (s *Store) ReorderCameras(orderedIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(orderedIDs) == 0 || len(s.cfg.Cameras) == 0 {
		return nil
	}
	byID := make(map[string]Camera, len(s.cfg.Cameras))
	for _, c := range s.cfg.Cameras {
		byID[c.ID] = c
	}
	seen := make(map[string]bool, len(orderedIDs))
	next := make([]Camera, 0, len(s.cfg.Cameras))
	order := 10
	for _, id := range orderedIDs {
		if c, ok := byID[id]; ok && !seen[id] {
			c.SortOrder = order
			order += 10
			next = append(next, c)
			seen[id] = true
		}
	}
	for _, c := range s.cfg.Cameras {
		if !seen[c.ID] {
			c.SortOrder = order
			order += 10
			next = append(next, c)
		}
	}
	s.cfg.Cameras = next
	return s.save()
}

func (s *Store) SetCameraAutoRecord(id string, enable bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Cameras {
		if s.cfg.Cameras[i].ID == id || s.cfg.Cameras[i].IP == id {
			s.cfg.Cameras[i].AutoRecord = enable
			return s.save()
		}
	}
	return nil
}

func (s *Store) SetAllAutoRecord(enable bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Cameras {
		s.cfg.Cameras[i].AutoRecord = enable
	}
	return s.save()
}

func (s *Store) UpdateAppSettings(defaultUser, defaultPass, subnet string, rec RecordingConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(defaultUser) != "" {
		s.cfg.DefaultUsername = strings.TrimSpace(defaultUser)
	}
	s.cfg.DefaultPassword = defaultPass
	if strings.TrimSpace(subnet) != "" {
		s.cfg.SubnetPrefix = strings.TrimSuffix(strings.TrimSpace(subnet), ".")
	}
	normalizeRecordingConfig(&rec, filepath.Dir(s.filePath))
	s.cfg.Recording = rec
	return s.save()
}

func (s *Store) MergeDiscovered(discovered []Camera) (added int, updated int, err error) {
	return s.BatchUpsertCameras(discovered, "", "", "")
}

func (s *Store) SetUpdateURL(updateURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.UpdateURL = strings.TrimSpace(updateURL)
	return s.save()
}

func (s *Store) GetAutoStart() (enabled bool, mode string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.AutoStartEnabled, NormalizeAutoStartMode(s.cfg.AutoStartMode)
}

func (s *Store) SetAutoStart(enabled bool, mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.AutoStartEnabled = enabled
	s.cfg.AutoStartMode = NormalizeAutoStartMode(mode)
	return s.save()
}

func InferSupportsPTZFromModel(model string) bool {
	m := strings.ToUpper(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	for _, kw := range []string{
		"IPC42A", "IPC42", "IPC43", "IPC44", "IPC45", "IPC48",
		"IPC6", "IPC7",
		"MIPC4", "MIPC6",
		"C200", "C210", "C220", "C225", "C500", "C510", "C520",
		"PTZ", "SD", "E1", "云台", "球机",
	} {
		if strings.Contains(m, kw) {
			return true
		}
	}
	return false
}

func (s *Store) SetCameraSupportsPTZ(id string, supports bool) error {
	return s.SetCameraPTZInfo(id, supports, "", "")
}

func (s *Store) SetCameraPTZInfo(id string, supports bool, model string, flipType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	model = strings.TrimSpace(model)
	flipType = strings.TrimSpace(flipType)
	for i := range s.cfg.Cameras {
		if s.cfg.Cameras[i].ID == id || s.cfg.Cameras[i].IP == id {
			changed := false
			if s.cfg.Cameras[i].SupportsPTZ != supports {
				s.cfg.Cameras[i].SupportsPTZ = supports
				changed = true
			}
			if model != "" && s.cfg.Cameras[i].Model != model {
				s.cfg.Cameras[i].Model = model
				changed = true
			}
			if flipType != "" && s.cfg.Cameras[i].FlipType != flipType {
				s.cfg.Cameras[i].FlipType = flipType
				changed = true
			}
			if !changed {
				return nil
			}
			return s.save()
		}
	}
	return nil
}

func (s *Store) SetCameraPTZInvert(id string, invertX *bool, invertY *bool, flipType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	flipType = strings.TrimSpace(flipType)
	for i := range s.cfg.Cameras {
		if s.cfg.Cameras[i].ID == id || s.cfg.Cameras[i].IP == id {
			if flipType != "" {
				s.cfg.Cameras[i].FlipType = flipType
			}
			if invertX != nil {
				v := *invertX
				s.cfg.Cameras[i].PTZInvertX = &v
			}
			if invertY != nil {
				v := *invertY
				s.cfg.Cameras[i].PTZInvertY = &v
			}
			return s.save()
		}
	}
	return nil
}

func (s *Store) UpsertCameraPTZPreset(id string, preset PTZPreset) ([]PTZPreset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Cameras {
		if s.cfg.Cameras[i].ID == id || s.cfg.Cameras[i].IP == id {
			list := s.cfg.Cameras[i].PTZPresets
			if preset.ID == "" {
				preset.ID = strconv.Itoa(len(list) + 1)
				for {
					conflict := false
					for _, p := range list {
						if p.ID == preset.ID {
							conflict = true
							break
						}
					}
					if !conflict {
						break
					}
					n, _ := strconv.Atoi(preset.ID)
					preset.ID = strconv.Itoa(n + 1)
				}
			}
			updated := false
			for j := range list {
				if list[j].ID == preset.ID || (preset.Name != "" && list[j].Name == preset.Name) {
					preset.ID = list[j].ID
					list[j] = preset
					updated = true
					break
				}
			}
			if !updated {
				list = append(list, preset)
			}
			s.cfg.Cameras[i].PTZPresets = list
			err := s.save()
			return append([]PTZPreset(nil), list...), err
		}
	}
	return nil, nil
}

func (s *Store) DeleteCameraPTZPreset(id, presetID string) ([]PTZPreset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Cameras {
		if s.cfg.Cameras[i].ID == id || s.cfg.Cameras[i].IP == id {
			list := s.cfg.Cameras[i].PTZPresets
			next := make([]PTZPreset, 0, len(list))
			for _, p := range list {
				if p.ID != presetID && p.HWToken != presetID {
					next = append(next, p)
				}
			}
			s.cfg.Cameras[i].PTZPresets = next
			err := s.save()
			return append([]PTZPreset(nil), next...), err
		}
	}
	return nil, nil
}
