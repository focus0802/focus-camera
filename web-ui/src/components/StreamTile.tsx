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
  animate,
  motion,
  useMotionValue,
  useReducedMotion,
} from "motion/react";
import { motionTokens } from "./arc/lib/motion-tokens";
import {
  cameraSupportsTalk,
  type Camera,
  type RecordingState,
} from "../types";
import { RoomAudioPlayer } from "../lib/audio";

/** Same settle spring as @uiarc/reorderable-list */
const settleSpring = { type: "spring" as const, stiffness: 520, damping: 40 };

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

interface StreamIssueInfo {
  title: string;
  hint: string;
  isChannelFull: boolean;
}

function describeStreamIssue(rawError: string): StreamIssueInfo {
  const text = rawError.trim();
  const lower = text.toLowerCase();

  if (
    lower.includes("406") ||
    lower.includes("453") ||
    text.includes("通道连接数已满") ||
    text.includes("通道已占满")
  ) {
    return {
      title: "摄像头画面通道已占满",
      hint: "可能被录像机或其他软件占用，可点击下方按钮重启释放",
      isChannelFull: true,
    };
  }

  if (lower.includes("401") || text.includes("密码") || text.includes("鉴权")) {
    return {
      title: "摄像头密码验证未通过",
      hint: "请在「设备管理」中核对该摄像头的登录密码",
      isChannelFull: false,
    };
  }

  if (lower.includes("404") || text.includes("未找到") || text.includes("码流路径")) {
    return {
      title: "未找到摄像头画面通道",
      hint: "请在「设备管理」中检查摄像头品牌或码流路径设置",
      isChannelFull: false,
    };
  }

  if (
    lower.includes("connection refused") ||
    lower.includes("actively refused") ||
    text.includes("拒绝连接") ||
    text.includes("正在重启")
  ) {
    return {
      title: "摄像头暂未就绪",
      hint: "设备可能正在重启或服务启动中，系统正在自动重连",
      isChannelFull: false,
    };
  }

  if (
    lower.includes("no route to host") ||
    lower.includes("host is down") ||
    lower.includes("unreachable") ||
    text.includes("离线") ||
    text.includes("网络不通")
  ) {
    return {
      title: "摄像头离线或网络不通",
      hint: "请检查摄像头电源、网线连接或 IP 地址设置",
      isChannelFull: false,
    };
  }

  if (
    lower.includes("i/o timeout") ||
    lower.includes("timed out") ||
    lower.includes("timeout") ||
    lower.includes("did not properly respond") ||
    lower.includes("failed to respond") ||
    text.includes("无法连接摄像头") ||
    text.includes("连接摄像头失败") ||
    text.includes("连接 RTSP 失败")
  ) {
    return {
      title: "无法连接摄像头",
      hint: "设备未响应，请检查电源、网线或 IP 地址是否正确",
      isChannelFull: false,
    };
  }

  if (
    lower.includes("connection reset") ||
    lower.includes("forcibly closed") ||
    lower.includes("broken pipe") ||
    lower.includes("eof") ||
    text.includes("中断") ||
    text.includes("超时")
  ) {
    return {
      title: "画面连接已中断",
      hint: "网络出现波动，系统正在自动尝试恢复画面",
      isChannelFull: false,
    };
  }

  return {
    title: "摄像头暂未连接",
    hint: "请检查设备电源与网络连接，系统正在自动重连",
    isChannelFull: false,
  };
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
  const [manualReconnecting, setManualReconnecting] = useState<boolean>(false);
  const [manualRetryFeedback, setManualRetryFeedback] = useState<string>("");
  const manualReconnectPendingRef = useRef<boolean>(false);
  const manualReconnectStartRef = useRef<number>(0);
  const manualFeedbackTimerRef = useRef<number | undefined>(undefined);
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
      const isManualAttempt = manualReconnectPendingRef.current;
      manualReconnectPendingRef.current = false;
      setStatus((prev) =>
        isManualAttempt
          ? "connecting"
          : hasPaintedOnce || prev === "live"
          ? "reconnecting"
          : "connecting"
      );

      const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
      const streamParam = stream === "stream1" ? "main" : "sub";
      const wsUrl = `${proto}//${window.location.host}/ws/stream?id=${encodeURIComponent(
        camera.id
      )}&stream=${streamParam}&audio=${audioEnabledRef.current ? "1" : "0"}${
        isManualAttempt ? "&reconnect=1" : ""
      }`;

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
            if (manualFeedbackTimerRef.current) {
              window.clearTimeout(manualFeedbackTimerRef.current);
              manualFeedbackTimerRef.current = undefined;
            }
            manualReconnectStartRef.current = 0;
            setManualReconnecting(false);
            setManualRetryFeedback("");
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
              if (manualFeedbackTimerRef.current) {
                window.clearTimeout(manualFeedbackTimerRef.current);
                manualFeedbackTimerRef.current = undefined;
              }
              manualReconnectStartRef.current = 0;
              setManualReconnecting(false);
              setManualRetryFeedback("");
              setStatus("live");
              setErrorText("");
              onStatusChange?.(camera.id, true);
            } else if (st.error) {
              const applyErrorState = () => {
                if (unmounted) return;
                const wasManual = manualReconnectStartRef.current > 0;
                manualReconnectStartRef.current = 0;
                setManualReconnecting(false);
                setErrorText(st.error);
                setStatus(hasPaintedOnce ? "reconnecting" : "error");
                if (wasManual) {
                  const nowStr = new Date().toLocaleTimeString("zh-CN", {
                    hour12: false,
                    hour: "2-digit",
                    minute: "2-digit",
                    second: "2-digit",
                  });
                  setManualRetryFeedback(`已于 ${nowStr} 重试，仍未连接成功`);
                }
                onStatusChange?.(camera.id, false);
              };

              if (manualReconnectStartRef.current > 0) {
                const elapsed = Date.now() - manualReconnectStartRef.current;
                const minDisplayMs = 750;
                if (elapsed < minDisplayMs) {
                  if (manualFeedbackTimerRef.current) {
                    window.clearTimeout(manualFeedbackTimerRef.current);
                  }
                  manualFeedbackTimerRef.current = window.setTimeout(
                    applyErrorState,
                    minDisplayMs - elapsed
                  );
                } else {
                  applyErrorState();
                }
              } else {
                applyErrorState();
              }
            }
          } catch {}
        }
      };

      ws.onclose = () => {
        if (unmounted) return;
        if (manualReconnectStartRef.current > 0) {
          const elapsed = Date.now() - manualReconnectStartRef.current;
          const remain = Math.max(0, 750 - elapsed);
          if (manualFeedbackTimerRef.current) {
            window.clearTimeout(manualFeedbackTimerRef.current);
          }
          manualFeedbackTimerRef.current = window.setTimeout(() => {
            if (unmounted) return;
            manualReconnectStartRef.current = 0;
            setManualReconnecting(false);
            setStatus((prev) => (prev === "live" ? "reconnecting" : "error"));
            const nowStr = new Date().toLocaleTimeString("zh-CN", {
              hour12: false,
              hour: "2-digit",
              minute: "2-digit",
              second: "2-digit",
            });
            setManualRetryFeedback(`已于 ${nowStr} 重试，仍未连接成功`);
          }, remain);
        }
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
      if (manualFeedbackTimerRef.current) {
        window.clearTimeout(manualFeedbackTimerRef.current);
        manualFeedbackTimerRef.current = undefined;
      }
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
  const issueInfo = describeStreamIssue(errorText);
  const isChannelFull406 = issueInfo.isChannelFull;
  const isConnected = hasFrame && status === "live";
  const raised = dragging || lifted;

  function handleManualReconnect(e: React.MouseEvent) {
    e.stopPropagation();
    if (manualReconnecting || rebooting) return;
    if (manualFeedbackTimerRef.current) {
      window.clearTimeout(manualFeedbackTimerRef.current);
      manualFeedbackTimerRef.current = undefined;
    }
    manualReconnectPendingRef.current = true;
    manualReconnectStartRef.current = Date.now();
    setManualReconnecting(true);
    setManualRetryFeedback("");
    setStatus("connecting");
    setErrorText("");
    setReloadNonce((n) => n + 1);
  }

  async function handleRebootClick(e: React.MouseEvent) {
    e.stopPropagation();
    if (!onRebootCamera || rebooting) return;
    setRebooting(true);
    setManualRetryFeedback("");
    try {
      await onRebootCamera(camera);
      window.setTimeout(() => {
        manualReconnectPendingRef.current = true;
        manualReconnectStartRef.current = Date.now();
        setManualReconnecting(true);
        setReloadNonce((n) => n + 1);
      }, 6000);
    } catch (err: any) {
      setManualRetryFeedback(err?.message || "重启指令发送失败");
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
          if (isConnected && !isModalView && onOpenZoom) {
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
                {rebooting
                  ? "正在重启摄像头…"
                  : manualReconnecting
                  ? "正在重新连接摄像头…"
                  : status === "connecting" || status === "live"
                  ? "正在加载实时画面…"
                  : issueInfo.title}
              </p>
              <p className="camera-tile__overlay-hint">
                {rebooting
                  ? "重启大约需要 6 秒，完成后将自动恢复画面"
                  : manualReconnecting
                  ? `正在尝试连接 ${camera.ip}，请稍候…`
                  : status === "connecting" || status === "live"
                  ? `正在连接 ${camera.ip}…`
                  : issueInfo.hint}
              </p>
              {manualRetryFeedback && !manualReconnecting && !rebooting && (
                <span className="camera-tile__retry-feedback" role="status" aria-live="polite">
                  {manualRetryFeedback}
                </span>
              )}
              <div className="camera-tile__overlay-actions">
                {isChannelFull406 && !manualReconnecting && onRebootCamera && (
                  <button
                    type="button"
                    className="tile-action-btn tile-action-btn--warn"
                    disabled={rebooting}
                    onClick={handleRebootClick}
                  >
                    <RotateCcw size={13} className={rebooting ? "spin-icon" : ""} />
                    <span>{rebooting ? "正在重启摄像头…" : "一键重启释放通道"}</span>
                  </button>
                )}
                <button
                  type="button"
                  className={`tile-action-btn ${
                    manualReconnecting ? "tile-action-btn--loading" : ""
                  }`}
                  disabled={manualReconnecting || rebooting}
                  onClick={handleManualReconnect}
                >
                  <RefreshCw size={13} className={manualReconnecting ? "spin-icon" : ""} />
                  <span>{manualReconnecting ? "正在重连…" : "立即重连"}</span>
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
            isRecording ? " · 正在录制 1080P+音频" : !isConnected ? " · 未连接" : ""
          } · 按住可拖拽排序，方向键可移动`}
          aria-label={`${camera.name}，位置 ${index + 1} / ${count}。按方向键可调整排序。`}
          onClick={() => {
            if (Date.now() - lastDragTimeRef.current < 260) return;
            if (isConnected && !isModalView && onOpenZoom) {
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
          <span className="camera-tile__name">{camera.name}</span>
          <span className="camera-tile__ip">{camera.ip}</span>
        </button>

        {isConnected && (
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
                  onPointerDown={(e) => {
                    if (e.button !== 0) return;
                    e.stopPropagation();
                    e.currentTarget.setPointerCapture(e.pointerId);
                    onStartPTT(camera);
                  }}
                  onPointerUp={(e) => {
                    if (e.currentTarget.hasPointerCapture(e.pointerId)) {
                      e.currentTarget.releasePointerCapture(e.pointerId);
                    }
                    onStopPTT(camera);
                  }}
                  onPointerCancel={() => {
                    if (talkActive && talkMode === "ptt") onStopPTT(camera);
                  }}
                  onLostPointerCapture={() => {
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
                className="tile-ctrl-btn"
                onClick={() => onOpenZoom(camera)}
                title="放大查看 1080P 高清主码流"
                aria-label={`放大查看 ${camera.name}`}
              >
                <Maximize2 size={13} />
                <span className="tile-ctrl-btn__label">全屏</span>
              </button>
            )}
          </div>
        )}
      </div>
    </motion.article>
  );
}
