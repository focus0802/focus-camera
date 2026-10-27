package recorder

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"sync"
)

// G.711 A-law to 16-bit signed linear PCM lookup table
var alawToPCM16 [256]int16

// CRC-8 (poly 0x07) and CRC-16 (poly 0x8005) tables for RFC 9639 FLAC frames
var (
	flacCRC8Table  [256]uint8
	flacCRC16Table [256]uint16
)

func init() {
	for i := 0; i < 256; i++ {
		v := i ^ 0x55
		t := (v & 0x0F) << 4
		seg := (v & 0x70) >> 4
		switch seg {
		case 0:
			t += 8
		case 1:
			t += 0x108
		default:
			t += 0x108
			t <<= uint(seg - 1)
		}
		if (v & 0x80) == 0 {
			alawToPCM16[i] = -int16(t)
		} else {
			alawToPCM16[i] = int16(t)
		}
	}

	for i := 0; i < 256; i++ {
		c8 := uint8(i)
		for b := 0; b < 8; b++ {
			if (c8 & 0x80) != 0 {
				c8 = (c8 << 1) ^ 0x07
			} else {
				c8 <<= 1
			}
		}
		flacCRC8Table[i] = c8

		c16 := uint16(i) << 8
		for b := 0; b < 8; b++ {
			if (c16 & 0x8000) != 0 {
				c16 = (c16 << 1) ^ 0x8005
			} else {
				c16 <<= 1
			}
		}
		flacCRC16Table[i] = c16
	}
}

func flacCRC8(data []byte) uint8 {
	var crc uint8
	for _, b := range data {
		crc = flacCRC8Table[crc^b]
	}
	return crc
}

func flacCRC16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc = (crc << 8) ^ flacCRC16Table[byte(crc>>8)^b]
	}
	return crc
}

func writeFLACUTF8Uint64(buf *bytes.Buffer, v uint64) {
	switch {
	case v < 0x80:
		buf.WriteByte(byte(v))
	case v < 0x800:
		buf.WriteByte(byte(0xC0 | (v >> 6)))
		buf.WriteByte(byte(0x80 | (v & 0x3F)))
	case v < 0x10000:
		buf.WriteByte(byte(0xE0 | (v >> 12)))
		buf.WriteByte(byte(0x80 | ((v >> 6) & 0x3F)))
		buf.WriteByte(byte(0x80 | (v & 0x3F)))
	case v < 0x200000:
		buf.WriteByte(byte(0xF0 | (v >> 18)))
		buf.WriteByte(byte(0x80 | ((v >> 12) & 0x3F)))
		buf.WriteByte(byte(0x80 | ((v >> 6) & 0x3F)))
		buf.WriteByte(byte(0x80 | (v & 0x3F)))
	default:
		buf.WriteByte(byte(0xF8 | (v >> 24)))
		buf.WriteByte(byte(0x80 | ((v >> 18) & 0x3F)))
		buf.WriteByte(byte(0x80 | ((v >> 12) & 0x3F)))
		buf.WriteByte(byte(0x80 | ((v >> 6) & 0x3F)))
		buf.WriteByte(byte(0x80 | (v & 0x3F)))
	}
}

// encodeFLACVerbatimFrame encodes a fixed-block-size (e.g. 1600 samples = 200ms @ 8000Hz)
// 16-bit mono PCM buffer into a valid RFC 9639 FLAC frame using SUBFRAME_VERBATIM.
func encodeFLACVerbatimFrame(frameNumber uint64, pcmaSamples []byte) []byte {
	n := len(pcmaSamples)
	if n == 0 {
		return nil
	}
	var buf bytes.Buffer
	// Sync code (14 bits: 11111111111110) + Reserved (0) + Blocking strategy (0 = fixed block size)
	buf.WriteByte(0xFF)
	buf.WriteByte(0xF8)
	// Block size: 0x07 (16-bit blocksize-1 at end of header), Sample rate: 0x00 (from STREAMINFO)
	buf.WriteByte(0x70)
	// Channel assignment: 0x00 (1 channel mono), Sample size: 0x04 (16 bits), Reserved: 0
	buf.WriteByte(0x08)
	// Frame number (UTF-8 coded)
	writeFLACUTF8Uint64(&buf, frameNumber)
	// 16-bit (blocksize - 1)
	var bs [2]byte
	binary.BigEndian.PutUint16(bs[:], uint16(n-1))
	buf.Write(bs[:])
	// 8-bit CRC-8 of frame header
	hdrCRC := flacCRC8(buf.Bytes())
	buf.WriteByte(hdrCRC)

	// Subframe header: 0 (pad) | 000001 (SUBFRAME_VERBATIM) | 0 (no wasted bits) = 0x02
	buf.WriteByte(0x02)

	// Uncompressed 16-bit big-endian PCM samples
	for _, alaw := range pcmaSamples {
		s := alawToPCM16[alaw]
		buf.WriteByte(byte(uint16(s) >> 8))
		buf.WriteByte(byte(uint16(s)))
	}

	// Byte-align before CRC-16 (already byte-aligned since 16-bit samples)
	crc16 := flacCRC16(buf.Bytes())
	buf.WriteByte(byte(crc16 >> 8))
	buf.WriteByte(byte(crc16))
	return buf.Bytes()
}

