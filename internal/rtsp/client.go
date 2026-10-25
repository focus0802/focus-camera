package rtsp

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"focus-camera/internal/config"
	"focus-camera/internal/discovery"
)

const (
	MsgTypeCodecConfig = 0x01
	MsgTypeVideoFrame  = 0x02
	MsgTypeAudioPCMA   = 0x03
	MsgTypeStatusJSON  = 0x04
)

type Subscriber struct {
	ID        string
	WantAudio bool
	Ch        chan []byte
}

type StreamHub struct {
	mu      sync.Mutex
	store   *config.Store
	streams map[string]*cameraStream
}

func NewStreamHub(store *config.Store) *StreamHub {
	return &StreamHub{
		store:   store,
		streams: make(map[string]*cameraStream),
	}
}

func (h *StreamHub) Subscribe(camID string, streamType string, wantAudio bool) (*Subscriber, func(), error) {
	cam, ok := h.store.GetCamera(camID)
	if !ok {
		return nil, nil, fmt.Errorf("未找到摄像头: %s", camID)
	}
	if streamType == "main" || streamType == "stream1" {
		streamType = "main"
	} else {
		streamType = "sub"
	}
	key := cam.ID + ":" + streamType
	oppType := "sub"
	if streamType == "sub" {
		oppType = "main"
	}
	oppKey := cam.ID + ":" + oppType

	// If the opposite stream (sub vs main) on the same camera has 0 subscribers,
	// tear it down immediately so 2-session-limit cameras (like TL-IPC42A-4 6.0) never return 406.
	h.mu.Lock()
	var waitDone chan struct{}
	if opp, exists := h.streams[oppKey]; exists {
		if doneCh, stopped := opp.stopIfIdle(); stopped {
			delete(h.streams, oppKey)
			waitDone = doneCh
		}
	}
	h.mu.Unlock()

	if waitDone != nil {
		select {
		case <-waitDone:
		case <-time.After(400 * time.Millisecond):
		}
	}

	h.mu.Lock()
	cs, exists := h.streams[key]
	if !exists {
		cs = newCameraStream(cam, streamType, func() {
			h.mu.Lock()
			delete(h.streams, key)
			h.mu.Unlock()
		})
		h.streams[key] = cs
		go cs.run()
	}
	sub := cs.addSubscriber(wantAudio)
	h.mu.Unlock()

	unsubscribe := func() {
		cs.removeSubscriber(sub.ID)
	}
	return sub, unsubscribe, nil
}

func (h *StreamHub) SetSubscriberAudio(camID, streamType, subID string, wantAudio bool) {
	cam, ok := h.store.GetCamera(camID)
	if !ok {
		return
	}
	if streamType == "main" || streamType == "stream1" {
		streamType = "main"
	} else {
		streamType = "sub"
	}
	key := cam.ID + ":" + streamType
	h.mu.Lock()
	cs, exists := h.streams[key]
	h.mu.Unlock()
	if exists {
		cs.SetSubscriberAudio(subID, wantAudio)
	}
}

type StreamStats struct {
	CameraID    string `json:"camera_id"`
	StreamType  string `json:"stream_type"`
	Online      bool   `json:"online"`
	Subscribers int    `json:"subscribers"`
	LastError   string `json:"last_error,omitempty"`
}

func (h *StreamHub) GetActiveStats() map[string]StreamStats {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]StreamStats, len(h.streams))
	for k, cs := range h.streams {
		cs.mu.RLock()
		out[k] = StreamStats{
			CameraID:    cs.cam.ID,
			StreamType:  cs.streamType,
			Online:      cs.online,
			Subscribers: len(cs.subs),
			LastError:   cs.lastError,
		}
		cs.mu.RUnlock()
	}
	return out
}

type cameraStream struct {
	cam        config.Camera
	streamType string
	onShutdown func()

	mu          sync.RWMutex
	subs        map[string]*Subscriber
	nextSubID   uint64
	codecConfig []byte
	lastIDR     []byte
	online      bool
	lastError   string
	stopCh      chan struct{}
	doneCh      chan struct{}
	stopped     bool
}

