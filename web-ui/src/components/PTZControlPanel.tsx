import { useCallback, useEffect, useRef, useState } from "react";
import {
  ArrowDown,
  ArrowDownLeft,
  ArrowDownRight,
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  ArrowUpLeft,
  ArrowUpRight,
  BookmarkPlus,
  Clock,
  Compass,
  Crosshair,
  FlipHorizontal2,
  FlipVertical2,
  RefreshCw,
  RotateCcw,
  Save,
  Trash2,
  Type,
} from "lucide-react";
import type {
  Camera,
  CameraOSDConfig,
  CameraTimeStatus,
  OSDPositionPreset,
  PTZDirection,
  PTZPreset,
} from "../types";
import { Button } from "./arc/button/button";
import { Badge } from "./arc/badge/badge";
import { Input } from "./arc/input/input";
import { Switch } from "./arc/switch/switch";
import SegmentedControl from "./arc/segmented-control/segmented-control";

export type ZoomSideTab = "ptz" | "osd";

interface PTZControlPanelProps {
  camera: Camera;
  activeTab?: ZoomSideTab;
  onTabChange?: (tab: ZoomSideTab) => void;
  onPresetsUpdated?: (cameraId: string, presets: PTZPreset[]) => void;
  onCameraUpdated?: () => Promise<void> | void;
}

export function formatTimeDriftLabel(driftSec?: number): string {
  if (typeof driftSec !== "number" || !Number.isFinite(driftSec)) {
    return "待读取";
  }
  const abs = Math.abs(Math.round(driftSec));
  if (abs <= 3) {
    return "误差 <3s";
  }
  const sign = driftSec > 0 ? "+" : "-";
  if (abs < 60) {
    return `偏差 ${sign}${abs}秒`;
  }
  const mins = Math.floor(abs / 60);
  const secs = abs % 60;
  if (mins < 60) {
    return `偏差 ${sign}${mins}分${secs > 0 ? `${secs}秒` : ""}`;
  }
  const hours = Math.floor(mins / 60);
  const remMins = mins % 60;
  if (hours < 48) {
    return `偏差 ${sign}${hours}小时${remMins > 0 ? `${remMins}分` : ""}`;
  }
  const days = Math.floor(hours / 24);
  return `偏差 ${sign}${days}天`;
}

