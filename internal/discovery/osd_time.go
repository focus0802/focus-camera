package discovery

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"focus-camera/internal/config"
)

type CameraOSDConfig struct {
	LabelEnabled   bool   `json:"label_enabled"`
	LabelText      string `json:"label_text"`
	LabelX         string `json:"label_x,omitempty"`
	LabelY         string `json:"label_y,omitempty"`
	Label2Enabled  bool   `json:"label2_enabled"`
	Label2Text     string `json:"label2_text"`
	Label2X        string `json:"label2_x,omitempty"`
	Label2Y        string `json:"label2_y,omitempty"`
	DateEnabled    bool   `json:"date_enabled"`
	DateX          string `json:"date_x,omitempty"`
	DateY          string `json:"date_y,omitempty"`
	WeekEnabled    bool   `json:"week_enabled"`
	PositionPreset string `json:"position_preset"` // "top_left" | "bottom_left" | "top_right" | "bottom_right" | "custom"
	DevAlias       string `json:"dev_alias,omitempty"`
}

type CameraTimeStatus struct {
	CameraTime   string `json:"camera_time"`
	CameraUnix   int64  `json:"camera_unix"`
	HostTime     string `json:"host_time"`
	HostUnix     int64  `json:"host_unix"`
	DriftSeconds int64  `json:"drift_seconds"`
	Timezone     string `json:"timezone"`
	TimingMode   string `json:"timing_mode"` // "ntp" | "manual"
	NTPServer    string `json:"ntp_server,omitempty"`
}

type CameraOSDTimePayload struct {
	Protocol string           `json:"protocol"` // "tplink" | "onvif"
	OSD      CameraOSDConfig  `json:"osd"`
	Time     CameraTimeStatus `json:"time"`
}

type SetOSDRequest struct {
	LabelEnabled  *bool   `json:"label_enabled,omitempty"`
	LabelText     *string `json:"label_text,omitempty"`
	Label2Enabled *bool   `json:"label2_enabled,omitempty"`
	Label2Text    *string `json:"label2_text,omitempty"`
	DateEnabled   *bool   `json:"date_enabled,omitempty"`
	WeekEnabled   *bool   `json:"week_enabled,omitempty"`
	Position      string  `json:"position,omitempty"` // "keep" | "top_left" | "bottom_left" | "top_right" | "bottom_right"
	SyncDevAlias  *bool   `json:"sync_dev_alias,omitempty"`
}

type BatchItemResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IP      string `json:"ip"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Error   string `json:"error,omitempty"`
}

type BatchMaintResult struct {
	Action    string            `json:"action"`
	Total     int               `json:"total"`
	Succeeded int               `json:"succeeded"`
	Failed    int               `json:"failed"`
	Items     []BatchItemResult `json:"items"`
}

type tpOSDLabelEntry struct {
	Enabled string `json:"enabled"`
	Text    string `json:"text"`
	XCoor   string `json:"x_coor"`
	YCoor   string `json:"y_coor"`
}

var (
	camHTTPMuMap sync.Map // map[string]*sync.Mutex
)

func lockCameraHTTP(ip string) func() {
	key := strings.TrimSpace(ip)
	if key == "" {
		return func() {}
	}
	v, _ := camHTTPMuMap.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func isFactoryPlaceholderOSD2(text string, enabled bool) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return true
	}
	// TP-LINK cameras ship with label_info_2 enabled="off" and text="自定义字符1"
	if !enabled {
		return true
	}
	switch t {
	case "自定义字符1", "自定义字符2", "自定义字符3", "Custom String 1", "Custom String 2":
		return true
	}
	return false
}

func inferOSDPositionPreset(xStr, yStr string) string {
	x, errX := strconv.Atoi(strings.TrimSpace(xStr))
	y, errY := strconv.Atoi(strings.TrimSpace(yStr))
	if errX != nil || errY != nil {
		return "bottom_left"
	}
	isRight := x >= 5000
	isBottom := y >= 5000
	switch {
	case !isRight && !isBottom:
		return "top_left"
	case !isRight && isBottom:
		return "bottom_left"
	case isRight && !isBottom:
		return "top_right"
	default:
		return "bottom_right"
	}
}

// EstimateTPLinkOSDWidth estimates the horizontal width of a text string in TP-LINK's 0..10000 OSD coordinate space.
// Calibrated against 1080P hardware streams: ASCII runes are ~170 units wide and CJK/wide runes are ~305 units wide.
func EstimateTPLinkOSDWidth(text string) int {
	t := strings.TrimSpace(text)
	if t == "" {
		return 1200
	}
	width := 0
	for _, r := range t {
		if r <= 0x7f {
			width += 170
		} else {
			width += 305
		}
	}
	return width
}

// RightAlignedTPLinkX computes the top-left x_coor (in 0..10000) so the text's right edge aligns to 9600
// (a 4.0% right margin, symmetric with x_coor="400" on the left).
func RightAlignedTPLinkX(text string) string {
	w := EstimateTPLinkOSDWidth(text)
	x := 9600 - w
	if x < 5200 {
		x = 5200
	}
	if x > 9100 {
		x = 9100
	}
	x = ((x + 25) / 50) * 50
	return strconv.Itoa(x)
}

func resolveOSDCoordinates(preset string, osd CameraOSDConfig) (l1X, l1Y, l2X, l2Y string) {
	pos := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(preset)), "-", "_")
	if pos == "" || pos == "keep" {
		// Automatically repair coordinates left behind by earlier fixed-7000/1200 presets
		switch {
		case osd.LabelX == "7000" && osd.LabelY == "500":
			pos = "top_right"
		case osd.LabelX == "7000" && osd.LabelY == "8800":
			pos = "bottom_right"
		case osd.LabelX == "400" && osd.LabelY == "1200":
			pos = "top_left"
		}
	}

	dateX, errDX := strconv.Atoi(strings.TrimSpace(osd.DateX))
	dateY, errDY := strconv.Atoi(strings.TrimSpace(osd.DateY))
	hasDateCoords := errDX == nil && errDY == nil
	dateAtTop := osd.DateEnabled && (!hasDateCoords || dateY < 1500)

	rightX1 := RightAlignedTPLinkX(osd.LabelText)
	rightX2 := RightAlignedTPLinkX(osd.Label2Text)
	if strings.TrimSpace(osd.Label2Text) == "" {
		rightX2 = rightX1
	}

	switch pos {
	case "top_left":
		l1X = "400"
		l2X = "400"
		l1End := 400 + EstimateTPLinkOSDWidth(osd.LabelText)
		if dateAtTop && hasDateCoords && dateX >= 0 && dateX < l1End+200 {
			l1Y = "1150"
			l2Y = "1800"
		} else {
			l1Y = "500"
			l2Y = "1150"
		}
	case "bottom_left":
		l1X = "400"
		l1Y = "8800"
		l2X = "400"
		l2Y = "8100"
	case "top_right":
		l1X = rightX1
		l2X = rightX2
		dateW := 3200
		if osd.WeekEnabled {
			dateW = 4350
		}
		r1Int, _ := strconv.Atoi(rightX1)
		dateOverlapsRight := !hasDateCoords || dateX >= 2000 || (dateX+dateW+200 > r1Int)
		if dateAtTop && dateOverlapsRight {
			l1Y = "1150"
			l2Y = "1800"
		} else {
			l1Y = "550"
			l2Y = "1200"
		}
	case "bottom_right":
		l1X = rightX1
		l1Y = "8800"
		l2X = rightX2
		l2Y = "8100"
	default:
		l1X = strings.TrimSpace(osd.LabelX)
		l1Y = strings.TrimSpace(osd.LabelY)
		if l1X == "" {
			l1X = "400"
		}
		if l1Y == "" {
			l1Y = "8800"
		}
		l2X = strings.TrimSpace(osd.Label2X)
		l2Y = strings.TrimSpace(osd.Label2Y)
		if l2X == "" {
			l2X = "400"
		}
		if l2Y == "" {
			l2Y = "8100"
		}
	}
	return l1X, l1Y, l2X, l2Y
}

func unescapeCameraText(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if u, err := url.QueryUnescape(s); err == nil && u != "" {
		return strings.TrimSpace(u)
	}
	return s
}

func xmlEscapeText(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// extractPlainTextOSDToken finds the <trt:OSDs token="..."> element that contains <tt:PlainText> or <tt:Type>Plain</tt:Type>
// so SetOSD updates the text OSD block rather than overwriting a DateAndTime OSD block.
func extractPlainTextOSDToken(xmlStr string) string {
	rest := xmlStr
	for {
		idx := strings.Index(rest, "OSDs")
		if idx < 0 {
			break
		}
		blockStart := strings.LastIndex(rest[:idx+4], "<")
		if blockStart < 0 {
			rest = rest[idx+4:]
			continue
		}
		endIdx := strings.Index(rest[idx:], "OSDs>")
		if endIdx < 0 {
			break
		}
		block := rest[blockStart : idx+endIdx+5]
		if strings.Contains(block, "PlainText") || strings.Contains(block, ">Plain<") {
			if tok := extractXMLAttr(block, "OSDs", "token"); tok != "" {
				return tok
			}
		}
		rest = rest[idx+endIdx+5:]
	}
	return ""
}

func buildWSUsernameTokenAt(username, password string, createdTime time.Time) string {
	var nonceBytes [16]byte
	_, _ = rand.Read(nonceBytes[:])
	created := createdTime.UTC().Format("2006-01-02T15:04:05.000Z")
	h := sha1.New()
	h.Write(nonceBytes[:])
	h.Write([]byte(created))
	h.Write([]byte(password))
	digest := base64.StdEncoding.EncodeToString(h.Sum(nil))
	nonceB64 := base64.StdEncoding.EncodeToString(nonceBytes[:])
	return fmt.Sprintf(
		`<Security s:mustUnderstand="1" xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"><UsernameToken><Username>%s</Username><Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</Password><Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">%s</Nonce><Created xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">%s</Created></UsernameToken></Security>`,
		xmlEscapeText(username), digest, nonceB64, created,
	)
}

