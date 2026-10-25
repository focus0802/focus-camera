import { useEffect, useMemo, useState } from "react";
import {
  ArrowUpDown,
  CheckCircle2,
  Clock,
  Edit3,
  Plus,
  Radar,
  RotateCcw,
  Save,
  Trash2,
  Video,
  VideoOff,
  Wifi,
  X,
} from "lucide-react";
import type { Camera, DiscoveredCamera, RecordingConfig, RecordingState } from "../types";
import { Input } from "./arc/input/input";
import { PasswordField } from "./arc/password-field/password-field";
import { NumberField } from "./arc/number-field/number-field";
import { TimePicker } from "./arc/time-picker/time-picker";
import { MorphSelect } from "./arc/morph-select/morph-select";
import { SearchField } from "./arc/search-field/search-field";
import { ScrollArea } from "./arc/scroll-area/scroll-area";
import { Button } from "./arc/button/button";
import { Badge } from "./arc/badge/badge";
import { Checkbox } from "./arc/checkbox/checkbox";
import { Switch } from "./arc/switch/switch";
import { EmptyState } from "./arc/empty-state/empty-state";
import { Alert } from "./arc/alert/alert";
import { Dialog, DialogContent } from "./arc/dialog/dialog";
import SegmentedControl from "./arc/segmented-control/segmented-control";
import {
  SortableDataTable,
  type DataColumn,
  type SortState,
} from "./arc/sortable-data-table/sortable-data-table";
import { Pagination } from "./arc/pagination/pagination";

interface DeviceManagerViewProps {
  cameras: Camera[];
  recordingStates: Record<string, RecordingState>;
  recordingConfig: RecordingConfig;
  defaultSubnet: string;
  defaultUsername?: string;
  defaultPassword?: string;
  policyModalOpen?: boolean;
  onPolicyModalOpenChange?: (open: boolean) => void;
  onOpenReorderModal?: () => void;
  onScanCompleteAndAdded: () => Promise<void>;
  onToggleCameraAutoRecord: (cameraId: string, enabled: boolean) => Promise<void>;
  onToggleAllAutoRecord: (enabled: boolean) => Promise<void>;
  onSaveCamera: (cam: Partial<Camera>, isEdit: boolean) => Promise<void>;
  onDeleteCamera: (id: string) => Promise<void>;
  onClearAllCameras: () => Promise<void>;
  onRebootCamera: (camera: Camera) => Promise<void>;
  onSaveSettings: (cfg: RecordingConfig) => Promise<void>;
}

interface EditableDiscovered extends DiscoveredCamera {
  selected: boolean;
  editName: string;
  editGroup: string;
  autoRecord: boolean;
}

type CameraRowItem = Camera & {
  order_index: number;
  rec_status_sort: number;
  auto_record_num: number;
} & Record<string, unknown>;

const GROUPS = ["琴房", "鼓房与录音室", "多媒体教室", "公共与其他"];

const zhNumericCollator = new Intl.Collator("zh-CN", {
  numeric: true,
  sensitivity: "base",
});

function formatBytes(bytes?: number): string {
  if (!bytes || bytes <= 0) return "0 MB";
  const mb = bytes / (1024 * 1024);
  if (mb < 1024) return `${mb.toFixed(1)} MB`;
  return `${(mb / 1024).toFixed(2)} GB`;
}

