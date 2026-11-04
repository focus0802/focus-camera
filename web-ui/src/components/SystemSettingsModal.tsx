import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Clock,
  Download,
  ExternalLink,
  FolderOpen,
  Monitor,
  Power,
  RefreshCw,
  Save,
  ShieldCheck,
  SlidersHorizontal,
  Sparkles,
} from "lucide-react";
import type {
  AutoStartMode,
  AutoStartStatus,
  DisplayInfo,
  DisplaysPayload,
  RecordingConfig,
  SystemSettingsTab,
  UpdateStatus,
} from "../types";
import { Dialog, DialogContent } from "./arc/dialog/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./arc/tabs/tabs";
import { Switch } from "./arc/switch/switch";
import SegmentedControl from "./arc/segmented-control/segmented-control";
import { MorphSelect, type MorphSelectOption } from "./arc/morph-select/morph-select";
import { Input } from "./arc/input/input";
import { NumberField } from "./arc/number-field/number-field";
import { TimePicker } from "./arc/time-picker/time-picker";
import { Button } from "./arc/button/button";
import { Badge } from "./arc/badge/badge";
import { Alert } from "./arc/alert/alert";
import { Progress } from "./arc/progress/progress";

interface DiskSpaceInfo {
  path: string;
  total_bytes: number;
  free_bytes: number;
  total_gb: number;
  free_gb: number;
  max_quota_gb: number;
  recommended_quota_gb: number;
}

interface SystemSettingsModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  activeTab: SystemSettingsTab;
  onTabChange: (tab: SystemSettingsTab) => void;
  displaysPayload: DisplaysPayload | null;
  switchingDisplay: boolean;
  onSelectDisplay: (displayId: string) => Promise<void>;
  onRefreshDisplays: () => Promise<void>;
  recordingConfig: RecordingConfig;
  activeRecordingCount: number;
  onSaveRecordingSettings: (cfg: RecordingConfig) => Promise<void>;
  updateStatus: UpdateStatus | null;
  updateUrlInput: string;
  onUpdateUrlInputChange: (val: string) => void;
  checkingUpdate: boolean;
  applyingUpdate: boolean;
  onCheckUpdate: () => Promise<void>;
  onApplyUpdate: () => Promise<void>;
}

