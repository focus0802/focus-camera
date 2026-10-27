package ptz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"focus-camera/internal/config"
	"focus-camera/internal/discovery"
)

type DigitalView struct {
	Zoom float64 `json:"zoom"`
	PanX float64 `json:"pan_x"`
	PanY float64 `json:"pan_y"`
}

type CommandRequest struct {
	ID          string       `json:"id"`
	Action      string       `json:"action"`    // "move" | "step" | "stop" | "calibrate" | "home" | "zoom_in" | "zoom_out" | "zoom_stop" | "preset_set" | "preset_goto" | "preset_delete" | "set_flip" | "set_invert"
	Direction   string       `json:"direction"` // "up" | "down" | "left" | "right" | "up_left" | "up_right" | "down_left" | "down_right"
	Speed       int          `json:"speed"`     // 1 (slow) .. 3 (fast)
	PresetID    string       `json:"preset_id"`
	PresetName  string       `json:"preset_name"`
	FlipType    string       `json:"flip_type,omitempty"` // "off" (正装) | "center" (倒装 180°) | "left_right" | "up_down"
	InvertX     *bool        `json:"invert_x,omitempty"`
	InvertY     *bool        `json:"invert_y,omitempty"`
	DigitalView *DigitalView `json:"digital_view,omitempty"`
}

type CapabilityResult struct {
	SupportsPTZ bool               `json:"supports_ptz"`
	Protocol    string             `json:"protocol"` // "tplink" | "onvif" | "none"
	Model       string             `json:"model,omitempty"`
	FlipType    string             `json:"flip_type,omitempty"`
	InvertX     bool               `json:"invert_x"`
	InvertY     bool               `json:"invert_y"`
	Presets     []config.PTZPreset `json:"presets"`
	Message     string             `json:"message,omitempty"`
}

type ExecuteResult struct {
	OK               bool               `json:"ok"`
	HardwareExecuted bool               `json:"hardware_executed"`
	SupportsPTZ      bool               `json:"supports_ptz"`
	Protocol         string             `json:"protocol"`
	FlipType         string             `json:"flip_type,omitempty"`
	InvertX          bool               `json:"invert_x"`
	InvertY          bool               `json:"invert_y"`
	CreatedHWToken   string             `json:"created_hw_token,omitempty"`
	Presets          []config.PTZPreset `json:"presets,omitempty"`
	Message          string             `json:"message,omitempty"`
	Error            string             `json:"error,omitempty"`
}

type cachedStok struct {
	stok      string
	expiresAt time.Time
}

type cachedONVIF struct {
	ptzURL       string
	profileToken string
	supportsPTZ  bool
	expiresAt    time.Time
}

type Controller struct {
	mu          sync.Mutex
	stokCache   map[string]cachedStok
	tpModCache  map[string]string // ip -> "motor" | "ptz"
	tpFlipCache map[string]string // ip -> "off" | "center" | "left_right" | "up_down"
	onvifCache  map[string]cachedONVIF
	httpClient  *http.Client
}

func NewController() *Controller {
	return &Controller{
		stokCache:   make(map[string]cachedStok),
		tpModCache:  make(map[string]string),
		tpFlipCache: make(map[string]string),
		onvifCache:  make(map[string]cachedONVIF),
		httpClient:  &http.Client{Timeout: 2800 * time.Millisecond},
	}
}

func (c *Controller) getTPModule(ip string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tpModCache[strings.TrimSpace(ip)]
}

func (c *Controller) setTPModule(ip, mod string) {
	c.mu.Lock()
	c.tpModCache[strings.TrimSpace(ip)] = mod
	c.mu.Unlock()
}

func (c *Controller) getTPFlip(ip string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tpFlipCache[strings.TrimSpace(ip)]
}

func (c *Controller) setTPFlip(ip, flip string) {
	c.mu.Lock()
	c.tpFlipCache[strings.TrimSpace(ip)] = strings.TrimSpace(flip)
	c.mu.Unlock()
}

func (c *Controller) probeTPFlip(ip, user, pass string) string {
	raw, code, httpOpen, err := c.postTPLinkDS(ip, user, pass, map[string]any{
		"method": "get",
		"image":  map[string]any{"name": []string{"switch"}},
	})
	if err == nil && httpOpen && code == 0 {
		var sw struct {
			Image struct {
				Switch struct {
					FlipType string `json:"flip_type"`
				} `json:"switch"`
			} `json:"image"`
		}
		if json.Unmarshal(raw, &sw) == nil && sw.Image.Switch.FlipType != "" {
			ft := strings.TrimSpace(sw.Image.Switch.FlipType)
			c.setTPFlip(ip, ft)
			return ft
		}
	}
	return ""
}

func ResolveEffectiveInvert(cam config.Camera, flipType string) (invX, invY bool) {
	ft := strings.ToLower(strings.TrimSpace(flipType))
	if ft == "" {
		ft = strings.ToLower(strings.TrimSpace(cam.FlipType))
	}
	switch ft {
	case "center":
		invX, invY = true, true
	case "left_right":
		invX = true
	case "up_down":
		invY = true
	}
	if cam.PTZInvertX != nil {
		invX = *cam.PTZInvertX
	}
	if cam.PTZInvertY != nil {
		invY = *cam.PTZInvertY
	}
	return invX, invY
}

func transformDirection(dir string, invX, invY bool) string {
	var dx, dy int
	switch strings.ToLower(strings.TrimSpace(dir)) {
	case "up", "90":
		dx, dy = 0, 1
	case "down", "270":
		dx, dy = 0, -1
	case "left", "180":
		dx, dy = -1, 0
	case "right", "0":
		dx, dy = 1, 0
	case "up_left", "upleft", "135":
		dx, dy = -1, 1
	case "up_right", "upright", "45":
		dx, dy = 1, 1
	case "down_left", "downleft", "225":
		dx, dy = -1, -1
	case "down_right", "downright", "315":
		dx, dy = 1, -1
	default:
		return dir
	}
	if invX {
		dx = -dx
	}
	if invY {
		dy = -dy
	}
	switch {
	case dx == 0 && dy == 1:
		return "up"
	case dx == 0 && dy == -1:
		return "down"
	case dx == -1 && dy == 0:
		return "left"
	case dx == 1 && dy == 0:
		return "right"
	case dx == -1 && dy == 1:
		return "up_left"
	case dx == 1 && dy == 1:
		return "up_right"
	case dx == -1 && dy == -1:
		return "down_left"
	case dx == 1 && dy == -1:
		return "down_right"
	default:
		return dir
	}
}

func (c *Controller) cacheKey(ip, user, pass string) string {
	return fmt.Sprintf("%s|%s|%s", strings.TrimSpace(ip), strings.TrimSpace(user), pass)
}