type videoSample struct {
	data     []byte
	duration uint32 // in 90kHz ticks
	isKey    bool
	tsMs     uint32
}

type audioSample struct {
	data     []byte
	duration uint32 // in 8000Hz samples
}

const flacBlockSamples = 1600 // 200ms per FLAC frame at 8000Hz

// FMP4Writer writes a crash-safe, browser-playable Fragmented MP4 (H.264 1080P + FLAC 8kHz audio).
type FMP4Writer struct {
	mu          sync.Mutex
	f           *os.File
	inited      bool
	closed      bool
	seqNum      uint32
	videoBaseTS uint64 // 90kHz ticks
	audioBaseTS uint64 // 8000Hz samples
	flacFrameNo uint64

	pendingVideo []videoSample
	pendingAudio []audioSample
	pcmaBuf      []byte
	bytesWritten int64
}

func NewFMP4Writer(path string) (*FMP4Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &FMP4Writer{
		f: f,
	}, nil
}

func (w *FMP4Writer) BytesWritten() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytesWritten
}

func (w *FMP4Writer) InitHeader(avccRecord []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inited || w.closed {
		return nil
	}
	hdr := buildFMP4InitSegment(avccRecord, 1920, 1080)
	n, err := w.f.Write(hdr)
	if err != nil {
		return err
	}
	w.bytesWritten += int64(n)
	w.inited = true
	return nil
}

func (w *FMP4Writer) WriteVideo(avccNALUs []byte, tsMs uint32, isKey bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.inited || w.closed || len(avccNALUs) == 0 {
		return nil
	}

	// If a new keyframe arrives and we already have buffered video frames, flush the previous GOP fragment
	if isKey && len(w.pendingVideo) > 0 {
		if err := w.flushFragmentLocked(tsMs); err != nil {
			return err
		}
	}

	cp := make([]byte, len(avccNALUs))
	copy(cp, avccNALUs)
	w.pendingVideo = append(w.pendingVideo, videoSample{
		data:     cp,
		duration: 3600, // default 40ms (25fps) in 90kHz ticks; refined on flush
		isKey:    isKey,
		tsMs:     tsMs,
	})

	// Safety bound: flush if fragment exceeds 60 frames (~2.4s) even without keyframe
	if len(w.pendingVideo) >= 60 {
		return w.flushFragmentLocked(tsMs + 40)
	}
	return nil
}

func (w *FMP4Writer) WriteAudioPCMA(pcma []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.inited || w.closed || len(pcma) == 0 {
		return nil
	}
	w.pcmaBuf = append(w.pcmaBuf, pcma...)
	for len(w.pcmaBuf) >= flacBlockSamples {
		chunk := w.pcmaBuf[:flacBlockSamples]
		w.pcmaBuf = w.pcmaBuf[flacBlockSamples:]
		flacBytes := encodeFLACVerbatimFrame(w.flacFrameNo, chunk)
		w.flacFrameNo++
		w.pendingAudio = append(w.pendingAudio, audioSample{
			data:     flacBytes,
			duration: flacBlockSamples,
		})
	}
	return nil
}