func postTPLinkJSON(client *http.Client, ip, stok string, payload map[string]any) ([]byte, int, error) {
	body, _ := json.Marshal(payload)
	resp, err := client.Post(
		fmt.Sprintf("http://%s/stok=%s/ds", ip, stok),
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

func parseTPLinkOSDLabels(rawLabelInfo json.RawMessage) (l1 tpOSDLabelEntry, hasL1 bool, l2 tpOSDLabelEntry, hasL2 bool) {
	if len(rawLabelInfo) == 0 {
		return
	}
	// Format A: [{"label_info_1": {...}}, {"label_info_2": {...}}]
	var arr []map[string]tpOSDLabelEntry
	if err := json.Unmarshal(rawLabelInfo, &arr); err == nil && len(arr) > 0 {
		for _, item := range arr {
			if v, ok := item["label_info_1"]; ok {
				l1 = v
				hasL1 = true
			}
			if v, ok := item["label_info_2"]; ok {
				l2 = v
				hasL2 = true
			}
		}
		return
	}
	// Format B: {"label_info_1": {...}, "label_info_2": {...}}
	var obj map[string]tpOSDLabelEntry
	if err := json.Unmarshal(rawLabelInfo, &obj); err == nil && len(obj) > 0 {
		if v, ok := obj["label_info_1"]; ok {
			l1 = v
			hasL1 = true
		}
		if v, ok := obj["label_info_2"]; ok {
			l2 = v
			hasL2 = true
		}
	}
	return
}

func parseTPLinkClock(localTimeStr string, rawSeconds any, now time.Time) (camTimeStr string, camUnix int64, ok bool) {
	lt := strings.TrimSpace(localTimeStr)
	if lt != "" {
		for _, layout := range []string{"2006-01-02 15:04:05", "2006/01/02 15:04:05", time.RFC3339} {
			if parsed, err := time.ParseInLocation(layout, lt, time.Local); err == nil {
				return parsed.Format("2006-01-02 15:04:05"), parsed.Unix(), true
			}
		}
	}
	var sec int64
	switch v := rawSeconds.(type) {
	case float64:
		sec = int64(v)
	case int64:
		sec = v
	case string:
		sec, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	case json.Number:
		sec, _ = v.Int64()
	}
	if sec > 0 {
		// Some TP-LINK firmwares store UTC+8 shifted epoch in clock_status.seconds.
		// Check whether sec is closer to now.Unix() or now.Unix()+offset.
		_, tzOffset := now.Zone()
		if tzOffset != 0 && math.Abs(float64(sec-(now.Unix()+int64(tzOffset)))) < 600 && math.Abs(float64(sec-now.Unix())) > 1800 {
			sec -= int64(tzOffset)
		}
		t := time.Unix(sec, 0).In(time.Local)
		return t.Format("2006-01-02 15:04:05"), sec, true
	}
	return "", 0, false
}

func parseONVIFDateTimeBlock(xmlBlock string, loc *time.Location) (time.Time, bool) {
	yr, errY := strconv.Atoi(extractXMLTag(xmlBlock, "Year"))
	mo, errM := strconv.Atoi(extractXMLTag(xmlBlock, "Month"))
	dy, errD := strconv.Atoi(extractXMLTag(xmlBlock, "Day"))
	hr, errH := strconv.Atoi(extractXMLTag(xmlBlock, "Hour"))
	mi, errMin := strconv.Atoi(extractXMLTag(xmlBlock, "Minute"))
	sc, errS := strconv.Atoi(extractXMLTag(xmlBlock, "Second"))
	if errY != nil || errM != nil || errD != nil || errH != nil || errMin != nil || errS != nil || yr < 1970 {
		return time.Time{}, false
	}
	return time.Date(yr, time.Month(mo), dy, hr, mi, sc, 0, loc), true
}

func queryONVIFSystemDateAndTime(client *http.Client, ip string) (camLocal time.Time, camUTC time.Time, timingMode string, tz string, endpoint string, ok bool) {
	endpoints := []string{
		fmt.Sprintf("http://%s:2020/onvif/device_service", ip),
		fmt.Sprintf("http://%s/onvif/device_service", ip),
		fmt.Sprintf("http://%s:8000/onvif/device_service", ip),
		fmt.Sprintf("http://%s:8080/onvif/device_service", ip),
	}
	soapReq := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl"><s:Body><tds:GetSystemDateAndTime/></s:Body></s:Envelope>`
	for _, ep := range endpoints {
		req, err := http.NewRequest("POST", ep, strings.NewReader(soapReq))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		bodyStr := string(raw)
		if !strings.Contains(bodyStr, "GetSystemDateAndTimeResponse") {
			continue
		}
		dtType := strings.ToLower(extractXMLTag(bodyStr, "DateTimeType"))
		mode := "manual"
		if dtType == "ntp" {
			mode = "ntp"
		}
		tzStr := extractXMLTag(bodyStr, "TZ")
		if tzStr == "" {
			tzStr = "GMT+08:00"
		}

		var utcTime, localTime time.Time
		var hasUTC, hasLocal bool
		if uIdx := strings.Index(bodyStr, "UTCDateTime>"); uIdx >= 0 {
			endIdx := strings.Index(bodyStr[uIdx:], "</")
			if endIdx < 0 {
				endIdx = len(bodyStr) - uIdx
			}
			// Take up to 400 chars for the UTCDateTime block
			limit := uIdx + 400
			if limit > len(bodyStr) {
				limit = len(bodyStr)
			}
			utcTime, hasUTC = parseONVIFDateTimeBlock(bodyStr[uIdx:limit], time.UTC)
		}
		if lIdx := strings.Index(bodyStr, "LocalDateTime>"); lIdx >= 0 {
			limit := lIdx + 400
			if limit > len(bodyStr) {
				limit = len(bodyStr)
			}
			localTime, hasLocal = parseONVIFDateTimeBlock(bodyStr[lIdx:limit], time.Local)
		}

		if hasLocal {
			if !hasUTC {
				utcTime = localTime.UTC()
			}
			return localTime, utcTime, mode, tzStr, ep, true
		}
		if hasUTC {
			return utcTime.In(time.Local), utcTime, mode, tzStr, ep, true
		}
	}
	return time.Time{}, time.Time{}, "", "", "", false
}

func setONVIFSystemDateAndTime(client *http.Client, ip, username, password string, targetTime time.Time, enableNTP bool) error {
	if username == "" {
		username = "admin"
	}
	_, camUTC, _, _, activeEP, hasPreClock := queryONVIFSystemDateAndTime(client, ip)
	endpoints := []string{
		fmt.Sprintf("http://%s:2020/onvif/device_service", ip),
		fmt.Sprintf("http://%s/onvif/device_service", ip),
		fmt.Sprintf("http://%s:8000/onvif/device_service", ip),
		fmt.Sprintf("http://%s:8080/onvif/device_service", ip),
	}
	if activeEP != "" {
		endpoints = append([]string{activeEP}, endpoints...)
	}

	utcTarget := targetTime.UTC()
	dtType := "Manual"
	if enableNTP {
		// Always set manual UTCDateTime first so clock is immediately accurate even without internet,
		// then switch to NTP if requested.
		dtType = "Manual"
	}

	buildSetBody := func(tokenCreated time.Time, modeType string) string {
		secHdr := "<s:Header>" + buildWSUsernameTokenAt(username, password, tokenCreated) + "</s:Header>"
		utcXML := ""
		if modeType == "Manual" {
			utcXML = fmt.Sprintf(
				`<tds:UTCDateTime><tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time><tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date></tds:UTCDateTime>`,
				utcTarget.Hour(), utcTarget.Minute(), utcTarget.Second(),
				utcTarget.Year(), int(utcTarget.Month()), utcTarget.Day(),
			)
		}
		return fmt.Sprintf(
			`<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">%s<s:Body><tds:SetSystemDateAndTime><tds:DateTimeType>%s</tds:DateTimeType><tds:DaylightSavings>false</tds:DaylightSavings><tds:TimeZone><tt:TZ>CST-8</tt:TZ></tds:TimeZone>%s</tds:SetSystemDateAndTime></s:Body></s:Envelope>`,
			secHdr, modeType, utcXML,
		)
	}

	tokenTimestamps := []time.Time{time.Now().UTC()}
	if hasPreClock && !camUTC.IsZero() && math.Abs(camUTC.Sub(time.Now().UTC()).Seconds()) > 5 {
		// If the camera's internal clock is skewed, also try signing WS-UsernameToken with the camera's own clock
		tokenTimestamps = append(tokenTimestamps, camUTC)
	}

	seenEP := make(map[string]bool)
	for _, ep := range endpoints {
		if seenEP[ep] {
			continue
		}
		seenEP[ep] = true
		for _, ts := range tokenTimestamps {
			soapBody := buildSetBody(ts, dtType)
			req, err := http.NewRequest("POST", ep, strings.NewReader(soapBody))
			if err != nil {
				continue
			}
			req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
			resp, err := client.Do(req)
			if err != nil {
				break
			}
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			respStr := string(raw)
			if resp.StatusCode == 200 && strings.Contains(respStr, "SetSystemDateAndTimeResponse") {
				if enableNTP {
					ntpBody := buildSetBody(time.Now().UTC(), "NTP")
					if ntpReq, err := http.NewRequest("POST", ep, strings.NewReader(ntpBody)); err == nil {
						ntpReq.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
						if ntpResp, err := client.Do(ntpReq); err == nil {
							_ = ntpResp.Body.Close()
						}
					}
				}
				return nil
			}
		}
	}
	return fmt.Errorf("ONVIF SetSystemDateAndTime 未获响应或鉴权未通过")
}

// GetCameraOSDAndTime reads both the on-screen display (OSD) configuration and hardware system clock
// from a camera via TP-LINK/Mercury HTTP /ds API or standard ONVIF fallback.
func GetCameraOSDAndTime(ip, username, password string) (CameraOSDTimePayload, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return CameraOSDTimePayload{}, fmt.Errorf("摄像头 IP 不能为空")
	}
	unlock := lockCameraHTTP(ip)
	defer unlock()
	return getCameraOSDAndTimeUnlocked(ip, username, password)
}

func getCameraOSDAndTimeUnlocked(ip, username, password string) (CameraOSDTimePayload, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return CameraOSDTimePayload{}, fmt.Errorf("摄像头 IP 不能为空")
	}
	if username == "" {
		username = "admin"
	}
	now := time.Now()
	payload := CameraOSDTimePayload{
		Protocol: "tplink",
		OSD: CameraOSDConfig{
			LabelEnabled:   true,
			DateEnabled:    true,
			PositionPreset: "bottom_left",
		},
		Time: CameraTimeStatus{
			HostTime:   now.Format("2006-01-02 15:04:05"),
			HostUnix:   now.Unix(),
			Timezone:   "GMT+08:00",
			TimingMode: "manual",
		},
	}

	client := &http.Client{Timeout: 3200 * time.Millisecond}
	candidates := dedupPasswords([]string{password}, true)
	stok, _, httpOpen := loginTPLinkHTTP(client, ip, username, candidates)
	if stok != "" {
		// 1. Query OSD and System modules
		raw, code, err := postTPLinkJSON(client, ip, stok, map[string]any{
			"method": "get",
			"OSD": map[string]any{
				"name":  []string{"date", "week", "font"},
				"table": []string{"label_info"},
			},
			"system": map[string]any{
				"name": []string{"sys", "clock_status", "basic", "ntp"},
			},
		})
		if err != nil || code != 0 || !bytes.Contains(raw, []byte(`"OSD"`)) {
			// Fallback minimal query for firmwares that reject optional sections like "week" or "ntp"
			raw2, _, err2 := postTPLinkJSON(client, ip, stok, map[string]any{
				"method": "get",
				"OSD": map[string]any{
					"name":  []string{"date"},
					"table": []string{"label_info"},
				},
				"system": map[string]any{
					"name": []string{"sys", "clock_status"},
				},
			})
			if err2 == nil && len(raw2) > 0 {
				raw = raw2
			}
		}

		var ds struct {
			OSD struct {
				Date struct {
					Enabled string `json:"enabled"`
					XCoor   string `json:"x_coor"`
					YCoor   string `json:"y_coor"`
				} `json:"date"`
				Week struct {
					Enabled string `json:"enabled"`
				} `json:"week"`
				LabelInfo  json.RawMessage  `json:"label_info"`
				LabelInfo1 *tpOSDLabelEntry `json:"label_info_1"`
				LabelInfo2 *tpOSDLabelEntry `json:"label_info_2"`
			} `json:"OSD"`
			System struct {
				Sys struct {
					DevAlias string `json:"dev_alias"`
				} `json:"sys"`
				ClockStatus struct {
					Seconds   any    `json:"seconds"`
					LocalTime string `json:"local_time"`
				} `json:"clock_status"`
				Basic struct {
					TimingMode string `json:"timing_mode"`
					Timezone   string `json:"timezone"`
				} `json:"basic"`
				NTP struct {
					Server string `json:"server"`
				} `json:"ntp"`
			} `json:"system"`
		}
		_ = json.Unmarshal(raw, &ds)

		l1, hasL1, l2, hasL2 := parseTPLinkOSDLabels(ds.OSD.LabelInfo)
		if !hasL1 && ds.OSD.LabelInfo1 != nil {
			l1 = *ds.OSD.LabelInfo1
			hasL1 = true
		}
		if !hasL2 && ds.OSD.LabelInfo2 != nil {
			l2 = *ds.OSD.LabelInfo2
			hasL2 = true
		}

		alias := unescapeCameraText(ds.System.Sys.DevAlias)
		payload.OSD.DevAlias = alias
		if hasL1 {
			payload.OSD.LabelEnabled = !strings.EqualFold(l1.Enabled, "off")
			payload.OSD.LabelText = unescapeCameraText(l1.Text)
			payload.OSD.LabelX = strings.TrimSpace(l1.XCoor)
			payload.OSD.LabelY = strings.TrimSpace(l1.YCoor)
		} else if alias != "" {
			payload.OSD.LabelText = alias
		}
		if hasL2 {
			payload.OSD.Label2Enabled = strings.EqualFold(l2.Enabled, "on")
			rawL2 := unescapeCameraText(l2.Text)
			if isFactoryPlaceholderOSD2(rawL2, payload.OSD.Label2Enabled) {
				payload.OSD.Label2Text = ""
			} else {
				payload.OSD.Label2Text = rawL2
			}
			payload.OSD.Label2X = strings.TrimSpace(l2.XCoor)
			payload.OSD.Label2Y = strings.TrimSpace(l2.YCoor)
		}
		if ds.OSD.Date.Enabled != "" {
			payload.OSD.DateEnabled = !strings.EqualFold(ds.OSD.Date.Enabled, "off")
			payload.OSD.DateX = strings.TrimSpace(ds.OSD.Date.XCoor)
			payload.OSD.DateY = strings.TrimSpace(ds.OSD.Date.YCoor)
		}
		if ds.OSD.Week.Enabled != "" {
			payload.OSD.WeekEnabled = strings.EqualFold(ds.OSD.Week.Enabled, "on")
		}
		payload.OSD.PositionPreset = inferOSDPositionPreset(payload.OSD.LabelX, payload.OSD.LabelY)

		now = time.Now()
		payload.Time.HostTime = now.Format("2006-01-02 15:04:05")
		payload.Time.HostUnix = now.Unix()
		if ds.System.Basic.Timezone != "" {
			payload.Time.Timezone = strings.TrimSpace(ds.System.Basic.Timezone)
		}
		if ds.System.Basic.TimingMode != "" {
			payload.Time.TimingMode = strings.ToLower(strings.TrimSpace(ds.System.Basic.TimingMode))
		}
		if ds.System.NTP.Server != "" {
			payload.Time.NTPServer = strings.TrimSpace(ds.System.NTP.Server)
		}

		if camTimeStr, camUnix, ok := parseTPLinkClock(ds.System.ClockStatus.LocalTime, ds.System.ClockStatus.Seconds, now); ok {
			payload.Time.CameraTime = camTimeStr
			payload.Time.CameraUnix = camUnix
			payload.Time.DriftSeconds = camUnix - now.Unix()
		} else if onvifLocal, _, mode, tz, _, ok := queryONVIFSystemDateAndTime(client, ip); ok {
			payload.Time.CameraTime = onvifLocal.Format("2006-01-02 15:04:05")
			payload.Time.CameraUnix = onvifLocal.Unix()
			payload.Time.DriftSeconds = onvifLocal.Unix() - now.Unix()
			if mode != "" {
				payload.Time.TimingMode = mode
			}
			if tz != "" {
				payload.Time.Timezone = tz
			}
		}
		return payload, nil
	}

	// Fallback: Standard ONVIF GetSystemDateAndTime + GetOSDs
	payload.Protocol = "onvif"
	if onvifLocal, _, mode, tz, _, ok := queryONVIFSystemDateAndTime(client, ip); ok {
		httpOpen = true
		now = time.Now()
		payload.Time.HostTime = now.Format("2006-01-02 15:04:05")
		payload.Time.HostUnix = now.Unix()
		payload.Time.CameraTime = onvifLocal.Format("2006-01-02 15:04:05")
		payload.Time.CameraUnix = onvifLocal.Unix()
		payload.Time.DriftSeconds = onvifLocal.Unix() - now.Unix()
		payload.Time.TimingMode = mode
		payload.Time.Timezone = tz
	}

	mediaEndpoints := []string{
		fmt.Sprintf("http://%s:2020/onvif/media_service", ip),
		fmt.Sprintf("http://%s/onvif/media_service", ip),
		fmt.Sprintf("http://%s:8000/onvif/media_service", ip),
		fmt.Sprintf("http://%s/onvif/device_service", ip),
	}
	for _, ep := range mediaEndpoints {
		for _, pwd := range candidates {
			secHdr := "<s:Header>" + buildWSUsernameToken(username, pwd) + "</s:Header>"
			soapOSD := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl">` +
				secHdr + `<s:Body><trt:GetOSDs/></s:Body></s:Envelope>`
			req, err := http.NewRequest("POST", ep, strings.NewReader(soapOSD))
			if err != nil {
				continue
			}
			req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
			resp, err := client.Do(req)
			if err != nil {
				break
			}
			httpOpen = true
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			bodyStr := string(raw)
			if strings.Contains(bodyStr, "GetOSDsResponse") {
				txt := extractXMLTag(bodyStr, "PlainText")
				payload.OSD.LabelText = unescapeCameraText(txt)
				payload.OSD.LabelEnabled = payload.OSD.LabelText != ""
				payload.OSD.DateEnabled = strings.Contains(bodyStr, "DateAndTime")
				return payload, nil
			}
		}
	}

	if payload.Time.CameraTime != "" {
		return payload, nil
	}
	if !httpOpen {
		return payload, fmt.Errorf("无法连接摄像头管理端口 (%s)", ip)
	}
	return payload, fmt.Errorf("摄像头管理鉴权未通过或不支持读取 OSD/时间")
}