func (c *Controller) getOrLoginStok(ip, user, pass string, forceRefresh bool) (string, bool) {
	if user == "" {
		user = "admin"
	}
	key := c.cacheKey(ip, user, pass)
	if !forceRefresh {
		c.mu.Lock()
		entry, ok := c.stokCache[key]
		c.mu.Unlock()
		if ok && entry.stok != "" && time.Now().Before(entry.expiresAt) {
			return entry.stok, true
		}
	}

	stok, _, httpOpen := discovery.LoginTPLinkHTTP(c.httpClient, ip, user, []string{pass})
	if stok != "" {
		c.mu.Lock()
		c.stokCache[key] = cachedStok{
			stok:      stok,
			expiresAt: time.Now().Add(4 * time.Minute),
		}
		c.mu.Unlock()
	}
	return stok, httpOpen
}

func (c *Controller) invalidateStok(ip, user, pass string) {
	if user == "" {
		user = "admin"
	}
	key := c.cacheKey(ip, user, pass)
	c.mu.Lock()
	delete(c.stokCache, key)
	c.mu.Unlock()
}

func (c *Controller) postTPLinkDS(ip, user, pass string, payload map[string]any) ([]byte, int, bool, error) {
	stok, httpOpen := c.getOrLoginStok(ip, user, pass, false)
	if stok == "" {
		return nil, -1, httpOpen, fmt.Errorf("not a TP-LINK ds endpoint or auth failed")
	}

	doPost := func(token string) ([]byte, int, error) {
		body, _ := json.Marshal(payload)
		resp, err := c.httpClient.Post(
			fmt.Sprintf("http://%s/stok=%s/ds", ip, token),
			"application/json",
			bytes.NewReader(body),
		)
		if err != nil {
			return nil, -1, err
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, -1, err
		}
		raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
		var base struct {
			ErrorCode int `json:"error_code"`
		}
		if err := json.Unmarshal(raw, &base); err != nil {
			return raw, -1, err
		}
		return raw, base.ErrorCode, nil
	}

	raw, errCode, err := doPost(stok)
	if err != nil || errCode == -40401 {
		// Token expired or connection reset; refresh stok once
		c.invalidateStok(ip, user, pass)
		stok2, httpOpen2 := c.getOrLoginStok(ip, user, pass, true)
		if stok2 == "" {
			return nil, -1, httpOpen || httpOpen2, fmt.Errorf("re-login failed")
		}
		raw, errCode, err = doPost(stok2)
	}
	return raw, errCode, true, err
}

func directionToTPLinkDegree(dir string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(dir)) {
	case "right", "0":
		return "0", true
	case "up_right", "upright", "45":
		return "45", true
	case "up", "90":
		return "90", true
	case "up_left", "upleft", "135":
		return "135", true
	case "left", "180":
		return "180", true
	case "down_left", "downleft", "225":
		return "225", true
	case "down", "270":
		return "270", true
	case "down_right", "downright", "315":
		return "315", true
	default:
		return "", false
	}
}

func directionToONVIFVector(dir string, speed int) (pan, tilt float64, ok bool) {
	spd := 0.5
	switch speed {
	case 1:
		spd = 0.25
	case 2:
		spd = 0.55
	case 3:
		spd = 0.9
	}
	switch strings.ToLower(strings.TrimSpace(dir)) {
	case "up":
		return 0, spd, true
	case "down":
		return 0, -spd, true
	case "left":
		return -spd, 0, true
	case "right":
		return spd, 0, true
	case "up_left", "upleft":
		return -spd * 0.75, spd * 0.75, true
	case "up_right", "upright":
		return spd * 0.75, spd * 0.75, true
	case "down_left", "downleft":
		return -spd * 0.75, -spd * 0.75, true
	case "down_right", "downright":
		return spd * 0.75, -spd * 0.75, true
	default:
		return 0, 0, false
	}
}

