package talk

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"focus-camera/internal/discovery"
)

type Session struct {
	mu        sync.Mutex
	conn      net.Conn
	sessionID string
	muxer     *TSMuxer
	closed    bool
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func between(s, sub1, sub2 string) string {
	i := strings.Index(s, sub1)
	if i < 0 {
		return ""
	}
	s = s[i+len(sub1):]
	j := strings.Index(s, sub2)
	if j < 0 {
		return ""
	}
	return s[:j]
}

func humanizeTalkNetError(err error) string {
	if err == nil {
		return ""
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "actively refused"):
		return "摄像头拒绝语音连接，设备可能正在重启或不支持双向对讲"
	case strings.Contains(lower, "no route to host") || strings.Contains(lower, "host is down") || strings.Contains(lower, "unreachable"):
		return "摄像头离线或网络不通，请检查电源与网线连接"
	case strings.Contains(lower, "i/o timeout") || strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout") || strings.Contains(lower, "did not properly respond") || strings.Contains(lower, "failed to respond"):
		return "无法连接摄像头语音通道，请确认设备在线"
	default:
		return "摄像头语音对讲连接中断，请重试"
	}
}

func DialTalkSession(ip string, port int, username, password string) (*Session, error) {
	if port == 0 {
		port = 8800
	}
	host := fmt.Sprintf("%s:%d", ip, port)
	conn, err := net.DialTimeout("tcp", host, 3*time.Second)
	if err != nil {
		return nil, errors.New(humanizeTalkNetError(err))
	}

	req1, err := http.NewRequest("POST", "http://"+host+"/stream", nil)
	if err != nil {
		_ = conn.Close()
		return nil, errors.New(humanizeTalkNetError(err))
	}
	req1.Header.Set("Content-Type", "multipart/mixed; boundary=--client-stream-boundary--")

	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if err := req1.Write(conn); err != nil {
		_ = conn.Close()
		return nil, errors.New(humanizeTalkNetError(err))
	}

	br := bufio.NewReader(conn)
	res1, err := http.ReadResponse(br, req1)
	if err != nil {
		_ = conn.Close()
		return nil, errors.New(humanizeTalkNetError(err))
	}

	// Case 1: Older firmware (e.g. TL-IPC42A-Focus FW 1.0.x) returns 200 OK immediately!
	// Case 2: Newer firmware (e.g. TL-IPC42A-4 FW 1.1.x) returns 401 Digest challenge first.
	if res1.StatusCode == http.StatusUnauthorized {
		_, _ = io.Copy(io.Discard, res1.Body)
		_ = res1.Body.Close()

		auth := res1.Header.Get("WWW-Authenticate")
		if !strings.HasPrefix(auth, "Digest") {
			_ = conn.Close()
			return nil, errors.New("该摄像头不支持标准双向对讲协议")
		}

		encPwd := discovery.SecurityEncode(password)
		realm := between(auth, `realm="`, `"`)
		nonce := between(auth, `nonce="`, `"`)
		qop := between(auth, `qop="`, `"`)
		opaque := between(auth, `opaque="`, `"`)
		uri := "/stream"
		nc := "00000001"
		cnonce := "f0c5ca3e8a9b1d2e"

		ha1 := md5Hex(fmt.Sprintf("%s:%s:%s", username, realm, encPwd))
		ha2 := md5Hex(fmt.Sprintf("POST:%s", uri))
		var respHash string
		if qop != "" {
			respHash = md5Hex(fmt.Sprintf("%s:%s:%s:%s:%s:%s", ha1, nonce, nc, cnonce, qop, ha2))
		} else {
			respHash = md5Hex(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2))
		}

		authHeader := fmt.Sprintf(
			`Digest username="%s", realm="%s", nonce="%s", uri="%s", qop=%s, nc=%s, cnonce="%s", response="%s"`,
			username, realm, nonce, uri, qop, nc, cnonce, respHash,
		)
		if opaque != "" {
			authHeader += fmt.Sprintf(`, opaque="%s", algorithm=MD5`, opaque)
		}

		req2, _ := http.NewRequest("POST", "http://"+host+"/stream", nil)
		req2.Header.Set("Content-Type", "multipart/mixed; boundary=--client-stream-boundary--")
		req2.Header.Set("Authorization", authHeader)

		// Try sending on same socket first; if camera closed it on 401, redial cleanly.
		var res2 *http.Response
		if err := req2.Write(conn); err == nil {
			res2, err = http.ReadResponse(br, req2)
		}
		if err != nil || res2 == nil {
			_ = conn.Close()
			conn, err = net.DialTimeout("tcp", host, 3*time.Second)
			if err != nil {
				return nil, errors.New(humanizeTalkNetError(err))
			}
			_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
			if err := req2.Write(conn); err != nil {
				_ = conn.Close()
				return nil, errors.New(humanizeTalkNetError(err))
			}
			br = bufio.NewReader(conn)
			res2, err = http.ReadResponse(br, req2)
			if err != nil {
				_ = conn.Close()
				return nil, errors.New(humanizeTalkNetError(err))
			}
		}
		if res2.StatusCode != http.StatusOK {
			_ = conn.Close()
			return nil, errors.New("摄像头密码验证未通过，请在「设备管理」中核对密码")
		}
	} else if res1.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, errors.New("摄像头语音对讲服务暂不可用")
	}

	// Send talk session request
	talkReqJSON := []byte(`{"params":{"talk":{"mode":"aec"},"method":"get"},"seq":3,"type":"request"}`)
	var reqBuf bytes.Buffer
	reqBuf.WriteString("----client-stream-boundary--\r\n")
	reqBuf.WriteString("Content-Type: application/json\r\n")
	reqBuf.WriteString("Content-Length: " + strconv.Itoa(len(talkReqJSON)) + "\r\n\r\n")
	reqBuf.Write(talkReqJSON)
	reqBuf.WriteString("\r\n")

	if _, err := conn.Write(reqBuf.Bytes()); err != nil {
		_ = conn.Close()
		return nil, errors.New(humanizeTalkNetError(err))
	}

	mpReader := multipart.NewReader(br, "--device-stream-boundary--")
	part, err := mpReader.NextRawPart()
	if err != nil {
		_ = conn.Close()
		return nil, errors.New("未收到摄像头语音回应，请稍后重试")
	}

	var talkResp struct {
		Params struct {
			ErrorCode int    `json:"error_code"`
			SessionID string `json:"session_id"`
		} `json:"params"`
	}
	if err := json.NewDecoder(part).Decode(&talkResp); err != nil {
		_ = conn.Close()
		return nil, errors.New("摄像头语音协议响应异常，请重试")
	}
	if talkResp.Params.ErrorCode != 0 || talkResp.Params.SessionID == "" {
		_ = conn.Close()
		return nil, errors.New("摄像头语音通道正在被占用，请稍后再试")
	}

	// Clear read/write deadline for continuous streaming
	_ = conn.SetDeadline(time.Time{})

	sess := &Session{
		conn:      conn,
		sessionID: talkResp.Params.SessionID,
		muxer:     NewTSMuxer(),
	}

	// Drain any incoming camera responses in background so socket buffer never blocks
	go func() {
		_, _ = io.Copy(io.Discard, br)
	}()

	// Send MPEG-TS PAT+PMT header immediately
	if err := sess.writeMP2T(sess.muxer.GetHeader()); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("发送对讲音频头失败: %w", err)
	}

	return sess, nil
}

func (s *Session) writeMP2T(tsBytes []byte) error {
	var buf bytes.Buffer
	buf.WriteString("----client-stream-boundary--\r\n")
	buf.WriteString("Content-Type: audio/mp2t\r\n")
	buf.WriteString("X-If-Encrypt: 0\r\n")
	buf.WriteString("X-Session-Id: " + s.sessionID + "\r\n")
	buf.WriteString("Content-Length: " + strconv.Itoa(len(tsBytes)) + "\r\n\r\n")
	buf.Write(tsBytes)

	_ = s.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err := s.conn.Write(buf.Bytes())
	return err
}

func (s *Session) SendPCMA(pcma []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("talk session closed")
	}
	tsPayload := s.muxer.EncodePCMA(pcma)
	return s.writeMP2T(tsPayload)
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.conn.Close()
}