export function SystemSettingsModal({
  open,
  onOpenChange,
  activeTab,
  onTabChange,
  displaysPayload,
  switchingDisplay,
  onSelectDisplay,
  onRefreshDisplays,
  recordingConfig,
  activeRecordingCount,
  onSaveRecordingSettings,
  updateStatus,
  updateUrlInput,
  onUpdateUrlInputChange,
  checkingUpdate,
  applyingUpdate,
  onCheckUpdate,
  onApplyUpdate,
}: SystemSettingsModalProps) {
  // 1. Auto-Start & Auto-Time-Sync State
  const [autoStartStatus, setAutoStartStatus] = useState<AutoStartStatus | null>(
    null
  );
  const [autoTimeSync, setAutoTimeSync] = useState<boolean>(true);
  const [savingAutoStart, setSavingAutoStart] = useState<boolean>(false);
  const [autoStartMsg, setAutoStartMsg] = useState<{
    ok: boolean;
    text: string;
  } | null>(null);

  // 2. Recording & Storage Policy State
  const [cfgForm, setCfgForm] = useState<RecordingConfig>(recordingConfig);
  const [savingCfg, setSavingCfg] = useState<boolean>(false);
  const [selectingFolder, setSelectingFolder] = useState<boolean>(false);
  const [diskInfo, setDiskInfo] = useState<DiskSpaceInfo | null>(null);
  const [policySavedMsg, setPolicySavedMsg] = useState<{
    ok: boolean;
    text: string;
  } | null>(null);
  const wasOpenRef = useRef<boolean>(false);

  // Sync cfgForm only when modal transitions from closed -> open (or while closed),
  // preventing the 4-second background /api/recordings poll from resetting user edits or disk quota clamping.
  useEffect(() => {
    if (open && !wasOpenRef.current) {
      const nextForm = { ...recordingConfig };
      if (
        diskInfo &&
        diskInfo.max_quota_gb > 0 &&
        nextForm.max_storage_gb > diskInfo.max_quota_gb
      ) {
        nextForm.max_storage_gb =
          diskInfo.recommended_quota_gb || diskInfo.max_quota_gb;
      }
      setCfgForm(nextForm);
    } else if (!open) {
      setCfgForm(recordingConfig);
    }
    wasOpenRef.current = open;
  }, [open, recordingConfig, diskInfo]);

  const fetchAutoStartStatus = useCallback(async () => {
    try {
      const res = await fetch("/api/autostart");
      if (!res.ok) return;
      const data = await res.json();
      if (data.ok && data.status) {
        setAutoStartStatus(data.status as AutoStartStatus);
      }
      if (typeof data.auto_time_sync === "boolean") {
        setAutoTimeSync(data.auto_time_sync);
      }
    } catch {
      // ignore transient errors
    }
  }, []);

  useEffect(() => {
    if (!open) {
      setAutoStartMsg(null);
      setPolicySavedMsg(null);
      return;
    }
    void fetchAutoStartStatus();
    void onRefreshDisplays();
  }, [open, fetchAutoStartStatus, onRefreshDisplays]);

  const displays = useMemo(
    () => displaysPayload?.displays ?? [],
    [displaysPayload?.displays]
  );

  const formatCleanDisplayName = useCallback((d: DisplayInfo): string => {
    const cleaned = (d.name || "")
      .replace(/\s*\((?:\\\\\.\\)?DISPLAY\d+\)\s*$/i, "")
      .trim();
    if (
      !cleaned ||
      /^显示器\s*\d+$/i.test(cleaned) ||
      /^Generic PnP Monitor$/i.test(cleaned) ||
      cleaned === "通用即插即用监视器"
    ) {
      return d.primary ? "主显示器" : `外接显示器 ${d.index}`;
    }
    return cleaned;
  }, []);

  const activeDisplayId = useMemo(() => {
    if (displaysPayload?.current_display_id) {
      return displaysPayload.current_display_id;
    }
    const cur = displays.find((d) => d.current) || displays[0];
    return cur?.id ?? "";
  }, [displays, displaysPayload?.current_display_id]);

  const displayOptions = useMemo<MorphSelectOption[]>(
    () =>
      displays.map((d) => ({
        value: d.id,
        label: `屏幕 ${d.index} · ${formatCleanDisplayName(d)}`,
        meta: `${d.width} × ${d.height}${d.primary ? " · 主屏" : ""}${
          d.current ? " · 当前显示" : ""
        }`,
      })),
    [displays, formatCleanDisplayName]
  );

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    const timer = window.setTimeout(async () => {
      try {
        const qs = new URLSearchParams({ path: cfgForm.storage_dir });
        const res = await fetch(`/api/recordings/disk-info?${qs.toString()}`);
        if (!res.ok || cancelled) return;
        const data = await res.json();
        if (data.ok && data.disk && !cancelled) {
          const d: DiskSpaceInfo = data.disk;
          setDiskInfo(d);
          if (typeof d.max_quota_gb === "number" && d.max_quota_gb > 0) {
            setCfgForm((prev) => {
              if (prev.max_storage_gb > d.max_quota_gb) {
                return {
                  ...prev,
                  max_storage_gb: d.recommended_quota_gb || d.max_quota_gb,
                };
              }
              return prev;
            });
          }
        }
      } catch {
        // ignore transient probe errors
      }
    }, 60);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [open, activeTab, cfgForm.storage_dir]);

  async function handleUpdateAutoStart(
    nextEnabled: boolean,
    nextMode: AutoStartMode,
    nextAutoTimeSync?: boolean
  ) {
    setSavingAutoStart(true);
    setAutoStartMsg(null);
    try {
      const payload: Record<string, unknown> = {
        enabled: nextEnabled,
        mode: nextMode,
      };
      if (typeof nextAutoTimeSync === "boolean") {
        payload.auto_time_sync = nextAutoTimeSync;
      }
      const res = await fetch("/api/autostart", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      const contentType = res.headers.get("content-type") || "";
      if (!contentType.includes("application/json")) {
        setAutoStartMsg({
          ok: false,
          text: "当前运行的客户端程序版本过旧，请重启最新编译的客户端程序",
        });
        return;
      }
      const data = await res.json();
      if (data.status) {
        setAutoStartStatus(data.status as AutoStartStatus);
      }
      if (typeof data.auto_time_sync === "boolean") {
        setAutoTimeSync(data.auto_time_sync);
      }
      if (!res.ok || !data.ok) {
        setAutoStartMsg({
          ok: false,
          text: data.error || "更新系统设置失败",
        });
        return;
      }
    } catch {
      setAutoStartMsg({
        ok: false,
        text: "请求系统设置接口失败",
      });
    } finally {
      setSavingAutoStart(false);
    }
  }

  async function handleSelectRecordingFolder() {
    setSelectingFolder(true);
    try {
      const res = await fetch("/api/recordings/select-folder", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ current_dir: cfgForm.storage_dir }),
      });
      if (!res.ok) return;
      const data = await res.json();
      if (
        data.ok &&
        !data.cancelled &&
        typeof data.path === "string" &&
        data.path.trim() !== ""
      ) {
        const nextPath = data.path.trim();
        const d: DiskSpaceInfo | undefined = data.disk;
        if (d && typeof d.max_quota_gb === "number" && d.max_quota_gb > 0) {
          setDiskInfo(d);
          setCfgForm((prev) => ({
            ...prev,
            storage_dir: nextPath,
            max_storage_gb:
              prev.max_storage_gb > d.max_quota_gb
                ? d.max_quota_gb
                : d.recommended_quota_gb || prev.max_storage_gb,
          }));
        } else {
          setCfgForm((prev) => ({ ...prev, storage_dir: nextPath }));
        }
      }
    } catch {
      // ignore transient errors
    } finally {
      setSelectingFolder(false);
    }
  }

  async function handleSavePolicyForm(e: React.FormEvent) {
    e.preventDefault();
    setSavingCfg(true);
    setPolicySavedMsg(null);
    try {
      await onSaveRecordingSettings(cfgForm);
      setPolicySavedMsg({ ok: true, text: "设置已保存" });
    } catch (err) {
      setPolicySavedMsg({
        ok: false,
        text: `保存失败：${err instanceof Error ? err.message : "请检查配置文件写入权限"}`,
      });
    } finally {
      setSavingCfg(false);
    }
  }

  const currentEnabled = Boolean(autoStartStatus?.enabled);
  const currentMode: AutoStartMode =
    autoStartStatus?.mode === "tray" ? "tray" : "window";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open && (
        <DialogContent
          className="form-dialog-content system-settings-dialog"
          title="系统设置"
        >
          <div className="system-settings-body">
            <Tabs
              value={activeTab}
              onValueChange={(val) => onTabChange(val as SystemSettingsTab)}
              className="system-settings-tabs"
            >
              <TabsList aria-label="系统设置分类页签">
                <TabsTrigger value="general">
                  <SlidersHorizontal size={14} />
                  <span>常规设置</span>
                </TabsTrigger>
                <TabsTrigger value="recording">
                  <Clock size={14} />
                  <span>录像存储</span>
                </TabsTrigger>
                <TabsTrigger value="update">
                  {updateStatus?.has_update ? (
                    <Sparkles size={14} />
                  ) : (
                    <RefreshCw size={14} />
                  )}
                  <span>
                    版本更新
                    {updateStatus?.has_update && updateStatus.latest_version
                      ? ` (${updateStatus.latest_version})`
                      : ""}
                  </span>
                </TabsTrigger>
              </TabsList>

              {/* Tab 1: General & Display & Auto-Start */}
              <TabsContent value="general" className="system-settings-panel">
                <div className="modal-form-stack">
                  <div className="settings-section-card">
                    <div className="settings-section-card__head">
                      <div className="display-card-title-row">
                        <Monitor size={15} />
                        <strong>监控大屏目标显示器</strong>
                      </div>
                      <div className="display-card-head-actions">
                        <Badge
                          tone={displays.length > 1 ? "info" : "neutral"}
                          size="sm"
                        >
                          {displays.length > 1
                            ? `已连接 ${displays.length} 台显示器`
                            : "单屏模式"}
                        </Badge>
                        <Button
                          type="button"
                          variant="secondary"
                          size="sm"
                          disabled={switchingDisplay}
                          onClick={() => void onRefreshDisplays()}
                          title="重新检测当前连接的显示器列表"
                        >
                          <RefreshCw size={13} />
                          <span>刷新</span>
                        </Button>
                      </div>
                    </div>

                    {displays.length === 1 && !displaysPayload?.fallback_used && (
                      <>
                        <div className="display-single-strip">
                          <div className="display-single-strip__left">
                            <span
                              className="display-status-dot"
                              aria-hidden="true"
                            />
                            <strong className="display-single-strip__name">
                              {formatCleanDisplayName(displays[0])}
                            </strong>
                            <span className="display-single-strip__res">
                              {displays[0].width} × {displays[0].height}
                            </span>
                          </div>
                          <Badge tone="success" size="sm">
                            当前主屏
                          </Badge>
                        </div>
                        <p className="settings-section-card__desc">
                          接入第二台显示器或电视大屏后，可在此指定投放屏幕或直接跨屏拖拽窗口自动记忆。
                        </p>
                      </>
                    )}

                    {(displays.length > 1 ||
                      Boolean(displaysPayload?.fallback_used)) && (
                      <>
                        <div
                          className="display-monitor-grid"
                          role="radiogroup"
                          aria-label="选择监控大屏所在显示器"
                        >
                          {displays.map((d) => {
                            const isCurrent =
                              d.id === activeDisplayId || d.current;
                            const isRemembered =
                              Boolean(displaysPayload?.preferred_display_id) &&
                              displaysPayload?.preferred_display_id === d.id;
                            return (
                              <button
                                key={d.id}
                                type="button"
                                role="radio"
                                aria-checked={isCurrent}
                                disabled={switchingDisplay}
                                className={`display-monitor-tile ${
                                  isCurrent ? "display-monitor-tile--active" : ""
                                }`}
                                onClick={() => {
                                  void onSelectDisplay(d.id);
                                }}
                              >
                                <div className="display-monitor-tile__top">
                                  <span className="display-monitor-tile__idx">
                                    {isCurrent && (
                                      <span
                                        className="display-status-dot"
                                        aria-hidden="true"
                                      />
                                    )}
                                    <span>屏幕 {d.index}</span>
                                  </span>
                                  <div className="display-monitor-tile__badges">
                                    {d.primary && !isCurrent && (
                                      <Badge tone="neutral" size="sm">
                                        主屏
                                      </Badge>
                                    )}
                                    {isCurrent && (
                                      <Badge tone="success" size="sm">
                                        {d.primary ? "主屏 · 当前显示" : "当前显示"}
                                      </Badge>
                                    )}
                                    {isRemembered && !isCurrent && (
                                      <Badge tone="info" size="sm">
                                        已记忆
                                      </Badge>
                                    )}
                                  </div>
                                </div>
                                <strong className="display-monitor-tile__name">
                                  {formatCleanDisplayName(d)}
                                </strong>
                                <span className="display-monitor-tile__res">
                                  {d.width} × {d.height}
                                </span>
                              </button>
                            );
                          })}
                        </div>

                        {displayOptions.length > 2 && (
                          <MorphSelect
                            label="快速选择目标显示器"
                            className="morph-select--field"
                            items={displayOptions}
                            value={activeDisplayId}
                            disabled={switchingDisplay}
                            searchable="auto"
                            onValueChange={(nextId) => {
                              if (nextId && nextId !== activeDisplayId) {
                                void onSelectDisplay(nextId);
                              }
                            }}
                          />
                        )}

                        {displaysPayload?.fallback_used && (
                          <Alert
                            tone="info"
                            title="上次记忆的显示器当前未连接，已自动回退至默认主显示器"
                          >
                            重新接入原外接显示器后，大屏窗口将自动恢复到记忆的显示器。
                          </Alert>
                        )}

                        <p className="settings-section-card__desc">
                          点击选择后将自动记忆，下次打开默认展示到该显示器；直接用鼠标将窗口拖拽到其他显示器松手后也会自动铺满并记忆。
                        </p>
                      </>
                    )}
                  </div>

                  <div className="settings-section-card">
                    <div className="settings-section-card__head">
                      <div className="display-card-title-row">
                        <Power size={15} />
                        <strong>开机自动启动 FocusCamera</strong>
                      </div>
                      <div className="display-card-head-actions">
                        <Switch
                          aria-label="开机自动启动 FocusCamera"
                          checked={currentEnabled}
                          disabled={
                            savingAutoStart ||
                            autoStartStatus?.supported === false
                          }
                          onCheckedChange={(checked) => {
                            void handleUpdateAutoStart(checked, currentMode);
                          }}
                        />
                      </div>
                    </div>

                    <div
                      className="autostart-mode-block"
                      data-disabled={!currentEnabled || undefined}
                    >
                      <span className="autostart-mode-block__title">
                        启动方式
                      </span>
                      <SegmentedControl
                        label="开机启动窗口方式"
                        options={[
                          {
                            value: "window",
                            label: "打开主窗口",
                          },
                          {
                            value: "tray",
                            label: "静默到托盘",
                          },
                        ]}
                        value={currentMode}
                        onValueChange={(val) => {
                          const nextMode = val as AutoStartMode;
                          if (nextMode !== currentMode) {
                            void handleUpdateAutoStart(
                              currentEnabled,
                              nextMode
                            );
                          }
                        }}
                      />
                    </div>

                    {autoStartStatus?.executable_path && (
                      <div className="autostart-path-strip">
                        <span className="autostart-path-strip__label">
                          程序路径
                        </span>
                        <code className="autostart-path-strip__code">
                          {autoStartStatus.executable_path}
                        </code>
                      </div>
                    )}
                  </div>

                  <div className="settings-section-card">
                    <div className="settings-section-card__head">
                      <div className="display-card-title-row">
                        <Clock size={15} />
                        <strong>启动时与后台每 6 小时自动校准全部摄像头时间</strong>
                      </div>
                      <div className="display-card-head-actions">
                        <Switch
                          aria-label="启动时与后台每 6 小时自动校准全部摄像头时间"
                          checked={autoTimeSync}
                          disabled={savingAutoStart}
                          onCheckedChange={(checked) => {
                            setAutoTimeSync(checked);
                            void handleUpdateAutoStart(
                              currentEnabled,
                              currentMode,
                              checked
                            );
                          }}
                        />
                      </div>
                    </div>
                    <p className="settings-section-card__desc">
                      针对封闭局域网无互联网 NTP 或断电后硬件时钟归零的情况，自动把本机电脑当前时间写入全部在线摄像头的机身时钟，确保画面时间水印与录像时间轴一致。
                    </p>
                  </div>

                  {autoStartStatus?.warning && (
                    <Alert tone="warning" title="程序运行路径提示">
                      {autoStartStatus.warning}
                    </Alert>
                  )}

                  {autoStartMsg && (
                    <Alert
                      tone={autoStartMsg.ok ? "success" : "danger"}
                      title={autoStartMsg.text}
                      onDismiss={() => setAutoStartMsg(null)}
                    />
                  )}
                </div>
              </TabsContent>

              {/* Tab 2: 1080P Recording & Disk Quota Policy */}
              <TabsContent value="recording" className="system-settings-panel">
                <form
                  className="modal-form-stack"
                  onSubmit={handleSavePolicyForm}
                >
                  <div className="policy-form-grid">
                    <div className="policy-full-span">
                      <div className="storage-path-row">
                        <div className="storage-path-row__input">
                          <Input
                            label="录像存储路径"
                            value={cfgForm.storage_dir}
                            onChange={(e) =>
                              setCfgForm({
                                ...cfgForm,
                                storage_dir: e.target.value,
                              })
                            }
                          />
                        </div>
                        <div className="storage-path-row__actions">
                          <Button
                            type="button"
                            variant="secondary"
                            size="md"
                            loading={selectingFolder}
                            onClick={() => {
                              void handleSelectRecordingFolder();
                            }}
                            title="调用系统原生文件夹选择框选择存储路径"
                          >
                            <FolderOpen size={15} />
                            <span>选择文件夹...</span>
                          </Button>
                          <Button
                            type="button"
                            variant="secondary"
                            size="md"
                            onClick={() => {
                              void fetch("/api/recordings/open-folder", {
                                method: "POST",
                                headers: {
                                  "Content-Type": "application/json",
                                },
                                body: JSON.stringify({
                                  path: cfgForm.storage_dir,
                                }),
                              });
                            }}
                            title="在系统文件管理器中打开当前存储文件夹"
                          >
                            <ExternalLink size={15} />
                            <span>打开目录</span>
                          </Button>
                        </div>
                      </div>
                      {cfgForm.storage_dir.trim().startsWith("\\\\") && (
                        <Alert
                          tone="warning"
                          title="当前路径位于跨机/虚拟机共享目录 (UNC 网络路径)"
                          className="storage-unc-alert"
                        >
                          <div className="storage-unc-alert__body">
                            <span>
                              建议改为 Windows 本地磁盘路径。
                            </span>
                            <Button
                              type="button"
                              variant="secondary"
                              size="sm"
                              onClick={() =>
                                setCfgForm({
                                  ...cfgForm,
                                  storage_dir: "C:\\FocusCamera\\recordings",
                                })
                              }
                            >
                              设为 C:\FocusCamera\recordings
                            </Button>
                          </div>
                        </Alert>
                      )}
                    </div>
                    <NumberField
                      label="单个 MP4 分片时长"
                      min={1}
                      max={120}
                      step={5}
                      value={cfgForm.segment_minutes}
                      onValueChange={(v) =>
                        setCfgForm({ ...cfgForm, segment_minutes: v })
                      }
                      suffix=" 分钟"
                      scrub
                      limitHint={(edge, limit) =>
                        edge === "max"
                          ? `上限 ${limit} 分钟`
                          : `下限 ${limit} 分钟`
                      }
                    />
                    <NumberField
                      label="磁盘空间上限配额"
                      min={1}
                      max={
                        diskInfo && diskInfo.max_quota_gb > 0
                          ? diskInfo.max_quota_gb
                          : 20000
                      }
                      step={
                        diskInfo && diskInfo.max_quota_gb <= 100 ? 5 : 10
                      }
                      value={cfgForm.max_storage_gb}
                      onValueChange={(v) =>
                        setCfgForm({ ...cfgForm, max_storage_gb: v })
                      }
                      suffix=" GB"
                      scrub
                      limitHint={(edge, limit) =>
                        edge === "max"
                          ? `最多可用 ${limit} GB`
                          : `最少 ${limit} GB`
                      }
                      description={
                        diskInfo && diskInfo.total_gb > 0
                          ? `磁盘剩余 ${diskInfo.free_gb.toFixed(1)} GB / 共 ${diskInfo.total_gb.toFixed(0)} GB`
                          : undefined
                      }
                    />

                    <div className="schedule-row">
                      <Switch
                        checked={cfgForm.schedule_enabled}
                        onCheckedChange={(on) =>
                          setCfgForm({ ...cfgForm, schedule_enabled: on })
                        }
                        label="每日定时录像"
                      />
                      {cfgForm.schedule_enabled && (
                        <div className="schedule-times">
                          <TimePicker
                            label="开始时间"
                            format="24h"
                            minuteStep={15}
                            value={cfgForm.schedule_start_time}
                            onChange={(val) =>
                              setCfgForm({
                                ...cfgForm,
                                schedule_start_time: val,
                              })
                            }
                          />
                          <TimePicker
                            label="结束时间"
                            format="24h"
                            minuteStep={15}
                            value={cfgForm.schedule_end_time}
                            onChange={(val) =>
                              setCfgForm({
                                ...cfgForm,
                                schedule_end_time: val,
                              })
                            }
                          />
                        </div>
                      )}
                    </div>
                  </div>

                  {policySavedMsg && (
                    <Alert
                      tone={policySavedMsg.ok ? "success" : "danger"}
                      title={policySavedMsg.text}
                      onDismiss={() => setPolicySavedMsg(null)}
                    />
                  )}

                  <div className="manual-cam-actions">
                    <Button
                      type="submit"
                      variant="primary"
                      size="sm"
                      loading={savingCfg}
                    >
                      <Save size={14} />
                      <span>保存</span>
                    </Button>
                  </div>
                </form>
              </TabsContent>

              {/* Tab 3: Controlled Self-Update */}
              <TabsContent value="update" className="system-settings-panel">
                <div className="modal-form-stack">
                  <div className="update-meta-strip">
                    <Badge tone="neutral" size="sm">
                      当前版本 {updateStatus?.current_version || "v2.4.2"}
                    </Badge>
                    {updateStatus?.os_arch && (
                      <Badge tone="neutral" size="sm">
                        {updateStatus.os_arch}
                      </Badge>
                    )}
                    {updateStatus?.has_update && updateStatus.latest_version && (
                      <Badge tone="success" size="sm">
                        最新 {updateStatus.latest_version}
                      </Badge>
                    )}
                    {updateStatus?.last_checked_at && (
                      <span className="text-muted-sm">
                        上次检查: {updateStatus.last_checked_at}
                      </span>
                    )}
                  </div>

                  <form
                    className="update-source-row"
                    onSubmit={(e) => {
                      e.preventDefault();
                      void onCheckUpdate();
                    }}
                  >
                    <div className="update-source-row__input">
                      <Input
                        label="更新源地址"
                        value={updateUrlInput}
                        onChange={(e) =>
                          onUpdateUrlInputChange(e.target.value)
                        }
                        placeholder="https://api.github.com/repos/focus0802/focus-camera/releases/latest"
                      />
                    </div>
                    <Button
                      type="submit"
                      variant="secondary"
                      size="md"
                      loading={
                        checkingUpdate || updateStatus?.state === "checking"
                      }
                      disabled={
                        updateStatus?.state === "downloading" ||
                        updateStatus?.state === "applying" ||
                        updateStatus?.state === "restarting"
                      }
                    >
                      <RefreshCw size={15} />
                      <span>检查更新</span>
                    </Button>
                  </form>

                  {updateStatus?.error && (
                    <Alert tone="danger" title="检查或执行更新时遇到问题">
                      {updateStatus.error}
                    </Alert>
                  )}

                  {!updateStatus?.error &&
                    updateStatus?.state === "up_to_date" &&
                    updateStatus.message && (
                      <Alert tone="success" title={updateStatus.message} />
                    )}

                  {!updateStatus?.error &&
                    updateStatus?.state === "idle" &&
                    updateStatus.message && (
                      <Alert tone="info" title={updateStatus.message} />
                    )}

                  {updateStatus?.has_update && (
                    <div className="update-release-card">
                      <div className="update-release-card__head">
                        <strong>
                          新版本 {updateStatus.latest_version}
                          {updateStatus.published_at
                            ? ` (${updateStatus.published_at})`
                            : ""}
                        </strong>
                        {updateStatus.expected_sha256 && (
                          <Badge tone="info" size="sm">
                            <ShieldCheck size={12} />
                            <span>SHA-256 校验</span>
                          </Badge>
                        )}
                      </div>
                      {updateStatus.release_notes && (
                        <pre className="update-release-notes">
                          {updateStatus.release_notes}
                        </pre>
                      )}
                    </div>
                  )}

                  {(updateStatus?.state === "downloading" ||
                    updateStatus?.state === "verifying" ||
                    updateStatus?.state === "applying" ||
                    updateStatus?.state === "restarting") && (
                    <div className="update-progress-box">
                      <Progress
                        value={updateStatus.progress_pct}
                        max={100}
                        label={updateStatus.message || "正在安全升级…"}
                        showValue
                      />
                    </div>
                  )}

                  {updateStatus?.has_update && (
                    <div className="manual-cam-actions">
                      <Button
                        type="button"
                        variant="primary"
                        size="sm"
                        loading={
                          applyingUpdate ||
                          updateStatus?.state === "downloading" ||
                          updateStatus?.state === "verifying" ||
                          updateStatus?.state === "applying" ||
                          updateStatus?.state === "restarting"
                        }
                        onClick={() => void onApplyUpdate()}
                      >
                        <Download size={14} />
                        <span>立即升级 ({updateStatus.latest_version})</span>
                      </Button>
                    </div>
                  )}
                </div>
              </TabsContent>
            </Tabs>
          </div>
        </DialogContent>
      )}
    </Dialog>
  );
}
