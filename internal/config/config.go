package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Camera struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Group        string `json:"group"`
	IP           string `json:"ip"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	RTSPPort     int    `json:"rtsp_port"`
	TalkPort     int    `json:"talk_port"`
	Model        string `json:"model"`
	SupportsTalk bool   `json:"supports_talk"`
	AutoRecord   bool   `json:"auto_record"`
	SortOrder    int    `json:"sort_order"`
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
	DefaultUsername string          `json:"default_username"`
	DefaultPassword string          `json:"default_password"`
	SubnetPrefix    string          `json:"subnet_prefix"`
	UpdateURL       string          `json:"update_url"`
	Recording       RecordingConfig `json:"recording"`
	Cameras         []Camera        `json:"cameras"`
}

type Store struct {
	mu       sync.RWMutex
	filePath string
	cfg      AppConfig
}

var numRegex = regexp.MustCompile(`(\d+)`)

func InferGroupAndOrder(name string, ip string) (string, int) {
	n := strings.TrimSpace(name)
	num := 999
	if m := numRegex.FindStringSubmatch(n); len(m) > 1 {
		if v, err := strconv.Atoi(m[1]); err == nil {
			num = v
		}
	}
	switch {
	case strings.Contains(n, "琴房"):
		return "琴房", 100 + num
	case strings.Contains(n, "鼓房") || strings.Contains(n, "录音"):
		return "鼓房与录音室", 300 + num
	case strings.Contains(n, "多媒体") || strings.Contains(n, "教室"):
		return "多媒体教室", 500 + num
	default:
		parts := strings.Split(ip, ".")
		lastOctet := 999
		if len(parts) == 4 {
			if v, err := strconv.Atoi(parts[3]); err == nil {
				lastOctet = v
			}
		}
		return "公共与其他", 700 + lastOctet
	}
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
	if strings.TrimSpace(rc.StorageDir) == "" {
		rc.StorageDir = filepath.Join(baseDir, "recordings")
	}
	if rc.SegmentMinutes < 1 || rc.SegmentMinutes > 120 {
		rc.SegmentMinutes = 15
	}
	if rc.RetentionDays < 1 || rc.RetentionDays > 365 {
		rc.RetentionDays = 7
	}
	if rc.MaxStorageGB < 5 {
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
			DefaultUsername: "admin",
			DefaultPassword: "",
			SubnetPrefix:    "192.168.1",
			Recording:       DefaultRecordingConfig(baseDir),
			Cameras:         DefaultCameras(),
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
	normalizeRecordingConfig(&cfg.Recording, filepath.Dir(s.filePath))
	if cfg.Cameras == nil {
		cfg.Cameras = []Camera{}
	}
	SortCameras(cfg.Cameras)
	s.cfg = cfg
	return nil
}

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
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
	if cam.ID == "" {
		cam.ID = MakeID(cam.IP)
	}
	if cam.Username == "" {
		cam.Username = s.cfg.DefaultUsername
	}
	if cam.Password == "" {
		cam.Password = s.cfg.DefaultPassword
	}
	if cam.RTSPPort == 0 {
		cam.RTSPPort = 554
	}
	if cam.TalkPort == 0 {
		cam.TalkPort = 8800
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.normalizeCameraLocked(&cam)
	updated := false
	for i := range s.cfg.Cameras {
		if s.cfg.Cameras[i].ID == cam.ID || s.cfg.Cameras[i].IP == cam.IP {
			cam.ID = MakeID(cam.IP)
			s.cfg.Cameras[i] = cam
			updated = true
			break
		}
	}
	if !updated {
		s.cfg.Cameras = append(s.cfg.Cameras, cam)
	}
	SortCameras(s.cfg.Cameras)
	return s.save()
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
		_, cam.SortOrder = InferGroupAndOrder(cam.Name, cam.IP)

		found := false
		// 1. Reconcile by room name if a room's DHCP IP shifted
		for i := range s.cfg.Cameras {
			if cam.Name != "" && !strings.HasPrefix(cam.Name, "摄像头 (") && s.cfg.Cameras[i].Name == cam.Name {
				cam.AutoRecord = s.cfg.Cameras[i].AutoRecord
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
