const ALAW_DECODE_TABLE = new Float32Array(256);

(function initAlawTable() {
  for (let i = 0; i < 256; i++) {
    let val = i ^ 0x55;
    let t = (val & 0x0f) << 4;
    const seg = (val & 0x70) >> 4;
    switch (seg) {
      case 0:
        t += 8;
        break;
      case 1:
        t += 0x108;
        break;
      default:
        t += 0x108;
        t <<= seg - 1;
        break;
    }
    const pcm16 = val & 0x80 ? t : -t;
    ALAW_DECODE_TABLE[i] = pcm16 / 32768.0;
  }
})();

function linearToAlaw(pcmVal: number): number {
  let mask = 0xd5;
  let sample = pcmVal;
  if (sample < 0) {
    sample = -sample - 1;
    mask = 0x55;
  }
  if (sample > 32767) sample = 32767;

  let seg = 0;
  const segEnd = [0x1f, 0x3f, 0x7f, 0xff, 0x1ff, 0x3ff, 0x7ff, 0xfff];
  const shifted = sample >> 3;
  while (seg < 8 && shifted > segEnd[seg]) {
    seg++;
  }
  if (seg >= 8) {
    return 0x7f ^ mask;
  }
  const aval = seg << 4;
  const mant = seg < 2 ? ( sample >> 4 ) & 0x0f : ( sample >> (seg + 3) ) & 0x0f;
  return (aval | mant) ^ mask;
}

export class RoomAudioPlayer {
  private ctx: AudioContext | null = null;
  private nextTime = 0;
  private enabled = false;

  setEnabled(on: boolean) {
    this.enabled = on;
    if (!on && this.ctx) {
      this.ctx.close().catch(() => {});
      this.ctx = null;
      this.nextTime = 0;
    }
  }

  feedAlaw(payload: Uint8Array) {
    if (!this.enabled || payload.length === 0) return;
    if (!this.ctx) {
      const AudioCtx =
        window.AudioContext ||
        (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext;
      this.ctx = new AudioCtx({ sampleRate: 8000 });
      this.nextTime = this.ctx.currentTime + 0.06;
    }
    if (this.ctx.state === "suspended") {
      this.ctx.resume().catch(() => {});
    }
    const buf = this.ctx.createBuffer(1, payload.length, 8000);
    const ch = buf.getChannelData(0);
    for (let i = 0; i < payload.length; i++) {
      ch[i] = ALAW_DECODE_TABLE[payload[i]];
    }
    const src = this.ctx.createBufferSource();
    src.buffer = buf;
    src.connect(this.ctx.destination);
    const now = this.ctx.currentTime;
    if (this.nextTime < now || this.nextTime > now + 0.45) {
      this.nextTime = now + 0.05;
    }
    src.start(this.nextTime);
    this.nextTime += buf.duration;
  }

  destroy() {
    this.setEnabled(false);
  }
}

export interface TalkbackCallbacks {
  onStatus: (state: "connecting" | "ready" | "error" | "closed", message: string) => void;
  onLevel?: (level: number) => void;
}

export class TalkbackSession {
  private ws: WebSocket | null = null;
  private mediaStream: MediaStream | null = null;
  private audioCtx: AudioContext | null = null;
  private processor: ScriptProcessorNode | null = null;
  private closed = false;

  constructor(
    private cameraId: string,
    private callbacks: TalkbackCallbacks
  ) {}

  async start() {
    this.closed = false;
    this.callbacks.onStatus("connecting", "正在申请麦克风并握手 8800 对讲通道…");
    try {
      this.mediaStream = await navigator.mediaDevices.getUserMedia({
        audio: {
          echoCancellation: true,
          noiseSuppression: true,
          autoGainControl: true,
          channelCount: 1,
        },
      });
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      this.callbacks.onStatus("error", `麦克风权限获取失败: ${msg}`);
      return;
    }

    if (this.closed) {
      this.stop();
      return;
    }

    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${proto}//${window.location.host}/ws/talk?id=${encodeURIComponent(this.cameraId)}`;
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    this.ws = ws;

    ws.onmessage = (ev) => {
      if (typeof ev.data !== "string") return;
      try {
        const msg = JSON.parse(ev.data);
        if (msg.type === "ready") {
          this.callbacks.onStatus("ready", msg.message || "双向语音对讲已接通");
          this.startMicPump();
        } else if (msg.type === "error") {
          this.callbacks.onStatus("error", msg.message || msg.error || "对讲连接异常");
          this.stop();
        } else if (msg.type === "status") {
          this.callbacks.onStatus("connecting", msg.message || "正在握手…");
        }
      } catch {
        // ignore non-json
      }
    };

    ws.onerror = () => {
      if (!this.closed) {
        this.callbacks.onStatus("error", "对讲 WebSocket 通道异常断开");
      }
    };

    ws.onclose = () => {
      if (!this.closed) {
        this.callbacks.onStatus("closed", "对讲已结束");
      }
      this.cleanupAudio();
    };
  }

  private startMicPump() {
    if (!this.mediaStream || this.closed) return;
    const AudioCtx =
      window.AudioContext ||
      (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext;
    const ctx = new AudioCtx();
    this.audioCtx = ctx;
    const src = ctx.createMediaStreamSource(this.mediaStream);
    const proc = ctx.createScriptProcessor(2048, 1, 1);
    this.processor = proc;

    const inRate = ctx.sampleRate;
    const outRate = 8000;

    proc.onaudioprocess = (ev) => {
      if (this.closed || !this.ws || this.ws.readyState !== WebSocket.OPEN) return;
      const input = ev.inputBuffer.getChannelData(0);
      const ratio = inRate / outRate;
      const outLen = Math.floor(input.length / ratio);
      const alawBytes = new Uint8Array(outLen);
      let sumSq = 0;

      for (let i = 0; i < outLen; i++) {
        const idx = Math.min(input.length - 1, Math.floor(i * ratio));
        const s = Math.max(-1, Math.min(1, input[idx]));
        sumSq += s * s;
        const pcm16 = s < 0 ? Math.round(s * 32768) : Math.round(s * 32767);
        alawBytes[i] = linearToAlaw(pcm16);
      }

      if (this.callbacks.onLevel && outLen > 0) {
        const rms = Math.sqrt(sumSq / outLen);
        this.callbacks.onLevel(Math.min(1, rms * 4.2));
      }

      this.ws.send(alawBytes.buffer);
    };

    src.connect(proc);
    proc.connect(ctx.destination);
  }

  private cleanupAudio() {
    if (this.processor) {
      try {
        this.processor.disconnect();
      } catch {}
      this.processor = null;
    }
    if (this.audioCtx) {
      this.audioCtx.close().catch(() => {});
      this.audioCtx = null;
    }
    if (this.mediaStream) {
      this.mediaStream.getTracks().forEach((t) => t.stop());
      this.mediaStream = null;
    }
    this.callbacks.onLevel?.(0);
  }

  stop() {
    this.closed = true;
    if (this.ws) {
      try {
        this.ws.close();
      } catch {}
      this.ws = null;
    }
    this.cleanupAudio();
    this.callbacks.onStatus("closed", "对讲已挂断");
  }
}