func (w *FMP4Writer) flushFragmentLocked(nextKeyTSMs uint32) error {
	if len(w.pendingVideo) == 0 {
		return nil
	}
	// Refine sample durations from RTP millisecond timestamps
	for i := 0; i < len(w.pendingVideo); i++ {
		var deltaMs uint32 = 40
		if i+1 < len(w.pendingVideo) {
			if w.pendingVideo[i+1].tsMs > w.pendingVideo[i].tsMs {
				deltaMs = w.pendingVideo[i+1].tsMs - w.pendingVideo[i].tsMs
			}
		} else if nextKeyTSMs > w.pendingVideo[i].tsMs {
			deltaMs = nextKeyTSMs - w.pendingVideo[i].tsMs
		}
		if deltaMs < 10 || deltaMs > 500 {
			deltaMs = 40
		}
		w.pendingVideo[i].duration = deltaMs * 90
	}

	w.seqNum++
	fragBytes, vTicks, aSamples := buildFMP4Fragment(
		w.seqNum,
		w.videoBaseTS,
		w.audioBaseTS,
		w.pendingVideo,
		w.pendingAudio,
	)
	w.videoBaseTS += vTicks
	w.audioBaseTS += aSamples
	w.pendingVideo = w.pendingVideo[:0]
	w.pendingAudio = w.pendingAudio[:0]

	n, err := w.f.Write(fragBytes)
	if err != nil {
		return err
	}
	w.bytesWritten += int64(n)
	return nil
}

func (w *FMP4Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.inited && len(w.pendingVideo) > 0 {
		lastTS := w.pendingVideo[len(w.pendingVideo)-1].tsMs + 40
		_ = w.flushFragmentLocked(lastTS)
	}
	_ = w.f.Sync()
	return w.f.Close()
}

// --- ISO BMFF Box Builders ---

func mp4Box(boxType string, payload []byte) []byte {
	sz := uint32(8 + len(payload))
	out := make([]byte, sz)
	binary.BigEndian.PutUint32(out[0:4], sz)
	copy(out[4:8], boxType)
	copy(out[8:], payload)
	return out
}

func mp4FullBox(boxType string, version byte, flags uint32, payload []byte) []byte {
	sz := uint32(12 + len(payload))
	out := make([]byte, sz)
	binary.BigEndian.PutUint32(out[0:4], sz)
	copy(out[4:8], boxType)
	out[8] = version
	out[9] = byte(flags >> 16)
	out[10] = byte(flags >> 8)
	out[11] = byte(flags)
	copy(out[12:], payload)
	return out
}

func isHVCCRecord(cfg []byte) bool {
	return len(cfg) > 23 && cfg[15] == 0xFC && cfg[16] == 0xFD && (cfg[23]&0x3F) >= 32 && (cfg[23]&0x3F) <= 34
}

func buildFMP4InitSegment(avcc []byte, width, height uint16) []byte {
	// 1. ftyp
	var ftypBody bytes.Buffer
	ftypBody.WriteString("isom")
	_ = binary.Write(&ftypBody, binary.BigEndian, uint32(0x200))
	if isHVCCRecord(avcc) {
		ftypBody.WriteString("isomiso2hvc1iso6mp41")
	} else {
		ftypBody.WriteString("isomiso2avc1iso6mp41")
	}
	ftyp := mp4Box("ftyp", ftypBody.Bytes())

	// 2. moov
	var moovBody bytes.Buffer
	moovBody.Write(buildMVHD())
	moovBody.Write(buildVideoTRAK(avcc, width, height))
	moovBody.Write(buildAudioFLACTRAK())
	moovBody.Write(buildMVEX())
	moov := mp4Box("moov", moovBody.Bytes())

	return append(ftyp, moov...)
}

func buildMVHD() []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(0))    // creation_time
	_ = binary.Write(&b, binary.BigEndian, uint32(0))    // modification_time
	_ = binary.Write(&b, binary.BigEndian, uint32(1000)) // timescale
	_ = binary.Write(&b, binary.BigEndian, uint32(0))    // duration
	_ = binary.Write(&b, binary.BigEndian, uint32(0x00010000)) // rate 1.0
	_ = binary.Write(&b, binary.BigEndian, uint16(0x0100))     // volume 1.0
	b.Write(make([]byte, 10))                                  // reserved
	// Unity matrix
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		_ = binary.Write(&b, binary.BigEndian, v)
	}
	b.Write(make([]byte, 24))                         // pre_defined
	_ = binary.Write(&b, binary.BigEndian, uint32(3)) // next_track_ID
	return mp4FullBox("mvhd", 0, 0, b.Bytes())
}