function formatDuration(sec?: number): string {
  if (!sec || sec <= 0) return "00:00";
  const m = Math.floor(sec / 60);
  const s = Math.floor(sec % 60);
  return `${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

export function DeviceManagerView({
  cameras,
  recordingStates,
  recordingConfig,
  defaultSubnet,
  defaultUsername = "admin",
  defaultPassword = "",
  policyModalOpen = false,
  onPolicyModalOpenChange,
  onOpenReorderModal,
  onScanCompleteAndAdded,
  onToggleCameraAutoRecord,
  onToggleAllAutoRecord,
  onSaveCamera,
  onDeleteCamera,
  onClearAllCameras,
  onRebootCamera,
  onSaveSettings,
}: DeviceManagerViewProps) {
  // 1. Compact LAN Scan Bar State
  const [subnet, setSubnet] = useState<string>(defaultSubnet || "192.168.1");
  const [scanUser, setScanUser] = useState<string>(defaultUsername || "admin");
  const [scanPassword, setScanPassword] = useState<string>(defaultPassword || "");

  useEffect(() => {
    if (defaultUsername) setScanUser(defaultUsername);
  }, [defaultUsername]);

  useEffect(() => {
    if (defaultPassword) setScanPassword(defaultPassword);
  }, [defaultPassword]);
  const [scanning, setScanning] = useState<boolean>(false);
  const [scanItems, setScanItems] = useState<EditableDiscovered[]>([]);
  const [hasScanned, setHasScanned] = useState<boolean>(false);
  const [batchAdding, setBatchAdding] = useState<boolean>(false);
  const [banner, setBanner] = useState<{
    tone: "info" | "success" | "warning" | "danger";
    title: string;
    detail?: string;
  } | null>(null);

  // 2. Managed Camera List Filtering, Sorting & Pagination State
  const [groupFilter, setGroupFilter] = useState<string>("all");
  const [statusFilter, setStatusFilter] = useState<string>("all");
  const [searchQuery, setSearchQuery] = useState<string>("");
  const [sortState, setSortState] = useState<SortState>({
    key: "order_index",
    direction: "asc",
  });
  const [page, setPage] = useState<number>(1);
  const [pageSize, setPageSize] = useState<number>(30);

  // 3. Modals State (Add/Edit Camera Modal + Policy Modal + Delete/Clear Confirm Modals)
  const [editingCam, setEditingCam] = useState<Partial<Camera> | null>(null);
  const [isEditMode, setIsEditMode] = useState<boolean>(false);
  const [rebootingId, setRebootingId] = useState<string | null>(null);
  const [pendingDeleteCam, setPendingDeleteCam] = useState<Camera | null>(null);
  const [deletingCam, setDeletingCam] = useState<boolean>(false);
  const [confirmClearAllOpen, setConfirmClearAllOpen] = useState<boolean>(false);
  const [clearingAll, setClearingAll] = useState<boolean>(false);
  const [localPolicyOpen, setLocalPolicyOpen] = useState<boolean>(false);
  const isPolicyOpen = policyModalOpen || localPolicyOpen;
  const setPolicyOpen = (open: boolean) => {
    setLocalPolicyOpen(open);
    onPolicyModalOpenChange?.(open);
  };

  // 4. Storage & Recording Policy Form State
  const [cfgForm, setCfgForm] = useState<RecordingConfig>(recordingConfig);
  const [savingCfg, setSavingCfg] = useState<boolean>(false);

  useEffect(() => {
    setCfgForm(recordingConfig);
  }, [recordingConfig]);

  useEffect(() => {
    setPage(1);
  }, [groupFilter, statusFilter, searchQuery, pageSize]);

  async function handleRunScan(e?: React.FormEvent) {
    e?.preventDefault();
    setScanning(true);
    setBanner(null);
    try {
      const res = await fetch("/api/discover/scan", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          subnet: subnet.trim(),
          subnet_prefix: subnet.trim(),
          username: scanUser.trim() || "admin",
          password: scanPassword,
        }),
      });
      const data = await res.json();
      if (!res.ok) {
        throw new Error(data.error || "局域网扫描失败");
      }
      const list: DiscoveredCamera[] = data.discovered || data.items || [];
      const scannedSubnet = data.subnet || data.subnet_prefix || subnet.trim();
      const mapped: EditableDiscovered[] = list.map((item) => ({
        ...item,
        selected: item.status === "new" || item.status === "ip_shifted",
        editName: item.suggested_name || `摄像头 (${item.ip})`,
        editGroup: item.suggested_group || "公共与其他",
        autoRecord: false,
      }));
      setScanItems(mapped);
      setHasScanned(true);

      const newCount = mapped.filter((i) => i.status === "new").length;
      const shiftCount = mapped.filter((i) => i.status === "ip_shifted").length;
      setBanner({
        tone: mapped.length > 0 ? "success" : "warning",
        title: `扫描完成：在 ${scannedSubnet}.1~254 网段共发现 ${mapped.length} 台 TP-LINK 摄像头`,
        detail:
          mapped.length > 0
            ? `其中新发现 ${newCount} 台，IP 发生漂移 ${shiftCount} 台（已为您默认勾选，核对房号后点击「添加所选摄像头」即可入库）。`
            : "请确认本机网卡是否处于该局域网段，且可 ping 通摄像头 IP。",
      });
    } catch (err) {
      setBanner({
        tone: "danger",
        title: "扫描局域网失败",
        detail: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setScanning(false);
    }
  }

  const selectedScanItems = useMemo(
    () => scanItems.filter((i) => i.selected),
    [scanItems]
  );

  async function handleBatchAddSelected() {
    if (selectedScanItems.length === 0) return;
    setBatchAdding(true);
    setBanner(null);
    try {
      const payload = {
        subnet_prefix: subnet.trim(),
        username: scanUser.trim() || "admin",
        password: scanPassword,
        cameras: selectedScanItems.map((item) => ({
          id: item.existing_id || undefined,
          name: item.editName.trim() || item.suggested_name || item.ip,
          ip: item.ip,
          group: item.editGroup || "公共与其他",
          username: scanUser.trim() || "admin",
          password: scanPassword,
          rtsp_port: 554,
          talk_port: 8800,
          sub_stream: true,
          auto_record: item.autoRecord,
        })),
      };
      const res = await fetch("/api/cameras/batch", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      const data = await res.json();
      if (!res.ok) {
        throw new Error(data.error || "批量添加失败");
      }
      await onScanCompleteAndAdded();
      setScanItems((prev) =>
        prev.map((item) =>
          item.selected ? { ...item, selected: false, status: "existing" } : item
        )
      );
      setBanner({
        tone: "success",
        title: `已成功入库 / 更新 ${selectedScanItems.length} 台摄像头`,
        detail: `新增 ${data.added ?? 0} 台，同步更新 ${data.updated ?? 0} 台，当前总计管理 ${data.total ?? cameras.length} 台设备。`,
      });
    } catch (err) {
      setBanner({
        tone: "danger",
        title: "批量入库失败",
        detail: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setBatchAdding(false);
    }
  }

  const filteredCameras = useMemo<CameraRowItem[]>(() => {
    const q = searchQuery.trim().toLowerCase();
    return cameras
      .map((c, idx) => {
        const recState = recordingStates[c.id];
        const isRec = Boolean(recState?.active);
        const hasErr = Boolean(recState?.last_error);
        return {
          ...c,
          order_index: idx + 1,
          rec_status_sort: isRec ? 2 : hasErr ? 1 : 0,
          auto_record_num: c.auto_record ? 1 : 0,
        };
      })
      .filter((c) => {
        if (groupFilter !== "all" && (c.group || "未分组") !== groupFilter) return false;
        const recState = recordingStates[c.id];
        const isRec = Boolean(recState?.active);
        if (statusFilter === "recording" && !isRec) return false;
        if (statusFilter === "idle" && isRec) return false;
        if (q) {
          const hay = `${c.name} ${c.ip} ${c.group || ""}`.toLowerCase();
          if (!hay.includes(q)) return false;
        }
        return true;
      });
  }, [cameras, recordingStates, groupFilter, statusFilter, searchQuery]);

  const sortedCameras = useMemo<CameraRowItem[]>(() => {
    if (!sortState) return filteredCameras;
    return filteredCameras
      .map((row, index) => ({ row, index }))
      .sort((a, b) => {
        const left = a.row[sortState.key];
        const right = b.row[sortState.key];
        if (left == null || right == null) return a.index - b.index;
        const result =
          typeof left === "number" && typeof right === "number"
            ? left - right
            : zhNumericCollator.compare(String(left), String(right));
        return (sortState.direction === "asc" ? result : -result) || a.index - b.index;
      })
      .map((entry) => entry.row);
  }, [filteredCameras, sortState]);

  const pageCount = Math.max(1, Math.ceil(sortedCameras.length / pageSize));
  const safePage = Math.min(Math.max(1, page), pageCount);
  const pagedCameras = useMemo<CameraRowItem[]>(() => {
    const start = (safePage - 1) * pageSize;
    return sortedCameras.slice(start, start + pageSize);
  }, [sortedCameras, safePage, pageSize]);

  async function handleSaveManualForm(e: React.FormEvent) {
    e.preventDefault();
    if (!editingCam || !editingCam.name || !editingCam.ip) return;
    await onSaveCamera(editingCam, isEditMode);
    setEditingCam(null);
  }

  async function handleSavePolicyForm(e: React.FormEvent) {
    e.preventDefault();
    setSavingCfg(true);
    try {
      await onSaveSettings(cfgForm);
      setPolicyOpen(false);
      setBanner({
        tone: "success",
        title: "1080P 录像与存储策略已保存生效",
        detail: `存储路径: ${cfgForm.storage_dir} · 分片时长: ${cfgForm.segment_minutes} 分钟 · 保留 ${cfgForm.retention_days} 天`,
      });
    } finally {
      setSavingCfg(false);
    }
  }

  const columns: DataColumn<CameraRowItem>[] = [
    {
      key: "order_index",
      label: "序号 / 房间名称",
      sortable: true,
      width: "21%",
      render: (_val, row) => (
        <div className="device-room-cell">
          <span className="device-order-badge mono-text">
            {String(row.order_index).padStart(2, "0")}
          </span>
          <strong>{row.name}</strong>
        </div>
      ),
    },
    {
      key: "ip",
      label: "IP 地址 / 端口",
      sortable: true,
      width: "20%",
      render: (_val, row) => (
        <div className="device-ip-cell">
          <span className="mono-text">{row.ip}</span>
          <span className="device-port-meta mono-text">
            :{row.rtsp_port || 554} / :{row.talk_port || 8800}
          </span>
        </div>
      ),
    },
    {
      key: "group",
      label: "分组",
      sortable: true,
      width: "12%",
      render: (_val, row) => (
        <Badge tone="neutral" size="sm">
          {row.group || "未分组"}
        </Badge>
      ),
    },
    {
      key: "rec_status_sort",
      label: "当前录像状态 (1080P + 音频)",
      sortable: true,
      width: "23%",
      render: (_val, row) => {
        const recState = recordingStates[row.id];
        const isRec = Boolean(recState?.active);
        if (isRec) {
          return (
            <div className="device-rec-cell">
              <Badge
                tone={recState?.mode === "manual" ? "danger" : "success"}
                size="sm"
              >
                {recState?.mode === "manual" ? "手动录制中" : "自动录制中"}
              </Badge>
              {(recState?.duration_sec || recState?.bytes_written) ? (
                <span className="device-rec-meta mono-text">
                  {formatDuration(recState?.duration_sec)} · {formatBytes(recState?.bytes_written)}
                </span>
              ) : null}
            </div>
          );
        }
        if (recState?.last_error) {
          return (
            <span className="error-text-sm" title={recState.last_error}>
              {recState.last_error}
            </span>
          );
        }
        return <span className="text-muted-sm">待命</span>;
      },
    },
    {
      key: "auto_record_num",
      label: "后台自动录像",
      sortable: true,
      width: "10%",
      render: (_val, row) => (
        <Switch
          checked={Boolean(row.auto_record)}
          onCheckedChange={(on) => void onToggleCameraAutoRecord(row.id, on)}
          label={row.auto_record ? "已开启" : "关闭"}
        />
      ),
    },
    {
      key: "actions",
      label: "设备维护与操作",
      sortable: false,
      width: "14%",
      render: (_val, row) => (
        <div className="table-actions">
          <button
            type="button"
            className="table-action-btn"
            disabled={rebootingId === row.id}
            onClick={async () => {
              setRebootingId(row.id);
              try {
                await onRebootCamera(row);
                setBanner({
                  tone: "info",
                  title: `已向 ${row.name} (${row.ip}) 发送远程重启指令`,
                  detail: "摄像头将在约 25 秒内重启完毕并释放所有卡死的 RTSP 通道。",
                });
              } finally {
                setRebootingId(null);
              }
            }}
            aria-label={`重启 ${row.name}`}
            title="当摄像头报 406 通道占满时，可一键远程重启摄像头释放通道"
          >
            <RotateCcw size={12} />
            <span>{rebootingId === row.id ? "重启中" : "重启"}</span>
          </button>
          <button
            type="button"
            className="table-action-btn"
            onClick={() => {
              setIsEditMode(true);
              setEditingCam({ ...row });
            }}
            aria-label={`编辑 ${row.name}`}
          >
            <Edit3 size={12} />
            <span>编辑</span>
          </button>
          <button
            type="button"
            className="table-action-btn table-action-btn--danger"
            onClick={() => setPendingDeleteCam(row)}
            aria-label={`删除 ${row.name}`}
            title="移除此摄像头"
          >
            <Trash2 size={12} />
          </button>
        </div>
      ),
    },
  ];

  const rangeStart = sortedCameras.length === 0 ? 0 : (safePage - 1) * pageSize + 1;
  const rangeEnd = Math.min(safePage * pageSize, sortedCameras.length);

  return (
    <div className="view-container device-view-container">
      {banner && (
        <div className="device-banner-wrap">
          <Alert
            tone={banner.tone}
            title={banner.title}
            onDismiss={() => setBanner(null)}
          >
            {banner.detail}
          </Alert>
        </div>
      )}

      {/* Section 1: Compact Single-Row LAN Discovery Bar */}
      <section className="panel-card discovery-compact-card" aria-label="局域网自动扫描与批量添加">
        <form className="discovery-compact-bar" onSubmit={handleRunScan}>
          <div className="discovery-compact-bar__title">
            <Radar size={16} className="playback-kpi-pill__icon" />
            <div>
              <strong>局域网自动发现</strong>
              <span className="discovery-compact-bar__hint">
                并发探测 :554/:8800 端口，自动提取 OSD 房号并识别 IP 漂移
              </span>
            </div>
          </div>

          <div className="discovery-compact-bar__inputs">
            <div className="compact-inline-input">
              <label htmlFor="scan-subnet-input">网段前缀</label>
              <input
                id="scan-subnet-input"
                type="text"
                value={subnet}
                onChange={(e) => setSubnet(e.target.value)}
                placeholder="192.168.1"
              />
            </div>

            <div className="compact-inline-input">
              <label htmlFor="scan-user-input">账号</label>
              <input
                id="scan-user-input"
                type="text"
                value={scanUser}
                onChange={(e) => setScanUser(e.target.value)}
                placeholder="admin"
              />
            </div>

            <div className="compact-password-field">
              <PasswordField
                label="密码"
                value={scanPassword}
                onChange={(e) => setScanPassword(e.target.value)}
                placeholder="摄像头密码"
                messages={{
                  showPassword: "显示密码",
                  hidePassword: "隐藏密码",
                }}
              />
            </div>

            <Button type="submit" variant="primary" size="sm" loading={scanning}>
              <Radar size={14} />
              <span>{scanning ? "正在并发探测…" : "扫描局域网设备"}</span>
            </Button>
          </div>
        </form>

        {hasScanned && scanItems.length === 0 && (
          <div className="scan-empty-inline">
            <Wifi size={16} />
            <span>未在该网段发现开启 554/8800 端口的摄像头，请检查网段或手动添加。</span>
          </div>
        )}

        {scanItems.length > 0 && (
          <div className="scan-results-box">
            <div className="scan-results-toolbar">
              <div className="scan-select-btns">
                <Badge tone="info" size="sm">
                  共发现 {scanItems.length} 台
                </Badge>
                <Badge tone="success" size="sm">
                  新发现 {scanItems.filter((i) => i.status === "new").length} 台
                </Badge>
                <Badge tone="warning" size="sm">
                  IP变更 {scanItems.filter((i) => i.status === "ip_shifted").length} 台
                </Badge>
                <button
                  type="button"
                  className="chip-btn"
                  onClick={() =>
                    setScanItems((prev) =>
                      prev.map((item) => ({
                        ...item,
                        selected: item.status === "new" || item.status === "ip_shifted",
                      }))
                    )
                  }
                >
                  仅选新发现与IP变更
                </button>
                <button
                  type="button"
                  className="chip-btn"
                  onClick={() =>
                    setScanItems((prev) => prev.map((item) => ({ ...item, selected: true })))
                  }
                >
                  全选 ({scanItems.length})
                </button>
                <button
                  type="button"
                  className="chip-btn"
                  onClick={() =>
                    setScanItems((prev) => prev.map((item) => ({ ...item, selected: false })))
                  }
                >
                  取消全选
                </button>
              </div>

              <div className="toolbar-btn-row">
                <Button
                  variant="primary"
                  size="sm"
                  loading={batchAdding}
                  disabled={selectedScanItems.length === 0}
                  onClick={handleBatchAddSelected}
                >
                  <CheckCircle2 size={14} />
                  <span>入库 / 更新所选 ({selectedScanItems.length} 台)</span>
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setScanItems([]);
                    setHasScanned(false);
                  }}
                >
                  <X size={14} />
                  <span>收起结果</span>
                </Button>
              </div>
            </div>

            <ScrollArea
              maxHeight="15.5rem"
              fade={0}
              className="table-scroll scan-table-scroll"
              label="局域网扫描发现结果"
            >
              <table className="arc-custom-table">
                <thead>
                  <tr>
                    <th style={{ width: 48 }}>选择</th>
                    <th style={{ width: 96 }}>状态</th>
                    <th style={{ width: 125 }}>IP 地址</th>
                    <th>房间名称 (自动读取机内 OSD，可直接修改)</th>
                    <th style={{ width: 145 }}>所属分组</th>
                    <th style={{ width: 170 }}>硬件型号 / 固件</th>
                    <th style={{ width: 130 }}>服务端口</th>
                    <th style={{ width: 100 }}>自动录1080P</th>
                  </tr>
                </thead>
                <tbody>
                  {scanItems.map((item, idx) => (
                    <tr
                      key={item.ip}
                      className={item.selected ? "row-selected" : ""}
                    >
                      <td>
                        <Checkbox
                          checked={item.selected}
                          onCheckedChange={(checked) => {
                            const on = checked === true;
                            setScanItems((prev) =>
                              prev.map((row, i) =>
                                i === idx ? { ...row, selected: on } : row
                              )
                            );
                          }}
                          aria-label={`选择 ${item.editName}`}
                        />
                      </td>
                      <td>
                        {item.status === "new" ? (
                          <Badge tone="success" size="sm">
                            新发现
                          </Badge>
                        ) : item.status === "ip_shifted" ? (
                          <Badge tone="warning" size="sm">
                            IP已变更
                          </Badge>
                        ) : (
                          <Badge tone="neutral" size="sm">
                            已添加
                          </Badge>
                        )}
                      </td>
                      <td className="mono-text">{item.ip}</td>
                      <td>
                        <input
                          type="text"
                          className="inline-table-input"
                          value={item.editName}
                          onChange={(e) => {
                            const val = e.target.value;
                            setScanItems((prev) =>
                              prev.map((row, i) =>
                                i === idx ? { ...row, editName: val } : row
                              )
                            );
                          }}
                        />
                      </td>
                      <td>
                        <select
                          className="inline-table-select"
                          value={item.editGroup}
                          onChange={(e) => {
                            const val = e.target.value;
                            setScanItems((prev) =>
                              prev.map((row, i) =>
                                i === idx ? { ...row, editGroup: val } : row
                              )
                            );
                          }}
                        >
                          {GROUPS.map((g) => (
                            <option key={g} value={g}>
                              {g}
                            </option>
                          ))}
                        </select>
                      </td>
                      <td className="text-muted-sm">
                        {item.device_model || "TP-LINK IPC"}{" "}
                        {item.hardware_ver ? `v${item.hardware_ver}` : ""}
                      </td>
                      <td>
                        <div className="port-pills">
                          <span
                            className={`port-tag ${
                              item.rtsp_open ? "port-tag--ok" : "port-tag--off"
                            }`}
                          >
                            :554 视频
                          </span>
                          <span
                            className={`port-tag ${
                              item.talk_open ? "port-tag--ok" : "port-tag--off"
                            }`}
                          >
                            :8800 对讲
                          </span>
                        </div>
                      </td>
                      <td>
                        <Switch
                          checked={item.autoRecord}
                          onCheckedChange={(on) =>
                            setScanItems((prev) =>
                              prev.map((row, i) =>
                                i === idx ? { ...row, autoRecord: on } : row
                              )
                            )
                          }
                          aria-label={`${item.editName} 自动录像`}
                        />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollArea>
          </div>
        )}
      </section>

      {/* Section 2: Full-Viewport Sortable & Paginated Managed Cameras Workbench */}
      <section className="panel-card device-table-card" aria-label="已管理摄像头列表">
        <div className="device-table-card__top">
          <div className="device-table-card__title">
            <h2>已管理摄像头列表 ({cameras.length} 台)</h2>
            <span>
              支持表头排序、分组筛选、单独开关 1080P 自动录像与远程重启释放 RTSP 通道
            </span>
          </div>

          <div className="toolbar-btn-row">
            <Button
              variant="secondary"
              size="sm"
              disabled={cameras.length === 0}
              onClick={() => void onToggleAllAutoRecord(true)}
            >
              <Video size={14} />
              <span>开启全部自动录像</span>
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={cameras.length === 0}
              onClick={() => void onToggleAllAutoRecord(false)}
            >
              <VideoOff size={14} />
              <span>关闭全部自动录像</span>
            </Button>
            {onOpenReorderModal && cameras.length > 1 && (
              <Button variant="secondary" size="sm" onClick={onOpenReorderModal}>
                <ArrowUpDown size={14} />
                <span>调整排序</span>
              </Button>
            )}
            <Button variant="secondary" size="sm" onClick={() => setPolicyOpen(true)}>
              <Clock size={14} />
              <span>录像与存储策略</span>
            </Button>
            <Button
              variant="primary"
              size="sm"
              onClick={() => {
                setIsEditMode(false);
                setEditingCam({
                  name: "",
                  ip: `${defaultSubnet || "192.168.1"}.`,
                  group: "琴房",
                  username: scanUser || defaultUsername || "admin",
                  password: scanPassword || defaultPassword || "",
                  rtsp_port: 554,
                  talk_port: 8800,
                  sub_stream: true,
                  auto_record: false,
                });
              }}
            >
              <Plus size={14} />
              <span>手动添加单台</span>
            </Button>
            {cameras.length > 0 && (
              <Button
                variant="danger"
                size="sm"
                onClick={() => setConfirmClearAllOpen(true)}
              >
                <Trash2 size={14} />
                <span>清空列表</span>
              </Button>
            )}
          </div>
        </div>

        {cameras.length === 0 ? (
          <EmptyState
            icon={<Radar width={24} height={24} />}
            title="当前尚未添加任何摄像头"
            description="请使用上方的「局域网自动发现」一键扫描并批量勾选入库，或点击「手动添加单台」录入。"
          />
        ) : (
          <>
            <div className="device-filter-bar">
              <div className="device-filter-bar__left">
                <SegmentedControl
                  label="按房间分组筛选"
                  value={groupFilter}
                  onValueChange={setGroupFilter}
                  options={[
                    { value: "all", label: `全部 (${cameras.length})` },
                    ...GROUPS.map((g) => ({
                      value: g,
                      label: `${g} (${cameras.filter((c) => (c.group || "未分组") === g).length})`,
                    })),
                  ]}
                />

                <SegmentedControl
                  label="按录制状态筛选"
                  value={statusFilter}
                  onValueChange={setStatusFilter}
                  options={[
                    { value: "all", label: "全部状态" },
                    { value: "recording", label: "正在录制" },
                    { value: "idle", label: "待命/异常" },
                  ]}
                />
              </div>

              <div className="wall-search-box">
                <SearchField
                  label="搜索已添加的摄像头"
                  value={searchQuery}
                  onValueChange={setSearchQuery}
                  placeholder="按房号、IP 或分组筛选…"
                />
              </div>
            </div>

            <ScrollArea
              className="segments-table-scroll"
              fade={0}
              label="已管理摄像头设备列表"
            >
              <SortableDataTable
                rows={pagedCameras}
                columns={columns}
                rowKey="id"
                caption="已管理摄像头设备列表"
                emptyMessage="未找到符合筛选条件的摄像头"
                defaultSort={sortState}
                onSortChange={(nextSort) => {
                  setSortState(nextSort);
                  setPage(1);
                }}
              />
            </ScrollArea>

            <div className="segments-table-footer">
              <div className="segments-table-footer__meta">
                <span className="mono-text">
                  第 {rangeStart}–{rangeEnd} 台 / 共 {sortedCameras.length} 台
                </span>
                <SegmentedControl
                  label="每页显示摄像头台数"
                  value={String(pageSize)}
                  onValueChange={(v) => setPageSize(Number(v) || 30)}
                  options={[
                    { value: "15", label: "15台/页" },
                    { value: "30", label: "30台/页" },
                    { value: "50", label: "50台/页" },
                  ]}
                />
              </div>

              <Pagination
                page={safePage}
                pageCount={pageCount}
                onPageChange={setPage}
                label="摄像头列表分页导航"
              />
            </div>
          </>
        )}
      </section>

      {/* Modal 1: Add / Edit Camera Dialog */}
      <Dialog
        open={Boolean(editingCam)}
        onOpenChange={(open) => {
          if (!open) setEditingCam(null);
        }}
      >
        {editingCam && (
          <DialogContent
            className="form-dialog-content"
            title={isEditMode ? `编辑摄像头: ${editingCam.name}` : "手动添加单台摄像头"}
            description="配置摄像头的房间名称、局域网 IP 地址、分组以及 RTSP / 对讲访问凭据。"
          >
            <form className="modal-form-stack" onSubmit={handleSaveManualForm}>
              <div className="manual-cam-grid">
                <Input
                  label="房间名称"
                  required
                  value={editingCam.name || ""}
                  onChange={(e) => setEditingCam({ ...editingCam, name: e.target.value })}
                  placeholder="例如：2号琴房"
                />
                <Input
                  label="IP 地址"
                  required
                  value={editingCam.ip || ""}
                  onChange={(e) => setEditingCam({ ...editingCam, ip: e.target.value })}
                  placeholder="例如：192.168.1.202"
                />
                <MorphSelect
                  label="所属分组"
                  className="morph-select--field"
                  items={GROUPS.map((g) => ({ value: g, label: g }))}
                  value={editingCam.group || "琴房"}
                  onValueChange={(val) => setEditingCam({ ...editingCam, group: val })}
                  searchable={false}
                  panelWidth={220}
                />
                <Input
                  label="管理员账号"
                  value={editingCam.username || "admin"}
                  onChange={(e) => setEditingCam({ ...editingCam, username: e.target.value })}
                />
                <PasswordField
                  label="访问密码"
                  value={editingCam.password || ""}
                  onChange={(e) => setEditingCam({ ...editingCam, password: e.target.value })}
                  placeholder="留空则使用全局默认密码"
                  messages={{
                    showPassword: "显示密码",
                    hidePassword: "隐藏密码",
                  }}
                />
              </div>
              <div className="manual-cam-actions">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => setEditingCam(null)}
                >
                  取消
                </Button>
                <Button type="submit" variant="primary" size="sm">
                  <Save size={14} />
                  <span>保存摄像头配置</span>
                </Button>
              </div>
            </form>
          </DialogContent>
        )}
      </Dialog>

      {/* Modal 2: 1080P Recording & Disk Quota Policy Dialog */}
      <Dialog open={isPolicyOpen} onOpenChange={setPolicyOpen}>
        <DialogContent
          className="form-dialog-content"
          title="1080P 高清录像存储与计划策略"
          description="系统统一拉取摄像头 1080P 主码流 (stream1) + 房间声音写入抗断电损坏的标准 MP4 文件。"
        >
          <form className="modal-form-stack" onSubmit={handleSavePolicyForm}>
            <div className="policy-form-grid">
              <div className="policy-full-span">
                <Input
                  label="本地录像存储文件夹路径"
                  value={cfgForm.storage_dir}
                  onChange={(e) => setCfgForm({ ...cfgForm, storage_dir: e.target.value })}
                  description="支持相对路径 (./recordings) 或绝对路径"
                />
              </div>
              <NumberField
                label="单个 MP4 分片时长"
                min={1}
                max={120}
                step={5}
                value={cfgForm.segment_minutes}
                onValueChange={(v) => setCfgForm({ ...cfgForm, segment_minutes: v })}
                suffix=" 分钟"
                scrub
                description="建议 15 分钟，到点无感切分新文件"
              />
              <NumberField
                label="自动保留天数"
                min={1}
                max={365}
                step={1}
                value={cfgForm.retention_days}
                onValueChange={(v) => setCfgForm({ ...cfgForm, retention_days: v })}
                suffix=" 天"
                scrub
                description="超过该天数的历史录像将自动清理"
              />
              <NumberField
                label="磁盘空间上限配额"
                min={10}
                max={20000}
                step={50}
                value={cfgForm.max_storage_gb}
                onValueChange={(v) => setCfgForm({ ...cfgForm, max_storage_gb: v })}
                suffix=" GB"
                scrub
                description="达到上限时自动淘汰最旧的录像分片"
              />

              <div className="schedule-row">
                <Switch
                  checked={cfgForm.schedule_enabled}
                  onCheckedChange={(on) => setCfgForm({ ...cfgForm, schedule_enabled: on })}
                  label="启用每日定时自动录像时间段（关闭则 24 小时全天自动录像）"
                />
                {cfgForm.schedule_enabled && (
                  <div className="schedule-times">
                    <TimePicker
                      label="每日开始时间"
                      format="24h"
                      minuteStep={15}
                      value={cfgForm.schedule_start_time}
                      onChange={(val) =>
                        setCfgForm({ ...cfgForm, schedule_start_time: val })
                      }
                    />
                    <TimePicker
                      label="每日结束时间"
                      format="24h"
                      minuteStep={15}
                      value={cfgForm.schedule_end_time}
                      onChange={(val) =>
                        setCfgForm({ ...cfgForm, schedule_end_time: val })
                      }
                    />
                  </div>
                )}
              </div>
            </div>

            <div className="manual-cam-actions">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => setPolicyOpen(false)}
              >
                取消
              </Button>
              <Button type="submit" variant="primary" size="sm" loading={savingCfg}>
                <Save size={14} />
                <span>保存录像与存储策略</span>
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>

      {/* Modal 3: Remove Single Camera Confirmation Dialog */}
      <Dialog
        open={Boolean(pendingDeleteCam)}
        onOpenChange={(open) => {
          if (!open && !deletingCam) setPendingDeleteCam(null);
        }}
      >
        {pendingDeleteCam && (
          <DialogContent
            title={`移除摄像头：${pendingDeleteCam.name}`}
            description={`确定从监控列表中移除「${pendingDeleteCam.name} (${pendingDeleteCam.ip})」吗？移除后将停止该房间的实时预览与后台自动录像，但已保存在本地磁盘的 1080P 历史录像文件不会被删除。`}
          >
            <div className="manual-cam-actions">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={deletingCam}
                onClick={() => setPendingDeleteCam(null)}
              >
                取消
              </Button>
              <Button
                type="button"
                variant="danger"
                size="sm"
                loading={deletingCam}
                onClick={async () => {
                  setDeletingCam(true);
                  try {
                    await onDeleteCamera(pendingDeleteCam.id);
                    setPendingDeleteCam(null);
                  } finally {
                    setDeletingCam(false);
                  }
                }}
              >
                <Trash2 size={14} />
                <span>确认移除此摄像头</span>
              </Button>
            </div>
          </DialogContent>
        )}
      </Dialog>

      {/* Modal 4: Clear All Cameras Confirmation Dialog */}
      <Dialog
        open={confirmClearAllOpen}
        onOpenChange={(open) => {
          if (!open && !clearingAll) setConfirmClearAllOpen(false);
        }}
      >
        {confirmClearAllOpen && (
          <DialogContent
            title="清空全部已管理摄像头"
            description={`确定要清空当前全部 ${cameras.length} 台摄像头配置吗？后台自动录像将立即停止，但已保存在本地磁盘的 1080P 历史录像文件不会被删除。`}
          >
            <div className="manual-cam-actions">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={clearingAll}
                onClick={() => setConfirmClearAllOpen(false)}
              >
                取消
              </Button>
              <Button
                type="button"
                variant="danger"
                size="sm"
                loading={clearingAll}
                onClick={async () => {
                  setClearingAll(true);
                  try {
                    await onClearAllCameras();
                    setConfirmClearAllOpen(false);
                  } finally {
                    setClearingAll(false);
                  }
                }}
              >
                <Trash2 size={14} />
                <span>确认清空全部 ({cameras.length} 台)</span>
              </Button>
            </div>
          </DialogContent>
        )}
      </Dialog>
    </div>
  );
}
