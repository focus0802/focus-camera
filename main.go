package main

import (
	"bufio"
	"crypto/sha1"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"focus-camera/internal/config"
	"focus-camera/internal/discovery"
	"focus-camera/internal/ptz"
	"focus-camera/internal/recorder"
	"focus-camera/internal/rtsp"
	"focus-camera/internal/talk"
	"focus-camera/internal/updater"
)

//go:embed web/*
var webEmbed embed.FS

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer
	wmu  sync.Mutex
}

func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, fmt.Errorf("missing Sec-WebSocket-Key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return nil, fmt.Errorf("hijacking not supported")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}

	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &wsConn{
		conn: conn,
		br:   rw.Reader,
		bw:   rw.Writer,
	}, nil
}

func (ws *wsConn) WriteFrame(opcode byte, payload []byte) error {
	ws.wmu.Lock()
	defer ws.wmu.Unlock()

	_ = ws.conn.SetWriteDeadline(time.Now().Add(4 * time.Second))
	var hdr [10]byte
	hdr[0] = 0x80 | (opcode & 0x0F)
	n := len(payload)
	hdrLen := 2
	switch {
	case n < 126:
		hdr[1] = byte(n)
	case n <= 65535:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
		hdrLen = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(n))
		hdrLen = 10
	}
	if _, err := ws.bw.Write(hdr[:hdrLen]); err != nil {
		return err
	}
	if n > 0 {
		if _, err := ws.bw.Write(payload); err != nil {
			return err
		}
	}
	return ws.bw.Flush()
}

func (ws *wsConn) ReadFrame() (opcode byte, payload []byte, err error) {
	var b [2]byte
	if _, err = io.ReadFull(ws.br, b[:]); err != nil {
		return 0, nil, err
	}
	opcode = b[0] & 0x0F
	masked := (b[1] & 0x80) != 0
	length := uint64(b[1] & 0x7F)

	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(ws.br, ext[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(ws.br, ext[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > 4*1024*1024 {
		return 0, nil, fmt.Errorf("websocket frame too large: %d", length)
	}

	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(ws.br, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, length)
	if length > 0 {
		if _, err = io.ReadFull(ws.br, payload); err != nil {
			return 0, nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= maskKey[i&3]
			}
		}
	}
	return opcode, payload, nil
}

func (ws *wsConn) Close() error {
	return ws.conn.Close()
}

func detectLocalSubnet() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "192.168.1"
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil {
			continue
		}
		if ip4[0] == 192 && ip4[1] == 168 {
			return fmt.Sprintf("192.168.%d", ip4[2])
		}
	}
	return "192.168.1"
}

func init() {
	// Required by macOS Cocoa/AppKit NSApplication event loop
	runtime.LockOSThread()
}

func resolveDefaultConfigPath() string {
	exePath, err := os.Executable()
	exeDir := ""
	if err == nil {
		if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
			exePath = resolved
		}
		exeDir = filepath.Dir(exePath)
		// If running inside FocusCamera.app/Contents/MacOS/FocusCamera, step out of .app bundle
		if strings.HasSuffix(filepath.ToSlash(exeDir), ".app/Contents/MacOS") {
			exeDir = filepath.Dir(filepath.Dir(filepath.Dir(exeDir)))
		}
	}

	candidates := []string{}
	if exeDir != "" && !strings.Contains(exePath, "go-build") {
		candidates = append(candidates,
			filepath.Join(exeDir, "cameras.json"),
			filepath.Join(filepath.Dir(exeDir), "cameras.json"),
		)
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "/" {
		candidates = append(candidates, filepath.Join(cwd, "cameras.json"))
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			_ = os.Chdir(filepath.Dir(c))
			return c
		}
	}

	if exeDir != "" && !strings.Contains(exePath, "go-build") {
		_ = os.Chdir(exeDir)
		return filepath.Join(exeDir, "cameras.json")
	}
	if cwd, err := os.Getwd(); err == nil {
		return filepath.Join(cwd, "cameras.json")
	}
	return "cameras.json"
}

type rotatingLogWriter struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	written int64
	maxSize int64
}

func newRotatingLogWriter(path string, maxSize int64) *rotatingLogWriter {
	w := &rotatingLogWriter{path: path, maxSize: maxSize}
	w.openOrRotate()
	return w
}

func (w *rotatingLogWriter) openOrRotate() {
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	if info, err := os.Stat(w.path); err == nil {
		if info.Size() >= w.maxSize {
			_ = os.Rename(w.path, w.path+".1")
		} else {
			w.written = info.Size()
		}
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		_ = os.Remove(w.path)
		f, err = os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	}
	if err == nil {
		w.f = f
	}
}

func (w *rotatingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return len(p), nil
	}
	if w.written+int64(len(p)) > w.maxSize {
		w.written = 0
		w.openOrRotate()
		if w.f == nil {
			return len(p), nil
		}
	}
	n, err := w.f.Write(p)
	w.written += int64(n)
	return n, err
}

