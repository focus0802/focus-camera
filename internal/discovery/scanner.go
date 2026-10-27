package discovery

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
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

type ProbeResult struct {
	Camera   config.Camera
	AuthOK   bool
	TalkOpen bool
	RTSPOpen bool
	HTTPOpen bool
}

func dedupPasswords(candidates []string, includeEmpty bool) []string {
	seen := make(map[string]bool, len(candidates)+1)
	out := make([]string, 0, len(candidates)+1)
	for _, p := range candidates {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if includeEmpty && !seen[""] {
		out = append(out, "")
	}
	if len(out) == 0 {
		out = append(out, "")
	}
	return out
}

func LoginTPLinkHTTP(client *http.Client, ip, username string, passwords []string) (stok string, matchedPassword string, httpOpen bool) {
	return loginTPLinkHTTP(client, ip, username, dedupPasswords(passwords, true))
}

func loginTPLinkHTTP(client *http.Client, ip, username string, passwords []string) (stok string, matchedPassword string, httpOpen bool) {
	if username == "" {
		username = "admin"
	}
	for _, pwd := range passwords {
		encPwd := SecurityEncode(pwd)
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
			return "", "", httpOpen
		}
		httpOpen = true
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			continue
		}
		raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
		var loginResp struct {
			ErrorCode int    `json:"error_code"`
			Stok      string `json:"stok"`
		}
		if err := json.Unmarshal(raw, &loginResp); err == nil && loginResp.Stok != "" {
			return loginResp.Stok, pwd, true
		}
	}
	return "", "", httpOpen
}

