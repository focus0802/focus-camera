import { useCallback, useEffect, useRef, useState } from "react";
import {
  Clock,
  Download,
  ExternalLink,
  FolderOpen,
  Power,
  RefreshCw,
  Save,
  ShieldCheck,
  Sparkles,
} from "lucide-react";
import type {
  AutoStartMode,
  AutoStartStatus,
  RecordingConfig,
  SystemSettingsTab,
  UpdateStatus,
} from "../types";
import { Dialog, DialogContent } from "./arc/dialog/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./arc/tabs/tabs";
import { Switch } from "./arc/switch/switch";
import SegmentedControl from "./arc/segmented-control/segmented-control";
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
  // 1. Auto-Start State
  const [autoStartStatus, setAutoStartStatus] = useState<AutoStartStatus | null>(
    null
  );
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
  }, [open, fetchAutoStartStatus]);

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
    nextMode: AutoStartMode
  ) {
    setSavingAutoStart(true);
    setAutoStartMsg(null);
    try {
      const res = await fetch("/api/autostart", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          enabled: nextEnabled,
          mode: nextMode,
        }),
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
      if (!res.ok || !data.ok) {
        setAutoStartMsg({
          ok: false,
          text: data.error || "更新开机自启动设置失败",
        });
        return;
      }
    } catch {
      setAutoStartMsg({
        ok: false,
        text: "请求开机自启动接口失败",
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
                  <Power size={14} />
                  <span>开机自启</span>
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

              {/* Tab 1: General & Auto-Start on Boot */}
              <TabsContent value="general" className="system-settings-panel">
                <div className="modal-form-stack">
                  <div className="settings-section-card">
                    <div className="settings-section-card__head">
                      <Switch
                        checked={currentEnabled}
                        disabled={
                          savingAutoStart ||
                          autoStartStatus?.supported === false
                        }
                        onCheckedChange={(checked) => {
                          void handleUpdateAutoStart(checked, currentMode);
                        }}
                        label="开机自动启动 FocusCamera"
                      />
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
                      当前版本 {updateStatus?.current_version || "v2.4.0"}
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