func main() {
	portFlag := flag.Int("port", 19820, "Local HTTP/WebSocket port")
	serverOnly := flag.Bool("server-only", false, "Run server without launching desktop app window")
	configPath := flag.String("config", "", "Path to cameras.json config file")
	flag.Parse()

	cfgFile := *configPath
	if cfgFile == "" {
		cfgFile = resolveDefaultConfigPath()
	}

	logPath := filepath.Join(filepath.Dir(cfgFile), "focus-camera.log")
	rotLog := newRotatingLogWriter(logPath, 10*1024*1024) // 10 MB per log file
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetOutput(io.MultiWriter(os.Stderr, rotLog))

	store, err := config.NewStore(cfgFile)
	if err != nil {
		log.Fatalf("初始化配置失败: %v", err)
	}
	hub := rtsp.NewStreamHub(store)
	recMgr := recorder.NewManager(store, hub)
	updMgr := updater.NewManager(store)
	ptzCtrl := ptz.NewController()

	// Non-blocking startup update check against GitHub Releases (or configured update source)
	go func() {
		time.Sleep(2 * time.Second)
		updMgr.CheckSilent()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("收到系统退出信号 (%v)，正在安全刷盘并关闭所有 1080P 录像分片...", sig)
		recMgr.StopAll()
		log.Printf("所有录像分片已安全落盘，服务退出。")
		os.Exit(0)
	}()

	mux := http.NewServeMux()

	// 1. API: Get/Upsert/Delete Cameras
	mux.HandleFunc("/api/cameras", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.Method {
		case http.MethodGet:
			cfg := store.GetConfig()
			if cfg.SubnetPrefix == "" {
				cfg.SubnetPrefix = detectLocalSubnet()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"cameras":        cfg.Cameras,
				"default_subnet": cfg.SubnetPrefix,
				"config":         cfg,
				"stats":          hub.GetActiveStats(),
				"record_status":  recMgr.GetStatus(),
				"detected_lan":   detectLocalSubnet(),
			})
		case http.MethodPost, http.MethodPut:
			var cam config.Camera
			if err := json.NewDecoder(r.Body).Decode(&cam); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			cam.IP = strings.TrimSpace(cam.IP)
			cam.Name = strings.TrimSpace(cam.Name)
			if cam.IP != "" {
				cfg := store.GetConfig()
				user := strings.TrimSpace(cam.Username)
				if user == "" {
					user = cfg.DefaultUsername
				}
				pass := cam.Password
				if pass == "" && !cam.NoPassword {
					pass = cfg.DefaultPassword
				}
				if cam.Name == "" || strings.HasPrefix(cam.Name, "摄像头 (") {
					if res, ok := discovery.ProbeTPLinkCameraDetail(cam.IP, user, []string{pass}, true, 1800*time.Millisecond); ok && res.AuthOK {
						if cam.Name == "" || strings.HasPrefix(cam.Name, "摄像头 (") {
							cam.Name = res.Camera.Name
						}
						if cam.Group == "" {
							cam.Group = res.Camera.Group
						}
						if cam.Model == "" {
							cam.Model = res.Camera.Model
						}
						if cam.Vendor == "" {
							cam.Vendor = res.Camera.Vendor
						}
						if cam.MainStreamPath == "" {
							cam.MainStreamPath = res.Camera.MainStreamPath
						}
						if cam.SubStreamPath == "" {
							cam.SubStreamPath = res.Camera.SubStreamPath
						}
						if res.Camera.SupportsTalk {
							cam.SupportsTalk = true
						}
						cam.Password = res.Camera.Password
						cam.NoPassword = res.Camera.NoPassword
					}
				} else if cam.IsTPLink() {
					go func(ip, u, p string) {
						_ = discovery.EnsureH264Video(ip, u, p)
					}(cam.IP, user, pass)
				}
			}
			oldID, newID, err := store.UpsertCameraTrackID(cam)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if oldID != "" && oldID != newID {
				hub.CloseCamera(oldID)
				recMgr.MigrateCameraID(oldID, newID)
			} else {
				recMgr.SyncNow()
			}
			updCfg := store.GetConfig()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":            true,
				"cameras":       updCfg.Cameras,
				"config":        updCfg,
				"record_status": recMgr.GetStatus(),
			})
		case http.MethodDelete:
			id := r.URL.Query().Get("id")
			if err := store.DeleteCamera(id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			recMgr.SyncNow()
			updCfg := store.GetConfig()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":            true,
				"cameras":       updCfg.Cameras,
				"config":        updCfg,
				"record_status": recMgr.GetStatus(),
			})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// 1b. API: Batch add/update selected cameras from LAN scan preview
	mux.HandleFunc("/api/cameras/batch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var req struct {
			Subnet       string          `json:"subnet"`
			SubnetPrefix string          `json:"subnet_prefix"`
			Username     string          `json:"username"`
			Password     string          `json:"password"`
			Cameras      []config.Camera `json:"cameras"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sub := req.SubnetPrefix
		if sub == "" {
			sub = req.Subnet
		}

		// When cameras were scanned without a password (Name is empty or "摄像头 (<IP>)")
		// and the user supplies their password(s) at add time, probe them in parallel to
		// automatically fetch their internal OSD room names and ensure H.264.
		var probeWg sync.WaitGroup
		for i := range req.Cameras {
			idx := i
			c := &req.Cameras[idx]
			c.IP = strings.TrimSpace(c.IP)
			c.Name = strings.TrimSpace(c.Name)
			if c.IP == "" {
				continue
			}
			user := strings.TrimSpace(c.Username)
			if user == "" {
				user = strings.TrimSpace(req.Username)
			}
			pass := c.Password
			if pass == "" && !c.NoPassword {
				pass = req.Password
			}
			if c.Name == "" || strings.HasPrefix(c.Name, "摄像头 (") {
				probeWg.Add(1)
				go func(target *config.Camera, u, p string) {
					defer probeWg.Done()
					if res, ok := discovery.ProbeTPLinkCameraDetail(target.IP, u, []string{p}, true, 1800*time.Millisecond); ok && res.AuthOK {
						if target.Name == "" || strings.HasPrefix(target.Name, "摄像头 (") {
							target.Name = res.Camera.Name
						}
						if target.Group == "" {
							target.Group = res.Camera.Group
						}
						if target.Model == "" {
							target.Model = res.Camera.Model
						}
						if target.Vendor == "" {
							target.Vendor = res.Camera.Vendor
						}
						if target.MainStreamPath == "" {
							target.MainStreamPath = res.Camera.MainStreamPath
						}
						if target.SubStreamPath == "" {
							target.SubStreamPath = res.Camera.SubStreamPath
						}
						if res.Camera.SupportsTalk {
							target.SupportsTalk = true
						}
						target.Password = res.Camera.Password
						target.NoPassword = res.Camera.NoPassword
					}
				}(c, user, pass)
			}
		}
		probeWg.Wait()

		prevCfg := store.GetConfig()
		oldIDByName := make(map[string]string, len(prevCfg.Cameras))
		for _, c := range prevCfg.Cameras {
			if c.Name != "" && !strings.HasPrefix(c.Name, "摄像头 (") {
				oldIDByName[c.Name] = c.ID
			}
		}

		added, updated, err := store.BatchUpsertCameras(req.Cameras, sub, req.Username, req.Password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		updCfg := store.GetConfig()
		for _, c := range updCfg.Cameras {
			if oldID, ok := oldIDByName[c.Name]; ok && oldID != "" && oldID != c.ID {
				hub.CloseCamera(oldID)
				recMgr.MigrateCameraID(oldID, c.ID)
			}
		}
		recMgr.SyncNow()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":            true,
			"added":         added,
			"updated":       updated,
			"total":         len(updCfg.Cameras),
			"config":        updCfg,
			"record_status": recMgr.GetStatus(),
		})
	})

	// 1c. API: Clear all cameras
	mux.HandleFunc("/api/cameras/clear", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := store.ClearAllCameras(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		recMgr.SyncNow()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":            true,
			"config":        store.GetConfig(),
			"record_status": recMgr.GetStatus(),
		})
	})

	// 1c-2. API: Reorder cameras (drag-and-drop sorting on Live Wall)
	mux.HandleFunc("/api/cameras/reorder", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var req struct {
			OrderedIDs []string `json:"ordered_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.ReorderCameras(req.OrderedIDs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		updCfg := store.GetConfig()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"cameras": updCfg.Cameras,
			"config":  updCfg,
		})
	})

	// 1d. API: Update Global & Recording Settings
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var req struct {
			DefaultUsername   string                  `json:"default_username"`
			DefaultPassword   string                  `json:"default_password"`
			SubnetPrefix      string                  `json:"subnet_prefix"`
			Recording         *config.RecordingConfig `json:"recording"`
			StorageDir        string                  `json:"storage_dir"`
			SegmentMinutes    int                     `json:"segment_minutes"`
			RetentionDays     int                     `json:"retention_days"`
			MaxStorageGB      int                     `json:"max_storage_gb"`
			ScheduleEnabled   bool                    `json:"schedule_enabled"`
			ScheduleStartTime string                  `json:"schedule_start_time"`
			ScheduleEndTime   string                  `json:"schedule_end_time"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		recCfg := store.GetConfig().Recording
		if req.Recording != nil {
			recCfg = *req.Recording
		} else {
			if req.StorageDir != "" {
				recCfg.StorageDir = req.StorageDir
			}
			if req.SegmentMinutes > 0 {
				recCfg.SegmentMinutes = req.SegmentMinutes
			}
			if req.RetentionDays > 0 {
				recCfg.RetentionDays = req.RetentionDays
			}
			if req.MaxStorageGB > 0 {
				recCfg.MaxStorageGB = req.MaxStorageGB
			}
			recCfg.ScheduleEnabled = req.ScheduleEnabled
			if req.ScheduleStartTime != "" {
				recCfg.ScheduleStartTime = req.ScheduleStartTime
			}
			if req.ScheduleEndTime != "" {
				recCfg.ScheduleEndTime = req.ScheduleEndTime
			}
		}
		if err := store.UpdateAppSettings(req.DefaultUsername, req.DefaultPassword, req.SubnetPrefix, recCfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		recMgr.SyncNow()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":            true,
			"config":        store.GetConfig(),
			"record_status": recMgr.GetStatus(),
		})
	})

	// 2. API: Scan LAN and return discovered cameras for interactive preview/selection
	mux.HandleFunc("/api/discover/scan", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var req struct {
			Subnet       string `json:"subnet"`
			SubnetPrefix string `json:"subnet_prefix"`
			Username     string `json:"username"`
			Password     string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		cfg := store.GetConfig()
		if req.SubnetPrefix == "" {
			req.SubnetPrefix = req.Subnet
		}
		if req.SubnetPrefix == "" {
			req.SubnetPrefix = cfg.SubnetPrefix
		}
		if req.SubnetPrefix == "" {
			req.SubnetPrefix = detectLocalSubnet()
		}
		req.SubnetPrefix = discovery.NormalizeSubnetPrefix(req.SubnetPrefix)
		if req.Username == "" {
			req.Username = cfg.DefaultUsername
		}

		byIP := make(map[string]config.Camera, len(cfg.Cameras))
		byName := make(map[string]config.Camera, len(cfg.Cameras))
		perIPPasswords := make(map[string]string, len(cfg.Cameras))
		defaultPasswords := make([]string, 0, 4+len(cfg.Cameras))
		if req.Password != "" {
			defaultPasswords = append(defaultPasswords, req.Password)
		}
		if cfg.DefaultPassword != "" {
			defaultPasswords = append(defaultPasswords, cfg.DefaultPassword)
		}
		for _, c := range cfg.Cameras {
			byIP[c.IP] = c
			if c.Name != "" && !strings.HasPrefix(c.Name, "摄像头 (") {
				byName[c.Name] = c
			}
			if c.Password != "" || c.NoPassword {
				perIPPasswords[c.IP] = c.Password
			}
			if c.Password != "" {
				defaultPasswords = append(defaultPasswords, c.Password)
			}
		}

		found := discovery.ScanSubnetDetailed(req.SubnetPrefix, req.Username, defaultPasswords, perIPPasswords)

		type DiscoveredItem struct {
			config.Camera
			State          string `json:"state"`  // "new" | "existing" | "ip_shifted"
			Status         string `json:"status"` // "new" | "existing" | "ip_shifted"
			OldIP          string `json:"old_ip,omitempty"`
			ExistingID     string `json:"existing_id,omitempty"`
			Reachable      bool   `json:"reachable"`
			RTSPOpen       bool   `json:"rtsp_open"`
			TalkOpen       bool   `json:"talk_open"`
			HTTPOpen       bool   `json:"http_open"`
			DeviceModel    string `json:"device_model"`
			SuggestedName  string `json:"suggested_name"`
			SuggestedGroup string `json:"suggested_group"`
			AuthOK         bool   `json:"auth_ok"`
		}
		items := make([]DiscoveredItem, 0, len(found))
		for _, res := range found {
			d := res.Camera
			st := "new"
			oldIP := ""
			existingID := ""
			if ex, ok := byName[d.Name]; ok && ex.IP != d.IP {
				st = "ip_shifted"
				oldIP = ex.IP
				existingID = ex.ID
				d.Group = ex.Group
				d.AutoRecord = ex.AutoRecord
			} else if ex, ok := byIP[d.IP]; ok {
				st = "existing"
				existingID = ex.ID
				if !res.AuthOK || strings.HasPrefix(d.Name, "摄像头 (") {
					d.Name = ex.Name
				}
				d.Group = ex.Group
				d.AutoRecord = ex.AutoRecord
			}
			items = append(items, DiscoveredItem{
				Camera:         d,
				State:          st,
				Status:         st,
				OldIP:          oldIP,
				ExistingID:     existingID,
				Reachable:      true,
				RTSPOpen:       res.RTSPOpen,
				TalkOpen:       res.TalkOpen,
				HTTPOpen:       res.HTTPOpen,
				DeviceModel:    d.Model,
				SuggestedName:  d.Name,
				SuggestedGroup: d.Group,
				AuthOK:         res.AuthOK,
			})
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":            true,
			"subnet":        req.SubnetPrefix,
			"subnet_prefix": req.SubnetPrefix,
			"found":         len(items),
			"items":         items,
			"discovered":    items,
		})
	})

	// 2a. API: Probe a single camera IP with credentials to verify password and fetch internal OSD room name
	mux.HandleFunc("/api/discover/probe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var req struct {
			IP       string `json:"ip"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ip := strings.TrimSpace(req.IP)
		if ip == "" {
			http.Error(w, "ip is required", http.StatusBadRequest)
			return
		}
		cfg := store.GetConfig()
		user := strings.TrimSpace(req.Username)
		if user == "" {
			user = cfg.DefaultUsername
		}
		res, ok := discovery.ProbeTPLinkCameraDetail(ip, user, []string{req.Password}, true, 2500*time.Millisecond)
		if !ok {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":        false,
				"reachable": false,
				"auth_ok":   false,
				"error":     "无法连接该 IP 的 554/8800 端口，请检查 IP 是否在线",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":              true,
			"reachable":       true,
			"auth_ok":         res.AuthOK,
			"rtsp_open":       res.RTSPOpen,
			"talk_open":       res.TalkOpen,
			"http_open":       res.HTTPOpen,
			"camera":          res.Camera,
			"device_model":    res.Camera.Model,
			"suggested_name":  res.Camera.Name,
			"suggested_group": res.Camera.Group,
			"password":        res.Camera.Password,
			"no_password":     res.Camera.NoPassword,
		})
	})

	// 2b. API: Direct one-step discover & merge
	mux.HandleFunc("/api/discover", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var req struct {
			Subnet       string `json:"subnet"`
			SubnetPrefix string `json:"subnet_prefix"`
			Username     string `json:"username"`
			Password     string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		cfg := store.GetConfig()
		if req.SubnetPrefix == "" {
			req.SubnetPrefix = req.Subnet
		}
		if req.SubnetPrefix == "" {
			req.SubnetPrefix = cfg.SubnetPrefix
		}
		if req.Username == "" {
			req.Username = cfg.DefaultUsername
		}
		if req.Password == "" {
			req.Password = cfg.DefaultPassword
		}

		found := discovery.ScanSubnet(req.SubnetPrefix, req.Username, req.Password)
		added, updated, err := store.BatchUpsertCameras(found, req.SubnetPrefix, req.Username, req.Password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		recMgr.SyncNow()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":            true,
			"found":         len(found),
			"added":         added,
			"updated":       updated,
			"config":        store.GetConfig(),
			"record_status": recMgr.GetStatus(),
		})
	})

	// 3. API: Test voice talkback handshake on a camera
	mux.HandleFunc("/api/talk/test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		camID := r.URL.Query().Get("id")
		cam, ok := store.GetCamera(camID)
		if !ok {
			http.Error(w, `{"ok":false,"error":"未找到摄像头"}`, http.StatusNotFound)
			return
		}
		sess, err := talk.DialTalkSession(cam.IP, cam.TalkPort, cam.Username, cam.Password)
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		_ = sess.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"camera": cam.Name,
			"ip":     cam.IP,
			"model":  cam.Model,
		})
	})

	// 3b. API: Reboot a camera via HTTP management API to release stuck RTSP/406 sessions
	mux.HandleFunc("/api/cameras/reboot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ID string `json:"id"`
			IP string `json:"ip"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		camID := r.URL.Query().Get("id")
		if camID == "" {
			camID = req.ID
		}
		cam, ok := store.GetCamera(camID)
		if !ok && req.IP != "" {
			cfg := store.GetConfig()
			cam = config.Camera{
				IP:       req.IP,
				Username: cfg.DefaultUsername,
				Password: cfg.DefaultPassword,
			}
			ok = true
		}
		if !ok {
			http.Error(w, `{"ok":false,"error":"未找到摄像头"}`, http.StatusNotFound)
			return
		}
		if err := discovery.RebootTPLinkCamera(cam.IP, cam.Username, cam.Password); err != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"camera": cam.Name,
			"ip":     cam.IP,
		})
	})

	// 3c. API: Read or modify a camera's hardware network configuration (Static IP / Netmask / Gateway / DHCP)
	mux.HandleFunc("/api/cameras/network", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.Method {
		case http.MethodGet:
			camID := strings.TrimSpace(r.URL.Query().Get("id"))
			ip := strings.TrimSpace(r.URL.Query().Get("ip"))
			user := strings.TrimSpace(r.URL.Query().Get("username"))
			pass := r.URL.Query().Get("password")

			cfg := store.GetConfig()
			if cam, ok := store.GetCamera(camID); ok {
				if ip == "" {
					ip = cam.IP
				}
				if user == "" {
					user = cam.Username
				}
				if pass == "" && !cam.NoPassword {
					pass = cam.Password
				}
			} else if ip != "" {
				if camByIP, ok := store.GetCamera(ip); ok {
					if user == "" {
						user = camByIP.Username
					}
					if pass == "" && !camByIP.NoPassword {
						pass = camByIP.Password
					}
				}
			}
			if user == "" {
				user = cfg.DefaultUsername
			}
			if pass == "" {
				pass = cfg.DefaultPassword
			}
			if ip == "" {
				http.Error(w, `{"ok":false,"error":"缺少摄像头 IP 参数"}`, http.StatusBadRequest)
				return
			}

			netCfg, err := discovery.GetCameraNetworkConfig(ip, user, pass)
			if err != nil {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok":      false,
					"network": netCfg,
					"error":   err.Error(),
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":      true,
				"network": netCfg,
			})

		case http.MethodPost, http.MethodPut:
			var req struct {
				ID          string `json:"id"`
				OldIP       string `json:"old_ip"`
				NewIP       string `json:"new_ip"`
				Proto       string `json:"proto"` // "static" | "dhcp"
				Netmask     string `json:"netmask"`
				Gateway     string `json:"gateway"`
				Username    string `json:"username"`
				Password    string `json:"password"`
				NoPassword  bool   `json:"no_password"`
				UpdateStore *bool  `json:"update_store"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			cfg := store.GetConfig()
			oldIP := strings.TrimSpace(req.OldIP)
			newIP := strings.TrimSpace(req.NewIP)
			user := strings.TrimSpace(req.Username)
			pass := req.Password

			var existingCam config.Camera
			var hasExisting bool
			if req.ID != "" {
				existingCam, hasExisting = store.GetCamera(req.ID)
			}
			if !hasExisting && oldIP != "" {
				existingCam, hasExisting = store.GetCamera(oldIP)
			}
			if hasExisting {
				if oldIP == "" {
					oldIP = existingCam.IP
				}
				if user == "" {
					user = existingCam.Username
				}
				if pass == "" && !req.NoPassword && !existingCam.NoPassword {
					pass = existingCam.Password
				}
			}
			if user == "" {
				user = cfg.DefaultUsername
			}
			if pass == "" && !req.NoPassword && (!hasExisting || !existingCam.NoPassword) {
				pass = cfg.DefaultPassword
			}

			if oldIP == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok":    false,
					"error": "原摄像头 IP 不能为空",
				})
				return
			}
			proto := strings.ToLower(strings.TrimSpace(req.Proto))
			if proto != "dhcp" {
				proto = "static"
			}
			if proto == "static" {
				if newIP == "" {
					newIP = oldIP
				}
				parsed := net.ParseIP(newIP)
				if parsed == nil || parsed.To4() == nil {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"ok":    false,
						"error": fmt.Sprintf("新 IP 地址 (%s) 格式无效，请输入合法的 IPv4 地址", newIP),
					})
					return
				}
			}

			// Read current hardware network info (for MAC & default netmask/gateway)
			curNet, _ := discovery.GetCameraNetworkConfig(oldIP, user, pass)
			netmask := strings.TrimSpace(req.Netmask)
			if netmask == "" {
				netmask = curNet.Netmask
			}
			gateway := strings.TrimSpace(req.Gateway)
			if gateway == "" {
				// If moving across subnets, infer gateway from newIP; otherwise preserve camera's current gateway
				oldGw, _ := discovery.InferGatewayAndNetmask(oldIP)
				newGw, _ := discovery.InferGatewayAndNetmask(newIP)
				if newGw != oldGw || curNet.Gateway == "" {
					gateway = newGw
				} else {
					gateway = curNet.Gateway
				}
			}

			// Pre-check IP conflict if target IP is changing
			if proto == "static" && newIP != oldIP {
				for _, c := range cfg.Cameras {
					if c.IP == newIP && (!hasExisting || c.ID != existingCam.ID) {
						_ = json.NewEncoder(w).Encode(map[string]any{
							"ok":       false,
							"conflict": true,
							"error":    fmt.Sprintf("目标 IP %s 已被列表中另一台摄像头「%s」使用，请更换其他 IP", newIP, c.Name),
						})
						return
					}
				}
				if conflict, reason := discovery.CheckIPConflict(newIP, curNet.MAC, user, []string{pass, cfg.DefaultPassword}); conflict {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"ok":       false,
						"conflict": true,
						"error":    reason,
					})
					return
				}
			}

			// Stop active stream & recording worker on old IP before reconfiguring hardware network interface
			if hasExisting && proto == "static" && newIP != oldIP {
				hub.CloseCamera(existingCam.ID)
				recMgr.StopCamera(existingCam.ID)
			}

			appliedNet, err := discovery.SetCameraNetworkConfig(oldIP, user, pass, discovery.NetworkConfig{
				Proto:   proto,
				IPAddr:  newIP,
				Netmask: netmask,
				Gateway: gateway,
				MAC:     curNet.MAC,
				DNS:     curNet.DNS,
			})
			if err != nil {
				if hasExisting {
					recMgr.SyncNow()
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok":    false,
					"error": err.Error(),
				})
				return
			}

			shouldUpdateStore := req.UpdateStore == nil || *req.UpdateStore
			newID := config.MakeID(appliedNet.IPAddr)
			if shouldUpdateStore && hasExisting && appliedNet.IPAddr != "" {
				oldID := existingCam.ID
				existingCam.IP = appliedNet.IPAddr
				existingCam.Username = user
				existingCam.Password = pass
				existingCam.NoPassword = req.NoPassword || pass == ""
				_, newID, _ = store.UpsertCameraTrackID(existingCam)
				if oldID != "" && oldID != newID {
					recMgr.MigrateCameraID(oldID, newID)
				} else {
					recMgr.SyncNow()
				}
			}

			updCfg := store.GetConfig()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":            true,
				"old_ip":        oldIP,
				"new_ip":        appliedNet.IPAddr,
				"id":            newID,
				"network":       appliedNet,
				"cameras":       updCfg.Cameras,
				"config":        updCfg,
				"record_status": recMgr.GetStatus(),
			})

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// 3d. API: Camera PTZ (Pan-Tilt-Zoom, Presets & Calibration)
	mux.HandleFunc("/api/cameras/ptz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.Method {
		case http.MethodGet:
			camID := strings.TrimSpace(r.URL.Query().Get("id"))
			cam, ok := store.GetCamera(camID)
			if !ok {
				http.Error(w, `{"ok":false,"error":"未找到摄像头"}`, http.StatusNotFound)
				return
			}
			capRes := ptzCtrl.ProbeCapabilities(cam)
			if capRes.SupportsPTZ != cam.SupportsPTZ || (capRes.Model != "" && capRes.Model != cam.Model) || (capRes.FlipType != "" && capRes.FlipType != cam.FlipType) {
				_ = store.SetCameraPTZInfo(cam.ID, capRes.SupportsPTZ, capRes.Model, capRes.FlipType)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":           true,
				"supports_ptz": capRes.SupportsPTZ,
				"protocol":     capRes.Protocol,
				"model":        capRes.Model,
				"flip_type":    capRes.FlipType,
				"invert_x":     capRes.InvertX,
				"invert_y":     capRes.InvertY,
				"presets":      capRes.Presets,
				"message":      capRes.Message,
			})

		case http.MethodPost:
			var req ptz.CommandRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			camID := strings.TrimSpace(req.ID)
			if camID == "" {
				camID = strings.TrimSpace(r.URL.Query().Get("id"))
			}
			cam, ok := store.GetCamera(camID)
			if !ok {
				http.Error(w, `{"ok":false,"error":"未找到摄像头"}`, http.StatusNotFound)
				return
			}

			action := strings.ToLower(strings.TrimSpace(req.Action))
			if action == "set_invert" {
				_ = store.SetCameraPTZInvert(cam.ID, req.InvertX, req.InvertY, "")
				updatedCam, _ := store.GetCamera(cam.ID)
				invX, invY := ptz.ResolveEffectiveInvert(updatedCam, updatedCam.FlipType)
				_ = json.NewEncoder(w).Encode(ptz.ExecuteResult{
					OK:               true,
					HardwareExecuted: true,
					SupportsPTZ:      updatedCam.SupportsPTZ,
					Protocol:         "tplink",
					FlipType:         updatedCam.FlipType,
					InvertX:          invX,
					InvertY:          invY,
					Presets:          updatedCam.PTZPresets,
					Message:          "已保存当前摄像头的云台方向校准设置",
				})
				return
			}

			execRes := ptzCtrl.Execute(cam, req)
			if execRes.HardwareExecuted && !cam.SupportsPTZ {
				_ = store.SetCameraSupportsPTZ(cam.ID, true)
			}

			switch action {
			case "set_flip":
				if execRes.OK && execRes.FlipType != "" {
					invX := execRes.FlipType == "center" || execRes.FlipType == "left_right"
					invY := execRes.FlipType == "center" || execRes.FlipType == "up_down"
					_ = store.SetCameraPTZInvert(cam.ID, &invX, &invY, execRes.FlipType)
					execRes.InvertX = invX
					execRes.InvertY = invY
				}
				if updatedCam, ok := store.GetCamera(cam.ID); ok {
					execRes.Presets = updatedCam.PTZPresets
				}

			case "preset_set":
				pName := strings.TrimSpace(req.PresetName)
				if pName == "" {
					pName = fmt.Sprintf("预置位 %d", len(cam.PTZPresets)+1)
				}
				pID := strings.TrimSpace(req.PresetID)
				if pID == "" && execRes.CreatedHWToken != "" {
					pID = execRes.CreatedHWToken
				}
				preset := config.PTZPreset{
					ID:       pID,
					Name:     pName,
					HWToken:  execRes.CreatedHWToken,
					Hardware: execRes.HardwareExecuted,
				}
				if req.DigitalView != nil {
					preset.Zoom = req.DigitalView.Zoom
					preset.PanX = req.DigitalView.PanX
					preset.PanY = req.DigitalView.PanY
				}
				presets, _ := store.UpsertCameraPTZPreset(cam.ID, preset)
				execRes.Presets = presets

			case "preset_delete":
				presets, _ := store.DeleteCameraPTZPreset(cam.ID, strings.TrimSpace(req.PresetID))
				execRes.Presets = presets

			default:
				if updatedCam, ok := store.GetCamera(cam.ID); ok {
					execRes.Presets = updatedCam.PTZPresets
				}
			}

			_ = json.NewEncoder(w).Encode(execRes)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// 4. API: Recording & Playback Queries and Controls
	mux.HandleFunc("/api/recordings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		dateStr := r.URL.Query().Get("date")
		camID := r.URL.Query().Get("camera_id")
		summary := recMgr.GetStorageSummary()
		segments := recMgr.ListSegments(dateStr, camID)
		statusMap := recMgr.GetStatus()
		statesList := make([]recorder.CameraRecordState, 0, len(statusMap))
		for _, st := range statusMap {
			statesList = append(statesList, st)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":            true,
			"date":          dateStr,
			"config":        store.GetConfig().Recording,
			"states":        statesList,
			"dates":         summary.AvailableDates,
			"total_bytes":   summary.TotalBytes,
			"segment_count": summary.TotalFiles,
			"summary":       summary,
			"segments":      segments,
			"record_status": statusMap,
		})
	})

	mux.HandleFunc("/api/recordings/control", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var req struct {
			CameraID string `json:"camera_id"`
			Action   string `json:"action"` // "start_manual" | "stop_manual" | "set_auto" | "auto_on" | "auto_off" | "all_auto_on" | "all_auto_off"
			Enabled  bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch req.Action {
		case "start_manual":
			_ = recMgr.StartManual(req.CameraID)
		case "stop_manual":
			_ = recMgr.StopManual(req.CameraID)
		case "set_auto":
			_ = store.SetCameraAutoRecord(req.CameraID, req.Enabled)
			recMgr.SyncNow()
		case "auto_on":
			_ = store.SetCameraAutoRecord(req.CameraID, true)
			recMgr.SyncNow()
		case "auto_off":
			_ = store.SetCameraAutoRecord(req.CameraID, false)
			recMgr.SyncNow()
		case "all_auto_on":
			_ = store.SetAllAutoRecord(true)
			recMgr.SyncNow()
		case "all_auto_off":
			_ = store.SetAllAutoRecord(false)
			recMgr.SyncNow()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":            true,
			"config":        store.GetConfig(),
			"record_status": recMgr.GetStatus(),
		})
	})

	mux.HandleFunc("/api/recordings/file", func(w http.ResponseWriter, r *http.Request) {
		relPath := r.URL.Query().Get("path")
		fullPath, err := recMgr.ResolveSafeFilePath(relPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			f, err := os.Open(fullPath)
			if err != nil {
				http.Error(w, "recording not found", http.StatusNotFound)
				return
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				http.Error(w, "stat failed", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			if r.URL.Query().Get("download") == "1" {
				w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(fullPath)))
			}
			http.ServeContent(w, r, filepath.Base(fullPath), info.ModTime(), f)
		case http.MethodDelete:
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/recordings/open-folder", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		rc := store.GetConfig().Recording
		_ = os.MkdirAll(rc.StorageDir, 0o755)
		absDir, err := filepath.Abs(rc.StorageDir)
		if err != nil {
			absDir = rc.StorageDir
		}
		switch runtime.GOOS {
		case "windows":
			_ = exec.Command("explorer", absDir).Start()
		case "darwin":
			_ = exec.Command("open", absDir).Start()
		default:
			_ = exec.Command("xdg-open", absDir).Start()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path": absDir})
	})

	// 5. WebSocket: Video + Audio Live Stream (/ws/stream?id=...&stream=sub|main&audio=0|1)
	mux.HandleFunc("/ws/stream", func(w http.ResponseWriter, r *http.Request) {
		camID := r.URL.Query().Get("id")
		streamType := r.URL.Query().Get("stream")
		wantAudio := r.URL.Query().Get("audio") == "1"

		ws, err := upgradeWebSocket(w, r)
		if err != nil {
			return
		}
		defer ws.Close()

		sub, unsubscribe, err := hub.Subscribe(camID, streamType, wantAudio)
		if err != nil {
			errJSON, _ := json.Marshal(map[string]string{"status": "error", "error": err.Error()})
			_ = ws.WriteFrame(0x02, append([]byte{rtsp.MsgTypeStatusJSON}, errJSON...))
			return
		}
		defer unsubscribe()

		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				opcode, payload, err := ws.ReadFrame()
				if err != nil {
					return
				}
				switch opcode {
				case 0x08: // Close
					return
				case 0x09: // Ping
					_ = ws.WriteFrame(0x0A, payload)
				case 0x01: // Text JSON command
					var cmd struct {
						Action string `json:"action"`
						Enable bool   `json:"enable"`
					}
					if json.Unmarshal(payload, &cmd) == nil && cmd.Action == "audio" {
						hub.SetSubscriberAudio(camID, streamType, sub.ID, cmd.Enable)
					}
				}
			}
		}()

		for {
			select {
			case <-done:
				return
			case pkt, ok := <-sub.Ch:
				if !ok {
					return
				}
				if err := ws.WriteFrame(0x02, pkt); err != nil {
					return
				}
			}
		}
	})

	// 6. WebSocket: Two-Way Voice Talkback (/ws/talk?id=...)
	mux.HandleFunc("/ws/talk", func(w http.ResponseWriter, r *http.Request) {
		camID := r.URL.Query().Get("id")
		cam, ok := store.GetCamera(camID)
		if !ok {
			http.Error(w, "camera not found", http.StatusNotFound)
			return
		}

		ws, err := upgradeWebSocket(w, r)
		if err != nil {
			return
		}
		defer ws.Close()

		sess, err := talk.DialTalkSession(cam.IP, cam.TalkPort, cam.Username, cam.Password)
		if err != nil {
			msg, _ := json.Marshal(map[string]any{
				"type":    "error",
				"error":   err.Error(),
				"message": err.Error(),
			})
			_ = ws.WriteFrame(0x01, msg)
			return
		}
		defer sess.Close()

		readyMsg, _ := json.Marshal(map[string]any{
			"type":    "ready",
			"camera":  cam.Name,
			"ip":      cam.IP,
			"message": fmt.Sprintf("双向语音对讲已接通 (%s)", cam.Name),
		})
		if err := ws.WriteFrame(0x01, readyMsg); err != nil {
			return
		}

		for {
			opcode, payload, err := ws.ReadFrame()
			if err != nil {
				return
			}
			switch opcode {
			case 0x08: // Close
				return
			case 0x09: // Ping
				_ = ws.WriteFrame(0x0A, payload)
			case 0x02: // Binary PCMA 8000Hz microphone bytes
				if len(payload) > 0 {
					if err := sess.SendPCMA(payload); err != nil {
						errMsg, _ := json.Marshal(map[string]any{
							"type":    "error",
							"error":   "对讲发送中断: " + err.Error(),
							"message": "对讲发送中断: " + err.Error(),
						})
						_ = ws.WriteFrame(0x01, errMsg)
						return
					}
				}
			}
		}
	})

	// 7. API: Controlled Self-Update Status, Check & Safe Atomic Apply
	mux.HandleFunc("/api/update/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(updMgr.GetStatus())
	})

	mux.HandleFunc("/api/update/check", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var req struct {
			UpdateURL string `json:"update_url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.Body != nil {
			// Allow explicitly saving or clearing update_url when provided
			if strings.TrimSpace(req.UpdateURL) != "" {
				_ = store.SetUpdateURL(req.UpdateURL)
			}
		}
		st, _ := updMgr.Check(req.UpdateURL)
		_ = json.NewEncoder(w).Encode(st)
	})

	mux.HandleFunc("/api/update/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		err := updMgr.StartUpgrade(func() {
			log.Printf("[Updater] 升级替换前正在安全刷盘并关闭所有 1080P 录像分片...")
			recMgr.StopAll()
			log.Printf("[Updater] 所有 1080P 录像分片已安全落盘。")
		}, true)
		st := updMgr.GetStatus()
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
		}
		_ = json.NewEncoder(w).Encode(st)
	})

	// 7b. API: Graceful shutdown for replacing older background instance
	mux.HandleFunc("/api/shutdown", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
		go func() {
			log.Printf("收到 /api/shutdown 请求，正在安全刷盘并关闭所有录像分片...")
			recMgr.StopAll()
			time.Sleep(100 * time.Millisecond)
			os.Exit(0)
		}()
	})

	// 8. Static UI files: prefer live workspace web/ directory if present on disk,
	// falling back to embedded web/ for standalone single-binary distribution.
	webSub, err := fs.Sub(webEmbed, "web")
	if err != nil {
		log.Fatal(err)
	}
	embeddedFileServer := http.FileServer(http.FS(webSub))
	cfgDir := filepath.Dir(cfgFile)
	diskWebCandidates := []string{
		filepath.Join(cfgDir, "web"),
		filepath.Join(filepath.Dir(cfgDir), "web"),
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		for _, dir := range diskWebCandidates {
			if st, err := os.Stat(filepath.Join(dir, "index.html")); err == nil && !st.IsDir() {
				http.FileServer(http.Dir(dir)).ServeHTTP(w, r)
				return
			}
		}
		embeddedFileServer.ServeHTTP(w, r)
	})

	killPreviousInstances(*portFlag)

	addr := fmt.Sprintf("127.0.0.1:%d", *portFlag)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatalf("无法监听本地端口: %v", err)
		}
	}
	actualURL := fmt.Sprintf("http://%s", ln.Addr().String())
	log.Printf("Focus Camera 监控对讲服务已启动: %s (配置文件: %s, 日志文件: %s)", actualURL, cfgFile, logPath)

	go func() {
		if err := http.Serve(ln, mux); err != nil {
			log.Fatalf("服务停止: %v", err)
		}
	}()

	if *serverOnly {
		select {}
	}

	runNativeDesktopWindow(fmt.Sprintf("%s/?_v=%d", actualURL, time.Now().UnixMilli()))
	log.Printf("桌面窗口已关闭，正在安全刷盘并关闭所有 1080P 录像分片...")
	recMgr.StopAll()
	log.Printf("所有录像分片已安全落盘，程序退出。")
}

func killPreviousInstances(port int) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	if resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/api/shutdown", port), "application/json", nil); err == nil {
		_ = resp.Body.Close()
		time.Sleep(250 * time.Millisecond)
	}
	if runtime.GOOS == "windows" {
		myPID := os.Getpid()
		for _, img := range []string{"FocusCamera-Windows-x64.exe", "FocusCamera-Windows-Console.exe", "FocusCamera.exe"} {
			_ = exec.Command("taskkill", "/F", "/FI", fmt.Sprintf("PID ne %d", myPID), "/IM", img).Run()
		}
		time.Sleep(200 * time.Millisecond)
	}
}