// SetCameraOSD updates the camera's hardware OSD text overlay, date/week stamp switches, and device alias.
func SetCameraOSD(ip, username, password string, req SetOSDRequest) (CameraOSDConfig, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return CameraOSDConfig{}, fmt.Errorf("摄像头 IP 不能为空")
	}
	unlock := lockCameraHTTP(ip)
	defer unlock()
	if username == "" {
		username = "admin"
	}

	cur, _ := getCameraOSDAndTimeUnlocked(ip, username, password)
	osd := cur.OSD

	if req.LabelEnabled != nil {
		osd.LabelEnabled = *req.LabelEnabled
	}
	if req.LabelText != nil {
		osd.LabelText = strings.TrimSpace(*req.LabelText)
		if req.LabelEnabled == nil && osd.LabelText != "" {
			osd.LabelEnabled = true
		}
	}
	if req.Label2Enabled != nil {
		osd.Label2Enabled = *req.Label2Enabled
	}
	if req.Label2Text != nil {
		osd.Label2Text = strings.TrimSpace(*req.Label2Text)
	}
	if req.DateEnabled != nil {
		osd.DateEnabled = *req.DateEnabled
	}
	if req.WeekEnabled != nil {
		osd.WeekEnabled = *req.WeekEnabled
	}

	osd.LabelX, osd.LabelY, osd.Label2X, osd.Label2Y = resolveOSDCoordinates(req.Position, osd)
	osd.PositionPreset = inferOSDPositionPreset(osd.LabelX, osd.LabelY)

	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}

	client := &http.Client{Timeout: 3500 * time.Millisecond}
	candidates := dedupPasswords([]string{password}, true)
	stok, _, httpOpen := loginTPLinkHTTP(client, ip, username, candidates)
	if stok != "" {
		l1Map := map[string]any{
			"enabled": onOff(osd.LabelEnabled),
			"text":    osd.LabelText,
			"x_coor":  osd.LabelX,
			"y_coor":  osd.LabelY,
		}
		l2Map := map[string]any{
			"enabled": onOff(osd.Label2Enabled),
			"text":    osd.Label2Text,
			"x_coor":  osd.Label2X,
			"y_coor":  osd.Label2Y,
		}
		dateMap := map[string]any{
			"enabled": onOff(osd.DateEnabled),
		}
		if osd.DateX != "" && osd.DateY != "" {
			dateMap["x_coor"] = osd.DateX
			dateMap["y_coor"] = osd.DateY
		}
		weekMap := map[string]any{
			"enabled": onOff(osd.WeekEnabled),
		}

		// 1. Send TP-LINK /ds OSD set (Format A: named section label_info_1 + date + week)
		osdPayloadA := map[string]any{
			"date":         dateMap,
			"week":         weekMap,
			"label_info_1": l1Map,
		}
		if req.Label2Enabled != nil || req.Label2Text != nil {
			osdPayloadA["label_info_2"] = l2Map
		}
		_, codeA, errA := postTPLinkJSON(client, ip, stok, map[string]any{
			"method": "set",
			"OSD":    osdPayloadA,
		})

		// 2. Also send Format B (table array label_info: [{"label_info_1": ...}]) for firmwares expecting array format
		labelArr := []map[string]any{
			{"label_info_1": l1Map},
		}
		if req.Label2Enabled != nil || req.Label2Text != nil {
			labelArr = append(labelArr, map[string]any{"label_info_2": l2Map})
		}
		_, codeB, errB := postTPLinkJSON(client, ip, stok, map[string]any{
			"method": "set",
			"OSD": map[string]any{
				"date":       dateMap,
				"label_info": labelArr,
			},
		})

		// 3. Sync system.sys.dev_alias when LabelText is provided
		shouldSyncAlias := req.SyncDevAlias == nil || *req.SyncDevAlias
		if shouldSyncAlias && osd.LabelText != "" {
			_, _, _ = postTPLinkJSON(client, ip, stok, map[string]any{
				"method": "set",
				"system": map[string]any{
					"sys": map[string]any{
						"dev_alias": osd.LabelText,
					},
				},
			})
			osd.DevAlias = osd.LabelText
		}

		if errA != nil && errB != nil {
			return osd, fmt.Errorf("发送 OSD 设置指令失败: %v", errA)
		}
		if codeA != 0 && codeB != 0 {
			// Fallback: try minimal label_info_1 without week/coordinates
			_, codeMin, _ := postTPLinkJSON(client, ip, stok, map[string]any{
				"method": "set",
				"OSD": map[string]any{
					"label_info_1": map[string]any{
						"enabled": onOff(osd.LabelEnabled),
						"text":    osd.LabelText,
					},
				},
			})
			if codeMin != 0 {
				return osd, fmt.Errorf("摄像头固件拒绝修改 OSD 设置 (错误码: %d)", codeA)
			}
		}

		// Read back to confirm latest state
		if updated, err := getCameraOSDAndTimeUnlocked(ip, username, password); err == nil {
			return updated.OSD, nil
		}
		return osd, nil
	}

	// Fallback: Standard ONVIF GetOSDs -> SetOSD
	mediaEndpoints := []string{
		fmt.Sprintf("http://%s:2020/onvif/media_service", ip),
		fmt.Sprintf("http://%s/onvif/media_service", ip),
		fmt.Sprintf("http://%s:8000/onvif/media_service", ip),
		fmt.Sprintf("http://%s/onvif/device_service", ip),
	}
	for _, ep := range mediaEndpoints {
		for _, pwd := range candidates {
			secHdr := "<s:Header>" + buildWSUsernameToken(username, pwd) + "</s:Header>"
			soapGet := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl">` +
				secHdr + `<s:Body><trt:GetOSDs/></s:Body></s:Envelope>`
			gReq, err := http.NewRequest("POST", ep, strings.NewReader(soapGet))
			if err != nil {
				continue
			}
			gReq.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
			gResp, err := client.Do(gReq)
			if err != nil {
				break
			}
			httpOpen = true
			gRaw, _ := io.ReadAll(gResp.Body)
			_ = gResp.Body.Close()
			gStr := string(gRaw)
			if !strings.Contains(gStr, "GetOSDsResponse") {
				continue
			}

			osdToken := extractPlainTextOSDToken(gStr)
			if osdToken == "" {
				osdToken = extractXMLAttr(gStr, "OSDs", "token")
			}
			if osdToken == "" {
				osdToken = "osd_1"
			}
			vscToken := extractXMLTag(gStr, "VideoSourceConfigurationToken")
			if vscToken == "" {
				vscToken = "vsconf"
			}
			onvifPosType := "LowerLeft"
			switch osd.PositionPreset {
			case "top_left":
				onvifPosType = "UpperLeft"
			case "top_right":
				onvifPosType = "UpperRight"
			case "bottom_right":
				onvifPosType = "LowerRight"
			}

			secHdrSet := "<s:Header>" + buildWSUsernameToken(username, pwd) + "</s:Header>"
			soapSet := fmt.Sprintf(
				`<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">%s<s:Body><trt:SetOSD><trt:OSD token="%s"><tt:VideoSourceConfigurationToken>%s</tt:VideoSourceConfigurationToken><tt:Type>Text</tt:Type><tt:Position><tt:Type>%s</tt:Type></tt:Position><tt:TextString><tt:Type>Plain</tt:Type><tt:PlainText>%s</tt:PlainText></tt:TextString></trt:OSD></trt:SetOSD></s:Body></s:Envelope>`,
				secHdrSet, xmlEscapeText(osdToken), xmlEscapeText(vscToken), onvifPosType, xmlEscapeText(osd.LabelText),
			)
			sReq, err := http.NewRequest("POST", ep, strings.NewReader(soapSet))
			if err != nil {
				continue
			}
			sReq.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
			sResp, err := client.Do(sReq)
			if err == nil {
				sRaw, _ := io.ReadAll(sResp.Body)
				_ = sResp.Body.Close()
				if sResp.StatusCode == 200 || strings.Contains(string(sRaw), "SetOSDResponse") {
					return osd, nil
				}
			}
		}
	}

	if !httpOpen {
		return osd, fmt.Errorf("无法连接摄像头管理端口 (%s)", ip)
	}
	return osd, fmt.Errorf("摄像头管理鉴权失败或该型号不支持远程设置 OSD")
}