func parseTPLinkPresets(raw []byte) []config.PTZPreset {
	var out []config.PTZPreset

	// Case 0: Parallel arrays returned by {"method": "get", "preset": {"name": ["preset"]}}
	// e.g. {"preset": {"preset": {"id": ["1"], "name": ["钢琴"], "read_only": ["0"]}}}
	var parallel struct {
		Preset struct {
			Preset struct {
				ID   []string `json:"id"`
				Name []string `json:"name"`
			} `json:"preset"`
		} `json:"preset"`
	}
	if err := json.Unmarshal(raw, &parallel); err == nil && len(parallel.Preset.Preset.ID) > 0 {
		for i, rawID := range parallel.Preset.Preset.ID {
			id := strings.TrimSpace(rawID)
			rawName := ""
			if i < len(parallel.Preset.Preset.Name) {
				rawName = parallel.Preset.Preset.Name[i]
			}
			name, err := url.QueryUnescape(rawName)
			if err != nil || name == "" {
				name = rawName
			}
			name = strings.TrimSpace(name)
			if id == "" && name == "" {
				continue
			}
			if name == "" {
				name = "预置位 " + id
			}
			out = append(out, config.PTZPreset{
				ID:       id,
				Name:     name,
				HWToken:  id,
				Hardware: true,
			})
		}
		if len(out) > 0 {
			return out
		}
	}

	var wrapper struct {
		Preset struct {
			Preset []map[string]json.RawMessage `json:"preset"`
		} `json:"preset"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper.Preset.Preset) > 0 {
		for _, item := range wrapper.Preset.Preset {
			// Case A: [{"preset_1": {"id":"1","name":"...","enabled":"1"}}]
			var matched bool
			for k, v := range item {
				if strings.HasPrefix(k, "preset_") {
					var p struct {
						ID      string `json:"id"`
						Name    string `json:"name"`
						Enabled string `json:"enabled"`
					}
					if json.Unmarshal(v, &p) == nil && p.Enabled != "0" && (p.ID != "" || p.Name != "") {
						name, err := url.QueryUnescape(p.Name)
						if err != nil || name == "" {
							name = p.Name
						}
						id := strings.TrimSpace(p.ID)
						if id == "" {
							id = strings.TrimPrefix(k, "preset_")
						}
						out = append(out, config.PTZPreset{
							ID:       id,
							Name:     strings.TrimSpace(name),
							HWToken:  id,
							Hardware: true,
						})
						matched = true
					}
				}
			}
			if !matched {
				// Case B: [{"id":"1","name":"...","enabled":"1"}]
				b, _ := json.Marshal(item)
				var p struct {
					ID      string `json:"id"`
					Name    string `json:"name"`
					Enabled string `json:"enabled"`
				}
				if json.Unmarshal(b, &p) == nil && p.Enabled != "0" && (p.ID != "" || p.Name != "") {
					name, err := url.QueryUnescape(p.Name)
					if err != nil || name == "" {
						name = p.Name
					}
					out = append(out, config.PTZPreset{
						ID:       strings.TrimSpace(p.ID),
						Name:     strings.TrimSpace(name),
						HWToken:  strings.TrimSpace(p.ID),
						Hardware: true,
					})
				}
			}
		}
	}
	return out
}

func mergePresets(hwPresets []config.PTZPreset, saved []config.PTZPreset) []config.PTZPreset {
	if len(hwPresets) == 0 {
		return append([]config.PTZPreset(nil), saved...)
	}
	byID := make(map[string]config.PTZPreset, len(saved))
	byName := make(map[string]config.PTZPreset, len(saved))
	for _, s := range saved {
		byID[s.ID] = s
		if s.HWToken != "" {
			byID[s.HWToken] = s
		}
		if s.Name != "" {
			byName[s.Name] = s
		}
	}
	seenSaved := make(map[string]bool)
	merged := make([]config.PTZPreset, 0, len(hwPresets)+len(saved))
	for _, hw := range hwPresets {
		if sv, ok := byID[hw.ID]; ok {
			hw.Zoom = sv.Zoom
			hw.PanX = sv.PanX
			hw.PanY = sv.PanY
			if sv.Name != "" {
				hw.Name = sv.Name
			}
			seenSaved[sv.ID] = true
		} else if sv, ok := byName[hw.Name]; ok {
			hw.Zoom = sv.Zoom
			hw.PanX = sv.PanX
			hw.PanY = sv.PanY
			seenSaved[sv.ID] = true
		}
		merged = append(merged, hw)
	}
	for _, sv := range saved {
		if !seenSaved[sv.ID] {
			merged = append(merged, sv)
		}
	}
	return merged
}

func extractXMLTagLocal(xmlStr, tag string) string {
	for _, prefix := range []string{"tptz:", "trt:", "tt:", "tds:", ""} {
		openTag := "<" + prefix + tag
		closeTag := "</" + prefix + tag + ">"
		i := strings.Index(xmlStr, openTag)
		if i < 0 {
			continue
		}
		gt := strings.Index(xmlStr[i:], ">")
		if gt < 0 {
			continue
		}
		start := i + gt + 1
		end := strings.Index(xmlStr[start:], closeTag)
		if end >= 0 {
			return strings.TrimSpace(xmlStr[start : start+end])
		}
	}
	return ""
}

func extractXMLAttrLocal(xmlStr, tag, attr string) string {
	for _, prefix := range []string{"tptz:", "trt:", "tt:", "tds:", ""} {
		openTag := "<" + prefix + tag
		i := strings.Index(xmlStr, openTag)
		if i < 0 {
			continue
		}
		gt := strings.Index(xmlStr[i:], ">")
		if gt < 0 {
			continue
		}
		hdr := xmlStr[i : i+gt]
		key := attr + `="`
		kIdx := strings.Index(hdr, key)
		if kIdx < 0 {
			continue
		}
		rest := hdr[kIdx+len(key):]
		end := strings.IndexByte(rest, '"')
		if end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	return ""
}

func parseONVIFPresets(xmlStr string) []config.PTZPreset {
	var out []config.PTZPreset
	search := xmlStr
	for {
		idx := strings.Index(search, "Preset")
		if idx < 0 {
			break
		}
		// Ensure it's an opening tag like <tptz:Preset token="1"> or <Preset token="1">
		if idx == 0 || (search[idx-1] != '<' && search[idx-1] != ':') {
			search = search[idx+6:]
			continue
		}
		tagStart := strings.LastIndexByte(search[:idx], '<')
		if tagStart < 0 {
			search = search[idx+6:]
			continue
		}
		gt := strings.IndexByte(search[tagStart:], '>')
		if gt < 0 {
			break
		}
		header := search[tagStart : tagStart+gt+1]
		if strings.HasPrefix(header, "</") || strings.Contains(header, "GetPresetsResponse") {
			search = search[tagStart+gt+1:]
			continue
		}
		closeIdx := strings.Index(search[tagStart+gt+1:], "Preset>")
		if closeIdx < 0 {
			break
		}
		block := search[tagStart : tagStart+gt+1+closeIdx+7]
		token := extractXMLAttrLocal(block, "Preset", "token")
		name := extractXMLTagLocal(block, "Name")
		if token != "" {
			if name == "" {
				name = "预置位 " + token
			}
			out = append(out, config.PTZPreset{
				ID:       token,
				Name:     name,
				HWToken:  token,
				Hardware: true,
			})
		}
		search = search[tagStart+gt+1+closeIdx+7:]
	}
	return out
}

func (c *Controller) sendONVIFSOAP(endpoint, user, pass, bodyXML string) (string, int, error) {
	secHdr := ""
	if user != "" || pass != "" {
		if user == "" {
			user = "admin"
		}
		secHdr = "<s:Header>" + discovery.BuildWSUsernameToken(user, pass) + "</s:Header>"
	}
	envelope := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" ` +
		`xmlns:tds="http://www.onvif.org/ver10/device/wsdl" ` +
		`xmlns:trt="http://www.onvif.org/ver10/media/wsdl" ` +
		`xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl" ` +
		`xmlns:tt="http://www.onvif.org/ver10/schema">` +
		secHdr + `<s:Body>` + bodyXML + `</s:Body></s:Envelope>`

	req, err := http.NewRequest("POST", endpoint, strings.NewReader(envelope))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return string(raw), resp.StatusCode, nil
}

func (c *Controller) resolveONVIFPTZ(ip, user, pass string) (ptzURL, profileToken string, supportsPTZ bool) {
	key := c.cacheKey(ip, user, pass)
	c.mu.Lock()
	cached, ok := c.onvifCache[key]
	c.mu.Unlock()
	if ok && time.Now().Before(cached.expiresAt) {
		return cached.ptzURL, cached.profileToken, cached.supportsPTZ
	}

	ports := []string{"2020", "80", "8000", "8080"}
	for _, p := range ports {
		host := ip
		if p != "80" {
			host = ip + ":" + p
		}
		mediaEP := fmt.Sprintf("http://%s/onvif/media_service", host)
		devEP := fmt.Sprintf("http://%s/onvif/device_service", host)
		srvEP := fmt.Sprintf("http://%s/onvif/service", host)
		ptzEP := fmt.Sprintf("http://%s/onvif/ptz_service", host)

		// 1. Try GetProfiles on media_service, service, or device_service
		for _, ep := range []string{mediaEP, srvEP, devEP} {
			body, code, err := c.sendONVIFSOAP(ep, user, pass, `<trt:GetProfiles/>`)
			if err != nil {
				continue
			}
			if code == 200 && strings.Contains(body, "Profiles") {
				tok := extractXMLAttrLocal(body, "Profiles", "token")
				if tok == "" {
					tok = "profile_1"
				}
				hasPTZ := strings.Contains(body, "PTZConfiguration")
				activePTZEP := ep
				for _, candEP := range []string{ep, ptzEP, srvEP, devEP} {
					pBody, pCode, pErr := c.sendONVIFSOAP(candEP, user, pass, fmt.Sprintf(`<tptz:GetPresets><tptz:ProfileToken>%s</tptz:ProfileToken></tptz:GetPresets>`, tok))
					if pErr == nil && pCode == 200 && strings.Contains(pBody, "GetPresetsResponse") {
						hasPTZ = true
						activePTZEP = candEP
						break
					}
				}
				c.mu.Lock()
				c.onvifCache[key] = cachedONVIF{
					ptzURL:       activePTZEP,
					profileToken: tok,
					supportsPTZ:  hasPTZ,
					expiresAt:    time.Now().Add(5 * time.Minute),
				}
				c.mu.Unlock()
				return activePTZEP, tok, hasPTZ
			}
		}

		// 2. Try ptz_service and service directly with common profile tokens
		for _, candEP := range []string{ptzEP, srvEP} {
			for _, tok := range []string{"profile_1", "Profile_1", "MainStream", "Profile000"} {
				pBody, pCode, pErr := c.sendONVIFSOAP(candEP, user, pass, fmt.Sprintf(`<tptz:GetPresets><tptz:ProfileToken>%s</tptz:ProfileToken></tptz:GetPresets>`, tok))
				if pErr == nil && pCode == 200 && strings.Contains(pBody, "GetPresetsResponse") {
					c.mu.Lock()
					c.onvifCache[key] = cachedONVIF{
						ptzURL:       candEP,
						profileToken: tok,
						supportsPTZ:  true,
						expiresAt:    time.Now().Add(5 * time.Minute),
					}
					c.mu.Unlock()
					return candEP, tok, true
				}
			}
		}
	}

	c.mu.Lock()
	c.onvifCache[key] = cachedONVIF{
		ptzURL:       "",
		profileToken: "profile_1",
		supportsPTZ:  false,
		expiresAt:    time.Now().Add(2 * time.Minute),
	}
	c.mu.Unlock()
	return "", "profile_1", false
}

func motorDeltaDegrees(dir string, deg int) (string, string) {
	diag := deg * 3 / 4
	if diag < 1 {
		diag = 1
	}
	switch strings.ToLower(strings.TrimSpace(dir)) {
	case "up":
		return "0", strconv.Itoa(deg)
	case "down":
		return "0", strconv.Itoa(-deg)
	case "left":
		return strconv.Itoa(-deg), "0"
	case "right":
		return strconv.Itoa(deg), "0"
	case "up_left", "upleft":
		return strconv.Itoa(-diag), strconv.Itoa(diag)
	case "up_right", "upright":
		return strconv.Itoa(diag), strconv.Itoa(diag)
	case "down_left", "downleft":
		return strconv.Itoa(-diag), strconv.Itoa(-diag)
	case "down_right", "downright":
		return strconv.Itoa(diag), strconv.Itoa(-diag)
	default:
		return "0", "0"
	}
}

// ProbeCapabilities inspects whether a camera supports hardware PTZ (TP-LINK motor/ptz API or ONVIF PTZ)
// and fetches its hardware preset list, merging with saved presets from cameras.json.
func (c *Controller) ProbeCapabilities(cam config.Camera) CapabilityResult {
	ip := strings.TrimSpace(cam.IP)
	user := strings.TrimSpace(cam.Username)
	if user == "" {
		user = "admin"
	}
	pass := cam.Password
	modelLabel := strings.TrimSpace(cam.Model)

	// 1. Try TP-LINK / Mercury HTTP /ds motor, ptz & preset inquiry
	if rawInfo, codeInfo, httpOpenInfo, errInfo := c.postTPLinkDS(ip, user, pass, map[string]any{
		"method":      "get",
		"device_info": map[string]any{"name": []string{"info"}},
	}); errInfo == nil && httpOpenInfo && codeInfo == 0 {
		var di struct {
			DeviceInfo struct {
				Info struct {
					DeviceModel string `json:"device_model"`
				} `json:"info"`
			} `json:"device_info"`
		}
		if json.Unmarshal(rawInfo, &di) == nil && di.DeviceInfo.Info.DeviceModel != "" {
			if unesc, err := url.QueryUnescape(di.DeviceInfo.Info.DeviceModel); err == nil && unesc != "" {
				modelLabel = strings.TrimSpace(unesc)
			} else {
				modelLabel = strings.TrimSpace(di.DeviceInfo.Info.DeviceModel)
			}
		}
	}

	rawMotor, codeMotor, httpOpen, errMotor := c.postTPLinkDS(ip, user, pass, map[string]any{
		"method": "get",
		"motor":  map[string]any{"name": []string{"capability"}},
	})
	if errMotor == nil && httpOpen {
		hasMotor := codeMotor == 0 && bytes.Contains(rawMotor, []byte(`"motor"`))
		if hasMotor {
			c.setTPModule(ip, "motor")
		} else {
			// Newer TP-LINK firmware (e.g. TL-IPC42A-4 6.0) exposes PTZ under module "ptz" section "capability"
			rawPTZ, codePTZ, _, errPTZ := c.postTPLinkDS(ip, user, pass, map[string]any{
				"method": "get",
				"ptz":    map[string]any{"name": []string{"capability"}},
			})
			if errPTZ == nil && codePTZ == 0 && bytes.Contains(rawPTZ, []byte(`"ptz"`)) {
				hasMotor = true
				c.setTPModule(ip, "ptz")
			}
		}

		// Query configured presets via {"name": ["preset"]} first (supported by both 3.0/5.0 and 6.0),
		// falling back to {"table": ["preset"]}.
		var hwPresets []config.PTZPreset
		if rawPreset, codePreset, _, errP := c.postTPLinkDS(ip, user, pass, map[string]any{
			"method": "get",
			"preset": map[string]any{"name": []string{"preset"}},
		}); errP == nil && codePreset == 0 {
			hwPresets = parseTPLinkPresets(rawPreset)
		} else if rawPresetTbl, codePresetTbl, _, errPT := c.postTPLinkDS(ip, user, pass, map[string]any{
			"method": "get",
			"preset": map[string]any{"table": []string{"preset"}},
		}); errPT == nil && codePresetTbl == 0 {
			hwPresets = parseTPLinkPresets(rawPresetTbl)
		}

		// Query hardware mounting / image flip status ("off" = 正装, "center" = 倒装 180°)
		flipType := c.probeTPFlip(ip, user, pass)
		if flipType == "" {
			flipType = strings.TrimSpace(cam.FlipType)
		}
		if flipType == "" {
			flipType = "off"
		}
		invX, invY := ResolveEffectiveInvert(cam, flipType)
		mountLabel := "正装"
		if flipType == "center" {
			mountLabel = "倒装 180°翻转"
		} else if flipType == "left_right" {
			mountLabel = "左右镜像"
		} else if flipType == "up_down" {
			mountLabel = "上下翻转"
		}

		if hasMotor || len(hwPresets) > 0 || config.InferSupportsPTZFromModel(modelLabel) {
			return CapabilityResult{
				SupportsPTZ: true,
				Protocol:    "tplink",
				Model:       modelLabel,
				FlipType:    flipType,
				InvertX:     invX,
				InvertY:     invY,
				Presets:     mergePresets(hwPresets, cam.PTZPresets),
				Message:     fmt.Sprintf("已连接硬件云台 (%s · %s)", modelLabel, mountLabel),
			}
		}
		// Check if the camera also exposes ONVIF PTZ on port 2020
		if ptzURL, profTok, ok := c.resolveONVIFPTZ(ip, user, pass); ok && ptzURL != "" {
			pBody, pCode, _ := c.sendONVIFSOAP(ptzURL, user, pass, fmt.Sprintf(`<tptz:GetPresets><tptz:ProfileToken>%s</tptz:ProfileToken></tptz:GetPresets>`, profTok))
			if pCode == 200 {
				hwPresets = parseONVIFPresets(pBody)
			}
			return CapabilityResult{
				SupportsPTZ: true,
				Protocol:    "onvif",
				Model:       modelLabel,
				FlipType:    flipType,
				InvertX:     invX,
				InvertY:     invY,
				Presets:     mergePresets(hwPresets, cam.PTZPresets),
				Message:     "已连接 ONVIF 硬件云台控制接口",
			}
		}
		if modelLabel == "" {
			modelLabel = "定焦机型"
		}
		return CapabilityResult{
			SupportsPTZ: false,
			Protocol:    "none",
			Model:       modelLabel,
			FlipType:    flipType,
			InvertX:     invX,
			InvertY:     invY,
			Presets:     mergePresets(nil, cam.PTZPresets),
			Message:     fmt.Sprintf("当前设备 (%s) 未检测到内置物理云台电机", modelLabel),
		}
	}

	// 2. Try standard ONVIF PTZ service
	flipFallback := strings.TrimSpace(cam.FlipType)
	if flipFallback == "" {
		flipFallback = "off"
	}
	invXFallback, invYFallback := ResolveEffectiveInvert(cam, flipFallback)
	if ptzURL, profTok, ok := c.resolveONVIFPTZ(ip, user, pass); ok && ptzURL != "" {
		var hwPresets []config.PTZPreset
		pBody, pCode, _ := c.sendONVIFSOAP(ptzURL, user, pass, fmt.Sprintf(`<tptz:GetPresets><tptz:ProfileToken>%s</tptz:ProfileToken></tptz:GetPresets>`, profTok))
		if pCode == 200 {
			hwPresets = parseONVIFPresets(pBody)
		}
		return CapabilityResult{
			SupportsPTZ: true,
			Protocol:    "onvif",
			Model:       modelLabel,
			FlipType:    flipFallback,
			InvertX:     invXFallback,
			InvertY:     invYFallback,
			Presets:     mergePresets(hwPresets, cam.PTZPresets),
			Message:     "已连接 ONVIF 硬件云台控制接口",
		}
	}

	return CapabilityResult{
		SupportsPTZ: cam.SupportsPTZ || config.InferSupportsPTZFromModel(modelLabel),
		Protocol:    "tplink",
		Model:       modelLabel,
		FlipType:    flipFallback,
		InvertX:     invXFallback,
		InvertY:     invYFallback,
		Presets:     mergePresets(nil, cam.PTZPresets),
		Message:     "物理云台就绪 · 点击方向键旋转机身",
	}
}

type tpCommandCandidate struct {
	mod     string
	payload map[string]any
}

func orderTPCandidates(preferredMod string, list []tpCommandCandidate) []tpCommandCandidate {
	if preferredMod == "" {
		return list
	}
	out := make([]tpCommandCandidate, 0, len(list))
	for _, item := range list {
		if item.mod == preferredMod {
			out = append(out, item)
		}
	}
	for _, item := range list {
		if item.mod != preferredMod {
			out = append(out, item)
		}
	}
	return out
}

// Execute runs a PTZ command (move, step, stop, calibrate, zoom, set_flip, or preset operation)
// against TP-LINK /ds or ONVIF PTZ service, falling back gracefully when the camera is fixed-lens.
func (c *Controller) Execute(cam config.Camera, req CommandRequest) ExecuteResult {
	ip := strings.TrimSpace(cam.IP)
	user := strings.TrimSpace(cam.Username)
	if user == "" {
		user = "admin"
	}
	pass := cam.Password
	action := strings.ToLower(strings.TrimSpace(req.Action))
	speed := req.Speed
	if speed < 1 || speed > 3 {
		speed = 2
	}
	speedStr := strconv.Itoa(speed)
	preferredMod := c.getTPModule(ip)

	flipType := c.getTPFlip(ip)
	if flipType == "" {
		flipType = strings.TrimSpace(cam.FlipType)
	}
	if flipType == "" && (action == "move" || action == "step") {
		flipType = c.probeTPFlip(ip, user, pass)
	}
	invX, invY := ResolveEffectiveInvert(cam, flipType)

	// 1. Attempt TP-LINK / Mercury HTTP /ds first if applicable
	if cam.IsTPLink() || cam.Vendor == "" || cam.Vendor == "tapo" {
		switch action {
		case "set_flip":
			targetFlip := strings.ToLower(strings.TrimSpace(req.FlipType))
			switch targetFlip {
			case "off", "center", "left_right", "up_down":
			default:
				targetFlip = "off"
			}
			_, code, httpOpen, err := c.postTPLinkDS(ip, user, pass, map[string]any{
				"method": "set",
				"image": map[string]any{
					"switch": map[string]any{"flip_type": targetFlip},
				},
			})
			if err == nil && httpOpen && code == 0 {
				c.setTPFlip(ip, targetFlip)
				mountMsg := "已切换为正装模式 (标准画面与正装方向)"
				if targetFlip == "center" {
					mountMsg = "已切换为倒装模式 (画面 180° 翻转且云台方向自动反向)"
				}
				return ExecuteResult{
					OK:               true,
					HardwareExecuted: true,
					SupportsPTZ:      true,
					Protocol:         "tplink",
					FlipType:         targetFlip,
					Message:          mountMsg,
				}
			}
			return ExecuteResult{
				OK:               false,
				HardwareExecuted: false,
				SupportsPTZ:      cam.SupportsPTZ,
				Protocol:         "tplink",
				Error:            "切换摄像头正装/倒装模式失败，请检查设备连接",
			}

		case "move":
			effDir := transformDirection(req.Direction, invX, invY)
			deg, ok := directionToTPLinkDegree(effDir)
			if !ok {
				return ExecuteResult{OK: false, Error: "无效的云台方向参数: " + req.Direction}
			}
			panVec, tiltVec, _ := directionToONVIFVector(effDir, speed)
			// Continuous hold rotation on motor firmwares: rotate up to 90 degrees until stop is sent on release
			holdX, holdY := motorDeltaDegrees(effDir, 90)
			contMove := map[string]any{}
			if panVec != 0 {
				contMove["velocity_pan"] = fmt.Sprintf("%.6f", panVec)
			}
			if tiltVec != 0 {
				contMove["velocity_tilt"] = fmt.Sprintf("%.6f", tiltVec)
			}

			candidates := orderTPCandidates(preferredMod, []tpCommandCandidate{
				{
					mod: "motor",
					payload: map[string]any{
						"method": "do",
						"motor": map[string]any{
							"move": map[string]any{"x_coord": holdX, "y_coord": holdY},
						},
					},
				},
				{
					mod: "ptz",
					payload: map[string]any{
						"method": "do",
						"ptz": map[string]any{
							"continuous_move": contMove,
						},
					},
				},
				{
					mod: "motor",
					payload: map[string]any{
						"method": "do",
						"motor": map[string]any{
							"move": map[string]any{"direction": deg, "speed": speedStr},
						},
					},
				},
			})

			var httpOpen bool
			for _, cand := range candidates {
				_, code, hOpen, err := c.postTPLinkDS(ip, user, pass, cand.payload)
				if hOpen {
					httpOpen = true
				}
				if err == nil && code == 0 {
					c.setTPModule(ip, cand.mod)
					return ExecuteResult{
						OK:               true,
						HardwareExecuted: true,
						SupportsPTZ:      true,
						Protocol:         "tplink",
						FlipType:         flipType,
						InvertX:          invX,
						InvertY:          invY,
						Message:          "物理云台转动中",
					}
				}
				if !hOpen {
					break
				}
			}
			if httpOpen {
				onvifReq := req
				onvifReq.Direction = effDir
				if res, ok := c.executeONVIF(ip, user, pass, onvifReq); ok {
					return res
				}
				return ExecuteResult{
					OK:               false,
					HardwareExecuted: false,
					SupportsPTZ:      c.getTPModule(ip) != "" || cam.SupportsPTZ,
					Protocol:         "tplink",
					Error:            "云台电机暂未响应或已到达极限旋转角度",
				}
			}

		case "step":
			effDir := transformDirection(req.Direction, invX, invY)
			deg, ok := directionToTPLinkDegree(effDir)
			if !ok {
				return ExecuteResult{OK: false, Error: "无效的云台方向参数: " + req.Direction}
			}
			// Step angle for motor firmwares scaled by speed (1: 8°, 2: 15°, 3: 24°)
			stepDeg := 15
			switch speed {
			case 1:
				stepDeg = 8
			case 2:
				stepDeg = 15
			case 3:
				stepDeg = 24
			}
			stepX, stepY := motorDeltaDegrees(effDir, stepDeg)

			// Step translation for ptz (hw 6.0) firmwares in [-1.0, 1.0]
			panVec, tiltVec, _ := directionToONVIFVector(effDir, speed)
			stepPan := panVec * 0.22
			stepTilt := tiltVec * 0.22
			relMove := map[string]any{}
			if stepPan != 0 {
				relMove["translation_pan"] = fmt.Sprintf("%.6f", stepPan)
			}
			if stepTilt != 0 {
				relMove["translation_tilt"] = fmt.Sprintf("%.6f", stepTilt)
			}

			candidates := orderTPCandidates(preferredMod, []tpCommandCandidate{
				{
					mod: "motor",
					payload: map[string]any{
						"method": "do",
						"motor": map[string]any{
							"move": map[string]any{"x_coord": stepX, "y_coord": stepY},
						},
					},
				},
				{
					mod: "motor",
					payload: map[string]any{
						"method": "do",
						"motor": map[string]any{
							"movestep": map[string]any{"direction": deg},
						},
					},
				},
				{
					mod: "ptz",
					payload: map[string]any{
						"method": "do",
						"ptz": map[string]any{
							"relative_move": relMove,
						},
					},
				},
			})

			var httpOpen bool
			for _, cand := range candidates {
				_, code, hOpen, err := c.postTPLinkDS(ip, user, pass, cand.payload)
				if hOpen {
					httpOpen = true
				}
				if err == nil && code == 0 {
					c.setTPModule(ip, cand.mod)
					return ExecuteResult{
						OK:               true,
						HardwareExecuted: true,
						SupportsPTZ:      true,
						Protocol:         "tplink",
						FlipType:         flipType,
						InvertX:          invX,
						InvertY:          invY,
						Message:          "已旋转物理云台角度",
					}
				}
				if !hOpen {
					break
				}
			}
			if httpOpen {
				onvifReq := req
				onvifReq.Direction = effDir
				if res, ok := c.executeONVIF(ip, user, pass, onvifReq); ok {
					return res
				}
				return ExecuteResult{
					OK:               false,
					HardwareExecuted: false,
					SupportsPTZ:      c.getTPModule(ip) != "" || cam.SupportsPTZ,
					Protocol:         "tplink",
					Error:            "云台电机暂未响应或已到达极限旋转角度",
				}
			}

		case "stop", "zoom_stop":
			candidates := orderTPCandidates(preferredMod, []tpCommandCandidate{
				{
					mod: "motor",
					payload: map[string]any{
						"method": "do",
						"motor":  map[string]any{"stop": map[string]any{}},
					},
				},
				{
					mod: "ptz",
					payload: map[string]any{
						"method": "do",
						"ptz": map[string]any{
							"stop": map[string]any{"pan": "1", "tilt": "1", "zoom": "1"},
						},
					},
				},
			})
			var httpOpen bool
			for _, cand := range candidates {
				_, code, hOpen, err := c.postTPLinkDS(ip, user, pass, cand.payload)
				if hOpen {
					httpOpen = true
				}
				if err == nil && code == 0 {
					c.setTPModule(ip, cand.mod)
					return ExecuteResult{
						OK:               true,
						HardwareExecuted: true,
						SupportsPTZ:      true,
						Protocol:         "tplink",
					}
				}
				if !hOpen {
					break
				}
			}
			if httpOpen {
				if res, ok := c.executeONVIF(ip, user, pass, req); ok {
					return res
				}
				return ExecuteResult{
					OK:               true,
					HardwareExecuted: false,
					SupportsPTZ:      false,
					Protocol:         "digital",
				}
			}

		case "calibrate", "home":
			candidates := orderTPCandidates(preferredMod, []tpCommandCandidate{
				{
					mod: "motor",
					payload: map[string]any{
						"method": "do",
						"motor":  map[string]any{"manual_cali": map[string]any{}},
					},
				},
				{
					mod: "ptz",
					payload: map[string]any{
						"method": "do",
						"ptz":    map[string]any{"calibrate": map[string]any{}},
					},
				},
				{
					mod: "ptz",
					payload: map[string]any{
						"method": "do",
						"ptz": map[string]any{
							"absolute_move": map[string]any{
								"position_pan":  "0.000000",
								"position_tilt": "0.000000",
							},
						},
					},
				},
			})
			var httpOpen bool
			for _, cand := range candidates {
				_, code, hOpen, err := c.postTPLinkDS(ip, user, pass, cand.payload)
				if hOpen {
					httpOpen = true
				}
				if err == nil && code == 0 {
					c.setTPModule(ip, cand.mod)
					return ExecuteResult{
						OK:               true,
						HardwareExecuted: true,
						SupportsPTZ:      true,
						Protocol:         "tplink",
						Message:          "已触发物理云台自检校准复位",
					}
				}
				if !hOpen {
					break
				}
			}
			if httpOpen {
				if res, ok := c.executeONVIF(ip, user, pass, req); ok {
					return res
				}
				return ExecuteResult{
					OK:               true,
					HardwareExecuted: false,
					SupportsPTZ:      false,
					Protocol:         "digital",
					Message:          "已复位 1080P 电子云台全景画面",
				}
			}

		case "zoom_in", "zoom_out":
			zDir := "in"
			zVel := "0.500000"
			if action == "zoom_out" {
				zDir = "out"
				zVel = "-0.500000"
			}
			var httpOpen bool
			for _, payload := range []map[string]any{
				{
					"method": "do",
					"ptz": map[string]any{
						"continuous_move": map[string]any{"velocity_zoom": zVel},
					},
				},
				{
					"method": "do",
					"ptz": map[string]any{
						"zoom": map[string]any{"direction": zDir},
					},
				},
			} {
				_, code, hOpen, err := c.postTPLinkDS(ip, user, pass, payload)
				if hOpen {
					httpOpen = true
				}
				if err == nil && code == 0 {
					return ExecuteResult{
						OK:               true,
						HardwareExecuted: true,
						SupportsPTZ:      true,
						Protocol:         "tplink",
						Message:          "光学变倍调整中",
					}
				}
				if !hOpen {
					break
				}
			}
			if httpOpen {
				if res, ok := c.executeONVIF(ip, user, pass, req); ok {
					return res
				}
				return ExecuteResult{
					OK:               true,
					HardwareExecuted: false,
					SupportsPTZ:      c.getTPModule(ip) != "" || cam.SupportsPTZ,
					Protocol:         "digital",
					Message:          "已调整 1080P 电子变倍画面",
				}
			}

		case "preset_set":
			name := strings.TrimSpace(req.PresetName)
			if name == "" {
				name = "预置位"
			}
			setPayload := map[string]any{
				"method": "do",
				"preset": map[string]any{
					"set_preset": map[string]any{
						"name":     name,
						"save_ptz": "1",
					},
				},
			}
			raw, code, httpOpen, err := c.postTPLinkDS(ip, user, pass, setPayload)
			if httpOpen && (err != nil || code != 0) {
				// If stepper motor is still decelerating after a recent step/move, stop & retry once
				_, _, _, _ = c.postTPLinkDS(ip, user, pass, map[string]any{
					"method": "do",
					"motor":  map[string]any{"stop": map[string]any{}},
				})
				time.Sleep(350 * time.Millisecond)
				raw, code, httpOpen, err = c.postTPLinkDS(ip, user, pass, setPayload)
			}
			if err == nil && code == 0 {
				var resp struct {
					ID     string `json:"id"`
					Preset struct {
						SetPreset struct {
							ID string `json:"id"`
						} `json:"set_preset"`
					} `json:"preset"`
				}
				_ = json.Unmarshal(raw, &resp)
				hwToken := strings.TrimSpace(resp.ID)
				if hwToken == "" {
					hwToken = strings.TrimSpace(resp.Preset.SetPreset.ID)
				}
				return ExecuteResult{
					OK:               true,
					HardwareExecuted: true,
					SupportsPTZ:      true,
					Protocol:         "tplink",
					CreatedHWToken:   hwToken,
					Message:          fmt.Sprintf("已保存云台预置位「%s」", name),
				}
			}
			if httpOpen {
				if res, ok := c.executeONVIF(ip, user, pass, req); ok {
					return res
				}
				return ExecuteResult{
					OK:               true,
					HardwareExecuted: false,
					SupportsPTZ:      c.getTPModule(ip) != "" || cam.SupportsPTZ,
					Protocol:         "digital",
					Message:          fmt.Sprintf("已保存当前画面视角预置位「%s」", name),
				}
			}

		case "preset_goto":
			pid := strings.TrimSpace(req.PresetID)
			gotoPayload := map[string]any{
				"method": "do",
				"preset": map[string]any{
					"goto_preset": map[string]any{"id": pid},
				},
			}
			_, code, httpOpen, err := c.postTPLinkDS(ip, user, pass, gotoPayload)
			if httpOpen && (err != nil || code != 0) {
				_, _, _, _ = c.postTPLinkDS(ip, user, pass, map[string]any{
					"method": "do",
					"motor":  map[string]any{"stop": map[string]any{}},
				})
				time.Sleep(250 * time.Millisecond)
				_, code, httpOpen, err = c.postTPLinkDS(ip, user, pass, gotoPayload)
			}
			if err == nil && code == 0 {
				return ExecuteResult{
					OK:               true,
					HardwareExecuted: true,
					SupportsPTZ:      true,
					Protocol:         "tplink",
					Message:          "正在转到预置位",
				}
			}
			if httpOpen {
				if res, ok := c.executeONVIF(ip, user, pass, req); ok {
					return res
				}
				return ExecuteResult{
					OK:               true,
					HardwareExecuted: false,
					SupportsPTZ:      c.getTPModule(ip) != "" || cam.SupportsPTZ,
					Protocol:         "digital",
					Message:          "已切换至预置画面视角",
				}
			}

		case "preset_delete":
			pid := strings.TrimSpace(req.PresetID)
			_, code, httpOpen, err := c.postTPLinkDS(ip, user, pass, map[string]any{
				"method": "do",
				"preset": map[string]any{
					"remove_preset": map[string]any{"id": []string{pid}},
				},
			})
			if err == nil && code == 0 {
				return ExecuteResult{
					OK:               true,
					HardwareExecuted: true,
					SupportsPTZ:      true,
					Protocol:         "tplink",
					Message:          "已删除预置位",
				}
			}
			if httpOpen {
				if res, ok := c.executeONVIF(ip, user, pass, req); ok {
					return res
				}
				return ExecuteResult{
					OK:               true,
					HardwareExecuted: false,
					SupportsPTZ:      false,
					Protocol:         "digital",
					Message:          "已删除画面预置位",
				}
			}
		}
	}

	// 2. Try standard ONVIF PTZ service
	if res, ok := c.executeONVIF(ip, user, pass, req); ok {
		return res
	}

	// 3. Fallback when camera HTTP / ONVIF is unreachable
	return ExecuteResult{
		OK:               false,
		HardwareExecuted: false,
		SupportsPTZ:      false,
		Protocol:         "none",
		Error:            "无法连接摄像头云台控制接口，请检查设备网络或密码",
	}
}

func (c *Controller) executeONVIF(ip, user, pass string, req CommandRequest) (ExecuteResult, bool) {
	ptzURL, profTok, supports := c.resolveONVIFPTZ(ip, user, pass)
	if !supports || ptzURL == "" {
		return ExecuteResult{}, false
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	speed := req.Speed
	if speed < 1 || speed > 3 {
		speed = 2
	}

	switch action {
	case "move":
		pan, tilt, ok := directionToONVIFVector(req.Direction, speed)
		if !ok {
			return ExecuteResult{OK: false, Error: "无效的云台方向: " + req.Direction}, true
		}
		soap := fmt.Sprintf(
			`<tptz:ContinuousMove><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:Velocity><tt:PanTilt x="%.2f" y="%.2f"/></tptz:Velocity></tptz:ContinuousMove>`,
			profTok, pan, tilt,
		)
		_, code, err := c.sendONVIFSOAP(ptzURL, user, pass, soap)
		if err == nil && code == 200 {
			return ExecuteResult{
				OK:               true,
				HardwareExecuted: true,
				SupportsPTZ:      true,
				Protocol:         "onvif",
				Message:          "ONVIF 云台转动中",
			}, true
		}

	case "step":
		pan, tilt, ok := directionToONVIFVector(req.Direction, 1)
		if !ok {
			return ExecuteResult{OK: false, Error: "无效的云台方向: " + req.Direction}, true
		}
		// Try RelativeMove first
		stepX := pan * 0.25
		stepY := tilt * 0.25
		soapRel := fmt.Sprintf(
			`<tptz:RelativeMove><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:Translation><tt:PanTilt x="%.3f" y="%.3f"/></tptz:Translation></tptz:RelativeMove>`,
			profTok, stepX, stepY,
		)
		_, code, err := c.sendONVIFSOAP(ptzURL, user, pass, soapRel)
		if err == nil && code == 200 {
			return ExecuteResult{
				OK:               true,
				HardwareExecuted: true,
				SupportsPTZ:      true,
				Protocol:         "onvif",
				Message:          "已微调云台角度",
			}, true
		}
		// Fallback: ContinuousMove + Stop after 220ms
		soapMove := fmt.Sprintf(
			`<tptz:ContinuousMove><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:Velocity><tt:PanTilt x="%.2f" y="%.2f"/></tptz:Velocity></tptz:ContinuousMove>`,
			profTok, pan, tilt,
		)
		_, code2, err2 := c.sendONVIFSOAP(ptzURL, user, pass, soapMove)
		if err2 == nil && code2 == 200 {
			go func() {
				time.Sleep(220 * time.Millisecond)
				_, _, _ = c.sendONVIFSOAP(
					ptzURL,
					user,
					pass,
					fmt.Sprintf(`<tptz:Stop><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:PanTilt>true</tptz:PanTilt><tptz:Zoom>true</tptz:Zoom></tptz:Stop>`, profTok),
				)
			}()
			return ExecuteResult{
				OK:               true,
				HardwareExecuted: true,
				SupportsPTZ:      true,
				Protocol:         "onvif",
				Message:          "已微调云台角度",
			}, true
		}

	case "stop", "zoom_stop":
		soap := fmt.Sprintf(
			`<tptz:Stop><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:PanTilt>true</tptz:PanTilt><tptz:Zoom>true</tptz:Zoom></tptz:Stop>`,
			profTok,
		)
		_, code, err := c.sendONVIFSOAP(ptzURL, user, pass, soap)
		if err == nil && code == 200 {
			return ExecuteResult{
				OK:               true,
				HardwareExecuted: true,
				SupportsPTZ:      true,
				Protocol:         "onvif",
			}, true
		}

	case "calibrate", "home":
		soap := fmt.Sprintf(`<tptz:GotoHomePosition><tptz:ProfileToken>%s</tptz:ProfileToken></tptz:GotoHomePosition>`, profTok)
		_, code, err := c.sendONVIFSOAP(ptzURL, user, pass, soap)
		if err == nil && code == 200 {
			return ExecuteResult{
				OK:               true,
				HardwareExecuted: true,
				SupportsPTZ:      true,
				Protocol:         "onvif",
				Message:          "正在返回云台初始位置",
			}, true
		}

	case "zoom_in", "zoom_out":
		z := 0.5
		if action == "zoom_out" {
			z = -0.5
		}
		soap := fmt.Sprintf(
			`<tptz:ContinuousMove><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:Velocity><tt:Zoom x="%.2f"/></tptz:Velocity></tptz:ContinuousMove>`,
			profTok, z,
		)
		_, code, err := c.sendONVIFSOAP(ptzURL, user, pass, soap)
		if err == nil && code == 200 {
			return ExecuteResult{
				OK:               true,
				HardwareExecuted: true,
				SupportsPTZ:      true,
				Protocol:         "onvif",
				Message:          "光学变倍调整中",
			}, true
		}

	case "preset_set":
		name := strings.TrimSpace(req.PresetName)
		if name == "" {
			name = "Preset"
		}
		soap := fmt.Sprintf(
			`<tptz:SetPreset><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:PresetName>%s</tptz:PresetName></tptz:SetPreset>`,
			profTok, name,
		)
		body, code, err := c.sendONVIFSOAP(ptzURL, user, pass, soap)
		if err == nil && code == 200 {
			tok := extractXMLTagLocal(body, "PresetToken")
			return ExecuteResult{
				OK:               true,
				HardwareExecuted: true,
				SupportsPTZ:      true,
				Protocol:         "onvif",
				CreatedHWToken:   tok,
				Message:          fmt.Sprintf("已保存云台预置位「%s」", name),
			}, true
		}

	case "preset_goto":
		soap := fmt.Sprintf(
			`<tptz:GotoPreset><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:PresetToken>%s</tptz:PresetToken></tptz:GotoPreset>`,
			profTok, strings.TrimSpace(req.PresetID),
		)
		_, code, err := c.sendONVIFSOAP(ptzURL, user, pass, soap)
		if err == nil && code == 200 {
			return ExecuteResult{
				OK:               true,
				HardwareExecuted: true,
				SupportsPTZ:      true,
				Protocol:         "onvif",
				Message:          "正在转到预置位",
			}, true
		}

	case "preset_delete":
		soap := fmt.Sprintf(
			`<tptz:RemovePreset><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:PresetToken>%s</tptz:PresetToken></tptz:RemovePreset>`,
			profTok, strings.TrimSpace(req.PresetID),
		)
		_, code, err := c.sendONVIFSOAP(ptzURL, user, pass, soap)
		if err == nil && code == 200 {
			return ExecuteResult{
				OK:               true,
				HardwareExecuted: true,
				SupportsPTZ:      true,
				Protocol:         "onvif",
				Message:          "已删除预置位",
			}, true
		}
	}

	return ExecuteResult{}, false
}