func newCameraStream(cam config.Camera, streamType string, onShutdown func()) *cameraStream {
	return &cameraStream{
		cam:        cam,
		streamType: streamType,
		onShutdown: onShutdown,
		subs:       make(map[string]*Subscriber),
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

func (cs *cameraStream) stopIfIdle() (chan struct{}, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.subs) == 0 && !cs.stopped {
		cs.stopped = true
		close(cs.stopCh)
		return cs.doneCh, true
	}
	return nil, false
}

func (cs *cameraStream) addSubscriber(wantAudio bool) *Subscriber {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.nextSubID++
	sub := &Subscriber{
		ID:        fmt.Sprintf("sub-%d", cs.nextSubID),
		WantAudio: wantAudio,
		Ch:        make(chan []byte, 32),
	}
	cs.subs[sub.ID] = sub

	// Immediately push cached SPS/PPS config and latest IDR keyframe for instant startup!
	if len(cs.codecConfig) > 0 {
		sub.Ch <- cs.codecConfig
	}
	if len(cs.lastIDR) > 0 {
		sub.Ch <- cs.lastIDR
	}
	if cs.online {
		sub.Ch <- makeStatusPacket("online", "")
	} else if cs.lastError != "" {
		sub.Ch <- makeStatusPacket("connecting", cs.lastError)
	}
	return sub
}

func (cs *cameraStream) SetSubscriberAudio(subID string, wantAudio bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if sub, ok := cs.subs[subID]; ok {
		sub.WantAudio = wantAudio
	}
}

func (cs *cameraStream) removeSubscriber(id string) {
	cs.mu.Lock()
	if sub, ok := cs.subs[id]; ok {
		delete(cs.subs, id)
		close(sub.Ch)
	}
	empty := len(cs.subs) == 0
	cs.mu.Unlock()

	if empty {
		grace := 2500 * time.Millisecond
		if cs.streamType == "main" {
			grace = 500 * time.Millisecond
		}
		go func() {
			time.Sleep(grace)
			cs.mu.Lock()
			if len(cs.subs) == 0 && !cs.stopped {
				cs.stopped = true
				close(cs.stopCh)
				cs.mu.Unlock()
				cs.onShutdown()
				return
			}
			cs.mu.Unlock()
		}()
	}
}

func makeStatusPacket(status, errMsg string) []byte {
	b, _ := json.Marshal(map[string]string{
		"status": status,
		"error":  errMsg,
	})
	out := make([]byte, 1+len(b))
	out[0] = MsgTypeStatusJSON
	copy(out[1:], b)
	return out
}

func (cs *cameraStream) broadcastStatus(status, errMsg string) {
	pkt := makeStatusPacket(status, errMsg)
	cs.mu.Lock()
	cs.online = (status == "online")
	cs.lastError = errMsg
	for _, sub := range cs.subs {
		select {
		case sub.Ch <- pkt:
		default:
		}
	}
	cs.mu.Unlock()
}

func (cs *cameraStream) broadcastVideo(pkt []byte, isKey bool, newConfig []byte) {
	cs.mu.Lock()
	if len(newConfig) > 0 {
		cs.codecConfig = newConfig
	}
	if isKey {
		cs.lastIDR = pkt
	}
	if !cs.online {
		cs.online = true
		cs.lastError = ""
	}
	cfgPkt := cs.codecConfig
	for _, sub := range cs.subs {
		if len(newConfig) > 0 {
			select {
			case sub.Ch <- cfgPkt:
			default:
			}
		}
		select {
		case sub.Ch <- pkt:
		default:
			// Zero-latency drop-frame policy: if subscriber queue is full, flush old frames and send latest keyframe
			if isKey {
				for len(sub.Ch) > 0 {
					<-sub.Ch
				}
				if len(cfgPkt) > 0 {
					select {
					case sub.Ch <- cfgPkt:
					default:
					}
				}
				select {
				case sub.Ch <- pkt:
				default:
				}
			}
		}
	}
	cs.mu.Unlock()
}

func (cs *cameraStream) broadcastAudio(pcma []byte) {
	pkt := make([]byte, 1+len(pcma))
	pkt[0] = MsgTypeAudioPCMA
	copy(pkt[1:], pcma)

	cs.mu.RLock()
	for _, sub := range cs.subs {
		if !sub.WantAudio {
			continue
		}
		select {
		case sub.Ch <- pkt:
		default:
		}
	}
	cs.mu.RUnlock()
}

var (
	dialSemaphore = make(chan struct{}, 8)
	dialPacerMu   sync.Mutex
	lastDialTime  time.Time
)

func paceRTSPHandshake() func() {
	dialSemaphore <- struct{}{}
	dialPacerMu.Lock()
	now := time.Now()
	if wait := 20*time.Millisecond - now.Sub(lastDialTime); wait > 0 {
		time.Sleep(wait)
	}
	lastDialTime = time.Now()
	dialPacerMu.Unlock()
	return func() {
		<-dialSemaphore
	}
}

func (cs *cameraStream) run() {
	defer close(cs.doneCh)
	consecutive406 := 0
	for {
		select {
		case <-cs.stopCh:
			return
		default:
		}

		startAt := time.Now()
		err := cs.streamOnce()
		select {
		case <-cs.stopCh:
			return
		default:
		}
		if time.Since(startAt) > 3*time.Second {
			consecutive406 = 0
		}
		retryWait := 2 * time.Second
		if err != nil {
			errMsg := err.Error()
			if strings.Contains(errMsg, "已自动切换为 H.264") {
				retryWait = 350 * time.Millisecond
				consecutive406 = 0
			} else if strings.Contains(errMsg, "406") {
				consecutive406++
				switch consecutive406 {
				case 1:
					retryWait = 350 * time.Millisecond
				case 2:
					retryWait = 700 * time.Millisecond
				default:
					retryWait = 4 * time.Second
					cs.broadcastStatus("reconnecting", errMsg)
				}
			} else {
				consecutive406 = 0
				cs.broadcastStatus("reconnecting", errMsg)
			}
		}
		select {
		case <-cs.stopCh:
			return
		case <-time.After(retryWait):
		}
	}
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func buildDigestAuth(method, uri, wwwAuth, user, pass string) string {
	realm := extractAttr(wwwAuth, "realm")
	nonce := extractAttr(wwwAuth, "nonce")
	if realm == "" || nonce == "" {
		return ""
	}
	ha1 := md5Hex(fmt.Sprintf("%s:%s:%s", user, realm, pass))
	ha2 := md5Hex(fmt.Sprintf("%s:%s", method, uri))
	resp := md5Hex(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2))
	return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`, user, realm, nonce, uri, resp)
}

func extractAttr(s, key string) string {
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

func buildAVCCPacket(sps, pps []byte) []byte {
	if len(sps) < 4 || len(pps) == 0 {
		return nil
	}
	// AVCDecoderConfigurationRecord
	avccLen := 11 + len(sps) + len(pps)
	out := make([]byte, 4+avccLen)
	out[0] = MsgTypeCodecConfig
	out[1] = sps[1] // profile
	out[2] = sps[2] // compat
	out[3] = sps[3] // level

	avcc := out[4:]
	avcc[0] = 0x01 // version 1
	avcc[1] = sps[1]
	avcc[2] = sps[2]
	avcc[3] = sps[3]
	avcc[4] = 0xFF // 4-byte NALU length size
	avcc[5] = 0xE1 // 1 SPS
	binary.BigEndian.PutUint16(avcc[6:8], uint16(len(sps)))
	copy(avcc[8:8+len(sps)], sps)
	off := 8 + len(sps)
	avcc[off] = 0x01 // 1 PPS
	binary.BigEndian.PutUint16(avcc[off+1:off+3], uint16(len(pps)))
	copy(avcc[off+3:], pps)
	return out
}

func (cs *cameraStream) streamOnce() error {
	port := cs.cam.RTSPPort
	if port == 0 {
		port = 554
	}
	streamPath := "stream2"
	if cs.streamType == "main" {
		streamPath = "stream1"
	}
	baseURI := fmt.Sprintf("rtsp://%s:%d/%s", cs.cam.IP, port, streamPath)

	releaseHandshake := paceRTSPHandshake()
	handshakeReleased := false
	finishHandshake := func() {
		if !handshakeReleased {
			handshakeReleased = true
			releaseHandshake()
		}
	}
	defer finishHandshake()

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", cs.cam.IP, port), 4*time.Second)
	if err != nil {
		return fmt.Errorf("连接 RTSP 失败: %w", err)
	}
	defer conn.Close()
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
		_ = tcpConn.SetReadBuffer(256 * 1024)
	}

	streamDone := make(chan struct{})
	defer close(streamDone)
	go func() {
		select {
		case <-cs.stopCh:
			_ = conn.SetReadDeadline(time.Now())
		case <-streamDone:
		}
	}()

	br := bufio.NewReaderSize(conn, 131072)
	cseq := 0
	var wwwAuth string
	var sessionID string

	sendRTSP := func(method, uri string, extraHeaders map[string]string) (int, map[string]string, []byte, error) {
		cseq++
		var req bytes.Buffer
		fmt.Fprintf(&req, "%s %s RTSP/1.0\r\n", method, uri)
		fmt.Fprintf(&req, "CSeq: %d\r\n", cseq)
		fmt.Fprintf(&req, "User-Agent: FocusCamera/1.0\r\n")
		if wwwAuth != "" {
			authHdr := buildDigestAuth(method, uri, wwwAuth, cs.cam.Username, cs.cam.Password)
			if authHdr != "" {
				fmt.Fprintf(&req, "Authorization: %s\r\n", authHdr)
			}
		}
		if sessionID != "" {
			fmt.Fprintf(&req, "Session: %s\r\n", sessionID)
		}
		for k, v := range extraHeaders {
			fmt.Fprintf(&req, "%s: %s\r\n", k, v)
		}
		req.WriteString("\r\n")

		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(req.Bytes()); err != nil {
			return 0, nil, nil, err
		}

		// Skip any early interleaved RTP/RTCP packets ($ + channel + length) before RTSP response line
		var statusLine string
		for {
			b0, err := br.ReadByte()
			if err != nil {
				return 0, nil, nil, err
			}
			if b0 == '$' {
				var iHdr [3]byte
				if _, err := io.ReadFull(br, iHdr[:]); err != nil {
					return 0, nil, nil, err
				}
				pktLen := int(binary.BigEndian.Uint16(iHdr[1:3]))
				if pktLen > 0 {
					if _, err := io.CopyN(io.Discard, br, int64(pktLen)); err != nil {
						return 0, nil, nil, err
					}
				}
				continue
			}
			restLine, err := br.ReadString('\n')
			if err != nil {
				return 0, nil, nil, err
			}
			statusLine = string(b0) + restLine
			break
		}

		parts := strings.SplitN(strings.TrimSpace(statusLine), " ", 3)
		if len(parts) < 2 {
			return 0, nil, nil, fmt.Errorf("无效 RTSP 状态行: %s", statusLine)
		}
		code, _ := strconv.Atoi(parts[1])
		headers := make(map[string]string)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return 0, nil, nil, err
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if kv := strings.SplitN(line, ":", 2); len(kv) == 2 {
				headers[strings.ToLower(strings.TrimSpace(kv[0]))] = strings.TrimSpace(kv[1])
			}
		}
		var body []byte
		if clStr, ok := headers["content-length"]; ok {
			if cl, _ := strconv.Atoi(clStr); cl > 0 {
				body = make([]byte, cl)
				if _, err := io.ReadFull(br, body); err != nil {
					return 0, nil, nil, err
				}
			}
		}
		return code, headers, body, nil
	}

	// 1. DESCRIBE
	code, hdrs, sdpBytes, err := sendRTSP("DESCRIBE", baseURI, map[string]string{"Accept": "application/sdp"})
	if err != nil {
		return err
	}
	if code == 401 {
		wwwAuth = hdrs["www-authenticate"]
		code, hdrs, sdpBytes, err = sendRTSP("DESCRIBE", baseURI, map[string]string{"Accept": "application/sdp"})
		if err != nil {
			return err
		}
	}
	if code != 200 {
		return fmt.Errorf("RTSP DESCRIBE 返回 %d", code)
	}

	// Self-heal if camera main/sub stream was set to H.265/HEVC instead of H.264
	sdpUpper := strings.ToUpper(string(sdpBytes))
	if strings.Contains(sdpUpper, "H265") || strings.Contains(sdpUpper, "HEVC") {
		_ = discovery.EnsureH264Video(cs.cam.IP, cs.cam.Username, cs.cam.Password)
		return fmt.Errorf("检测到 H.265 编码，已自动切换为 H.264，正在重连")
	}

	contentBase := hdrs["content-base"]
	if contentBase == "" {
		contentBase = baseURI + "/"
	}
	if !strings.HasSuffix(contentBase, "/") {
		contentBase += "/"
	}

	// Parse SDP for sprop-parameter-sets and track control names
	var sps, pps []byte
	videoTrack := "track1"
	audioTrack := "track2"
	hasAudio := false
	currentMedia := ""

	for _, line := range strings.Split(string(sdpBytes), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "m=video"):
			currentMedia = "video"
		case strings.HasPrefix(line, "m=audio"):
			currentMedia = "audio"
			hasAudio = true
		case strings.HasPrefix(line, "a=control:"):
			ctrl := strings.TrimPrefix(line, "a=control:")
			if ctrl != "*" {
				if currentMedia == "video" {
					videoTrack = ctrl
				} else if currentMedia == "audio" {
					audioTrack = ctrl
				}
			}
		case strings.HasPrefix(line, "a=fmtp:") && strings.Contains(line, "sprop-parameter-sets="):
			idx := strings.Index(line, "sprop-parameter-sets=")
			sprop := line[idx+len("sprop-parameter-sets="):]
			if semi := strings.Index(sprop, ";"); semi >= 0 {
				sprop = sprop[:semi]
			}
			spParts := strings.Split(sprop, ",")
			if len(spParts) >= 2 {
				sps, _ = base64.StdEncoding.DecodeString(strings.TrimSpace(spParts[0]))
				pps, _ = base64.StdEncoding.DecodeString(strings.TrimSpace(spParts[1]))
			}
		}
	}

	resolveTrackURI := func(track string) string {
		if strings.HasPrefix(track, "rtsp://") {
			return track
		}
		return contentBase + strings.TrimPrefix(track, "/")
	}

	// 2. SETUP video (interleaved 0-1)
	code, hdrs, _, err = sendRTSP("SETUP", resolveTrackURI(videoTrack), map[string]string{
		"Transport": "RTP/AVP/TCP;unicast;interleaved=0-1",
	})
	if err != nil || code != 200 {
		if code == 406 {
			return fmt.Errorf("通道连接数已满 (406): 可能被录像机或原厂客户端占满")
		}
		return fmt.Errorf("RTSP SETUP video 失败 (%d): %v", code, err)
	}
	if sess := hdrs["session"]; sess != "" {
		sessionID = strings.TrimSpace(strings.Split(sess, ";")[0])
	}

	// 3. SETUP audio (interleaved 2-3)
	if hasAudio {
		_, _, _, _ = sendRTSP("SETUP", resolveTrackURI(audioTrack), map[string]string{
			"Transport": "RTP/AVP/TCP;unicast;interleaved=2-3",
		})
	}

	// 4. PLAY
	code, _, _, err = sendRTSP("PLAY", contentBase, map[string]string{
		"Range": "npt=0.000-",
	})
	if err != nil || code != 200 {
		return fmt.Errorf("RTSP PLAY 失败 (%d): %v", code, err)
	}
	finishHandshake()
	_ = conn.SetWriteDeadline(time.Time{})

	defer func() {
		if sessionID != "" {
			cseq++
			var td bytes.Buffer
			fmt.Fprintf(&td, "TEARDOWN %s RTSP/1.0\r\nCSeq: %d\r\nSession: %s\r\n", contentBase, cseq, sessionID)
			if wwwAuth != "" {
				if authHdr := buildDigestAuth("TEARDOWN", contentBase, wwwAuth, cs.cam.Username, cs.cam.Password); authHdr != "" {
					fmt.Fprintf(&td, "Authorization: %s\r\n", authHdr)
				}
			}
			td.WriteString("\r\n")
			_ = conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
			_, _ = conn.Write(td.Bytes())
		}
	}()

	var curConfigPkt []byte
	configDirty := false
	if len(sps) >= 4 && len(pps) > 0 {
		curConfigPkt = buildAVCCPacket(sps, pps)
		cs.mu.Lock()
		cs.codecConfig = curConfigPkt
		for _, sub := range cs.subs {
			select {
			case sub.Ch <- curConfigPkt:
			default:
			}
		}
		cs.mu.Unlock()
	}

	cs.broadcastStatus("online", "")

	// Assemble state for H.264 RTP
	var (
		fuBuffer      bytes.Buffer
		fuActive      bool
		auBuffer      bytes.Buffer
		auHasKeyframe bool
		auHasVCL      bool
		lastRTPTime   uint32
		hasRTPTime    bool
		lastKeepalive = time.Now()
	)

	appendNALU := func(nalu []byte) {
		if len(nalu) == 0 {
			return
		}
		nalType := nalu[0] & 0x1F
		switch nalType {
		case 7: // SPS
			if !bytes.Equal(sps, nalu) {
				sps = append([]byte(nil), nalu...)
				configDirty = true
			}
		case 8: // PPS
			if !bytes.Equal(pps, nalu) {
				pps = append([]byte(nil), nalu...)
				configDirty = true
			}
		case 5: // IDR Keyframe slice
			auHasKeyframe = true
			auHasVCL = true
			var lenBuf [4]byte
			binary.BigEndian.PutUint32(lenBuf[:], uint32(len(nalu)))
			auBuffer.Write(lenBuf[:])
			auBuffer.Write(nalu)
		case 1, 2, 3, 4, 6: // Non-IDR slice / SEI
			if nalType >= 1 && nalType <= 4 {
				auHasVCL = true
			}
			var lenBuf [4]byte
			binary.BigEndian.PutUint32(lenBuf[:], uint32(len(nalu)))
			auBuffer.Write(lenBuf[:])
			auBuffer.Write(nalu)
		}
	}

	flushAccessUnit := func(rtpTS uint32) {
		if !auHasVCL || auBuffer.Len() == 0 {
			auBuffer.Reset()
			auHasKeyframe = false
			auHasVCL = false
			return
		}
		var newCfg []byte
		if configDirty && len(sps) >= 4 && len(pps) > 0 {
			newCfg = buildAVCCPacket(sps, pps)
			curConfigPkt = newCfg
			configDirty = false
		}
		// Build MsgTypeVideoFrame: [0x02, isKey(1/0), ts_ms(4B), ...AVCC NALUs...]
		pkt := make([]byte, 6+auBuffer.Len())
		pkt[0] = MsgTypeVideoFrame
		if auHasKeyframe {
			pkt[1] = 1
		}
		tsMs := rtpTS / 90
		binary.BigEndian.PutUint32(pkt[2:6], tsMs)
		copy(pkt[6:], auBuffer.Bytes())

		cs.broadcastVideo(pkt, auHasKeyframe && len(curConfigPkt) > 0, newCfg)

		auBuffer.Reset()
		auHasKeyframe = false
		auHasVCL = false
	}

	var hdrBuf [4]byte
	for {
		select {
		case <-cs.stopCh:
			return nil
		default:
		}

		if time.Since(lastKeepalive) > 20*time.Second {
			lastKeepalive = time.Now()
			cseq++
			var ka bytes.Buffer
			fmt.Fprintf(&ka, "GET_PARAMETER %s RTSP/1.0\r\nCSeq: %d\r\nSession: %s\r\nUser-Agent: FocusCamera/1.0\r\n", contentBase, cseq, sessionID)
			if wwwAuth != "" {
				if authHdr := buildDigestAuth("GET_PARAMETER", contentBase, wwwAuth, cs.cam.Username, cs.cam.Password); authHdr != "" {
					fmt.Fprintf(&ka, "Authorization: %s\r\n", authHdr)
				}
			}
			ka.WriteString("\r\n")
			_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
			_, _ = conn.Write(ka.Bytes())
			_ = conn.SetWriteDeadline(time.Time{})
		}

		_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		b0, err := br.ReadByte()
		if err != nil {
			select {
			case <-cs.stopCh:
				return nil
			default:
			}
			return fmt.Errorf("视频流网络超时，正在自动重连")
		}

		if b0 != '$' {
			// RTSP text response (e.g. from GET_PARAMETER keepalive), read headers and optional Content-Length body
			contentLen := 0
			for {
				line, err := br.ReadString('\n')
				if err != nil {
					return err
				}
				trimmed := strings.TrimSpace(line)
				if trimmed == "" {
					break
				}
				if kv := strings.SplitN(trimmed, ":", 2); len(kv) == 2 {
					if strings.EqualFold(strings.TrimSpace(kv[0]), "content-length") {
						contentLen, _ = strconv.Atoi(strings.TrimSpace(kv[1]))
					}
				}
			}
			if contentLen > 0 && contentLen < 65536 {
				if _, err := io.CopyN(io.Discard, br, int64(contentLen)); err != nil {
					return err
				}
			}
			continue
		}

		if _, err := io.ReadFull(br, hdrBuf[1:4]); err != nil {
			return err
		}
		channel := hdrBuf[1]
		pktLen := int(binary.BigEndian.Uint16(hdrBuf[2:4]))
		if pktLen == 0 {
			continue
		}
		if pktLen < 0 || pktLen > 65535 {
			return fmt.Errorf("异常 RTP 包长度: %d", pktLen)
		}

		rtpPkt := make([]byte, pktLen)
		if _, err := io.ReadFull(br, rtpPkt); err != nil {
			return err
		}

		if (channel != 0 && channel != 2) || len(rtpPkt) < 12 {
			continue
		}

		vpxcc := rtpPkt[0]
		hasPadding := (vpxcc & 0x20) != 0
		hasExt := (vpxcc & 0x10) != 0
		csrcCount := int(vpxcc & 0x0F)
		marker := (rtpPkt[1] & 0x80) != 0
		rtpTS := binary.BigEndian.Uint32(rtpPkt[4:8])

		offset := 12 + csrcCount*4
		if len(rtpPkt) < offset {
			continue
		}
		if hasExt {
			if len(rtpPkt) < offset+4 {
				continue
			}
			extWords := int(binary.BigEndian.Uint16(rtpPkt[offset+2 : offset+4]))
			offset += 4 + extWords*4
		}
		end := len(rtpPkt)
		if hasPadding && end > offset {
			padLen := int(rtpPkt[end-1])
			if padLen > 0 && end-padLen >= offset {
				end -= padLen
			}
		}
		if offset >= end {
			continue
		}
		payload := rtpPkt[offset:end]

		if channel == 2 {
			// Audio RTP (PCMA 8000Hz)
			cs.broadcastAudio(payload)
			continue
		}

		// Channel 0: Video H.264 RTP
		if hasRTPTime && rtpTS != lastRTPTime && auHasVCL {
			flushAccessUnit(lastRTPTime)
		}
		lastRTPTime = rtpTS
		hasRTPTime = true

		nal0 := payload[0]
		if len(payload) >= 3 && ((nal0 == 0x42 && payload[1] == 0x01) || (nal0 == 0x62 && payload[1] == 0x01)) {
			_ = discovery.EnsureH264Video(cs.cam.IP, cs.cam.Username, cs.cam.Password)
			return fmt.Errorf("检测到 H.265 编码，已自动切换为 H.264，正在重连")
		}
		nalType := nal0 & 0x1F

		switch {
		case nalType >= 1 && nalType <= 23:
			appendNALU(payload)
		case nalType == 24: // STAP-A
			p := payload[1:]
			for len(p) >= 2 {
				sz := int(binary.BigEndian.Uint16(p[:2]))
				p = p[2:]
				if sz > len(p) {
					break
				}
				appendNALU(p[:sz])
				p = p[sz:]
			}
		case nalType == 28 && len(payload) >= 2: // FU-A
			fuIndicator := payload[0]
			fuHeader := payload[1]
			startBit := (fuHeader & 0x80) != 0
			endBit := (fuHeader & 0x40) != 0
			origType := fuHeader & 0x1F

			if startBit {
				fuBuffer.Reset()
				fuBuffer.WriteByte((fuIndicator & 0xE0) | origType)
				fuBuffer.Write(payload[2:])
				fuActive = true
			} else if fuActive {
				fuBuffer.Write(payload[2:])
				if endBit {
					appendNALU(fuBuffer.Bytes())
					fuBuffer.Reset()
					fuActive = false
				}
			}
		}

		if marker && auHasVCL {
			flushAccessUnit(rtpTS)
		}
	}
}