// SyncCameraTime synchronizes a camera's hardware RTC / system clock to targetTime (defaulting to host time)
// via TP-LINK/Mercury HTTP /ds API and/or standard ONVIF SetSystemDateAndTime.
func SyncCameraTime(ip, username, password string, targetTime time.Time, enableNTP *bool, ntpServer string) (CameraTimeStatus, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return CameraTimeStatus{}, fmt.Errorf("摄像头 IP 不能为空")
	}
	unlock := lockCameraHTTP(ip)
	defer unlock()
	if username == "" {
		username = "admin"
	}
	if targetTime.IsZero() {
		targetTime = time.Now()
	}
	targetLocal := targetTime.In(time.Local)
	localStr := targetLocal.Format("2006-01-02 15:04:05")
	unixSec := targetLocal.Unix()

	client := &http.Client{Timeout: 3500 * time.Millisecond}
	candidates := dedupPasswords([]string{password}, true)
	stok, matchedPwd, httpOpen := loginTPLinkHTTP(client, ip, username, candidates)
	if stok != "" {
		mode := "manual"
		if enableNTP != nil && *enableNTP {
			mode = "ntp"
		}
		if strings.TrimSpace(ntpServer) == "" {
			ntpServer = "ntp.aliyun.com"
		}

		// 1. Set system.clock_status (numeric seconds + local_time) and timezone
		_, code1, _ := postTPLinkJSON(client, ip, stok, map[string]any{
			"method": "set",
			"system": map[string]any{
				"basic": map[string]any{
					"timing_mode": "manual",
					"timezone":    "GMT+08:00",
					"zone_id":     "Hongkong",
				},
				"clock_status": map[string]any{
					"seconds":    unixSec,
					"local_time": localStr,
				},
			},
		})

		// 2. Also send string-formatted seconds for firmwares expecting string types in UCI/ubus
		if code1 != 0 {
			_, _, _ = postTPLinkJSON(client, ip, stok, map[string]any{
				"method": "set",
				"system": map[string]any{
					"clock_status": map[string]any{
						"seconds":    strconv.FormatInt(unixSec, 10),
						"local_time": localStr,
					},
				},
			})
		}

		// 3. Also invoke "method": "do" clock_status for firmwares that trigger RTC write via action hook
		_, _, _ = postTPLinkJSON(client, ip, stok, map[string]any{
			"method": "do",
			"system": map[string]any{
				"clock_status": map[string]any{
					"seconds":    strconv.FormatInt(unixSec, 10),
					"local_time": localStr,
					"timezone":   "GMT+08:00",
				},
			},
		})

		// 4. If NTP mode was requested, configure NTP server after immediate clock write
		if mode == "ntp" {
			_, _, _ = postTPLinkJSON(client, ip, stok, map[string]any{
				"method": "set",
				"system": map[string]any{
					"basic": map[string]any{
						"timing_mode": "ntp",
						"timezone":    "GMT+08:00",
					},
					"ntp": map[string]any{
						"server":   strings.TrimSpace(ntpServer),
						"port":     "123",
						"interval": "24",
					},
				},
			})
		}

		// 5. Verify via read-back; if camera firmware ignores /ds clock_status and uses ONVIF clock, call ONVIF SetSystemDateAndTime
		if updated, err := getCameraOSDAndTimeUnlocked(ip, username, matchedPwd); err == nil {
			if math.Abs(float64(updated.Time.CameraUnix-targetLocal.Unix())) <= 5 {
				return updated.Time, nil
			}
		}
		_ = setONVIFSystemDateAndTime(client, ip, username, matchedPwd, targetLocal, mode == "ntp")
		if updated2, err := getCameraOSDAndTimeUnlocked(ip, username, matchedPwd); err == nil && updated2.Time.CameraTime != "" {
			return updated2.Time, nil
		}

		now := time.Now()
		return CameraTimeStatus{
			CameraTime:   localStr,
			CameraUnix:   unixSec,
			HostTime:     now.Format("2006-01-02 15:04:05"),
			HostUnix:     now.Unix(),
			DriftSeconds: 0,
			Timezone:     "GMT+08:00",
			TimingMode:   mode,
			NTPServer:    ntpServer,
		}, nil
	}

	// Fallback: Standard ONVIF SetSystemDateAndTime
	for _, pwd := range candidates {
		if err := setONVIFSystemDateAndTime(client, ip, username, pwd, targetLocal, enableNTP != nil && *enableNTP); err == nil {
			if updated, gErr := getCameraOSDAndTimeUnlocked(ip, username, pwd); gErr == nil && updated.Time.CameraTime != "" {
				return updated.Time, nil
			}
			now := time.Now()
			return CameraTimeStatus{
				CameraTime:   localStr,
				CameraUnix:   unixSec,
				HostTime:     now.Format("2006-01-02 15:04:05"),
				HostUnix:     now.Unix(),
				DriftSeconds: 0,
				Timezone:     "GMT+08:00",
				TimingMode:   "manual",
			}, nil
		}
	}

	if !httpOpen {
		return CameraTimeStatus{}, fmt.Errorf("无法连接摄像头管理端口 (%s)", ip)
	}
	return CameraTimeStatus{}, fmt.Errorf("摄像头管理鉴权失败或不支持远程校时")
}