func buildVideoTRAK(avcc []byte, width, height uint16) []byte {
	// tkhd
	var tk bytes.Buffer
	_ = binary.Write(&tk, binary.BigEndian, uint32(0)) // creation_time
	_ = binary.Write(&tk, binary.BigEndian, uint32(0)) // modification_time
	_ = binary.Write(&tk, binary.BigEndian, uint32(1)) // track_ID = 1
	_ = binary.Write(&tk, binary.BigEndian, uint32(0)) // reserved
	_ = binary.Write(&tk, binary.BigEndian, uint32(0)) // duration
	tk.Write(make([]byte, 8))                          // reserved
	_ = binary.Write(&tk, binary.BigEndian, uint16(0)) // layer
	_ = binary.Write(&tk, binary.BigEndian, uint16(0)) // alternate_group
	_ = binary.Write(&tk, binary.BigEndian, uint16(0)) // volume
	_ = binary.Write(&tk, binary.BigEndian, uint16(0)) // reserved
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		_ = binary.Write(&tk, binary.BigEndian, v)
	}
	_ = binary.Write(&tk, binary.BigEndian, uint32(width)<<16)
	_ = binary.Write(&tk, binary.BigEndian, uint32(height)<<16)
	tkhd := mp4FullBox("tkhd", 0, 0x000003, tk.Bytes())

	// mdhd (timescale 90000)
	var md bytes.Buffer
	_ = binary.Write(&md, binary.BigEndian, uint32(0))
	_ = binary.Write(&md, binary.BigEndian, uint32(0))
	_ = binary.Write(&md, binary.BigEndian, uint32(90000))
	_ = binary.Write(&md, binary.BigEndian, uint32(0))
	_ = binary.Write(&md, binary.BigEndian, uint16(0x55C4)) // "und"
	_ = binary.Write(&md, binary.BigEndian, uint16(0))
	mdhd := mp4FullBox("mdhd", 0, 0, md.Bytes())

	// hdlr
	var hd bytes.Buffer
	_ = binary.Write(&hd, binary.BigEndian, uint32(0))
	hd.WriteString("vide")
	hd.Write(make([]byte, 12))
	hd.WriteString("VideoHandler\x00")
	hdlr := mp4FullBox("hdlr", 0, 0, hd.Bytes())

	// vmhd
	vmhd := mp4FullBox("vmhd", 0, 1, make([]byte, 8))

	// dinf -> dref -> url
	urlBox := mp4FullBox("url ", 0, 1, nil)
	var drefBody bytes.Buffer
	_ = binary.Write(&drefBody, binary.BigEndian, uint32(1))
	drefBody.Write(urlBox)
	dinf := mp4Box("dinf", mp4FullBox("dref", 0, 0, drefBody.Bytes()))

	// stsd -> avc1 (avcC) or hvc1 (hvcC)
	sampleTag := "avc1"
	configTag := "avcC"
	if isHVCCRecord(avcc) {
		sampleTag = "hvc1"
		configTag = "hvcC"
	}
	var avc1Body bytes.Buffer
	avc1Body.Write(make([]byte, 6))                            // reserved
	_ = binary.Write(&avc1Body, binary.BigEndian, uint16(1))   // data_reference_index
	avc1Body.Write(make([]byte, 16))                           // pre_defined + reserved
	_ = binary.Write(&avc1Body, binary.BigEndian, width)       // width
	_ = binary.Write(&avc1Body, binary.BigEndian, height)      // height
	_ = binary.Write(&avc1Body, binary.BigEndian, uint32(0x00480000)) // horizres 72dpi
	_ = binary.Write(&avc1Body, binary.BigEndian, uint32(0x00480000)) // vertres 72dpi
	_ = binary.Write(&avc1Body, binary.BigEndian, uint32(0))          // reserved
	_ = binary.Write(&avc1Body, binary.BigEndian, uint16(1))          // frame_count
	avc1Body.Write(make([]byte, 32))                                  // compressorname
	_ = binary.Write(&avc1Body, binary.BigEndian, uint16(0x0018))     // depth 24
	_ = binary.Write(&avc1Body, binary.BigEndian, int16(-1))          // pre_defined
	avc1Body.Write(mp4Box(configTag, avcc))
	avc1Box := mp4Box(sampleTag, avc1Body.Bytes())

	var stsdBody bytes.Buffer
	_ = binary.Write(&stsdBody, binary.BigEndian, uint32(1))
	stsdBody.Write(avc1Box)
	stsd := mp4FullBox("stsd", 0, 0, stsdBody.Bytes())

	emptyCount := make([]byte, 4)
	stts := mp4FullBox("stts", 0, 0, emptyCount)
	stsc := mp4FullBox("stsc", 0, 0, emptyCount)
	stsz := mp4FullBox("stsz", 0, 0, make([]byte, 8))
	stco := mp4FullBox("stco", 0, 0, emptyCount)

	var stblBody bytes.Buffer
	stblBody.Write(stsd)
	stblBody.Write(stts)
	stblBody.Write(stsc)
	stblBody.Write(stsz)
	stblBody.Write(stco)
	stbl := mp4Box("stbl", stblBody.Bytes())

	var minfBody bytes.Buffer
	minfBody.Write(vmhd)
	minfBody.Write(dinf)
	minfBody.Write(stbl)
	minf := mp4Box("minf", minfBody.Bytes())

	var mdiaBody bytes.Buffer
	mdiaBody.Write(mdhd)
	mdiaBody.Write(hdlr)
	mdiaBody.Write(minf)
	mdia := mp4Box("mdia", mdiaBody.Bytes())

	return mp4Box("trak", append(tkhd, mdia...))
}

