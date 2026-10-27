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
  Compass,
  Crosshair,
  FlipHorizontal2,
  FlipVertical2,
  RotateCcw,
  Trash2,
} from "lucide-react";
import type { Camera, PTZDirection, PTZPreset } from "../types";
import { Button } from "./arc/button/button";
import { Badge } from "./arc/badge/badge";
import { Input } from "./arc/input/input";

interface PTZControlPanelProps {
  camera: Camera;
  onPresetsUpdated?: (cameraId: string, presets: PTZPreset[]) => void;
}

export function PTZControlPanel({
  camera,
  onPresetsUpdated,
}: PTZControlPanelProps) {
  const [speed, setSpeed] = useState<string>("2");
  const [supportsHardwarePTZ, setSupportsHardwarePTZ] = useState<boolean>(
    camera.supports_ptz ?? true
  );
  const [detectedModel, setDetectedModel] = useState<string>(
    camera.model || ""
  );
  const [protocol, setProtocol] = useState<string>("tplink");
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
  const [statusMsg, setStatusMsg] = useState<string>(
    "物理云台就绪 · 点击微调角度，按住连续旋转 (支持方向键 / WASD)"
  );
  const [activeDir, setActiveDir] = useState<PTZDirection | null>(null);
  const [calibrating, setCalibrating] = useState<boolean>(false);
  const [savingPreset, setSavingPreset] = useState<boolean>(false);

  const holdTimerRef = useRef<number | null>(null);
  const holdKeepaliveRef = useRef<number | null>(null);
  const isHoldingMoveRef = useRef<boolean>(false);
  const activePressDirRef = useRef<PTZDirection | null>(null);
  const activeKeyDirRef = useRef<PTZDirection | null>(null);

  // Probe PTZ capabilities, hardware flip_type & presets on mount / camera change
  useEffect(() => {
    let cancelled = false;
    setPresets(camera.ptz_presets ?? []);
    setSupportsHardwarePTZ(camera.supports_ptz ?? true);
    setDetectedModel(camera.model || "");
    const initFlip = camera.flip_type || "off";
    setFlipType(initFlip);
    setInvertX(
      camera.ptz_invert_x ??
        (initFlip === "center" || initFlip === "left_right")
    );
    setInvertY(
      camera.ptz_invert_y ?? (initFlip === "center" || initFlip === "up_down")
    );

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
        if (data.protocol) {
          setProtocol(data.protocol);
        }
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
        if (data.message) {
          setStatusMsg(data.message);
        }
      } catch {
        // Ignore transient probe errors
      }
    }
    void loadCapabilities();
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
        if (data.protocol) {
          setProtocol(data.protocol);
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
      // Clicking "倒装" again when already in "center" mode also cancels it back to "off" (正装)
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
        // Keep continuous rotation alive during long holds across motor firmwares
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

  return (
    <aside className="ptz-panel" aria-label="物理云台控制面板">
      <div className="ptz-panel__header">
        <div className="ptz-panel__title-row">
          <div className="ptz-panel__title-left">
            <Compass size={15} className="ptz-panel__icon" />
            <strong>物理云台控制</strong>
          </div>
          <div className="ptz-panel__badges">
            <Badge tone={supportsHardwarePTZ ? "success" : "warning"} size="sm">
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
      </div>

      {/* 8-Way Physical Directional Pad */}
      <div className="ptz-dpad-section">
        <div className="ptz-dpad-grid" role="group" aria-label="八向物理云台方向盘">
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
          <span className="ptz-field-label">云台旋转速度</span>
          <div className="ptz-speed-actions" role="group" aria-label="云台旋转速度">
            {[
              { value: "1", label: "慢速 (8°)" },
              { value: "2", label: "标准 (15°)" },
              { value: "3", label: "快速 (24°)" },
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
            <span className="ptz-field-label">
              安装模式 (机内画面翻转 + 方向联动)
            </span>
            <div className="ptz-invert-actions">
              <button
                type="button"
                className={`ptz-invert-btn ${
                  flipType !== "center" ? "ptz-invert-btn--active" : ""
                }`}
                onClick={() => void handleFlipTypeChange("off")}
                title="切换为正装标准模式（恢复正向画面与正向云台方向）"
              >
                <span>正装 (标准画面)</span>
              </button>
              <button
                type="button"
                className={`ptz-invert-btn ${
                  flipType === "center" ? "ptz-invert-btn--active" : ""
                }`}
                onClick={() => void handleFlipTypeChange("center")}
                title="切换为倒装 180° 翻转模式（再次点击可取消倒装恢复正装）"
              >
                <span>
                  {flipType === "center" ? "倒装 (已开启 · 点击取消)" : "倒装 (180°翻转)"}
                </span>
              </button>
            </div>
          </div>

          <div className="ptz-invert-row">
            <span className="ptz-field-label">方向单独校准 (不翻转视频画面)</span>
            <div className="ptz-invert-actions">
              <button
                type="button"
                className={`ptz-invert-btn ${
                  invertX ? "ptz-invert-btn--active" : ""
                }`}
                onClick={() => void handleToggleInvertX()}
                title="仅反转左右控制方向（解决往左调画面往右，且保持当前画面不翻转）"
              >
                <FlipHorizontal2 size={13} />
                <span>左右反向：{invertX ? "已开启" : "正常"}</span>
              </button>
              <button
                type="button"
                className={`ptz-invert-btn ${
                  invertY ? "ptz-invert-btn--active" : ""
                }`}
                onClick={() => void handleToggleInvertY()}
                title="仅反转上下控制方向（保持当前画面不翻转）"
              >
                <FlipVertical2 size={13} />
                <span>上下反向：{invertY ? "已开启" : "正常"}</span>
              </button>
            </div>
          </div>
        </div>
      </div>

      {/* Presets & Calibration Section */}
      <div className="ptz-presets-section">
        <div className="ptz-section-head">
          <span className="ptz-field-label">常看视角与预置位 ({presets.length})</span>
          <button
            type="button"
            className="ptz-cali-link"
            disabled={calibrating}
            onClick={() => void handleCalibrateHardware()}
            title="触发物理云台步进电机自检归位"
          >
            <RotateCcw size={12} />
            <span>{calibrating ? "校准中…" : "云台自检校准"}</span>
          </button>
        </div>

        <form className="ptz-preset-form" onSubmit={(e) => void handleSavePreset(e)}>
          <div className="ptz-preset-form__input">
            <Input
              label="新预置位名称"
              placeholder="如：钢琴键盘特写…"
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

        {presets.length > 0 ? (
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
        ) : (
          <p className="ptz-preset-empty">
            将云台转动到常看位置（如琴键、鼓位、门口）后点击「存预置位」，即可一键调用转动。
          </p>
        )}
      </div>

      <p className="ptz-status-line" role="status">
        {statusMsg}
      </p>
    </aside>
  );
}