// BatchSyncCamerasTime concurrently synchronizes hardware clocks across all provided cameras to current host time.
func BatchSyncCamerasTime(cameras []config.Camera, defaultUser, defaultPass string) BatchMaintResult {
	res := BatchMaintResult{
		Action: "sync_time",
		Total:  len(cameras),
		Items:  make([]BatchItemResult, len(cameras)),
	}
	if len(cameras) == 0 {
		return res
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for i, cam := range cameras {
		wg.Add(1)
		go func(idx int, c config.Camera) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			user := strings.TrimSpace(c.Username)
			if user == "" {
				user = defaultUser
			}
			pass := c.Password
			if pass == "" && !c.NoPassword {
				pass = defaultPass
			}
			st, err := SyncCameraTime(c.IP, user, pass, time.Now(), nil, "")
			if err != nil {
				res.Items[idx] = BatchItemResult{
					ID:    c.ID,
					Name:  c.Name,
					IP:    c.IP,
					OK:    false,
					Error: err.Error(),
				}
				return
			}
			res.Items[idx] = BatchItemResult{
				ID:     c.ID,
				Name:   c.Name,
				IP:     c.IP,
				OK:     true,
				Detail: st.CameraTime,
			}
		}(i, cam)
	}
	wg.Wait()

	for _, item := range res.Items {
		if item.OK {
			res.Succeeded++
		} else {
			res.Failed++
		}
	}
	return res
}