func buildAudioFLACTRAK() []byte {
	// tkhd
	var tk bytes.Buffer
	_ = binary.Write(&tk, binary.BigEndian, uint32(0))
	_ = binary.Write(&tk, binary.BigEndian, uint32(0))
	_ = binary.Write(&tk, binary.BigEndian, uint32(2)) // track_ID = 2
	_ = binary.Write(&tk, binary.BigEndian, uint32(0))
	_ = binary.Write(&tk, binary.BigEndian, uint32(0))
	tk.Write(make([]byte, 8))
	_ = binary.Write(&tk, binary.BigEndian, uint16(0))
	_ = binary.Write(&tk, binary.BigEndian, uint16(0))
	_ = binary.Write(&tk, binary.BigEndian, uint16(0x0100)) // volume 1.0
	_ = binary.Write(&tk, binary.BigEndian, uint16(0))
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		_ = binary.Write(&tk, binary.BigEndian, v)
	}
	_ = binary.Write(&tk, binary.BigEndian, uint32(0))
	_ = binary.Write(&tk, binary.BigEndian, uint32(0))
	tkhd := mp4FullBox("tkhd", 0, 0x000003, tk.Bytes())

	// mdhd (timescale 8000)
	var md bytes.Buffer
	_ = binary.Write(&md, binary.BigEndian, uint32(0))
	_ = binary.Write(&md, binary.BigEndian, uint32(0))
	_ = binary.Write(&md, binary.BigEndian, uint32(8000))
	_ = binary.Write(&md, binary.BigEndian, uint32(0))
	_ = binary.Write(&md, binary.BigEndian, uint16(0x55C4))
	_ = binary.Write(&md, binary.BigEndian, uint16(0))
	mdhd := mp4FullBox("mdhd", 0, 0, md.Bytes())

	// hdlr
	var hd bytes.Buffer
	_ = binary.Write(&hd, binary.BigEndian, uint32(0))
	hd.WriteString("soun")
	hd.Write(make([]byte, 12))
	hd.WriteString("SoundHandler\x00")
	hdlr := mp4FullBox("hdlr", 0, 0, hd.Bytes())

	// smhd
	smhd := mp4FullBox("smhd", 0, 0, make([]byte, 4))

	// dinf
	urlBox := mp4FullBox("url ", 0, 1, nil)
	var drefBody bytes.Buffer
	_ = binary.Write(&drefBody, binary.BigEndian, uint32(1))
	drefBody.Write(urlBox)
	dinf := mp4Box("dinf", mp4FullBox("dref", 0, 0, drefBody.Bytes()))

	// dfLa FullBox containing STREAMINFO metadata block (38 bytes total inside FullBox)
	var dfla bytes.Buffer
	// Metadata block header: Last=1 (0x80), Type=0 (STREAMINFO), Length=34 (0x000022)
	dfla.Write([]byte{0x80, 0x00, 0x00, 0x22})
	var si [34]byte
	binary.BigEndian.PutUint16(si[0:2], flacBlockSamples) // min block size = 1600
	binary.BigEndian.PutUint16(si[2:4], flacBlockSamples) // max block size = 1600
	// min/max frame size = 0 (24-bit each in si[4:10])
	// Sample rate = 8000 (20 bits), channels-1 = 0 (3 bits), bits_per_sample-1 = 15 (5 bits), total_samples = 0 (36 bits)
	// 8000 = 0x01F40 -> 20 bits: [0x01, 0xF4, 0x0...]
	// Next 4 bits of byte 12: (channels-1 = 0 -> 000) + top bit of (bps-1 = 15 = 01111 -> 0) -> 0x00
	// Top 4 bits of byte 13: bottom 4 bits of (bps-1 = 15 -> 1111) -> 0xF0
	si[10] = 0x01
	si[11] = 0xF4
	si[12] = 0x00
	si[13] = 0xF0
	dfla.Write(si[:])
	dflaBox := mp4FullBox("dfLa", 0, 0, dfla.Bytes())

	// fLaC AudioSampleEntry
	var flacEntry bytes.Buffer
	flacEntry.Write(make([]byte, 6))                            // reserved
	_ = binary.Write(&flacEntry, binary.BigEndian, uint16(1))   // data_reference_index
	flacEntry.Write(make([]byte, 8))                            // reserved
	_ = binary.Write(&flacEntry, binary.BigEndian, uint16(1))   // channelcount = 1
	_ = binary.Write(&flacEntry, binary.BigEndian, uint16(16))  // samplesize = 16
	_ = binary.Write(&flacEntry, binary.BigEndian, uint16(0))   // pre_defined
	_ = binary.Write(&flacEntry, binary.BigEndian, uint16(0))   // reserved
	_ = binary.Write(&flacEntry, binary.BigEndian, uint32(8000)<<16) // samplerate 8000.0
	flacEntry.Write(dflaBox)
	flacBox := mp4Box("fLaC", flacEntry.Bytes())

	var stsdBody bytes.Buffer
	_ = binary.Write(&stsdBody, binary.BigEndian, uint32(1))
	stsdBody.Write(flacBox)
	stsd := mp4FullBox("stsd", 0, 0, stsdBody.Bytes())

	emptyCount := make([]byte, 4)
	stts := mp4FullBox("stts", 0, 0, emptyCount)
	stsc := mp4FullBox("stsc", 0, 0, emptyCount)
	stsz := mp4FullBox("stsz", 0, 0, make([]byte, 8))
	stco := mp4FullBox("stco", 0, 0, emptyCount)

	var stblBody bytes.Buffer
	stblBody.Write(stsd)
	stblBody.Write(stts)
	stblBody.Write(stsc)
	stblBody.Write(stsz)
	stblBody.Write(stco)
	stbl := mp4Box("stbl", stblBody.Bytes())

	var minfBody bytes.Buffer
	minfBody.Write(smhd)
	minfBody.Write(dinf)
	minfBody.Write(stbl)
	minf := mp4Box("minf", minfBody.Bytes())

	var mdiaBody bytes.Buffer
	mdiaBody.Write(mdhd)
	mdiaBody.Write(hdlr)
	mdiaBody.Write(minf)
	mdia := mp4Box("mdia", mdiaBody.Bytes())

	return mp4Box("trak", append(tkhd, mdia...))
}