func extractXMLTag(xmlStr, tag string) string {
	for _, prefix := range []string{"tds:", "tt:", "trt:", ""} {
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

func BuildWSUsernameToken(username, password string) string {
	return buildWSUsernameToken(username, password)
}

func buildWSUsernameToken(username, password string) string {
	var nonceBytes [16]byte
	_, _ = rand.Read(nonceBytes[:])
	created := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	h := sha1.New()
	h.Write(nonceBytes[:])
	h.Write([]byte(created))
	h.Write([]byte(password))
	digest := base64.StdEncoding.EncodeToString(h.Sum(nil))
	nonceB64 := base64.StdEncoding.EncodeToString(nonceBytes[:])
	return fmt.Sprintf(
		`<Security s:mustUnderstand="1" xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"><UsernameToken><Username>%s</Username><Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</Password><Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">%s</Nonce><Created xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">%s</Created></UsernameToken></Security>`,
		username, digest, nonceB64, created,
	)
}

func probeONVIFDevice(client *http.Client, ip, username string, passwords []string) (vendor, model, matchedPwd string, authOK, httpOpen bool) {
	endpoints := []string{
		fmt.Sprintf("http://%s/onvif/device_service", ip),
		fmt.Sprintf("http://%s:2020/onvif/device_service", ip),
		fmt.Sprintf("http://%s:8000/onvif/device_service", ip),
		fmt.Sprintf("http://%s:8080/onvif/device_service", ip),
	}
	for _, ep := range endpoints {
		for _, pwd := range passwords {
			secHdr := ""
			if username != "" {
				secHdr = "<s:Header>" + buildWSUsernameToken(username, pwd) + "</s:Header>"
			}
			soapReq := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">` +
				secHdr +
				`<s:Body><tds:GetDeviceInformation/></s:Body></s:Envelope>`
			req, err := http.NewRequest("POST", ep, strings.NewReader(soapReq))
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
			mfg := extractXMLTag(bodyStr, "Manufacturer")
			mdl := extractXMLTag(bodyStr, "Model")
			if mfg != "" || mdl != "" {
				v := config.NormalizeVendor(mfg, mdl)
				if v == "tplink" && !strings.Contains(strings.ToUpper(mfg+" "+mdl), "TP-LINK") && !strings.HasPrefix(strings.ToUpper(mdl), "TL-") {
					v = "onvif"
				}
				if mdl == "" {
					mdl = mfg
				}
				return v, mdl, pwd, true, true
			}
		}
		if httpOpen {
			break
		}
	}
	return "", "", "", false, httpOpen
}

func md5HexStr(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func extractRTSPAttr(s, key string) string {
	idx := strings.Index(s, key+`="`)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(key)+2:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func buildRTSPAuthHeader(method, uri, wwwAuth, user, pass string) string {
	upper := strings.ToUpper(wwwAuth)
	if strings.HasPrefix(upper, "DIGEST") {
		realm := extractRTSPAttr(wwwAuth, "realm")
		nonce := extractRTSPAttr(wwwAuth, "nonce")
		if realm == "" || nonce == "" {
			return ""
		}
		ha1 := md5HexStr(fmt.Sprintf("%s:%s:%s", user, realm, pass))
		ha2 := md5HexStr(fmt.Sprintf("%s:%s", method, uri))
		resp := md5HexStr(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2))
		return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`, user, realm, nonce, uri, resp)
	}
	if strings.HasPrefix(upper, "BASIC") {
		cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		return "Basic " + cred
	}
	return ""
}

func inferVendorFromRTSPBanner(banner string) (vendor, model string) {
	u := strings.ToUpper(banner)
	switch {
	case strings.Contains(u, "HIKVISION") || strings.Contains(u, "DNVRS-WEBS") || strings.Contains(u, "EZVIZ"):
		return "hikvision", "Hikvision IPC"
	case strings.Contains(u, "DAHUA") || strings.Contains(u, "IMOU"):
		return "dahua", "Dahua IPC"
	case strings.Contains(u, "UNIVIEW") || strings.Contains(u, "UNV"):
		return "uniview", "Uniview IPC"
	case strings.Contains(u, "REOLINK"):
		return "reolink", "Reolink IPC"
	case strings.Contains(u, "TAPO"):
		return "tapo", "Tapo IPC"
	case strings.Contains(u, "MERCURY") || strings.Contains(u, "MIPC"):
		return "mercury", "Mercury IPC"
	case strings.Contains(u, "TP-LINK") || strings.Contains(u, "TL-IPC"):
		return "tplink", "TP-LINK IPC"
	default:
		return "onvif", "RTSP IPC"
	}
}

func probeStandardRTSP(ip string, port int, username string, passwords []string, knownVendor string, timeout time.Duration) (vendor, model, mainPath, subPath, matchedPwd string, authOK, validRTSP bool) {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), timeout)
	if err != nil {
		return "", "", "", "", "", false, false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2200 * time.Millisecond))
	br := bufio.NewReader(conn)
	cseq := 0

	send := func(method, uri, authHdr string) (int, string, string, error) {
		cseq++
		var req bytes.Buffer
		fmt.Fprintf(&req, "%s %s RTSP/1.0\r\nCSeq: %d\r\nUser-Agent: FocusCamera/1.0\r\nAccept: application/sdp\r\n", method, uri, cseq)
		if authHdr != "" {
			fmt.Fprintf(&req, "Authorization: %s\r\n", authHdr)
		}
		req.WriteString("\r\n")
		if _, err := conn.Write(req.Bytes()); err != nil {
			return 0, "", "", err
		}
		statusLine, err := br.ReadString('\n')
		if err != nil {
			return 0, "", "", err
		}
		statusLine = strings.TrimSpace(statusLine)
		if !strings.HasPrefix(statusLine, "RTSP/1.0 ") {
			return 0, "", "", fmt.Errorf("not rtsp")
		}
		parts := strings.SplitN(statusLine, " ", 3)
		code := 0
		if len(parts) >= 2 {
			code, _ = strconv.Atoi(parts[1])
		}
		var wwwAuth, banner string
		contentLen := 0
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				break
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			banner += " " + line
			if kv := strings.SplitN(line, ":", 2); len(kv) == 2 {
				k := strings.ToLower(strings.TrimSpace(kv[0]))
				v := strings.TrimSpace(kv[1])
				if k == "www-authenticate" && wwwAuth == "" {
					wwwAuth = v
				} else if k == "content-length" {
					contentLen, _ = strconv.Atoi(v)
				}
			}
		}
		if contentLen > 0 && contentLen < 65536 {
			_, _ = io.CopyN(io.Discard, br, int64(contentLen))
		}
		return code, wwwAuth, banner, nil
	}

	vendor = knownVendor
	candidateVendors := []string{"tplink", "hikvision", "dahua", "uniview", "reolink"}
	if vendor != "" && vendor != "onvif" {
		candidateVendors = append([]string{vendor}, candidateVendors...)
	}

	uri0 := fmt.Sprintf("rtsp://%s:%d/", ip, port)
	if code, wwwAuth, banner, err := send("OPTIONS", uri0, ""); err == nil && code > 0 {
		validRTSP = true
		v, m := inferVendorFromRTSPBanner(banner + " " + wwwAuth)
		if vendor == "" || vendor == "onvif" {
			vendor = v
			model = m
		}
	}

	seenPaths := make(map[string]bool)
	for _, v := range candidateVendors {
		mp, sp := config.DefaultStreamPathsForVendor(v)
		if seenPaths[mp] {
			continue
		}
		seenPaths[mp] = true
		uri := fmt.Sprintf("rtsp://%s:%d%s", ip, port, mp)
		code, wwwAuth, banner, err := send("DESCRIBE", uri, "")
		if err != nil {
			continue
		}
		validRTSP = true
		if vendor == "" || vendor == "onvif" {
			iv, im := inferVendorFromRTSPBanner(banner + " " + wwwAuth)
			if iv != "onvif" {
				vendor = iv
				model = im
			}
		}
		if code == 200 {
			if vendor == "" || vendor == "onvif" {
				vendor = v
			}
			if model == "" {
				model = strings.ToUpper(vendor) + " IPC"
			}
			return vendor, model, mp, sp, "", true, true
		}
		if code == 401 && wwwAuth != "" {
			for _, pwd := range passwords {
				authHdr := buildRTSPAuthHeader("DESCRIBE", uri, wwwAuth, username, pwd)
				if authHdr == "" {
					continue
				}
				c2, _, _, err2 := send("DESCRIBE", uri, authHdr)
				if err2 != nil {
					break
				}
				if c2 == 200 {
					if vendor == "" || vendor == "onvif" {
						vendor = v
					}
					if model == "" {
						model = strings.ToUpper(vendor) + " IPC"
					}
					return vendor, model, mp, sp, pwd, true, true
				}
			}
			if mainPath == "" {
				mainPath = mp
				subPath = sp
			}
		}
	}

	if vendor == "" {
		vendor = "onvif"
	}
	if model == "" {
		model = "RTSP IPC"
	}
	if mainPath == "" {
		mainPath, subPath = config.DefaultStreamPathsForVendor(vendor)
	}
	return vendor, model, mainPath, subPath, "", false, validRTSP
}

func ProbeTPLinkCameraDetail(ip, username string, candidatePasswords []string, includeEmptyFallback bool, timeout time.Duration) (ProbeResult, bool) {
	if username == "" {
		username = "admin"
	}
	var (
		talkOpen bool
		rtspOpen bool
		rtspPort = 554
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
			rtspPort = 554
		} else if conn2, err2 := net.DialTimeout("tcp", fmt.Sprintf("%s:8554", ip), timeout/2); err2 == nil {
			_ = conn2.Close()
			rtspOpen = true
			rtspPort = 8554
		}
	}()
	pWg.Wait()

	if !talkOpen && !rtspOpen {
		return ProbeResult{}, false
	}

	primaryPwd := ""
	for _, p := range candidatePasswords {
		if p != "" {
			primaryPwd = p
			break
		}
	}

	fallbackRes := func(httpOpen bool) ProbeResult {
		name := fmt.Sprintf("摄像头 (%s)", ip)
		grp, order := config.InferGroupAndOrder(name, ip)
		return ProbeResult{
			Camera: config.Camera{
				ID:           config.MakeID(ip),
				Name:         name,
				Group:        grp,
				IP:           ip,
				Username:     username,
				Password:     primaryPwd,
				RTSPPort:     rtspPort,
				TalkPort:     8800,
				Vendor:       "tplink",
				Model:        "TL-IPC42A",
				SupportsTalk: talkOpen,
				SortOrder:    order,
			},
			AuthOK:   false,
			TalkOpen: talkOpen,
			RTSPOpen: rtspOpen,
			HTTPOpen: httpOpen,
		}
	}

	client := &http.Client{Timeout: 2500 * time.Millisecond}
	passwordsToTry := dedupPasswords(candidatePasswords, includeEmptyFallback)
	stok, matchedPwd, httpOpen := loginTPLinkHTTP(client, ip, username, passwordsToTry)
	if stok == "" {
		if talkOpen {
			return fallbackRes(httpOpen), true
		}
		// Standard non-TP-LINK RTSP / ONVIF camera probe
		onvifVendor, onvifModel, onvifPwd, onvifAuthOK, onvifHTTP := probeONVIFDevice(client, ip, username, passwordsToTry)
		if onvifHTTP {
			httpOpen = true
		}
		rtspVendor, rtspModel, mainPath, subPath, rtspPwd, rtspAuthOK, validRTSP := probeStandardRTSP(ip, rtspPort, username, passwordsToTry, onvifVendor, timeout)
		if !validRTSP && !onvifAuthOK {
			return ProbeResult{}, false
		}
		vendor := onvifVendor
		if vendor == "" || vendor == "onvif" {
			vendor = rtspVendor
		}
		model := onvifModel
		if model == "" {
			model = rtspModel
		}
		finalPwd := primaryPwd
		authOK := false
		if rtspAuthOK {
			finalPwd = rtspPwd
			authOK = true
		} else if onvifAuthOK {
			finalPwd = onvifPwd
			authOK = true
		}
		name := fmt.Sprintf("摄像头 (%s)", ip)
		if model != "" && model != "RTSP IPC" {
			name = fmt.Sprintf("%s (%s)", model, ip)
		}
		grp, order := config.InferGroupAndOrder(name, ip)
		return ProbeResult{
			Camera: config.Camera{
				ID:             config.MakeID(ip),
				Name:           name,
				Group:          grp,
				IP:             ip,
				Username:       username,
				Password:       finalPwd,
				NoPassword:     authOK && finalPwd == "",
				RTSPPort:       rtspPort,
				TalkPort:       0,
				Vendor:         vendor,
				MainStreamPath: mainPath,
				SubStreamPath:  subPath,
				Model:          model,
				SupportsTalk:   false,
				SupportsPTZ:    config.InferSupportsPTZFromModel(model),
				SortOrder:      order,
			},
			AuthOK:   authOK,
			TalkOpen: false,
			RTSPOpen: rtspOpen,
			HTTPOpen: httpOpen,
		}, true
	}

	dsBody, _ := json.Marshal(map[string]any{
		"method":      "get",
		"device_info": map[string]any{"name": []string{"info"}},
		"system":      map[string]any{"name": []string{"sys"}},
		"OSD":         map[string]any{"table": []string{"label_info"}},
		"video":       map[string]any{"name": []string{"main", "minor"}},
		"image":       map[string]any{"name": []string{"switch"}},
	})
	dsResp, err := client.Post(fmt.Sprintf("http://%s/stok=%s/ds", ip, stok), "application/json", bytes.NewReader(dsBody))
	if err != nil {
		res := fallbackRes(true)
		res.AuthOK = true
		res.Camera.Password = matchedPwd
		res.Camera.NoPassword = (matchedPwd == "")
		return res, true
	}
	defer dsResp.Body.Close()
	dsRaw, err := io.ReadAll(dsResp.Body)
	if err != nil {
		res := fallbackRes(true)
		res.AuthOK = true
		res.Camera.Password = matchedPwd
		res.Camera.NoPassword = (matchedPwd == "")
		return res, true
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
		Image struct {
			Switch struct {
				FlipType string `json:"flip_type"`
			} `json:"switch"`
		} `json:"image"`
	}
	_ = json.Unmarshal(dsRaw, &ds)
	flipType := strings.ToLower(strings.TrimSpace(ds.Image.Switch.FlipType))

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
		if setResp, err := client.Post(fmt.Sprintf("http://%s/stok=%s/ds", ip, stok), "application/json", bytes.NewReader(setBody)); err == nil {
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
	if name == "" || name == "IPC" || strings.HasPrefix(name, "TL-IPC") || strings.HasPrefix(name, "MIPC") {
		name = fmt.Sprintf("摄像头 (%s)", ip)
	}

	detectedVendor := config.NormalizeVendor("", model)
	if detectedVendor == "" {
		detectedVendor = "tplink"
	}

	grp, order := config.InferGroupAndOrder(name, ip)
	return ProbeResult{
		Camera: config.Camera{
			ID:             config.MakeID(ip),
			Name:           name,
			Group:          grp,
			IP:             ip,
			Username:       username,
			Password:       matchedPwd,
			NoPassword:     matchedPwd == "",
			RTSPPort:       rtspPort,
			TalkPort:       8800,
			Vendor:         detectedVendor,
			MainStreamPath: "/stream1",
			SubStreamPath:  "/stream2",
			Model:          model,
			FlipType:       flipType,
			SupportsTalk:   true,
			SupportsPTZ:    config.InferSupportsPTZFromModel(model),
			SortOrder:      order,
		},
		AuthOK:   true,
		TalkOpen: true,
		RTSPOpen: rtspOpen,
		HTTPOpen: true,
	}, true
}

func ProbeTPLinkCamera(ip, username, password string, timeout time.Duration) (config.Camera, bool) {
	res, ok := ProbeTPLinkCameraDetail(ip, username, []string{password}, true, timeout)
	if !ok {
		return config.Camera{}, false
	}
	return res.Camera, true
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

func ScanSubnetDetailed(subnetPrefix, username string, defaultPasswords []string, perIPPasswords map[string]string) []ProbeResult {
	subnetPrefix = NormalizeSubnetPrefix(subnetPrefix)
	if subnetPrefix == "" {
		subnetPrefix = "192.168.1"
	}
	var (
		mu    sync.Mutex
		found []ProbeResult
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

			cands := make([]string, 0, len(defaultPasswords)+2)
			if perIPPasswords != nil {
				if ipPwd, ok := perIPPasswords[targetIP]; ok {
					cands = append(cands, ipPwd)
				}
			}
			cands = append(cands, defaultPasswords...)

			if res, ok := ProbeTPLinkCameraDetail(targetIP, username, cands, true, 1000*time.Millisecond); ok {
				mu.Lock()
				found = append(found, res)
				mu.Unlock()
			}
		}(ip)
	}
	wg.Wait()

	// Sort by camera SortOrder / Name
	for i := 0; i < len(found); i++ {
		for j := i + 1; j < len(found); j++ {
			a, b := found[i].Camera, found[j].Camera
			swap := false
			if a.SortOrder != b.SortOrder {
				swap = b.SortOrder < a.SortOrder
			} else {
				swap = b.Name < a.Name
			}
			if swap {
				found[i], found[j] = found[j], found[i]
			}
		}
	}
	return found
}

func ScanSubnet(subnetPrefix, username, password string) []config.Camera {
	detailed := ScanSubnetDetailed(subnetPrefix, username, []string{password}, nil)
	out := make([]config.Camera, 0, len(detailed))
	for _, r := range detailed {
		out = append(out, r.Camera)
	}
	return out
}

func RebootTPLinkCamera(ip, username, password string) error {
	if username == "" {
		username = "admin"
	}
	client := &http.Client{Timeout: 4 * time.Second}
	stok, _, httpOpen := loginTPLinkHTTP(client, ip, username, dedupPasswords([]string{password}, true))
	if stok != "" {
		rbBody := []byte(`{"method":"do","system":{"reboot":null}}`)
		rbResp, err := client.Post(fmt.Sprintf("http://%s/stok=%s/ds", ip, stok), "application/json", bytes.NewReader(rbBody))
		if err != nil {
			return fmt.Errorf("发送重启指令失败: %w", err)
		}
		defer rbResp.Body.Close()
		return nil
	}

	// Fallback: try standard ONVIF SystemReboot
	soapReq := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl"><s:Header>` +
		buildWSUsernameToken(username, password) +
		`</s:Header><s:Body><tds:SystemReboot/></s:Body></s:Envelope>`
	for _, ep := range []string{
		fmt.Sprintf("http://%s/onvif/device_service", ip),
		fmt.Sprintf("http://%s:2020/onvif/device_service", ip),
		fmt.Sprintf("http://%s:8000/onvif/device_service", ip),
	} {
		req, err := http.NewRequest("POST", ep, strings.NewReader(soapReq))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
		resp, err := client.Do(req)
		if err == nil {
			httpOpen = true
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 || strings.Contains(string(raw), "SystemRebootResponse") {
				return nil
			}
		}
	}

	if !httpOpen {
		return fmt.Errorf("连接摄像头管理接口 (%s) 失败", ip)
	}
	return fmt.Errorf("摄像头管理鉴权失败或该型号不支持远程重启")
}

func EnsureH264Video(ip, username, password string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	stok, _, _ := loginTPLinkHTTP(client, ip, username, dedupPasswords([]string{password}, true))
	if stok == "" {
		return fmt.Errorf("login failed")
	}

	setBody, _ := json.Marshal(map[string]any{
		"method": "set",
		"video": map[string]any{
			"main":  map[string]any{"encode_type": "H264"},
			"minor": map[string]any{"encode_type": "H264"},
		},
	})
	setResp, err := client.Post(fmt.Sprintf("http://%s/stok=%s/ds", ip, stok), "application/json", bytes.NewReader(setBody))
	if err != nil {
		return err
	}
	defer setResp.Body.Close()
	return nil
}

type NetworkConfig struct {
	Proto   string `json:"proto"`   // "static" | "dhcp"
	IPAddr  string `json:"ipaddr"`  // e.g. "192.168.1.233"
	Netmask string `json:"netmask"` // e.g. "255.255.255.0"
	Gateway string `json:"gateway"` // e.g. "192.168.1.1"
	MAC     string `json:"mac,omitempty"`
	DNS     string `json:"dns,omitempty"`
}

func InferGatewayAndNetmask(ip string) (gateway, netmask string) {
	netmask = "255.255.255.0"
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed != nil {
		if ip4 := parsed.To4(); ip4 != nil {
			gateway = fmt.Sprintf("%d.%d.%d.1", ip4[0], ip4[1], ip4[2])
			return gateway, netmask
		}
	}
	parts := strings.Split(strings.TrimSpace(ip), ".")
	if len(parts) >= 3 {
		gateway = fmt.Sprintf("%s.%s.%s.1", parts[0], parts[1], parts[2])
	} else {
		gateway = "192.168.1.1"
	}
	return gateway, netmask
}

func normalizeMAC(raw string) string {
	s, err := url.QueryUnescape(strings.TrimSpace(raw))
	if err != nil || s == "" {
		s = strings.TrimSpace(raw)
	}
	s = strings.ToUpper(strings.ReplaceAll(s, "-", ":"))
	return s
}

func prefixLengthToNetmask(prefixLen int) string {
	if prefixLen <= 0 || prefixLen > 32 {
		return "255.255.255.0"
	}
	mask := net.CIDRMask(prefixLen, 32)
	return fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
}

func netmaskToPrefixLength(netmask string) int {
	ip := net.ParseIP(strings.TrimSpace(netmask))
	if ip == nil {
		return 24
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return 24
	}
	ones, _ := net.IPv4Mask(ip4[0], ip4[1], ip4[2], ip4[3]).Size()
	if ones <= 0 {
		return 24
	}
	return ones
}

func extractXMLAttr(xmlStr, tag, attr string) string {
	for _, prefix := range []string{"tds:", "tt:", "trt:", ""} {
		openTag := "<" + prefix + tag
		i := strings.Index(xmlStr, openTag)
		if i < 0 {
			continue
		}
		gt := strings.Index(xmlStr[i:], ">")
		if gt < 0 {
			continue
		}
		header := xmlStr[i : i+gt]
		key := attr + `="`
		kIdx := strings.Index(header, key)
		if kIdx < 0 {
			continue
		}
		rest := header[kIdx+len(key):]
		end := strings.IndexByte(rest, '"')
		if end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	return ""
}

func GetCameraNetworkConfig(ip, username, password string) (NetworkConfig, error) {
	ip = strings.TrimSpace(ip)
	if username == "" {
		username = "admin"
	}
	defGw, defMask := InferGatewayAndNetmask(ip)
	fallback := NetworkConfig{
		Proto:   "static",
		IPAddr:  ip,
		Netmask: defMask,
		Gateway: defGw,
	}

	client := &http.Client{Timeout: 3 * time.Second}
	candidates := dedupPasswords([]string{password}, true)
	stok, _, httpOpen := loginTPLinkHTTP(client, ip, username, candidates)
	if stok != "" {
		dsBody, _ := json.Marshal(map[string]any{
			"method": "get",
			"network": map[string]any{
				"name": []string{"wan", "wan_status"},
			},
		})
		dsResp, err := client.Post(fmt.Sprintf("http://%s/stok=%s/ds", ip, stok), "application/json", bytes.NewReader(dsBody))
		if err == nil {
			raw, _ := io.ReadAll(dsResp.Body)
			_ = dsResp.Body.Close()
			raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
			var resp struct {
				ErrorCode int `json:"error_code"`
				Network   struct {
					Wan struct {
						WanType string `json:"wan_type"`
						Proto   string `json:"proto"`
						IPAddr  string `json:"ipaddr"`
						Netmask string `json:"netmask"`
						Gateway string `json:"gateway"`
						MacAddr string `json:"macaddr"`
						DNS     string `json:"dns"`
					} `json:"wan"`
					WanStatus struct {
						Proto   string `json:"proto"`
						IPAddr  string `json:"ipaddr"`
						Netmask string `json:"netmask"`
						Gateway string `json:"gateway"`
						Mac     string `json:"mac"`
						PriDNS  string `json:"pri_dns"`
						SndDNS  string `json:"snd_dns"`
					} `json:"wan_status"`
				} `json:"network"`
			}
			if json.Unmarshal(raw, &resp) == nil && resp.ErrorCode == 0 {
				proto := strings.ToLower(strings.TrimSpace(resp.Network.Wan.Proto))
				if proto == "" {
					proto = strings.ToLower(strings.TrimSpace(resp.Network.Wan.WanType))
				}
				if proto == "" {
					proto = strings.ToLower(strings.TrimSpace(resp.Network.WanStatus.Proto))
				}
				if proto != "dhcp" {
					proto = "static"
				}
				ipAddr := strings.TrimSpace(resp.Network.Wan.IPAddr)
				if ipAddr == "" {
					ipAddr = strings.TrimSpace(resp.Network.WanStatus.IPAddr)
				}
				if ipAddr == "" {
					ipAddr = ip
				}
				netmask := strings.TrimSpace(resp.Network.Wan.Netmask)
				if netmask == "" {
					netmask = strings.TrimSpace(resp.Network.WanStatus.Netmask)
				}
				if netmask == "" {
					netmask = defMask
				}
				gateway := strings.TrimSpace(resp.Network.Wan.Gateway)
				if gateway == "" {
					gateway = strings.TrimSpace(resp.Network.WanStatus.Gateway)
				}
				if gateway == "" || gateway == "0.0.0.0" {
					gateway = defGw
				}
				mac := normalizeMAC(resp.Network.Wan.MacAddr)
				if mac == "" {
					mac = normalizeMAC(resp.Network.WanStatus.Mac)
				}
				dns, _ := url.QueryUnescape(strings.TrimSpace(resp.Network.Wan.DNS))
				if dns == "" && resp.Network.WanStatus.PriDNS != "" {
					dns = strings.TrimSpace(resp.Network.WanStatus.PriDNS + " " + resp.Network.WanStatus.SndDNS)
				}
				return NetworkConfig{
					Proto:   proto,
					IPAddr:  ipAddr,
					Netmask: netmask,
					Gateway: gateway,
					MAC:     mac,
					DNS:     dns,
				}, nil
			}
		}
	}

	// Fallback: Query ONVIF GetNetworkInterfaces + GetNetworkDefaultGateway
	endpoints := []string{
		fmt.Sprintf("http://%s:2020/onvif/device_service", ip),
		fmt.Sprintf("http://%s/onvif/device_service", ip),
		fmt.Sprintf("http://%s:8000/onvif/device_service", ip),
		fmt.Sprintf("http://%s:8080/onvif/device_service", ip),
	}
	for _, ep := range endpoints {
		for _, pwd := range candidates {
			secHdr := "<s:Header>" + buildWSUsernameToken(username, pwd) + "</s:Header>"
			soapIf := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">` +
				secHdr + `<s:Body><tds:GetNetworkInterfaces/></s:Body></s:Envelope>`
			req, err := http.NewRequest("POST", ep, strings.NewReader(soapIf))
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
			if !strings.Contains(bodyStr, "GetNetworkInterfacesResponse") {
				continue
			}
			dhcpStr := strings.ToLower(extractXMLTag(bodyStr, "DHCP"))
			proto := "static"
			if dhcpStr == "true" {
				proto = "dhcp"
			}
			addr := extractXMLTag(bodyStr, "Address")
			if addr == "" {
				addr = ip
			}
			prefixLen, _ := strconv.Atoi(extractXMLTag(bodyStr, "PrefixLength"))
			netmask := prefixLengthToNetmask(prefixLen)
			mac := normalizeMAC(extractXMLTag(bodyStr, "HwAddress"))
			gateway := defGw

			// Also try GetNetworkDefaultGateway
			soapGw := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">` +
				secHdr + `<s:Body><tds:GetNetworkDefaultGateway/></s:Body></s:Envelope>`
			if gwReq, err := http.NewRequest("POST", ep, strings.NewReader(soapGw)); err == nil {
				gwReq.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
				if gwResp, err := client.Do(gwReq); err == nil {
					gwRaw, _ := io.ReadAll(gwResp.Body)
					_ = gwResp.Body.Close()
					if gwIP := extractXMLTag(string(gwRaw), "IPv4Address"); gwIP != "" && gwIP != "0.0.0.0" {
						gateway = gwIP
					}
				}
			}

			return NetworkConfig{
				Proto:   proto,
				IPAddr:  addr,
				Netmask: netmask,
				Gateway: gateway,
				MAC:     mac,
			}, nil
		}
	}

	if !httpOpen {
		return fallback, fmt.Errorf("无法连接摄像头管理端口 (%s)", ip)
	}
	return fallback, fmt.Errorf("摄像头管理鉴权未通过，请检查账号密码")
}

// CheckIPConflict probes targetIP on common camera/HTTP ports to prevent setting an IP that is already in use.
func CheckIPConflict(targetIP string, allowMAC string, username string, passwords []string) (conflict bool, reason string) {
	targetIP = strings.TrimSpace(targetIP)
	if targetIP == "" {
		return false, ""
	}
	ports := []int{80, 554, 8800, 2020, 8000, 8554}
	var openPort int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range ports {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", targetIP, port), 550*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				mu.Lock()
				if openPort == 0 {
					openPort = port
				}
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()

	if openPort == 0 {
		return false, ""
	}

	// If allowMAC is provided, check whether the device already at targetIP is the exact same camera MAC
	if allowMAC != "" {
		pwd := ""
		if len(passwords) > 0 {
			pwd = passwords[0]
		}
		if netCfg, err := GetCameraNetworkConfig(targetIP, username, pwd); err == nil && netCfg.MAC != "" {
			if strings.EqualFold(normalizeMAC(netCfg.MAC), normalizeMAC(allowMAC)) {
				return false, ""
			}
		}
	}

	return true, fmt.Sprintf("目标 IP %s (端口 :%d) 已被局域网其他设备占用，请更换其他空闲 IP", targetIP, openPort)
}

func waitForCameraOnline(ip string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	ports := []int{554, 8800, 80, 2020, 8554}
	for time.Now().Before(deadline) {
		for _, p := range ports {
			if conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, p), 400*time.Millisecond); err == nil {
				_ = conn.Close()
				return true
			}
		}
		time.Sleep(350 * time.Millisecond)
	}
	return false
}

// SetCameraNetworkConfig modifies the camera's hardware network configuration (Static IP / DHCP, Netmask, Gateway)
// via TP-LINK / Mercury HTTP ds API or standard ONVIF SetNetworkInterfaces.
func SetCameraNetworkConfig(oldIP, username, password string, target NetworkConfig) (NetworkConfig, error) {
	oldIP = strings.TrimSpace(oldIP)
	if oldIP == "" {
		return NetworkConfig{}, fmt.Errorf("原摄像头 IP 不能为空")
	}
	if username == "" {
		username = "admin"
	}
	proto := strings.ToLower(strings.TrimSpace(target.Proto))
	if proto != "dhcp" {
		proto = "static"
	}
	targetIP := strings.TrimSpace(target.IPAddr)
	if proto == "static" {
		parsed := net.ParseIP(targetIP)
		if parsed == nil || parsed.To4() == nil {
			return NetworkConfig{}, fmt.Errorf("新 IP 地址格式无效: %s", targetIP)
		}
	} else if targetIP == "" {
		targetIP = oldIP
	}

	defGw, defMask := InferGatewayAndNetmask(targetIP)
	netmask := strings.TrimSpace(target.Netmask)
	if netmask == "" {
		netmask = defMask
	}
	if ip := net.ParseIP(netmask); ip == nil || ip.To4() == nil {
		return NetworkConfig{}, fmt.Errorf("子网掩码格式无效: %s", netmask)
	}
	gateway := strings.TrimSpace(target.Gateway)
	if gateway == "" {
		gateway = defGw
	}
	if ip := net.ParseIP(gateway); ip == nil || ip.To4() == nil {
		return NetworkConfig{}, fmt.Errorf("默认网关格式无效: %s", gateway)
	}

	finalCfg := NetworkConfig{
		Proto:   proto,
		IPAddr:  targetIP,
		Netmask: netmask,
		Gateway: gateway,
		MAC:     target.MAC,
		DNS:     target.DNS,
	}

	client := &http.Client{Timeout: 4 * time.Second}
	candidates := dedupPasswords([]string{password}, true)
	stok, _, httpOpen := loginTPLinkHTTP(client, oldIP, username, candidates)
	if stok != "" {
		wanPayload := map[string]any{
			"wan_type": proto,
			"proto":    proto,
		}
		if proto == "static" {
			wanPayload["ipaddr"] = targetIP
			wanPayload["netmask"] = netmask
			wanPayload["gateway"] = gateway
		}
		setBody, _ := json.Marshal(map[string]any{
			"method": "set",
			"network": map[string]any{
				"wan": wanPayload,
			},
		})
		setResp, err := client.Post(fmt.Sprintf("http://%s/stok=%s/ds", oldIP, stok), "application/json", bytes.NewReader(setBody))
		if err == nil {
			raw, _ := io.ReadAll(setResp.Body)
			_ = setResp.Body.Close()
			raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
			var rObj struct {
				ErrorCode int `json:"error_code"`
			}
			if json.Unmarshal(raw, &rObj) == nil && rObj.ErrorCode != 0 {
				return finalCfg, fmt.Errorf("摄像头硬件拒绝修改网络配置 (错误码: %d)", rObj.ErrorCode)
			}
		} else if proto == "static" && targetIP == oldIP {
			return finalCfg, fmt.Errorf("发送网络配置指令失败: %w", err)
		}

		if proto == "static" && targetIP != oldIP {
			// Wait up to 9 seconds for the camera's network interface to come online at targetIP
			waitForCameraOnline(targetIP, 9*time.Second)
		}
		return finalCfg, nil
	}

	// Fallback: Standard ONVIF SetNetworkInterfaces + SetNetworkDefaultGateway
	endpoints := []string{
		fmt.Sprintf("http://%s:2020/onvif/device_service", oldIP),
		fmt.Sprintf("http://%s/onvif/device_service", oldIP),
		fmt.Sprintf("http://%s:8000/onvif/device_service", oldIP),
		fmt.Sprintf("http://%s:8080/onvif/device_service", oldIP),
	}
	prefixLen := netmaskToPrefixLength(netmask)

	for _, ep := range endpoints {
		for _, pwd := range candidates {
			secHdr := "<s:Header>" + buildWSUsernameToken(username, pwd) + "</s:Header>"
			soapIf := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">` +
				secHdr + `<s:Body><tds:GetNetworkInterfaces/></s:Body></s:Envelope>`
			req, err := http.NewRequest("POST", ep, strings.NewReader(soapIf))
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
			if !strings.Contains(bodyStr, "GetNetworkInterfacesResponse") {
				continue
			}

			ifToken := extractXMLAttr(bodyStr, "NetworkInterfaces", "token")
			if ifToken == "" {
				ifToken = "eth0"
			}

			// 1. Set Default Gateway if static
			if proto == "static" && gateway != "" {
				secHdrGw := "<s:Header>" + buildWSUsernameToken(username, pwd) + "</s:Header>"
				soapGw := fmt.Sprintf(
					`<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">%s<s:Body><tds:SetNetworkDefaultGateway><tds:IPv4Address>%s</tds:IPv4Address></tds:SetNetworkDefaultGateway></s:Body></s:Envelope>`,
					secHdrGw, gateway,
				)
				if gwReq, err := http.NewRequest("POST", ep, strings.NewReader(soapGw)); err == nil {
					gwReq.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
					if gwResp, err := client.Do(gwReq); err == nil {
						_ = gwResp.Body.Close()
					}
				}
			}

			// 2. Set Network Interface IPv4
			secHdrSet := "<s:Header>" + buildWSUsernameToken(username, pwd) + "</s:Header>"
			var ipv4Body string
			if proto == "dhcp" {
				ipv4Body = `<tt:Enabled>true</tt:Enabled><tt:DHCP>true</tt:DHCP>`
			} else {
				ipv4Body = fmt.Sprintf(
					`<tt:Enabled>true</tt:Enabled><tt:Manual><tt:Address>%s</tt:Address><tt:PrefixLength>%d</tt:PrefixLength></tt:Manual><tt:DHCP>false</tt:DHCP>`,
					targetIP, prefixLen,
				)
			}
			soapSet := fmt.Sprintf(
				`<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">%s<s:Body><tds:SetNetworkInterfaces><tds:InterfaceToken>%s</tds:InterfaceToken><tds:NetworkInterface><tt:Enabled>true</tt:Enabled><tt:IPv4>%s</tt:IPv4></tds:NetworkInterface></tds:SetNetworkInterfaces></s:Body></s:Envelope>`,
				secHdrSet, ifToken, ipv4Body,
			)
			setReq, err := http.NewRequest("POST", ep, strings.NewReader(soapSet))
			if err != nil {
				continue
			}
			setReq.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
			setResp, err := client.Do(setReq)
			if err == nil {
				sRaw, _ := io.ReadAll(setResp.Body)
				_ = setResp.Body.Close()
				sStr := string(sRaw)
				if setResp.StatusCode == 200 || strings.Contains(sStr, "SetNetworkInterfacesResponse") {
					if strings.Contains(sStr, "<tds:RebootNeeded>true</tds:RebootNeeded>") || strings.Contains(sStr, "<RebootNeeded>true</RebootNeeded>") {
						_ = RebootTPLinkCamera(oldIP, username, pwd)
					}
					if proto == "static" && targetIP != oldIP {
						waitForCameraOnline(targetIP, 9*time.Second)
					}
					return finalCfg, nil
				}
			} else if proto == "static" && targetIP != oldIP {
				// Connection may reset immediately when ONVIF device changes its IP
				if waitForCameraOnline(targetIP, 9*time.Second) {
					return finalCfg, nil
				}
			}
		}
	}

	if !httpOpen {
		return finalCfg, fmt.Errorf("无法连接原摄像头 (%s) 管理接口，请确认设备在线", oldIP)
	}
	return finalCfg, fmt.Errorf("摄像头管理鉴权失败或该型号不支持远程修改 IP")
}