export function PTZControlPanel({
  camera,
  activeTab = "ptz",
  onTabChange,
  onPresetsUpdated,
  onCameraUpdated,
}: PTZControlPanelProps) {
  const [internalTab, setInternalTab] = useState<ZoomSideTab>(activeTab);
  const currentTab = onTabChange ? activeTab : internalTab;
  const handleSelectTab = (next: ZoomSideTab) => {
    setInternalTab(next);
    onTabChange?.(next);
  };

  useEffect(() => {
    setInternalTab(activeTab);
  }, [activeTab]);

  // 1. PTZ State
  const [speed, setSpeed] = useState<string>("2");
  const [supportsHardwarePTZ, setSupportsHardwarePTZ] = useState<boolean>(
    camera.supports_ptz ?? true
  );
  const [detectedModel, setDetectedModel] = useState<string>(
    camera.model || ""
  );
  const [flipType, setFlipType] = useState<string>(camera.flip_type || "off");
  const [invertX, setInvertX] = useState<boolean>(
    camera.ptz_invert_x ??
      (camera.flip_type === "center" || camera.flip_type === "left_right")
  );
  const [invertY, setInvertY] = useState<boolean>(
    camera.ptz_invert_y ??
      (camera.flip_type === "center" || camera.flip_type === "up_down")
  );
  const [presets, setPresets] = useState<PTZPreset[]>(camera.ptz_presets ?? []);
  const [newPresetName, setNewPresetName] = useState<string>("");
  const [statusMsg, setStatusMsg] = useState<string>("");
  const [activeDir, setActiveDir] = useState<PTZDirection | null>(null);
  const [calibrating, setCalibrating] = useState<boolean>(false);
  const [savingPreset, setSavingPreset] = useState<boolean>(false);

  // 2. OSD & Time Sync State
  const [loadingOSDTime, setLoadingOSDTime] = useState<boolean>(false);
  const [osdLabelEnabled, setOsdLabelEnabled] = useState<boolean>(true);
  const [osdLabelText, setOsdLabelText] = useState<string>(camera.name || "");
  const [osdLabel2Enabled, setOsdLabel2Enabled] = useState<boolean>(false);
  const [osdLabel2Text, setOsdLabel2Text] = useState<string>("");
  const [osdDateEnabled, setOsdDateEnabled] = useState<boolean>(true);
  const [osdWeekEnabled, setOsdWeekEnabled] = useState<boolean>(false);
  const [osdPosition, setOsdPosition] = useState<OSDPositionPreset>("keep");
  const [detectedCorner, setDetectedCorner] = useState<string>("");
  const [syncLocalCamName, setSyncLocalCamName] = useState<boolean>(true);
  const [savingOSD, setSavingOSD] = useState<boolean>(false);

  const [timeStatus, setTimeStatus] = useState<CameraTimeStatus | null>(null);
  const [timeFetchedAtMs, setTimeFetchedAtMs] = useState<number>(Date.now());
  const [liveNowMs, setLiveNowMs] = useState<number>(Date.now());
  const [syncingTime, setSyncingTime] = useState<boolean>(false);
  const [customTimeMode, setCustomTimeMode] = useState<boolean>(false);
  const [customDateTime, setCustomDateTime] = useState<string>("");
  const [osdStatusMsg, setOsdStatusMsg] = useState<{
    ok: boolean;
    text: string;
  } | null>(null);

  useEffect(() => {
    if (currentTab !== "osd") return;
    setLiveNowMs(Date.now());
    const timer = window.setInterval(() => {
      setLiveNowMs(Date.now());
    }, 1000);
    return () => window.clearInterval(timer);
  }, [currentTab]);

  const holdTimerRef = useRef<number | null>(null);
  const holdKeepaliveRef = useRef<number | null>(null);
  const isHoldingMoveRef = useRef<boolean>(false);
  const activePressDirRef = useRef<PTZDirection | null>(null);
  const activeKeyDirRef = useRef<PTZDirection | null>(null);

  const loadOSDAndTime = useCallback(
    async (camId: string, fallbackName: string) => {
      setLoadingOSDTime(true);
      try {
        const res = await fetch(
          `/api/cameras/osd-time?id=${encodeURIComponent(camId)}`
        );
        const contentType = res.headers.get("content-type") || "";
        if (!contentType.includes("application/json")) return;
        const data = await res.json();
        if (data.osd) {
          const o: CameraOSDConfig = data.osd;
          setOsdLabelEnabled(Boolean(o.label_enabled));
          setOsdLabelText(o.label_text || fallbackName || "");
          const l2On = Boolean(o.label2_enabled);
          const rawL2 = (o.label2_text || "").trim();
          const isPlaceholderL2 =
            !l2On ||
            rawL2 === "自定义字符1" ||
            rawL2 === "自定义字符2" ||
            rawL2 === "自定义字符3" ||
            rawL2 === "Custom String 1";
          setOsdLabel2Enabled(l2On && !isPlaceholderL2);
          setOsdLabel2Text(isPlaceholderL2 ? "" : rawL2);
          setOsdDateEnabled(Boolean(o.date_enabled));
          setOsdWeekEnabled(Boolean(o.week_enabled));
          setDetectedCorner(o.position_preset || "");
          setOsdPosition("keep");
        }
        if (data.time) {
          setTimeStatus(data.time as CameraTimeStatus);
          setTimeFetchedAtMs(Date.now());
          setLiveNowMs(Date.now());
        }
      } catch {
        // Ignore transient network errors
      } finally {
        setLoadingOSDTime(false);
      }
    },
    []
  );

  // Probe PTZ capabilities, hardware flip_type, presets & OSD/time on mount / camera change
  useEffect(() => {
    let cancelled = false;
    setPresets(camera.ptz_presets ?? []);
    setSupportsHardwarePTZ(camera.supports_ptz ?? true);
    setDetectedModel(camera.model || "");
    const initFlip = camera.flip_type || "off";
    setFlipType(initFlip);
    setInvertX(
      camera.ptz_invert_x ??
        (initFlip === "center" || camera.flip_type === "left_right")
    );
    setInvertY(
      camera.ptz_invert_y ?? (initFlip === "center" || initFlip === "up_down")
    );
    setOsdLabelText(camera.name || "");
    setOsdLabel2Text("");
    setOsdLabel2Enabled(false);
    setOsdStatusMsg(null);
    setTimeStatus(null);

    async function loadCapabilities() {
      try {
        const res = await fetch(
          `/api/cameras/ptz?id=${encodeURIComponent(camera.id)}`
        );
        if (!res.ok || cancelled) return;
        const contentType = res.headers.get("content-type") || "";
        if (!contentType.includes("application/json")) return;
        const data = await res.json();
        if (cancelled) return;
        setSupportsHardwarePTZ(Boolean(data.supports_ptz));
        if (data.model) {
          setDetectedModel(data.model);
        }
        if (typeof data.flip_type === "string" && data.flip_type) {
          setFlipType(data.flip_type);
        }
        if (typeof data.invert_x === "boolean") {
          setInvertX(data.invert_x);
        }
        if (typeof data.invert_y === "boolean") {
          setInvertY(data.invert_y);
        }
        if (Array.isArray(data.presets)) {
          setPresets(data.presets);
        }
        if (data.error) {
          setStatusMsg(data.error);
        }
      } catch {
        // Ignore transient probe errors
      }
    }

    async function loadSequentially() {
      if (currentTab === "osd") {
        await loadOSDAndTime(camera.id, camera.name);
        if (!cancelled) {
          await loadCapabilities();
        }
      } else {
        await loadCapabilities();
        if (!cancelled) {
          await loadOSDAndTime(camera.id, camera.name);
        }
      }
    }
    void loadSequentially();
    return () => {
      cancelled = true;
    };
    // Only re-probe when switching to a different camera ID
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [camera.id]);

  const sendPTZCommand = useCallback(
    async (payload: Record<string, unknown>) => {
      try {
        const res = await fetch("/api/cameras/ptz", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            id: camera.id,
            speed: Number(speed) || 2,
            ...payload,
          }),
        });
        const contentType = res.headers.get("content-type") || "";
        if (!contentType.includes("application/json")) {
          setStatusMsg(`云台请求失败 (HTTP ${res.status})`);
          return null;
        }
        const data = await res.json();
        if (typeof data.supports_ptz === "boolean" && data.hardware_executed) {
          setSupportsHardwarePTZ(true);
        }
        if (typeof data.flip_type === "string" && data.flip_type) {
          setFlipType(data.flip_type);
        }
        if (typeof data.invert_x === "boolean") {
          setInvertX(data.invert_x);
        }
        if (typeof data.invert_y === "boolean") {
          setInvertY(data.invert_y);
        }
        if (Array.isArray(data.presets)) {
          setPresets(data.presets);
          onPresetsUpdated?.(camera.id, data.presets);
        }
        if (data.error) {
          setStatusMsg(data.error);
        } else if (data.message) {
          setStatusMsg(data.message);
        }
        return data;
      } catch {
        setStatusMsg("云台控制指令发送失败，请检查摄像头网络连接");
        return null;
      }
    },
    [camera.id, speed, onPresetsUpdated]
  );

  const handleFlipTypeChange = useCallback(
    async (clickedMode: "off" | "center") => {
      const targetFlip =
        clickedMode === "center" && flipType === "center" ? "off" : clickedMode;
      setFlipType(targetFlip);
      setInvertX(targetFlip === "center");
      setInvertY(targetFlip === "center");
      await sendPTZCommand({
        action: "set_flip",
        flip_type: targetFlip,
      });
    },
    [flipType, sendPTZCommand]
  );

  const handleToggleInvertX = useCallback(async () => {
    const next = !invertX;
    setInvertX(next);
    await sendPTZCommand({
      action: "set_invert",
      invert_x: next,
      invert_y: invertY,
    });
  }, [invertX, invertY, sendPTZCommand]);

  const handleToggleInvertY = useCallback(async () => {
    const next = !invertY;
    setInvertY(next);
    await sendPTZCommand({
      action: "set_invert",
      invert_x: invertX,
      invert_y: next,
    });
  }, [invertX, invertY, sendPTZCommand]);

  const stopAllMovement = useCallback(
    (forceStopCmd = false) => {
      if (holdTimerRef.current) {
        window.clearTimeout(holdTimerRef.current);
        holdTimerRef.current = null;
      }
      if (holdKeepaliveRef.current) {
        window.clearInterval(holdKeepaliveRef.current);
        holdKeepaliveRef.current = null;
      }
      if (isHoldingMoveRef.current || forceStopCmd) {
        isHoldingMoveRef.current = false;
        void sendPTZCommand({ action: "stop" });
      }
      activePressDirRef.current = null;
      setActiveDir(null);
    },
    [sendPTZCommand]
  );

  useEffect(() => {
    return () => {
      if (holdTimerRef.current) window.clearTimeout(holdTimerRef.current);
      if (holdKeepaliveRef.current) window.clearInterval(holdKeepaliveRef.current);
    };
  }, []);

  const startDirectionControl = useCallback(
    (dir: PTZDirection) => {
      stopAllMovement();
      activePressDirRef.current = dir;
      setActiveDir(dir);

      holdTimerRef.current = window.setTimeout(() => {
        holdTimerRef.current = null;
        isHoldingMoveRef.current = true;
        void sendPTZCommand({ action: "move", direction: dir });
        holdKeepaliveRef.current = window.setInterval(() => {
          if (isHoldingMoveRef.current) {
            void sendPTZCommand({ action: "move", direction: dir });
          }
        }, 1800);
      }, 260);
    },
    [stopAllMovement, sendPTZCommand]
  );

  const finishDirectionControl = useCallback(
    (dir: PTZDirection) => {
      if (activePressDirRef.current !== dir) {
        return;
      }
      const wasHolding = isHoldingMoveRef.current;
      stopAllMovement();
      if (!wasHolding) {
        void sendPTZCommand({ action: "step", direction: dir });
      }
    },
    [stopAllMovement, sendPTZCommand]
  );

  const handleStopButton = useCallback(() => {
    stopAllMovement(true);
    setStatusMsg("已发送停止转动指令");
  }, [stopAllMovement]);

  const handleCalibrateHardware = useCallback(async () => {
    setCalibrating(true);
    try {
      await sendPTZCommand({ action: "calibrate" });
    } finally {
      setCalibrating(false);
    }
  }, [sendPTZCommand]);

  const handleSavePreset = useCallback(
    async (e?: React.FormEvent) => {
      e?.preventDefault();
      if (savingPreset) return;
      setSavingPreset(true);
      try {
        const name = newPresetName.trim() || `视角 ${presets.length + 1}`;
        await sendPTZCommand({
          action: "preset_set",
          preset_name: name,
        });
        setNewPresetName("");
      } finally {
        setSavingPreset(false);
      }
    },
    [savingPreset, newPresetName, presets.length, sendPTZCommand]
  );

  const handleGotoPreset = useCallback(
    async (preset: PTZPreset) => {
      await sendPTZCommand({
        action: "preset_goto",
        preset_id: preset.hw_token || preset.id,
      });
      setStatusMsg(`已转到预置位「${preset.name}」`);
    },
    [sendPTZCommand]
  );

  const handleDeletePreset = useCallback(
    async (preset: PTZPreset, e: React.MouseEvent) => {
      e.stopPropagation();
      await sendPTZCommand({
        action: "preset_delete",
        preset_id: preset.id,
      });
    },
    [sendPTZCommand]
  );

  const handleSaveOSD = async (e?: React.FormEvent) => {
    e?.preventDefault();
    setSavingOSD(true);
    setOsdStatusMsg(null);
    try {
      const res = await fetch("/api/cameras/osd", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          id: camera.id,
          label_enabled: osdLabelEnabled,
          label_text: osdLabelText.trim(),
          label2_enabled: osdLabel2Enabled,
          label2_text: osdLabel2Text.trim(),
          date_enabled: osdDateEnabled,
          week_enabled: osdWeekEnabled,
          position: osdPosition,
          sync_camera_name: syncLocalCamName && osdLabelText.trim() !== "",
        }),
      });
      const data = await res.json();
      if (!res.ok || !data.ok) {
        setOsdStatusMsg({
          ok: false,
          text: data.error || "写入画面文字失败",
        });
        return;
      }
      if (data.osd) {
        const o: CameraOSDConfig = data.osd;
        setOsdLabelEnabled(Boolean(o.label_enabled));
        setOsdLabelText(o.label_text || osdLabelText.trim());
        const l2On = Boolean(o.label2_enabled);
        const rawL2 = (o.label2_text || "").trim();
        const isPlaceholderL2 =
          !l2On ||
          rawL2 === "自定义字符1" ||
          rawL2 === "自定义字符2" ||
          rawL2 === "自定义字符3" ||
          rawL2 === "Custom String 1";
        setOsdLabel2Enabled(l2On && !isPlaceholderL2);
        setOsdLabel2Text(isPlaceholderL2 ? "" : rawL2);
        setOsdDateEnabled(Boolean(o.date_enabled));
        setOsdWeekEnabled(Boolean(o.week_enabled));
        setDetectedCorner(o.position_preset || "");
        if (
          osdPosition !== "keep" &&
          (o.position_preset === "top_left" ||
            o.position_preset === "bottom_left" ||
            o.position_preset === "top_right" ||
            o.position_preset === "bottom_right")
        ) {
          setOsdPosition(o.position_preset);
        }
      }
      setOsdStatusMsg({
        ok: true,
        text: "画面 OSD 水印已实时生效",
      });
      if (syncLocalCamName && osdLabelText.trim() !== "") {
        await onCameraUpdated?.();
      }
    } catch {
      setOsdStatusMsg({
        ok: false,
        text: "请求 OSD 接口失败，请检查网络连接",
      });
    } finally {
      setSavingOSD(false);
    }
  };

  const handleSyncCameraTime = async (useCustom: boolean) => {
    setSyncingTime(true);
    setOsdStatusMsg(null);
    try {
      const bodyPayload: Record<string, unknown> = {
        id: camera.id,
      };
      if (useCustom && customDateTime.trim() !== "") {
        bodyPayload.datetime = customDateTime.trim();
      }
      const res = await fetch("/api/cameras/time", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(bodyPayload),
      });
      const data = await res.json();
      if (!res.ok || !data.ok) {
        setOsdStatusMsg({
          ok: false,
          text: data.error || "摄像头校时失败",
        });
        return;
      }
      if (data.time) {
        setTimeStatus(data.time as CameraTimeStatus);
        setTimeFetchedAtMs(Date.now());
        setLiveNowMs(Date.now());
      }
      setOsdStatusMsg({
        ok: true,
        text: useCustom
          ? `已将机身时间设为 ${data.time?.camera_time || customDateTime}`
          : `已同步至本机时间 (${data.time?.camera_time || "刚刚"})`,
      });
      if (useCustom) {
        setCustomTimeMode(false);
      }
    } catch {
      setOsdStatusMsg({
        ok: false,
        text: "请求校时接口失败，请检查网络连接",
      });
    } finally {
      setSyncingTime(false);
    }
  };

  // Keyboard shortcuts (WASD / Arrow keys for physical PTZ, 0 to stop) while 1080P Modal is open
  useEffect(() => {
    const keyToDir = (key: string): PTZDirection | null => {
      switch (key) {
        case "ArrowUp":
        case "w":
        case "W":
          return "up";
        case "ArrowDown":
        case "s":
        case "S":
          return "down";
        case "ArrowLeft":
        case "a":
        case "A":
          return "left";
        case "ArrowRight":
        case "d":
        case "D":
          return "right";
        default:
          return null;
      }
    };

    const onKeyDown = (e: KeyboardEvent) => {
      if (
        e.target instanceof HTMLInputElement ||
        e.target instanceof HTMLTextAreaElement ||
        e.metaKey ||
        e.ctrlKey ||
        e.altKey
      ) {
        return;
      }
      const dir = keyToDir(e.key);
      if (dir) {
        e.preventDefault();
        if (e.repeat || activeKeyDirRef.current === dir) return;
        activeKeyDirRef.current = dir;
        startDirectionControl(dir);
        return;
      }
      if (e.key === "0") {
        e.preventDefault();
        handleStopButton();
      }
    };

    const onKeyUp = (e: KeyboardEvent) => {
      const dir = keyToDir(e.key);
      if (dir && activeKeyDirRef.current === dir) {
        e.preventDefault();
        activeKeyDirRef.current = null;
        finishDirectionControl(dir);
      }
    };

    window.addEventListener("keydown", onKeyDown);
    window.addEventListener("keyup", onKeyUp);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("keyup", onKeyUp);
    };
  }, [startDirectionControl, finishDirectionControl, handleStopButton]);

  const dpadItems: Array<{
    dir?: PTZDirection;
    label: string;
    icon: React.ReactNode;
    isCenter?: boolean;
  }> = [
    { dir: "up_left", label: "左上", icon: <ArrowUpLeft size={15} /> },
    { dir: "up", label: "向上 (W / ↑)", icon: <ArrowUp size={16} /> },
    { dir: "up_right", label: "右上", icon: <ArrowUpRight size={15} /> },
    { dir: "left", label: "向左 (A / ←)", icon: <ArrowLeft size={16} /> },
    {
      isCenter: true,
      label: "立即停止云台转动 (快捷键 0)",
      icon: <Crosshair size={16} />,
    },
    { dir: "right", label: "向右 (D / →)", icon: <ArrowRight size={16} /> },
    { dir: "down_left", label: "左下", icon: <ArrowDownLeft size={15} /> },
    { dir: "down", label: "向下 (S / ↓)", icon: <ArrowDown size={16} /> },
    { dir: "down_right", label: "右下", icon: <ArrowDownRight size={15} /> },
  ];

  const driftAbs = Math.abs(timeStatus?.drift_seconds ?? 0);
  const formatEpochSeconds = (unixSec: number): string => {
    const d = new Date(unixSec * 1000);
    const pad = (n: number) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(
      d.getHours()
    )}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  };
  const elapsedSec = Math.max(0, Math.floor((liveNowMs - timeFetchedAtMs) / 1000));
  const liveCameraTimeStr =
    timeStatus?.camera_unix && timeStatus.camera_unix > 0
      ? formatEpochSeconds(timeStatus.camera_unix + elapsedSec)
      : timeStatus?.camera_time || "—";
  const liveHostTimeStr = formatEpochSeconds(Math.floor(liveNowMs / 1000));
  const cornerLabelMap: Record<string, string> = {
    top_left: "左上角",
    bottom_left: "左下角",
    top_right: "右上角",
    bottom_right: "右下角",
  };

  return (
    <aside className="ptz-panel" aria-label="云台与画面控制面板">
      <div className="ptz-panel__header">
        <SegmentedControl
          label="控制面板功能切换"
          value={currentTab}
          onValueChange={(v) => handleSelectTab(v as ZoomSideTab)}
          options={[
            { value: "ptz", label: "云台控制" },
            { value: "osd", label: "画面文字与校时" },
          ]}
        />
      </div>

      {currentTab === "ptz" ? (
        <>
          <div className="ptz-panel__title-row">
            <div className="ptz-panel__title-left">
              <Compass size={15} className="ptz-panel__icon" />
              <strong>物理云台控制</strong>
            </div>
            <div className="ptz-panel__badges">
              <Badge
                tone={supportsHardwarePTZ ? "success" : "warning"}
                size="sm"
              >
                {supportsHardwarePTZ
                  ? detectedModel
                    ? `${detectedModel}`
                    : "硬件云台"
                  : "未检测到电机"}
              </Badge>
              <Badge
                tone={flipType === "center" ? "info" : "neutral"}
                size="sm"
              >
                {flipType === "center" ? "倒装 180°" : "正装"}
              </Badge>
            </div>
          </div>

          {/* 8-Way Physical Directional Pad */}
          <div className="ptz-dpad-section">
            <div
              className="ptz-dpad-grid"
              role="group"
              aria-label="八向物理云台方向盘"
            >
              {dpadItems.map((item, idx) => {
                if (item.isCenter || !item.dir) {
                  return (
                    <button
                      key={`center-${idx}`}
                      type="button"
                      className="ptz-dpad-btn ptz-dpad-btn--center"
                      title={item.label}
                      aria-label={item.label}
                      onClick={handleStopButton}
                    >
                      {item.icon}
                    </button>
                  );
                }
                const dir = item.dir;
                const isActive = activeDir === dir;
                return (
                  <button
                    key={dir}
                    type="button"
                    className={`ptz-dpad-btn ${
                      isActive ? "ptz-dpad-btn--active" : ""
                    }`}
                    title={`${item.label} · 点击步进微调，按住连续旋转`}
                    aria-label={item.label}
                    onPointerDown={(e) => {
                      if (e.button !== 0) return;
                      e.currentTarget.setPointerCapture(e.pointerId);
                      startDirectionControl(dir);
                    }}
                    onPointerUp={(e) => {
                      if (e.currentTarget.hasPointerCapture(e.pointerId)) {
                        e.currentTarget.releasePointerCapture(e.pointerId);
                      }
                      finishDirectionControl(dir);
                    }}
                    onLostPointerCapture={() => {
                      if (activeDir === dir) {
                        finishDirectionControl(dir);
                      }
                    }}
                  >
                    {item.icon}
                  </button>
                );
              })}
            </div>

            <div className="ptz-invert-row">
              <span className="ptz-field-label">旋转速度</span>
              <div
                className="ptz-speed-actions"
                role="group"
                aria-label="云台旋转速度"
              >
                {[
                  { value: "1", label: "慢速" },
                  { value: "2", label: "标准" },
                  { value: "3", label: "快速" },
                ].map((opt) => (
                  <button
                    key={opt.value}
                    type="button"
                    className={`ptz-invert-btn ${
                      speed === opt.value ? "ptz-invert-btn--active" : ""
                    }`}
                    onClick={() => setSpeed(opt.value)}
                  >
                    <span>{opt.label}</span>
                  </button>
                ))}
              </div>
            </div>

            {/* Mounting Mode & Direction Inversion */}
            <div className="ptz-mount-box">
              <div className="ptz-invert-row">
                <span className="ptz-field-label">安装模式</span>
                <div className="ptz-invert-actions">
                  <button
                    type="button"
                    className={`ptz-invert-btn ${
                      flipType !== "center" ? "ptz-invert-btn--active" : ""
                    }`}
                    onClick={() => void handleFlipTypeChange("off")}
                    title="切换为正装标准模式"
                  >
                    <span>正装</span>
                  </button>
                  <button
                    type="button"
                    className={`ptz-invert-btn ${
                      flipType === "center" ? "ptz-invert-btn--active" : ""
                    }`}
                    onClick={() => void handleFlipTypeChange("center")}
                    title="切换为倒装 180° 翻转模式"
                  >
                    <span>倒装 180°</span>
                  </button>
                </div>
              </div>

              <div className="ptz-invert-row">
                <span className="ptz-field-label">方向校准</span>
                <div className="ptz-invert-actions">
                  <button
                    type="button"
                    className={`ptz-invert-btn ${
                      invertX ? "ptz-invert-btn--active" : ""
                    }`}
                    onClick={() => void handleToggleInvertX()}
                    title="仅反转左右控制方向"
                  >
                    <FlipHorizontal2 size={13} />
                    <span>左右反向</span>
                  </button>
                  <button
                    type="button"
                    className={`ptz-invert-btn ${
                      invertY ? "ptz-invert-btn--active" : ""
                    }`}
                    onClick={() => void handleToggleInvertY()}
                    title="仅反转上下控制方向"
                  >
                    <FlipVertical2 size={13} />
                    <span>上下反向</span>
                  </button>
                </div>
              </div>
            </div>
          </div>

          {/* Presets & Calibration Section */}
          <div className="ptz-presets-section">
            <div className="ptz-section-head">
              <span className="ptz-field-label">预置位 ({presets.length})</span>
              <button
                type="button"
                className="ptz-cali-link"
                disabled={calibrating}
                onClick={() => void handleCalibrateHardware()}
                title="触发物理云台步进电机自检归位"
              >
                <RotateCcw size={12} />
                <span>{calibrating ? "校准中…" : "云台校准"}</span>
              </button>
            </div>

            <form
              className="ptz-preset-form"
              onSubmit={(e) => void handleSavePreset(e)}
            >
              <div className="ptz-preset-form__input">
                <Input
                  label="新预置位名称"
                  placeholder="预置位名称…"
                  value={newPresetName}
                  onChange={(e) => setNewPresetName(e.target.value)}
                />
              </div>
              <Button
                type="submit"
                variant="secondary"
                size="sm"
                loading={savingPreset}
                title="保存当前物理云台角度为快捷预置位"
              >
                <BookmarkPlus size={14} />
                <span>存预置位</span>
              </Button>
            </form>

            {presets.length > 0 && (
              <div className="ptz-preset-list">
                {presets.map((p) => (
                  <div key={p.id} className="ptz-preset-chip">
                    <button
                      type="button"
                      className="ptz-preset-chip__main"
                      onClick={() => void handleGotoPreset(p)}
                      title={`点击转到「${p.name}」`}
                    >
                      <span className="ptz-preset-chip__name">{p.name}</span>
                    </button>
                    <button
                      type="button"
                      className="ptz-preset-chip__del"
                      onClick={(e) => void handleDeletePreset(p, e)}
                      title={`删除预置位「${p.name}」`}
                      aria-label={`删除预置位 ${p.name}`}
                    >
                      <Trash2 size={12} />
                    </button>
                  </div>
                ))}
              </div>
            )}
          </div>

          {statusMsg && (
            <p className="ptz-status-line" role="status">
              {statusMsg}
            </p>
          )}
        </>
      ) : (
        /* Tab 2: Real-Time Hardware OSD Overlay & Camera System Clock Sync */
        <div className="ptz-osd-tab">
          <form className="ptz-osd-section" onSubmit={(e) => void handleSaveOSD(e)}>
            <div className="ptz-section-head">
              <div className="ptz-panel__title-left">
                <Type size={14} className="ptz-panel__icon" />
                <strong>画面自定义文字 (OSD)</strong>
              </div>
              <button
                type="button"
                className="ptz-cali-link"
                disabled={loadingOSDTime}
                onClick={() => void loadOSDAndTime(camera.id, camera.name)}
                title="从摄像头机身重新读取当前 OSD 与时间配置"
              >
                <RefreshCw size={11} />
                <span>{loadingOSDTime ? "读取中…" : "刷新"}</span>
              </button>
            </div>

            <Input
              label="主画面文字 (房间/机位名)"
              value={osdLabelText}
              onChange={(e) => setOsdLabelText(e.target.value)}
              placeholder="例如：1号琴房"
            />

            <Input
              label="附加自定义文字 (第 2 行，可选)"
              value={osdLabel2Text}
              onChange={(e) => {
                const val = e.target.value;
                setOsdLabel2Text(val);
                if (val.trim() !== "" && !osdLabel2Enabled) {
                  setOsdLabel2Enabled(true);
                } else if (val.trim() === "" && osdLabel2Enabled) {
                  setOsdLabel2Enabled(false);
                }
              }}
              placeholder="如：监控区域 / 请爱护乐器"
            />

            <div className="ptz-osd-switches">
              <Switch
                checked={osdLabelEnabled}
                onCheckedChange={setOsdLabelEnabled}
                label="显示主文字"
              />
              <Switch
                checked={osdDateEnabled}
                onCheckedChange={setOsdDateEnabled}
                label="显示时间水印"
              />
              <Switch
                checked={osdWeekEnabled}
                onCheckedChange={setOsdWeekEnabled}
                label="显示星期"
              />
              <Switch
                checked={syncLocalCamName}
                onCheckedChange={setSyncLocalCamName}
                label="同步软件名称"
              />
            </div>

            <div className="ptz-invert-row">
              <div className="ptz-section-head">
                <span className="ptz-field-label">文字位置</span>
                {detectedCorner && cornerLabelMap[detectedCorner] && (
                  <span className="ptz-corner-hint">
                    当前: {cornerLabelMap[detectedCorner]}
                  </span>
                )}
              </div>
              <div
                className="ptz-pos-actions"
                role="group"
                aria-label="画面文字位置"
              >
                {(
                  [
                    { value: "keep", label: "原位" },
                    { value: "top_left", label: "左上" },
                    { value: "bottom_left", label: "左下" },
                    { value: "top_right", label: "右上" },
                    { value: "bottom_right", label: "右下" },
                  ] as const
                ).map((opt) => (
                  <button
                    key={opt.value}
                    type="button"
                    className={`ptz-invert-btn ${
                      osdPosition === opt.value ? "ptz-invert-btn--active" : ""
                    }`}
                    onClick={() => setOsdPosition(opt.value)}
                  >
                    <span>{opt.label}</span>
                  </button>
                ))}
              </div>
            </div>

            <Button
              type="submit"
              variant="primary"
              size="sm"
              loading={savingOSD}
            >
              <Save size={14} />
              <span>应用画面文字设置</span>
            </Button>
          </form>

          <div className="ptz-time-section">
            <div className="ptz-section-head">
              <div className="ptz-panel__title-left">
                <Clock size={14} className="ptz-panel__icon" />
                <strong>机身时钟与校时</strong>
              </div>
              {timeStatus?.camera_time ? (
                <Badge
                  tone={
                    driftAbs <= 5
                      ? "success"
                      : driftAbs <= 60
                        ? "warning"
                        : "danger"
                  }
                  size="sm"
                >
                  {formatTimeDriftLabel(timeStatus.drift_seconds)}
                </Badge>
              ) : (
                <Badge tone="neutral" size="sm">
                  {loadingOSDTime ? "读取中…" : "未读取"}
                </Badge>
              )}
            </div>

            <div className="ptz-time-meta">
              <div className="ptz-time-meta__row">
                <span>机身时间</span>
                <code className="mono-text">{liveCameraTimeStr}</code>
              </div>
              <div className="ptz-time-meta__row">
                <span>电脑时间</span>
                <code className="mono-text">{liveHostTimeStr}</code>
              </div>
            </div>

            <div className="ptz-time-actions-row">
              <Button
                type="button"
                variant="secondary"
                size="sm"
                loading={syncingTime && !customTimeMode}
                onClick={() => void handleSyncCameraTime(false)}
              >
                <Clock size={14} />
                <span>立即同步本机时间</span>
              </Button>

              <button
                type="button"
                className={`ptz-invert-btn ${
                  customTimeMode ? "ptz-invert-btn--active" : ""
                }`}
                onClick={() => {
                  setCustomTimeMode((v) => !v);
                  if (!customDateTime) {
                    setCustomDateTime(liveHostTimeStr);
                  }
                }}
                title="展开或收起手动指定日期时间输入框"
              >
                <span>{customTimeMode ? "收起手动" : "手动指定"}</span>
              </button>
            </div>

            {customTimeMode && (
              <form
                className="ptz-preset-form"
                onSubmit={(e) => {
                  e.preventDefault();
                  void handleSyncCameraTime(true);
                }}
              >
                <div className="ptz-preset-form__input">
                  <Input
                    label="指定日期时间"
                    value={customDateTime}
                    onChange={(e) => setCustomDateTime(e.target.value)}
                    placeholder="YYYY-MM-DD HH:MM:SS"
                  />
                </div>
                <Button
                  type="submit"
                  variant="primary"
                  size="sm"
                  loading={syncingTime}
                >
                  <span>写入</span>
                </Button>
              </form>
            )}
          </div>

          {osdStatusMsg && (
            <p
              className={`ptz-status-line ${
                osdStatusMsg.ok ? "" : "ptz-status-line--error"
              }`}
              role="status"
            >
              {osdStatusMsg.text}
            </p>
          )}
        </div>
      )}
    </aside>
  );
}