func buildMVEX() []byte {
	makeTREX := func(trackID uint32) []byte {
		var b bytes.Buffer
		_ = binary.Write(&b, binary.BigEndian, trackID)
		_ = binary.Write(&b, binary.BigEndian, uint32(1)) // default_sample_description_index
		_ = binary.Write(&b, binary.BigEndian, uint32(0)) // default_sample_duration
		_ = binary.Write(&b, binary.BigEndian, uint32(0)) // default_sample_size
		_ = binary.Write(&b, binary.BigEndian, uint32(0)) // default_sample_flags
		return mp4FullBox("trex", 0, 0, b.Bytes())
	}
	return mp4Box("mvex", append(makeTREX(1), makeTREX(2)...))
}

func buildFMP4Fragment(
	seqNum uint32,
	videoBaseTS uint64,
	audioBaseTS uint64,
	vSamples []videoSample,
	aSamples []audioSample,
) (fragment []byte, totalVTicks uint64, totalASamples uint64) {
	// mfhd
	var mfhdBody [4]byte
	binary.BigEndian.PutUint32(mfhdBody[:], seqNum)
	mfhd := mp4FullBox("mfhd", 0, 0, mfhdBody[:])

	// Build video traf (with placeholder data_offset at known position)
	var vData bytes.Buffer
	var vTrun bytes.Buffer
	_ = binary.Write(&vTrun, binary.BigEndian, uint32(len(vSamples)))
	_ = binary.Write(&vTrun, binary.BigEndian, int32(0)) // placeholder for video data_offset
	for _, s := range vSamples {
		_ = binary.Write(&vTrun, binary.BigEndian, s.duration)
		_ = binary.Write(&vTrun, binary.BigEndian, uint32(len(s.data)))
		var flags uint32 = 0x01010000 // non-sync sample
		if s.isKey {
			flags = 0x02000000 // sync sample (IDR keyframe)
		}
		_ = binary.Write(&vTrun, binary.BigEndian, flags)
		vData.Write(s.data)
		totalVTicks += uint64(s.duration)
	}
	vTrunBox := mp4FullBox("trun", 0, 0x000701, vTrun.Bytes())

	var vTfhd [4]byte
	binary.BigEndian.PutUint32(vTfhd[:], 1)
	vTfhdBox := mp4FullBox("tfhd", 0, 0x020000, vTfhd[:])

	var vTfdt [8]byte
	binary.BigEndian.PutUint64(vTfdt[:], videoBaseTS)
	vTfdtBox := mp4FullBox("tfdt", 1, 0, vTfdt[:])

	var vTrafBody bytes.Buffer
	vTrafBody.Write(vTfhdBox)
	vTrafBody.Write(vTfdtBox)
	vTrafBody.Write(vTrunBox)
	vTraf := mp4Box("traf", vTrafBody.Bytes())

	// Optional audio traf
	var aTraf []byte
	var aData bytes.Buffer
	if len(aSamples) > 0 {
		var aTrun bytes.Buffer
		_ = binary.Write(&aTrun, binary.BigEndian, uint32(len(aSamples)))
		_ = binary.Write(&aTrun, binary.BigEndian, int32(0)) // placeholder for audio data_offset
		for _, s := range aSamples {
			_ = binary.Write(&aTrun, binary.BigEndian, s.duration)
			_ = binary.Write(&aTrun, binary.BigEndian, uint32(len(s.data)))
			aData.Write(s.data)
			totalASamples += uint64(s.duration)
		}
		aTrunBox := mp4FullBox("trun", 0, 0x000301, aTrun.Bytes())

		var aTfhd [4]byte
		binary.BigEndian.PutUint32(aTfhd[:], 2)
		aTfhdBox := mp4FullBox("tfhd", 0, 0x020000, aTfhd[:])

		var aTfdt [8]byte
		binary.BigEndian.PutUint64(aTfdt[:], audioBaseTS)
		aTfdtBox := mp4FullBox("tfdt", 1, 0, aTfdt[:])

		var aTrafBody bytes.Buffer
		aTrafBody.Write(aTfhdBox)
		aTrafBody.Write(aTfdtBox)
		aTrafBody.Write(aTrunBox)
		aTraf = mp4Box("traf", aTrafBody.Bytes())
	}

	var moofBody bytes.Buffer
	moofBody.Write(mfhd)
	moofBody.Write(vTraf)
	if len(aTraf) > 0 {
		moofBody.Write(aTraf)
	}
	moof := mp4Box("moof", moofBody.Bytes())

	// Patch data_offset in video trun and audio trun!
	// moof header (8) + mfhd (16) + vTraf header (8) + vTfhd (16) + vTfdt (20) + vTrun header (12) + sample_count (4) = offset 84
	videoDataOffset := int32(len(moof) + 8)
	binary.BigEndian.PutUint32(moof[84:88], uint32(videoDataOffset))

	if len(aTraf) > 0 {
		// Audio trun data_offset is at: 8 (moof hdr) + len(mfhd) + len(vTraf) + 8 (aTraf hdr) + 16 (aTfhd) + 20 (aTfdt) + 12 (aTrun hdr) + 4 (sample_count)
		aOffsetPos := 8 + len(mfhd) + len(vTraf) + 8 + 16 + 20 + 12 + 4
		audioDataOffset := videoDataOffset + int32(vData.Len())
		binary.BigEndian.PutUint32(moof[aOffsetPos:aOffsetPos+4], uint32(audioDataOffset))
	}

	var mdatPayload bytes.Buffer
	mdatPayload.Write(vData.Bytes())
	mdatPayload.Write(aData.Bytes())
	mdat := mp4Box("mdat", mdatPayload.Bytes())

	return append(moof, mdat...), totalVTicks, totalASamples
}

var _ io.Closer = (*FMP4Writer)(nil)
