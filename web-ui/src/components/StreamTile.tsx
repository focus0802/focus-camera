import { useEffect, useLayoutEffect, useRef, useState } from "react";
import {
  Maximize2,
  Mic,
  PhoneCall,
  PhoneOff,
  RefreshCw,
  RotateCcw,
  Square,
  Video,
  Volume2,
  VolumeX,
} from "lucide-react";
import {
  AnimatePresence,
  animate,
  motion,
  useMotionValue,
  useReducedMotion,
  type Variants,
} from "motion/react";
import { motionTokens } from "./arc/lib/motion-tokens";
import {
  cameraSupportsTalk,
  type Camera,
  type RecordingState,
} from "../types";
import { RoomAudioPlayer } from "../lib/audio";

/** Same settle spring & rolling digit variants as @uiarc/reorderable-list */
const settleSpring = { type: "spring" as const, stiffness: 520, damping: 40 };
const rollVariants: Variants = {
  enter: (direction: number) => ({
    opacity: 0,
    y: `${0.5 * direction}em`,
    filter: `blur(${motionTokens.blur.subtle}px)`,
  }),
  center: { opacity: 1, y: 0, filter: "blur(0px)" },
  exit: (direction: number) => ({
    opacity: 0,
    y: `${-0.5 * direction}em`,
    filter: `blur(${motionTokens.blur.subtle}px)`,
    transition: {
      duration: motionTokens.duration.instant,
      ease: [...motionTokens.ease.standard],
    },
  }),
};

interface StreamTileProps {
  camera: Camera;
  stream?: "stream1" | "stream2";
  recordingState?: RecordingState;
  audioEnabled: boolean;
  onToggleAudio: (cameraId: string) => void;
  talkActive: boolean;
  talkMode: "toggle" | "ptt" | null;
  onToggleTalk: (camera: Camera) => void;
  onStartPTT: (camera: Camera) => void;
  onStopPTT: (camera: Camera) => void;
  onToggleManualRecord: (camera: Camera, currentlyManual: boolean) => void;
  onOpenZoom?: (camera: Camera) => void;
  onRebootCamera?: (camera: Camera) => Promise<void>;
  onStatusChange?: (cameraId: string, online: boolean) => void;
  isModalView?: boolean;
  paused?: boolean;
  draggableCard?: boolean;
  index?: number;
  count?: number;
  gridColumns?: number;
  lifted?: boolean;
  tileRef?: (el: HTMLElement | null) => void;
  handleRef?: (el: HTMLButtonElement | null) => void;
  onKeyboardMove?: (cameraId: string, delta: number) => void;
  onPointerReorderStart?: (cameraId: string) => void;
  onPointerReorderMove?: (cameraId: string, clientX: number, clientY: number) => void;
  onPointerReorderEnd?: (cameraId: string) => void;
}

function hex2(b: number): string {
  return b.toString(16).padStart(2, "0");
}

function reverseBits32(v: number): number {
  let out = 0;
  for (let i = 0; i < 32; i++) {
    out = (out << 1) | ((v >>> i) & 1);
  }
  return out >>> 0;
}

function buildHEVCCodecString(hvcc: Uint8Array): string {
  if (hvcc.byteLength < 13) {
    return "hvc1.1.6.L120.90";
  }
  const spaceTierProfile = hvcc[1];
  const profileSpace = (spaceTierProfile >> 6) & 0x03;
  const tierFlag = (spaceTierProfile >> 5) & 0x01;
  const profileIdc = spaceTierProfile & 0x1f || 1;
  const spacePrefix = ["", "A", "B", "C"][profileSpace] ?? "";
  const compatRaw =
    ((hvcc[2] << 24) | (hvcc[3] << 16) | (hvcc[4] << 8) | hvcc[5]) >>> 0;
  const compatHex = (reverseBits32(compatRaw) || 6).toString(16).toUpperCase();
  const tierChar = tierFlag ? "H" : "L";
  const levelIdc = hvcc[12] || 120;

  const constraints: string[] = [];
  for (let i = 6; i <= 11; i++) {
    constraints.push(hvcc[i].toString(16).toUpperCase().padStart(2, "0"));
  }
  while (constraints.length > 1 && constraints[constraints.length - 1] === "00") {
    constraints.pop();
  }
  return `hvc1.${spacePrefix}${profileIdc}.${compatHex}.${tierChar}${levelIdc}.${constraints.join(".")}`;
}