// BatchSyncCamerasOSDNames concurrently writes each camera's configured Name into its hardware OSD label_info_1.
func BatchSyncCamerasOSDNames(cameras []config.Camera, defaultUser, defaultPass string) BatchMaintResult {
	res := BatchMaintResult{
		Action: "sync_osd_names",
		Total:  len(cameras),
		Items:  make([]BatchItemResult, len(cameras)),
	}
	if len(cameras) == 0 {
		return res
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for i, cam := range cameras {
		wg.Add(1)
		go func(idx int, c config.Camera) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			user := strings.TrimSpace(c.Username)
			if user == "" {
				user = defaultUser
			}
			pass := c.Password
			if pass == "" && !c.NoPassword {
				pass = defaultPass
			}
			name := strings.TrimSpace(c.Name)
			enabled := true
			osdCfg, err := SetCameraOSD(c.IP, user, pass, SetOSDRequest{
				LabelEnabled: &enabled,
				LabelText:    &name,
				DateEnabled:  &enabled,
				Position:     "keep",
			})
			if err != nil {
				res.Items[idx] = BatchItemResult{
					ID:    c.ID,
					Name:  c.Name,
					IP:    c.IP,
					OK:    false,
					Error: err.Error(),
				}
				return
			}
			res.Items[idx] = BatchItemResult{
				ID:     c.ID,
				Name:   c.Name,
				IP:     c.IP,
				OK:     true,
				Detail: osdCfg.LabelText,
			}
		}(i, cam)
	}
	wg.Wait()

	for _, item := range res.Items {
		if item.OK {
			res.Succeeded++
		} else {
			res.Failed++
		}
	}
	return res
}
