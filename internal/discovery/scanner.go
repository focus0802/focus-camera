package discovery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"focus-camera/internal/config"
)

const (
	keyShort = "RDpbLfCPsJZ7fiv"
	keyLong  = "yLwVl0zKqws7LgKPRQ84Mdt708T1qQ3Ha7xv3H7NyU84p21BriUWBU43odz3iP4rBL3cD02KZciXTysVXiV8ngg6vL48rPJyAUw0HurW20xqxv9aYb4M9wK1Ae0wlro510qXeU07kV57fQMc8L6aLgMLwygtc0F10a0Dg70TOoouyFhdysuRMO51yY5ZlOZZLEal1h0t9YQW0Ko7oBwmCAHoic4HYbUyVeU3sfQ1xtXcPcf1aT303wAQhv66qzW"
)

func SecurityEncode(s string) string {
	n := len(s)
	if len(keyShort) > n {
		n = len(keyShort)
	}
	b := make([]byte, n)
	for i := 0; i < n; i++ {
		c1 := 187
		c2 := 187
		if i < len(keyShort) {
			c1 = int(keyShort[i])
		}
		if i < len(s) {
			c2 = int(s[i])
		}
		b[i] = keyLong[(c1^c2)%len(keyLong)]
	}
	return string(b)
}

func ProbeTPLinkCamera(ip, username, password string, timeout time.Duration) (config.Camera, bool) {
	var (
		talkOpen bool
		rtspOpen bool
		pWg      sync.WaitGroup
	)
	pWg.Add(2)
	go func() {
		defer pWg.Done()
		if conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:8800", ip), timeout); err == nil {
			_ = conn.Close()
			talkOpen = true
		}
	}()
	go func() {
		defer pWg.Done()
		if conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:554", ip), timeout); err == nil {
			_ = conn.Close()
			rtspOpen = true
		}
	}()
	pWg.Wait()

	if !talkOpen && !rtspOpen {
		return config.Camera{}, false
	}

	fallbackCam := func() config.Camera {
		name := fmt.Sprintf("摄像头 (%s)", ip)
		grp, order := config.InferGroupAndOrder(name, ip)
		return config.Camera{
			ID:           config.MakeID(ip),
			Name:         name,
			Group:        grp,
			IP:           ip,
			Username:     username,
			Password:     password,
			RTSPPort:     554,
			TalkPort:     8800,
			Model:        "TL-IPC42A",
			SupportsTalk: talkOpen,
			SortOrder:    order,
		}
	}

	client := &http.Client{Timeout: 3000 * time.Millisecond}
	encPwd := SecurityEncode(password)

	loginBody, _ := json.Marshal(map[string]any{
		"method": "do",
		"login": map[string]string{
			"username":     username,
			"password":     encPwd,
			"encrypt_type": "1",
		},
	})

	resp, err := client.Post(fmt.Sprintf("http://%s/", ip), "application/json", bytes.NewReader(loginBody))
	if err != nil {
		if !talkOpen {
			return config.Camera{}, false
		}
		return fallbackCam(), true
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		if !talkOpen {
			return config.Camera{}, false
		}
		return fallbackCam(), true
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))

	var loginResp struct {
		ErrorCode int    `json:"error_code"`
		Stok      string `json:"stok"`
	}
	if err := json.Unmarshal(raw, &loginResp); err != nil || loginResp.Stok == "" {
		if !talkOpen {
			return config.Camera{}, false
		}
		return fallbackCam(), true
	}

	dsBody, _ := json.Marshal(map[string]any{
		"method":      "get",
		"device_info": map[string]any{"name": []string{"info"}},
		"system":      map[string]any{"name": []string{"sys"}},
		"OSD":         map[string]any{"table": []string{"label_info"}},
		"video":       map[string]any{"name": []string{"main", "minor"}},
	})
	dsResp, err := client.Post(fmt.Sprintf("http://%s/stok=%s/ds", ip, loginResp.Stok), "application/json", bytes.NewReader(dsBody))
	if err != nil {
		return fallbackCam(), true
	}
	defer dsResp.Body.Close()
	dsRaw, err := io.ReadAll(dsResp.Body)
	if err != nil {
		return fallbackCam(), true
	}
	dsRaw = bytes.TrimPrefix(dsRaw, []byte("\xef\xbb\xbf"))

	var ds struct {
		DeviceInfo struct {
			Info struct {
				DeviceModel string `json:"device_model"`
			} `json:"info"`
		} `json:"device_info"`
		System struct {
			Sys struct {
				DevAlias string `json:"dev_alias"`
			} `json:"sys"`
		} `json:"system"`
		OSD struct {
			LabelInfo []map[string]struct {
				Text string `json:"text"`
			} `json:"label_info"`
		} `json:"OSD"`
		Video struct {
			Main struct {
				EncodeType string `json:"encode_type"`
			} `json:"main"`
			Minor struct {
				EncodeType string `json:"encode_type"`
			} `json:"minor"`
		} `json:"video"`
	}
	_ = json.Unmarshal(dsRaw, &ds)

	if strings.EqualFold(ds.Video.Main.EncodeType, "H265") || strings.EqualFold(ds.Video.Minor.EncodeType, "H265") {
		setVideo := map[string]any{}
		if strings.EqualFold(ds.Video.Main.EncodeType, "H265") {
			setVideo["main"] = map[string]any{"encode_type": "H264"}
		}
		if strings.EqualFold(ds.Video.Minor.EncodeType, "H265") {
			setVideo["minor"] = map[string]any{"encode_type": "H264"}
		}
		setBody, _ := json.Marshal(map[string]any{
			"method": "set",
			"video":  setVideo,
		})
		if setResp, err := client.Post(fmt.Sprintf("http://%s/stok=%s/ds", ip, loginResp.Stok), "application/json", bytes.NewReader(setBody)); err == nil {
			_ = setResp.Body.Close()
		}
	}

	model, _ := url.QueryUnescape(ds.DeviceInfo.Info.DeviceModel)
	if model == "" {
		model = "TL-IPC42A"
	}
	alias, _ := url.QueryUnescape(ds.System.Sys.DevAlias)
	osdName := ""
	for _, item := range ds.OSD.LabelInfo {
		if l1, ok := item["label_info_1"]; ok {
			osdName, _ = url.QueryUnescape(l1.Text)
			break
		}
	}
	name := strings.TrimSpace(osdName)
	if name == "" || strings.HasPrefix(name, "自定义") {
		name = strings.TrimSpace(alias)
	}
	if name == "" || name == "IPC" || strings.HasPrefix(name, "TL-IPC") {
		name = fmt.Sprintf("摄像头 (%s)", ip)
	}

	grp, order := config.InferGroupAndOrder(name, ip)
	return config.Camera{
		ID:           config.MakeID(ip),
		Name:         name,
		Group:        grp,
		IP:           ip,
		Username:     username,
		Password:     password,
		RTSPPort:     554,
		TalkPort:     8800,
		Model:        model,
		SupportsTalk: true,
		SortOrder:    order,
	}, true
}