export function StreamTile({
  camera,
  stream = "stream2",
  recordingState,
  audioEnabled,
  onToggleAudio,
  talkActive,
  talkMode,
  onToggleTalk,
  onStartPTT,
  onStopPTT,
  onToggleManualRecord,
  onOpenZoom,
  onRebootCamera,
  onStatusChange,
  isModalView = false,
  paused = false,
  draggableCard = false,
  index = 0,
  count = 1,
  gridColumns = 6,
  lifted = false,
  tileRef,
  handleRef,
  onKeyboardMove,
  onPointerReorderStart,
  onPointerReorderMove,
  onPointerReorderEnd,
}: StreamTileProps) {
  const localTileRef = useRef<HTMLElement | null>(null);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const audioPlayerRef = useRef<RoomAudioPlayer | null>(null);
  const lastDragTimeRef = useRef<number>(0);

  const reduced = useReducedMotion() ?? false;
  const x = useMotionValue(0);
  const y = useMotionValue(0);
  const [dragging, setDragging] = useState(false);
  const [settling, setSettling] = useState(false);
  const draggingRef = useRef(false);

  const dragStateRef = useRef({
    originOffsetLeft: 0,
    originOffsetTop: 0,
    startClientX: 0,
    startClientY: 0,
    lastClientX: 0,
    lastClientY: 0,
  });

  // Track rolling position direction like @uiarc/reorderable-list
  const [previousIndex, setPreviousIndex] = useState(index);
  const [direction, setDirection] = useState(1);
  if (previousIndex !== index) {
    setDirection(index > previousIndex ? 1 : -1);
    setPreviousIndex(index);
  }

  // Compensate x/y synchronously before paint whenever this card's grid slot moves mid-drag
  useLayoutEffect(() => {
    if (!draggingRef.current || !localTileRef.current) return;
    const el = localTileRef.current;
    const slotDx = el.offsetLeft - dragStateRef.current.originOffsetLeft;
    const slotDy = el.offsetTop - dragStateRef.current.originOffsetTop;
    const ptrDx = dragStateRef.current.lastClientX - dragStateRef.current.startClientX;
    const ptrDy = dragStateRef.current.lastClientY - dragStateRef.current.startClientY;
    x.set(ptrDx - slotDx);
    y.set(ptrDy - slotDy);
  }, [index, x, y]);

  const [status, setStatus] = useState<"connecting" | "live" | "reconnecting" | "error">("connecting");
  const [errorText, setErrorText] = useState<string>("");
  const [fps, setFps] = useState<number>(0);
  const [hasFrame, setHasFrame] = useState<boolean>(false);
  const [rebooting, setRebooting] = useState<boolean>(false);
  const [reloadNonce, setReloadNonce] = useState<number>(0);
  const audioEnabledRef = useRef<boolean>(audioEnabled);
  audioEnabledRef.current = audioEnabled;

  useEffect(() => {
    if (!audioPlayerRef.current) {
      audioPlayerRef.current = new RoomAudioPlayer();
    }
    audioPlayerRef.current.setEnabled(audioEnabled);
    if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
      try {
        wsRef.current.send(JSON.stringify({ action: "audio", enable: audioEnabled }));
      } catch {}
    }
  }, [audioEnabled]);

  useEffect(() => {
    return () => {
      audioPlayerRef.current?.destroy();
    };
  }, []);

  useEffect(() => {
    if (paused) {
      return;
    }
    let unmounted = false;
    let ws: WebSocket | null = null;
    let decoder: VideoDecoder | null = null;
    let waitingKeyframe = true;
    let reconnectTimer: number | undefined;
    let frameCounter = 0;
    let hasPaintedOnce = false;

    const fpsTimer = window.setInterval(() => {
      if (!unmounted) {
        setFps(frameCounter);
        frameCounter = 0;
      }
    }, 1000);

    const targetW = stream === "stream1" ? 1280 : 480;
    const targetH = stream === "stream1" ? 720 : 270;
    let currentCodecSig = "";
    let lastCodecStr = "";
    let lastAvcc: Uint8Array | null = null;
    let useDefaultAccel = !/Chrome|Edg/.test(navigator.userAgent);

    function configureAVCCDecoder(
      codecStr: string,
      avccDescription: Uint8Array,
      forceRecreate = false
    ) {
      if (typeof VideoDecoder === "undefined") return;
      lastCodecStr = codecStr;
      lastAvcc = avccDescription;
      const sig = `${codecStr}:${avccDescription.byteLength}:${avccDescription[1] ?? 0}:${
        avccDescription[avccDescription.byteLength - 1] ?? 0
      }:${useDefaultAccel ? "def" : "sw"}`;
      if (
        !forceRecreate &&
        decoder &&
        decoder.state === "configured" &&
        currentCodecSig === sig
      ) {
        return;
      }
      currentCodecSig = sig;
      try {
        if (decoder && decoder.state !== "closed") {
          decoder.close();
        }
      } catch {}

      waitingKeyframe = true;

      decoder = new VideoDecoder({
        output: (frame) => {
          if (unmounted) {
            frame.close();
            return;
          }
          frameCounter++;

          const canvas = canvasRef.current;
          if (canvas) {
            if (canvas.width !== targetW || canvas.height !== targetH) {
              canvas.width = targetW;
              canvas.height = targetH;
            }
            const ctx = canvas.getContext("2d", { alpha: false });
            if (ctx) {
              ctx.drawImage(frame, 0, 0, targetW, targetH);
              if (!hasPaintedOnce) {
                hasPaintedOnce = true;
                setHasFrame(true);
              }
            }
          }
          frame.close();
        },
        error: () => {
          useDefaultAccel = true;
          waitingKeyframe = true;
          currentCodecSig = "";
        },
      });

      try {
        decoder.configure({
          codec: codecStr,
          description: avccDescription,
          optimizeForLatency: true,
          hardwareAcceleration: useDefaultAccel ? "no-preference" : "prefer-software",
        });
      } catch {
        try {
          const altCodec = codecStr.startsWith("hvc1.")
            ? codecStr.replace(/^hvc1\./, "hev1.")
            : codecStr;
          decoder.configure({
            codec: altCodec,
            description: avccDescription,
            optimizeForLatency: true,
          });
        } catch {}
      }
    }

    function connect() {
      if (unmounted) return;
      setStatus((prev) => (hasPaintedOnce || prev === "live" ? "reconnecting" : "connecting"));

      const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
      const streamParam = stream === "stream1" ? "main" : "sub";
      const wsUrl = `${proto}//${window.location.host}/ws/stream?id=${encodeURIComponent(
        camera.id
      )}&stream=${streamParam}&audio=${audioEnabledRef.current ? "1" : "0"}`;

      ws = new WebSocket(wsUrl);
      ws.binaryType = "arraybuffer";
      wsRef.current = ws;

      ws.onopen = () => {
        if (unmounted || !ws) return;
        try {
          ws.send(JSON.stringify({ action: "audio", enable: audioEnabledRef.current }));
        } catch {}
      };

      ws.onmessage = (ev) => {
        if (unmounted) return;
        if (typeof ev.data === "string") return;

        const buf = new Uint8Array(ev.data as ArrayBuffer);
        if (buf.length < 2) return;

        const pktType = buf[0];

        if (pktType === 0x01) {
          if (buf.length > 10) {
            const rawDesc = buf.slice(4);
            const isHevc =
              buf[1] === 0xff && buf[2] === 0x48 && buf[3] === 0x35;
            const codecStr = isHevc
              ? buildHEVCCodecString(rawDesc)
              : `avc1.${hex2(buf[1])}${hex2(buf[2])}${hex2(buf[3])}`;
            configureAVCCDecoder(codecStr, rawDesc);
          }
          return;
        }

        if (pktType === 0x02) {
          if (buf.length <= 6) return;
          const isKeyframe = buf[1] === 1;
          const payload = buf.subarray(6);

          if ((!decoder || decoder.state !== "configured") && isKeyframe && lastCodecStr && lastAvcc) {
            configureAVCCDecoder(lastCodecStr, lastAvcc, true);
          }
          if (!decoder || decoder.state !== "configured") return;
          if (waitingKeyframe) {
            if (!isKeyframe) return;
            waitingKeyframe = false;
          }
          if (decoder.decodeQueueSize > 6) {
            waitingKeyframe = true;
            return;
          }
          try {
            const view = new DataView(buf.buffer, buf.byteOffset + 2, 4);
            const tsMs = view.getUint32(0, false);
            const chunk = new EncodedVideoChunk({
              type: isKeyframe ? "key" : "delta",
              timestamp: tsMs * 1000,
              data: payload,
            });
            decoder.decode(chunk);
            setStatus("live");
            setErrorText("");
            onStatusChange?.(camera.id, true);
          } catch {
            waitingKeyframe = true;
          }
          return;
        }

        if (pktType === 0x03) {
          audioPlayerRef.current?.feedAlaw(buf.subarray(1));
          return;
        }

        if (pktType === 0x04) {
          try {
            const jsonText = new TextDecoder().decode(buf.subarray(1));
            const st = JSON.parse(jsonText);
            if (st.status === "online") {
              setStatus("live");
              setErrorText("");
              onStatusChange?.(camera.id, true);
            } else if (st.error) {
              setErrorText(st.error);
              setStatus(hasPaintedOnce ? "reconnecting" : "error");
              onStatusChange?.(camera.id, false);
            }
          } catch {}
        }
      };

      ws.onclose = () => {
        if (unmounted) return;
        onStatusChange?.(camera.id, false);
        reconnectTimer = window.setTimeout(connect, 2200);
      };
    }

    connect();

    return () => {
      unmounted = true;
      wsRef.current = null;
      window.clearInterval(fpsTimer);
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      try {
        ws?.close();
      } catch {}
      try {
        if (decoder && decoder.state !== "closed") {
          decoder.close();
        }
      } catch {}
    };
  }, [camera.id, stream, reloadNonce, paused]);

  function startDragInteraction(e: React.PointerEvent) {
    if (isModalView || !draggableCard || e.button !== 0) return;
    const el = localTileRef.current;
    if (!el) return;

    const downX = e.clientX;
    const downY = e.clientY;
    let active = false;

    const activateDrag = () => {
      if (active) return;
      active = true;
      x.stop();
      y.stop();
      x.set(0);
      y.set(0);
      dragStateRef.current = {
        originOffsetLeft: el.offsetLeft,
        originOffsetTop: el.offsetTop,
        startClientX: downX,
        startClientY: downY,
        lastClientX: downX,
        lastClientY: downY,
      };
      draggingRef.current = true;
      setDragging(true);
      setSettling(false);
      onPointerReorderStart?.(camera.id);
    };

    const onPointerMove = (ev: PointerEvent) => {
      const dx = ev.clientX - downX;
      const dy = ev.clientY - downY;
      if (!active) {
        if (Math.hypot(dx, dy) < 5) return;
        activateDrag();
      }
      dragStateRef.current.lastClientX = ev.clientX;
      dragStateRef.current.lastClientY = ev.clientY;
      const curEl = localTileRef.current;
      if (curEl) {
        const slotDx = curEl.offsetLeft - dragStateRef.current.originOffsetLeft;
        const slotDy = curEl.offsetTop - dragStateRef.current.originOffsetTop;
        x.set(dx - slotDx);
        y.set(dy - slotDy);
      }
      onPointerReorderMove?.(camera.id, ev.clientX, ev.clientY);
    };

    const finishDrag = () => {
      window.removeEventListener("pointermove", onPointerMove);
      window.removeEventListener("pointerup", finishDrag);
      window.removeEventListener("pointercancel", finishDrag);

      if (!active) return;
      active = false;
      draggingRef.current = false;
      setDragging(false);
      lastDragTimeRef.current = Date.now();
      onPointerReorderEnd?.(camera.id);

      if (reduced) {
        x.jump(0);
        y.jump(0);
        setSettling(false);
      } else {
        setSettling(true);
        const ax = animate(x, 0, settleSpring);
        const ay = animate(y, 0, settleSpring);
        void Promise.all([ax.finished, ay.finished]).finally(() => {
          setSettling(false);
        });
      }
    };

    window.addEventListener("pointermove", onPointerMove);
    window.addEventListener("pointerup", finishDrag);
    window.addEventListener("pointercancel", finishDrag);
  }

  const isRecording = Boolean(recordingState?.active);
  const isManualRecording = isRecording && recordingState?.mode === "manual";
  const isChannelFull406 = errorText.includes("406") || errorText.includes("通道连接数已满");
  const raised = dragging || lifted;

  async function handleRebootClick(e: React.MouseEvent) {
    e.stopPropagation();
    if (!onRebootCamera || rebooting) return;
    setRebooting(true);
    try {
      await onRebootCamera(camera);
      window.setTimeout(() => setReloadNonce((n) => n + 1), 6000);
    } finally {
      setRebooting(false);
    }
  }

  return (
    <motion.article
      ref={(node) => {
        localTileRef.current = node;
        tileRef?.(node);
      }}
      layout={!isModalView && draggableCard && !dragging && !settling ? "position" : false}
      style={!isModalView && draggableCard ? { x, y } : undefined}
      animate={!isModalView && draggableCard ? { scale: raised && !reduced ? 1.022 : 1 } : undefined}
      transition={
        reduced
          ? { duration: 0 }
          : {
              ...motionTokens.spring.smooth,
              scale: motionTokens.spring.snappy,
            }
      }
      data-lifted={raised || undefined}
      className={`camera-tile ${isModalView ? "camera-tile--modal" : ""} ${
        talkActive ? "camera-tile--talking" : ""
      } ${isManualRecording ? "camera-tile--recording" : ""} ${
        audioEnabled && !isModalView ? "camera-tile--listening" : ""
      }`}
    >
      {/* 16:9 Pure Video Stage — no top bar cluttering camera OSD */}
      <div
        className="camera-tile__stage"
        onPointerDown={(e) => {
          if (
            isModalView ||
            !draggableCard ||
            (e.target as HTMLElement | null)?.closest(".camera-tile__overlay, button")
          ) {
            return;
          }
          startDragInteraction(e);
        }}
        onClick={() => {
          if (Date.now() - lastDragTimeRef.current < 260) return;
          if (!isModalView && onOpenZoom) {
            onOpenZoom(camera);
          }
        }}
      >
        <canvas ref={canvasRef} className="camera-tile__canvas" />

        {/* Subtle corner telemetry only on hover or when manual recording/reconnecting */}
        <div className="camera-tile__corner-badges">
          {isManualRecording && (
            <span className="rec-pill rec-pill--manual" title="正在手动录制 1080P 视频+音频">
              <span className="rec-pill__dot" />
              <span>REC</span>
            </span>
          )}
          {status === "reconnecting" && hasFrame && (
            <span className="status-pill-warn">重连中</span>
          )}
          {status === "live" && fps > 0 && (
            <span className="fps-pill">
              {fps} FPS · {camera.ip}
            </span>
          )}
        </div>

        {/* Center Overlay only when no frame has painted yet or 406 channel full */}
        {(!hasFrame || (isChannelFull406 && status !== "live")) && (
          <div className="camera-tile__overlay" onClick={(e) => e.stopPropagation()}>
            <div className="camera-tile__overlay-card">
              <p className="camera-tile__overlay-title">
                {isChannelFull406
                  ? "摄像头内部通道已占满 (406)"
                  : status === "connecting" || status === "live"
                  ? "正在加载实时画面…"
                  : errorText || "视频流重连中…"}
              </p>
              <div className="camera-tile__overlay-actions">
                {isChannelFull406 && onRebootCamera && (
                  <button
                    type="button"
                    className="tile-action-btn tile-action-btn--warn"
                    disabled={rebooting}
                    onClick={handleRebootClick}
                  >
                    <RotateCcw size={13} />
                    <span>{rebooting ? "正在重启摄像头…" : "一键重启释放通道"}</span>
                  </button>
                )}
                <button
                  type="button"
                  className="tile-action-btn"
                  onClick={() => setReloadNonce((n) => n + 1)}
                >
                  <RefreshCw size={13} />
                  <span>立即重连</span>
                </button>
              </div>
            </div>
          </div>
        )}
      </div>

      {/* Unified Single-Row Bottom Bar: Room Identity on Left + Smart Action Pills on Right */}
      <div
        className="camera-tile__footer"
        onPointerDown={(e) => {
          if (
            isModalView ||
            !draggableCard ||
            (e.target as HTMLElement | null)?.closest(".camera-tile__controls, .tile-zoom-btn")
          ) {
            return;
          }
          startDragInteraction(e);
        }}
      >
        <button
          ref={handleRef}
          type="button"
          className="camera-tile__identity"
          title={`${camera.name} (${camera.ip})${
            isRecording ? " · 正在录制 1080P+音频" : ""
          } · 按住可拖拽排序，方向键可移动`}
          aria-label={`${camera.name}，位置 ${index + 1} / ${count}。按方向键可调整排序。`}
          onClick={() => {
            if (Date.now() - lastDragTimeRef.current < 260) return;
            if (!isModalView && onOpenZoom) {
              onOpenZoom(camera);
            }
          }}
          onKeyDown={(e) => {
            if (!draggableCard || isModalView) return;
            if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
              e.preventDefault();
              onKeyboardMove?.(camera.id, e.key === "ArrowLeft" ? -1 : 1);
            } else if (e.key === "ArrowUp" || e.key === "ArrowDown") {
              e.preventDefault();
              onKeyboardMove?.(
                camera.id,
                e.key === "ArrowUp" ? -gridColumns : gridColumns
              );
            }
          }}
        >
          <span
            className={`status-dot status-dot--${
              status === "live"
                ? "live"
                : status === "reconnecting"
                ? "warn"
                : status === "error"
                ? "danger"
                : "connecting"
            }`}
          />
          {!isModalView && draggableCard && (
            <span className="camera-tile__position">
              <span className="sr-only">{String(index + 1).padStart(2, "0")}</span>
              <AnimatePresence mode="popLayout" initial={false} custom={direction}>
                <motion.span
                  key={reduced ? "static" : index}
                  aria-hidden="true"
                  className="camera-tile__digits"
                  custom={direction}
                  variants={rollVariants}
                  initial={reduced ? false : "enter"}
                  animate="center"
                  exit={reduced ? undefined : "exit"}
                  transition={{
                    duration: reduced ? 0 : motionTokens.duration.standard,
                    ease: [...motionTokens.ease.enter],
                  }}
                >
                  {String(index + 1).padStart(2, "0")}
                </motion.span>
              </AnimatePresence>
            </span>
          )}
          <span className="camera-tile__name">{camera.name}</span>
          <span className="camera-tile__ip">{camera.ip}</span>
        </button>

        <div
          className={`camera-tile__controls ${
            audioEnabled || talkActive || isManualRecording
              ? "camera-tile__controls--active"
              : ""
          }`}
          onClick={(e) => e.stopPropagation()}
        >
          <button
            type="button"
            className={`tile-ctrl-btn ${audioEnabled ? "tile-ctrl-btn--active" : ""}`}
            onClick={() => onToggleAudio(camera.id)}
            title={audioEnabled ? "关闭该房间声音监听" : "开启该房间声音监听"}
            aria-label={audioEnabled ? `关闭 ${camera.name} 声音监听` : `开启 ${camera.name} 声音监听`}
          >
            {audioEnabled ? <Volume2 size={13} /> : <VolumeX size={13} />}
            <span className="tile-ctrl-btn__label">
              {audioEnabled ? "监听中" : "监听"}
            </span>
          </button>

          {cameraSupportsTalk(camera) && (
            <>
              <button
                type="button"
                className={`tile-ctrl-btn ${
                  talkActive && talkMode === "toggle" ? "tile-ctrl-btn--talk" : ""
                }`}
                onClick={() => onToggleTalk(camera)}
                title="点击开启或挂断持续双向语音对讲"
                aria-label={`与 ${camera.name} 双向通话`}
              >
                {talkActive && talkMode === "toggle" ? (
                  <PhoneOff size={13} />
                ) : (
                  <PhoneCall size={13} />
                )}
                <span className="tile-ctrl-btn__label">
                  {talkActive && talkMode === "toggle" ? "挂断" : "通话"}
                </span>
              </button>

              <button
                type="button"
                className={`tile-ctrl-btn ${
                  talkActive && talkMode === "ptt" ? "tile-ctrl-btn--talk" : ""
                }`}
                onPointerDown={() => onStartPTT(camera)}
                onPointerUp={() => onStopPTT(camera)}
                onPointerLeave={() => {
                  if (talkActive && talkMode === "ptt") onStopPTT(camera);
                }}
                title="按住说话，松开结束对讲"
                aria-label={`按住对 ${camera.name} 说话`}
              >
                <Mic size={13} />
                <span className="tile-ctrl-btn__label">
                  {talkActive && talkMode === "ptt" ? "讲话中" : "按住讲"}
                </span>
              </button>
            </>
          )}

          <button
            type="button"
            className={`tile-ctrl-btn ${
              isManualRecording ? "tile-ctrl-btn--rec" : ""
            }`}
            onClick={() => onToggleManualRecord(camera, isManualRecording)}
            title={
              isManualRecording
                ? "点击停止手动录像并保存 MP4"
                : "点击立即手动录制 1080P 高清画面 + 声音"
            }
            aria-label={`录制 ${camera.name} 1080P 视频`}
          >
            {isManualRecording ? <Square size={12} /> : <Video size={13} />}
            <span className="tile-ctrl-btn__label">
              {isManualRecording ? "停录" : "录像"}
            </span>
          </button>

          {!isModalView && onOpenZoom && (
            <button
              type="button"
              className="tile-zoom-btn"
              onClick={() => onOpenZoom(camera)}
              title="放大查看 1080P 高清主码流"
              aria-label={`放大查看 ${camera.name}`}
            >
              <Maximize2 size={13} />
            </button>
          )}
        </div>
      </div>
    </motion.article>
  );
}