func NormalizeSubnetPrefix(raw string) string {
	s := strings.TrimSpace(raw)
	if slash := strings.IndexByte(s, '/'); slash > 0 {
		s = s[:slash]
	}
	s = strings.TrimSuffix(s, ".")
	parts := strings.Split(s, ".")
	if len(parts) >= 3 {
		return fmt.Sprintf("%s.%s.%s", parts[0], parts[1], parts[2])
	}
	return s
}

func ScanSubnet(subnetPrefix, username, password string) []config.Camera {
	subnetPrefix = NormalizeSubnetPrefix(subnetPrefix)
	if subnetPrefix == "" {
		subnetPrefix = "192.168.1"
	}
	var (
		mu    sync.Mutex
		found []config.Camera
		wg    sync.WaitGroup
		sem   = make(chan struct{}, 64)
	)

	for i := 1; i <= 254; i++ {
		ip := fmt.Sprintf("%s.%d", subnetPrefix, i)
		wg.Add(1)
		go func(targetIP string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if cam, ok := ProbeTPLinkCamera(targetIP, username, password, 1000*time.Millisecond); ok {
				mu.Lock()
				found = append(found, cam)
				mu.Unlock()
			}
		}(ip)
	}
	wg.Wait()
	config.SortCameras(found)
	return found
}

func RebootTPLinkCamera(ip, username, password string) error {
	client := &http.Client{Timeout: 4 * time.Second}
	encPwd := SecurityEncode(password)

	loginBody, _ := json.Marshal(map[string]any{
		"method": "do",
		"login": map[string]string{
			"username":     username,
			"password":     encPwd,
			"encrypt_type": "1",
		},
	})
	resp, err := client.Post(fmt.Sprintf("http://%s/", ip), "application/json", bytes.NewReader(loginBody))
	if err != nil {
		return fmt.Errorf("连接摄像头管理接口失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))

	var loginResp struct {
		ErrorCode int    `json:"error_code"`
		Stok      string `json:"stok"`
	}
	if err := json.Unmarshal(raw, &loginResp); err != nil || loginResp.Stok == "" {
		return fmt.Errorf("摄像头管理登录失败 (code=%d)", loginResp.ErrorCode)
	}

	rbBody := []byte(`{"method":"do","system":{"reboot":null}}`)
	rbResp, err := client.Post(fmt.Sprintf("http://%s/stok=%s/ds", ip, loginResp.Stok), "application/json", bytes.NewReader(rbBody))
	if err != nil {
		return fmt.Errorf("发送重启指令失败: %w", err)
	}
	defer rbResp.Body.Close()
	return nil
}

func EnsureH264Video(ip, username, password string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	encPwd := SecurityEncode(password)

	loginBody, _ := json.Marshal(map[string]any{
		"method": "do",
		"login": map[string]string{
			"username":     username,
			"password":     encPwd,
			"encrypt_type": "1",
		},
	})
	resp, err := client.Post(fmt.Sprintf("http://%s/", ip), "application/json", bytes.NewReader(loginBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))

	var loginResp struct {
		Stok string `json:"stok"`
	}
	if err := json.Unmarshal(raw, &loginResp); err != nil || loginResp.Stok == "" {
		return fmt.Errorf("login failed")
	}

	setBody, _ := json.Marshal(map[string]any{
		"method": "set",
		"video": map[string]any{
			"main":  map[string]any{"encode_type": "H264"},
			"minor": map[string]any{"encode_type": "H264"},
		},
	})
	setResp, err := client.Post(fmt.Sprintf("http://%s/stok=%s/ds", ip, loginResp.Stok), "application/json", bytes.NewReader(setBody))
	if err != nil {
		return err
	}
	defer setResp.Body.Close()
	return nil
}
