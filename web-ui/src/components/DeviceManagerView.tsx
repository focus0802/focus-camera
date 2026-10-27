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
import type {
  Camera,
  CameraNetworkConfig,
  DiscoveredCamera,
  RecordingConfig,
  RecordingState,
} from "../types";
import { VendorIcon, VENDOR_PRESETS, getVendorMeta } from "./VendorIcon";
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
  editIP: string;
  editName: string;
  editGroup: string;
  editPassword: string;
  noPassword: boolean;
  authOk: boolean;
  probing?: boolean;
  changingIP?: boolean;
  ipChangedFrom?: string;
  probeError?: string;
  autoRecord: boolean;
}

type CameraRowItem = Camera & {
  order_index: number;
  rec_status_sort: number;
  auto_record_num: number;
} & Record<string, unknown>;

const DEFAULT_GROUP = "默认分组";

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

function inferGatewayFromIP(ip?: string): string {
  const parts = (ip || "").trim().split(".");
  if (parts.length >= 3 && parts[0] && parts[1] && parts[2]) {
    return `${parts[0]}.${parts[1]}.${parts[2]}.1`;
  }
  return "192.168.1.1";
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
  const [originalIP, setOriginalIP] = useState<string>("");
  const [syncHardwareIP, setSyncHardwareIP] = useState<boolean>(true);
  const [hwNetmask, setHwNetmask] = useState<string>("255.255.255.0");
  const [hwGateway, setHwGateway] = useState<string>("192.168.1.1");
  const [hwNetInfo, setHwNetInfo] = useState<CameraNetworkConfig | null>(null);
  const [loadingHwNet, setLoadingHwNet] = useState<boolean>(false);
  const [savingManualCam, setSavingManualCam] = useState<boolean>(false);
  const [customGroupMode, setCustomGroupMode] = useState<boolean>(false);
  const [probingModalCam, setProbingModalCam] = useState<boolean>(false);
  const [modalProbeMsg, setModalProbeMsg] = useState<{
    ok: boolean;
    text: string;
  } | null>(null);
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

  const availableGroups = useMemo(() => {
    const set = new Set<string>([DEFAULT_GROUP]);
    for (const c of cameras) {
      if (c.group && c.group.trim()) {
        set.add(c.group.trim());
      }
    }
    return Array.from(set);
  }, [cameras]);

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
        editIP: item.ip,
        editName: item.suggested_name || `摄像头 (${item.ip})`,
        editGroup: item.suggested_group || DEFAULT_GROUP,
        editPassword: item.password ?? (item.no_password ? "" : scanPassword),
        noPassword: Boolean(item.no_password),
        authOk: Boolean(item.auth_ok),
        autoRecord: false,
      }));
      setScanItems(mapped);
      setHasScanned(true);

      const newCount = mapped.filter((i) => i.status === "new").length;
      const shiftCount = mapped.filter((i) => i.status === "ip_shifted").length;
      const unauthCount = mapped.filter((i) => !i.authOk).length;
      setBanner({
        tone: mapped.length > 0 ? (unauthCount > 0 ? "info" : "success") : "warning",
        title: `扫描完成：在 ${scannedSubnet}.1~254 网段共发现 ${mapped.length} 台 RTSP / ONVIF 摄像头`,
        detail:
          mapped.length > 0
            ? unauthCount > 0
              ? `新发现 ${newCount} 台，IP 漂移 ${shiftCount} 台。其中 ${unauthCount} 台设有独立密码暂未通过验证，您可在下方表格对应行单独输入密码并点击「验证读取」，或填好密码后直接点击「入库 / 更新所选」。`
              : `其中新发现 ${newCount} 台，IP 发生漂移 ${shiftCount} 台（已自动识别设备品牌、型号与码流路径，核对名称后点击「入库 / 更新所选」即可）。`
            : "请确认本机网卡是否处于该局域网段，且摄像头已开启 RTSP (554) 或 ONVIF 服务。",
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

  async function handleProbeSingleRow(idx: number) {
    const target = scanItems[idx];
    if (!target) return;
    const pwdToTry =
      target.editPassword !== "" ? target.editPassword : scanPassword;

    setScanItems((prev) =>
      prev.map((row, i) =>
        i === idx ? { ...row, probing: true, probeError: undefined } : row
      )
    );
    try {
      const res = await fetch("/api/discover/probe", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          ip: target.ip,
          username: scanUser.trim() || "admin",
          password: pwdToTry,
        }),
      });
      const data = await res.json();
      if (!res.ok || !data.reachable) {
        setScanItems((prev) =>
          prev.map((row, i) =>
            i === idx
              ? { ...row, probing: false, probeError: data.error || "连接失败" }
              : row
          )
        );
        return;
      }
      if (!data.auth_ok) {
        setScanItems((prev) =>
          prev.map((row, i) =>
            i === idx
              ? { ...row, probing: false, authOk: false, probeError: "密码错误" }
              : row
          )
        );
        return;
      }
      setScanItems((prev) =>
        prev.map((row, i) =>
          i === idx
            ? {
                ...row,
                probing: false,
                authOk: true,
                probeError: undefined,
                editPassword: data.password ?? pwdToTry,
                noPassword: Boolean(data.no_password),
                editName: data.suggested_name || row.editName,
                editGroup: data.suggested_group || row.editGroup,
                device_model: data.device_model || row.device_model,
                vendor: data.camera?.vendor || row.vendor,
                main_stream_path: data.camera?.main_stream_path || row.main_stream_path,
                sub_stream_path: data.camera?.sub_stream_path || row.sub_stream_path,
                supports_talk: data.camera?.supports_talk ?? row.supports_talk,
              }
            : row
        )
      );
    } catch {
      setScanItems((prev) =>
        prev.map((row, i) =>
          i === idx ? { ...row, probing: false, probeError: "请求失败" } : row
        )
      );
    }
  }

  async function handleChangeScanRowIP(idx: number) {
    const target = scanItems[idx];
    if (!target) return;
    const nextIP = target.editIP.trim();
    if (!nextIP || nextIP === target.ip) return;

    const hasCustomPwd = target.editPassword !== "";
    const isExplicitEmpty = target.noPassword && !hasCustomPwd;
    const effPassword = hasCustomPwd
      ? target.editPassword
      : isExplicitEmpty
        ? ""
        : scanPassword;

    setScanItems((prev) =>
      prev.map((row, i) =>
        i === idx ? { ...row, changingIP: true, probeError: undefined } : row
      )
    );
    try {
      const res = await fetch("/api/cameras/network", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          id: target.existing_id || undefined,
          old_ip: target.ip,
          new_ip: nextIP,
          proto: "static",
          netmask: "255.255.255.0",
          gateway: inferGatewayFromIP(nextIP),
          username: scanUser.trim() || "admin",
          password: effPassword,
          no_password: isExplicitEmpty,
          update_store: Boolean(target.existing_id),
        }),
      });
      const data = await res.json();
      if (!res.ok || !data.ok) {
        setScanItems((prev) =>
          prev.map((row, i) =>
            i === idx
              ? {
                  ...row,
                  changingIP: false,
                  probeError: data.error || "修改硬件 IP 失败",
                }
              : row
          )
        );
        return;
      }

      const oldIP = target.ip;
      const appliedIP = data.new_ip || nextIP;
      setScanItems((prev) =>
        prev.map((row, i) => {
          if (i !== idx) return row;
          const updatedName =
            row.editName === `摄像头 (${oldIP})`
              ? `摄像头 (${appliedIP})`
              : row.editName;
          return {
            ...row,
            changingIP: false,
            probeError: undefined,
            ipChangedFrom: row.ipChangedFrom || oldIP,
            ip: appliedIP,
            editIP: appliedIP,
            editName: updatedName,
            existing_id: data.id || row.existing_id,
          };
        })
      );
      if (target.existing_id) {
        await onScanCompleteAndAdded();
      }
      setBanner({
        tone: "success",
        title: `摄像头机身静态 IP 修改成功：${oldIP} → ${appliedIP}`,
        detail: "设备已切换至新静态 IP 并自动设置为固定 IP 模式，防止路由器重启后再次漂移。",
      });
    } catch (err) {
      setScanItems((prev) =>
        prev.map((row, i) =>
          i === idx
            ? {
                ...row,
                changingIP: false,
                probeError: err instanceof Error ? err.message : "改 IP 失败",
              }
            : row
        )
      );
    }
  }

  async function handleBatchAddSelected() {
    if (selectedScanItems.length === 0) return;
    setBatchAdding(true);
    setBanner(null);
    try {
      // 1. If user modified editIP on any selected row without clicking "改机身IP" yet,
      // push the new static IP to the camera hardware first.
      const resolvedIPByOldIP = new Map<string, string>();
      for (const item of selectedScanItems) {
        const nextIP = item.editIP.trim();
        if (nextIP && nextIP !== item.ip) {
          const hasCustomPwd = item.editPassword !== "";
          const isExplicitEmpty = item.noPassword && !hasCustomPwd;
          const effPassword = hasCustomPwd
            ? item.editPassword
            : isExplicitEmpty
              ? ""
              : scanPassword;

          const netRes = await fetch("/api/cameras/network", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              id: item.existing_id || undefined,
              old_ip: item.ip,
              new_ip: nextIP,
              proto: "static",
              netmask: "255.255.255.0",
              gateway: inferGatewayFromIP(nextIP),
              username: scanUser.trim() || "admin",
              password: effPassword,
              no_password: isExplicitEmpty,
              update_store: Boolean(item.existing_id),
            }),
          });
          const netData = await netRes.json();
          if (!netRes.ok || !netData.ok) {
            throw new Error(
              `修改摄像头 ${item.ip} → ${nextIP} 硬件 IP 失败：${netData.error || "连接异常"}`
            );
          }
          resolvedIPByOldIP.set(item.ip, netData.new_ip || nextIP);
        }
      }

      const payload = {
        subnet_prefix: subnet.trim(),
        username: scanUser.trim() || "admin",
        password: scanPassword,
        cameras: selectedScanItems.map((item) => {
          const finalIP = resolvedIPByOldIP.get(item.ip) || item.ip;
          const hasCustomPwd = item.editPassword !== "";
          const isExplicitEmpty = item.noPassword && !hasCustomPwd;
          const effPassword = hasCustomPwd
            ? item.editPassword
            : isExplicitEmpty
              ? ""
              : scanPassword;
          const rawName = item.editName.trim() || item.suggested_name || finalIP;
          const finalName =
            rawName === `摄像头 (${item.ip})` && finalIP !== item.ip
              ? `摄像头 (${finalIP})`
              : rawName;
          return {
            id: item.existing_id || undefined,
            name: finalName,
            ip: finalIP,
            group: item.editGroup.trim() || DEFAULT_GROUP,
            username: scanUser.trim() || "admin",
            password: effPassword,
            no_password: isExplicitEmpty,
            vendor: item.vendor || undefined,
            main_stream_path: item.main_stream_path || undefined,
            sub_stream_path: item.sub_stream_path || undefined,
            model: item.device_model || undefined,
            supports_talk: item.supports_talk ?? item.talk_open,
            rtsp_port: 554,
            talk_port: item.talk_open ? 8800 : 0,
            sub_stream: true,
            auto_record: item.autoRecord,
          };
        }),
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
        prev.map((item) => {
          if (!item.selected) return item;
          const updatedIP = resolvedIPByOldIP.get(item.ip) || item.ip;
          return {
            ...item,
            ipChangedFrom:
              updatedIP !== item.ip ? item.ip : item.ipChangedFrom,
            ip: updatedIP,
            editIP: updatedIP,
            selected: false,
            status: "existing",
          };
        })
      );
      setBanner({
        tone: "success",
        title: `已成功入库 / 更新 ${selectedScanItems.length} 台摄像头`,
        detail: `新增 ${data.added ?? 0} 台，同步更新 ${data.updated ?? 0} 台${
          resolvedIPByOldIP.size > 0
            ? `（其中 ${resolvedIPByOldIP.size} 台已同步修改机身硬件静态 IP）`
            : ""
        }，当前总计管理 ${data.total ?? cameras.length} 台设备。`,
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
        if (groupFilter !== "all" && (c.group || DEFAULT_GROUP) !== groupFilter) return false;
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

  async function fetchCameraHardwareNetwork(
    camId?: string,
    ip?: string,
    username?: string,
    password?: string
  ) {
    const targetIP = (ip || "").trim();
    if (!targetIP) return;
    setLoadingHwNet(true);
    try {
      const qs = new URLSearchParams({
        ip: targetIP,
        ...(camId ? { id: camId } : {}),
        ...(username ? { username } : {}),
        ...(password ? { password } : {}),
      });
      const res = await fetch(`/api/cameras/network?${qs.toString()}`);
      const data = await res.json();
      if (res.ok && data.ok && data.network) {
        const net: CameraNetworkConfig = data.network;
        setHwNetInfo(net);
        if (net.netmask) setHwNetmask(net.netmask);
        if (net.gateway) setHwGateway(net.gateway);
      } else {
        setHwNetInfo(null);
      }
    } catch {
      setHwNetInfo(null);
    } finally {
      setLoadingHwNet(false);
    }
  }

  async function handleProbeModalCamera() {
    if (!editingCam || !editingCam.ip?.trim()) return;
    setProbingModalCam(true);
    setModalProbeMsg(null);
    try {
      const targetProbeIP =
        isEditMode && syncHardwareIP && originalIP
          ? originalIP
          : editingCam.ip.trim();
      const res = await fetch("/api/discover/probe", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          ip: targetProbeIP,
          username: editingCam.username?.trim() || "admin",
          password: editingCam.password || "",
        }),
      });
      const data = await res.json();
      if (!res.ok || !data.reachable) {
        setModalProbeMsg({
          ok: false,
          text: data.error || "无法连接该摄像头 IP，请检查网络",
        });
        return;
      }
      if (!data.auth_ok) {
        setModalProbeMsg({
          ok: false,
          text: "已连通设备端口，但密码鉴权未通过，无法读取设备信息",
        });
        return;
      }
      setEditingCam((prev) =>
        prev
          ? {
              ...prev,
              name: data.suggested_name || prev.name,
              group: data.suggested_group || prev.group || DEFAULT_GROUP,
              password: data.password ?? prev.password,
              no_password: Boolean(data.no_password),
              model: data.device_model || prev.model,
              vendor: data.camera?.vendor || prev.vendor || "tplink",
              main_stream_path:
                data.camera?.main_stream_path || prev.main_stream_path,
              sub_stream_path:
                data.camera?.sub_stream_path || prev.sub_stream_path,
              supports_talk:
                data.camera?.supports_talk ?? prev.supports_talk,
            }
          : prev
      );
      void fetchCameraHardwareNetwork(
        editingCam.id,
        targetProbeIP,
        editingCam.username?.trim() || "admin",
        data.password ?? editingCam.password
      );
      setModalProbeMsg({
        ok: true,
        text: `验证成功，已读取设备名称：「${data.suggested_name}」(${data.device_model || "RTSP IPC"})`,
      });
    } catch {
      setModalProbeMsg({
        ok: false,
        text: "探测请求失败",
      });
    } finally {
      setProbingModalCam(false);
    }
  }

  async function handleSaveManualForm(e: React.FormEvent) {
    e.preventDefault();
    if (!editingCam || !editingCam.ip?.trim()) return;
    const nextIP = editingCam.ip.trim();
    setSavingManualCam(true);
    setModalProbeMsg(null);
    try {
      let updatedCam: Partial<Camera> = {
        ...editingCam,
        ip: nextIP,
      };
      let hardwareIPUpdated = false;

      if (isEditMode && syncHardwareIP && originalIP) {
        const ipChanged = nextIP !== originalIP;
        const maskChanged =
          hwNetInfo && hwNetmask.trim() && hwNetmask.trim() !== hwNetInfo.netmask;
        const gwChanged =
          hwNetInfo && hwGateway.trim() && hwGateway.trim() !== hwNetInfo.gateway;
        const wasDhcp = hwNetInfo?.proto === "dhcp";

        if (ipChanged || maskChanged || gwChanged || wasDhcp) {
          const netRes = await fetch("/api/cameras/network", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              id: editingCam.id,
              old_ip: originalIP,
              new_ip: nextIP,
              proto: "static",
              netmask: hwNetmask.trim() || "255.255.255.0",
              gateway: hwGateway.trim() || inferGatewayFromIP(nextIP),
              username: editingCam.username?.trim() || "admin",
              password: editingCam.password || "",
              no_password: Boolean(editingCam.no_password),
              update_store: true,
            }),
          });
          const netData = await netRes.json();
          if (!netRes.ok || !netData.ok) {
            setModalProbeMsg({
              ok: false,
              text:
                netData.error ||
                "修改摄像头硬件静态 IP 失败（若仅需修改软件本地记录，可关闭「同步写入摄像头机身硬件静态 IP」开关）",
            });
            return;
          }
          hardwareIPUpdated = ipChanged || Boolean(wasDhcp);
          updatedCam = {
            ...updatedCam,
            id: netData.id || updatedCam.id,
            ip: netData.new_ip || nextIP,
          };
        }
      }

      await onSaveCamera(updatedCam, isEditMode);
      setEditingCam(null);
      setModalProbeMsg(null);
      if (hardwareIPUpdated) {
        setBanner({
          tone: "success",
          title: `摄像头硬件静态 IP 已修改生效：${originalIP} → ${updatedCam.ip}`,
          detail: `已将「${updatedCam.name || updatedCam.ip}」机身设为固定静态 IP (${updatedCam.ip})，历史 1080P 录像目录与实时流已自动平滑迁移。`,
        });
      }
    } finally {
      setSavingManualCam(false);
    }
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

  const allAutoRecordOn = useMemo(
    () => cameras.length > 0 && cameras.every((c) => c.auto_record),
    [cameras]
  );
  const anyAutoRecordOn = useMemo(
    () => cameras.some((c) => c.auto_record),
    [cameras]
  );

  const columns: DataColumn<CameraRowItem>[] = [
    {
      key: "order_index",
      label: "序号 / 设备名称",
      sortable: true,
      numeric: false,
      width: "18%",
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
      numeric: false,
      width: "16%",
      render: (_val, row) => (
        <div className="device-ip-cell">
          <span className="mono-text">{row.ip}</span>
          <span className="device-port-meta mono-text">
            :{row.rtsp_port || 554}
            {row.supports_talk !== false && getVendorMeta(row.vendor, row.model).supportsTalk
              ? `/:${row.talk_port || 8800}`
              : ""}
          </span>
        </div>
      ),
    },
    {
      key: "vendor",
      label: "品牌协议 / 码流路径",
      sortable: true,
      numeric: false,
      width: "18%",
      render: (_val, row) => {
        const meta = getVendorMeta(row.vendor, row.model);
        const mainPath = row.main_stream_path || meta.mainPath;
        return (
          <div className="device-ip-cell" title={`${meta.label} · 主码流 ${mainPath}`}>
            <span className="vendor-chip">
              <VendorIcon vendor={meta.value} size={15} />
              <span>{meta.shortLabel}</span>
            </span>
            <span className="device-port-meta mono-text">{mainPath}</span>
          </div>
        );
      },
    },
    {
      key: "group",
      label: "所属分组",
      sortable: true,
      numeric: false,
      width: "11%",
      render: (_val, row) => (
        <Badge tone="neutral" size="sm">
          {row.group || DEFAULT_GROUP}
        </Badge>
      ),
    },
    {
      key: "rec_status_sort",
      label: "当前录像状态 (1080P)",
      sortable: true,
      numeric: false,
      width: "15%",
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
          const errMsg = recState.last_error;
          const shortLabel = errMsg.includes("401")
            ? "密码鉴权失败"
            : errMsg.includes("406")
              ? "通道已占满"
              : "离线 / 重连中";
          return (
            <div className="device-rec-cell" title={errMsg}>
              <Badge tone="warning" size="sm">
                {shortLabel}
              </Badge>
            </div>
          );
        }
        if (row.auto_record) {
          if (recordingConfig.schedule_enabled) {
            return (
              <Badge tone="info" size="sm">
                定时待命 ({recordingConfig.schedule_start_time}-{recordingConfig.schedule_end_time})
              </Badge>
            );
          }
          return (
            <Badge tone="info" size="sm">
              等待视频流…
            </Badge>
          );
        }
        return <span className="text-muted-sm">未开启自动录像</span>;
      },
    },
    {
      key: "auto_record_num",
      label: "后台自动录像",
      sortable: true,
      numeric: false,
      width: "9%",
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
      numeric: false,
      width: "13%",
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
              const meta = getVendorMeta(row.vendor, row.model);
              setIsEditMode(true);
              setOriginalIP(row.ip);
              setSyncHardwareIP(true);
              setHwNetmask("255.255.255.0");
              setHwGateway(inferGatewayFromIP(row.ip));
              setHwNetInfo(null);
              setCustomGroupMode(false);
              setModalProbeMsg(null);
              setEditingCam({
                ...row,
                vendor: meta.value,
                main_stream_path: row.main_stream_path || meta.mainPath,
                sub_stream_path: row.sub_stream_path || meta.subPath,
              });
              void fetchCameraHardwareNetwork(
                row.id,
                row.ip,
                row.username,
                row.password
              );
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
                并发探测 RTSP (554/8554) 与 ONVIF 端口，自动识别海康、大华、TP-LINK 等多品牌设备与流路径
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
                label="默认密码"
                value={scanPassword}
                onChange={(e) => setScanPassword(e.target.value)}
                placeholder="可留空，支持按台填"
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
            <span>未在该网段发现开启 RTSP (554/8554) 或 ONVIF 的摄像头，请检查网段或手动添加。</span>
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
                {scanItems.some((i) => i.status === "ip_shifted") && (
                  <Badge tone="warning" size="sm">
                    IP变更 {scanItems.filter((i) => i.status === "ip_shifted").length} 台
                  </Badge>
                )}
                {scanItems.some((i) => !i.authOk) && (
                  <Badge tone="danger" size="sm">
                    待验证密码 {scanItems.filter((i) => !i.authOk).length} 台
                  </Badge>
                )}
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
                    <th style={{ width: 44 }}>选择</th>
                    <th style={{ width: 145 }}>状态 / 鉴权</th>
                    <th style={{ width: 195 }}>IP 地址 (可直接修改机身静态 IP)</th>
                    <th style={{ width: 235 }}>访问密码 (支持按台单独填密码验证)</th>
                    <th>设备名称 (自动读取机内名称，可直接修改)</th>
                    <th style={{ width: 145 }}>所属分组 (可自定义)</th>
                    <th style={{ width: 140 }}>硬件型号 / 协议</th>
                    <th style={{ width: 92 }}>自动录1080P</th>
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
                        <div className="scan-status-badges">
                          {item.ipChangedFrom ? (
                            <Badge tone="success" size="sm">
                              已改静态IP
                            </Badge>
                          ) : item.status === "new" ? (
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
                          {item.authOk ? (
                            <Badge tone="info" size="sm">
                              {item.noPassword ? "免密已通" : "密码已通"}
                            </Badge>
                          ) : (
                            <Badge tone="danger" size="sm">
                              需填密码
                            </Badge>
                          )}
                        </div>
                      </td>
                      <td>
                        <div className="scan-ip-cell">
                          <input
                            type="text"
                            className="inline-table-input mono-text scan-ip-input"
                            value={item.editIP}
                            aria-label={`${item.ip} 目标静态 IP 地址`}
                            title="直接修改此 IP 并点击「改机身IP」（或勾选后点击顶部「入库 / 更新所选」），即可把摄像头硬件改为新的固定静态 IP"
                            onChange={(e) => {
                              const val = e.target.value;
                              setScanItems((prev) =>
                                prev.map((row, i) =>
                                  i === idx
                                    ? { ...row, editIP: val, probeError: undefined }
                                    : row
                                )
                              );
                            }}
                            onKeyDown={(e) => {
                              if (
                                e.key === "Enter" &&
                                item.editIP.trim() &&
                                item.editIP.trim() !== item.ip
                              ) {
                                e.preventDefault();
                                void handleChangeScanRowIP(idx);
                              }
                            }}
                          />
                          {item.editIP.trim() !== "" &&
                            item.editIP.trim() !== item.ip && (
                              <Button
                                type="button"
                                variant="primary"
                                size="sm"
                                loading={Boolean(item.changingIP)}
                                onClick={() => void handleChangeScanRowIP(idx)}
                                title={`将摄像头机身硬件 IP 从 ${item.ip} 修改为固定静态 IP ${item.editIP.trim()}`}
                              >
                                <span>改机身IP</span>
                              </Button>
                            )}
                        </div>
                      </td>
                      <td>
                        <div className="scan-pass-cell">
                          <div className="table-password-field">
                            <PasswordField
                              label={`摄像头 ${item.ip} 密码`}
                              value={item.editPassword}
                              onChange={(e) => {
                                const val = e.target.value;
                                setScanItems((prev) =>
                                  prev.map((row, i) =>
                                    i === idx
                                      ? {
                                          ...row,
                                          editPassword: val,
                                          noPassword: val === "" && row.noPassword,
                                          probeError: undefined,
                                        }
                                      : row
                                  )
                                );
                              }}
                              onKeyDown={(e) => {
                                if (e.key === "Enter") {
                                  e.preventDefault();
                                  void handleProbeSingleRow(idx);
                                }
                              }}
                              placeholder={
                                item.authOk && item.noPassword
                                  ? "无密码 (免密已通)"
                                  : item.authOk
                                    ? "已匹配密码"
                                    : "输入此设备密码…"
                              }
                              messages={{
                                showPassword: "显示密码",
                                hidePassword: "隐藏密码",
                              }}
                            />
                          </div>
                          <Button
                            type="button"
                            variant={item.authOk ? "ghost" : "secondary"}
                            size="sm"
                            loading={Boolean(item.probing)}
                            onClick={() => void handleProbeSingleRow(idx)}
                            title="使用当前行填写的密码（或上方默认密码）验证此摄像头并读取设备名称"
                          >
                            <span>{item.authOk ? "重读" : "验证读取"}</span>
                          </Button>
                          {item.probeError && (
                            <span className="scan-probe-err">{item.probeError}</span>
                          )}
                        </div>
                      </td>
                      <td>
                        <input
                          type="text"
                          className="inline-table-input"
                          value={item.editName}
                          aria-label={`${item.ip} 设备名称`}
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
                        <input
                          type="text"
                          list="camera-group-datalist"
                          className="inline-table-input"
                          value={item.editGroup}
                          placeholder={DEFAULT_GROUP}
                          aria-label={`${item.ip} 所属分组`}
                          onChange={(e) => {
                            const val = e.target.value;
                            setScanItems((prev) =>
                              prev.map((row, i) =>
                                i === idx ? { ...row, editGroup: val } : row
                              )
                            );
                          }}
                        />
                      </td>
                      <td className="text-muted-sm">
                        <div className="scan-model-ports">
                          <span className="scan-vendor-row">
                            <VendorIcon vendor={getVendorMeta(item.vendor, item.device_model).value} size={14} />
                            <span>{item.device_model || getVendorMeta(item.vendor, item.device_model).shortLabel}</span>
                          </span>
                          <span className="mono-text">
                            {item.talk_open ? ":554/:8800" : ":554"}
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

      <datalist id="camera-group-datalist">
        {availableGroups.map((g) => (
          <option key={g} value={g} />
        ))}
      </datalist>

      {/* Section 2: Full-Viewport Sortable & Paginated Managed Cameras Workbench */}
      <section className="panel-card device-table-card" aria-label="已管理摄像头列表">
        <div className="device-table-card__top">
          <div className="device-table-card__title">
            <h2>已管理摄像头列表 ({cameras.length} 台)</h2>
            <span>
              支持表头排序、分组筛选、1080P 自动录像开关与远程重启
            </span>
          </div>

          <div className="toolbar-btn-row">
            <Button
              variant="secondary"
              size="sm"
              disabled={cameras.length === 0 || allAutoRecordOn}
              onClick={() => void onToggleAllAutoRecord(true)}
              title={allAutoRecordOn ? "当前所有摄像头均已开启后台自动录像" : "一键开启所有摄像头的后台自动录像"}
            >
              <Video size={14} />
              <span>{allAutoRecordOn ? "已全部开启自动录像" : "开启全部自动录像"}</span>
            </Button>
            {anyAutoRecordOn && (
              <Button
                variant="secondary"
                size="sm"
                disabled={cameras.length === 0}
                onClick={() => void onToggleAllAutoRecord(false)}
              >
                <VideoOff size={14} />
                <span>关闭全部自动录像</span>
              </Button>
            )}
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
                setOriginalIP("");
                setSyncHardwareIP(false);
                setHwNetInfo(null);
                setCustomGroupMode(false);
                setModalProbeMsg(null);
                setEditingCam({
                  name: "",
                  ip: `${defaultSubnet || "192.168.1"}.`,
                  group: availableGroups[0] || DEFAULT_GROUP,
                  username: scanUser || defaultUsername || "admin",
                  password: scanPassword || defaultPassword || "",
                  vendor: "tplink",
                  main_stream_path: "/stream1",
                  sub_stream_path: "/stream2",
                  supports_talk: true,
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
                  label="按设备分组筛选"
                  value={groupFilter}
                  onValueChange={setGroupFilter}
                  options={[
                    { value: "all", label: `全部 (${cameras.length})` },
                    ...availableGroups.map((g) => ({
                      value: g,
                      label: `${g} (${cameras.filter((c) => (c.group || DEFAULT_GROUP) === g).length})`,
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
                  placeholder="搜索名称、IP 或分组…"
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
          if (!open) {
            setEditingCam(null);
            setModalProbeMsg(null);
          }
        }}
      >
        {editingCam && (
          <DialogContent
            className="form-dialog-content"
            title={isEditMode ? `编辑摄像头: ${editingCam.name}` : "手动添加单台摄像头"}
            description="选择品牌预设可自动填充码流路径，或点击左下方按钮自动探测设备信息。"
          >
            <form className="modal-form-stack" onSubmit={handleSaveManualForm}>
              <div className="manual-cam-grid">
                <MorphSelect
                  label="品牌与协议预设"
                  className="morph-select--field"
                  items={VENDOR_PRESETS.map((v) => ({
                    value: v.value,
                    label: v.label,
                    icon: <VendorIcon vendor={v.value} size={18} />,
                  }))}
                  value={editingCam.vendor || "tplink"}
                  onValueChange={(val) => {
                    const preset = VENDOR_PRESETS.find((v) => v.value === val);
                    setEditingCam({
                      ...editingCam,
                      vendor: val,
                      main_stream_path: preset ? preset.mainPath : editingCam.main_stream_path,
                      sub_stream_path: preset ? preset.subPath : editingCam.sub_stream_path,
                      supports_talk: preset ? preset.supportsTalk : editingCam.supports_talk,
                    });
                  }}
                  searchable={false}
                  panelWidth={268}
                />
                <Input
                  label={isEditMode ? "目标 IP 地址 (可修改机身 IP)" : "IP 地址"}
                  required
                  value={editingCam.ip || ""}
                  onChange={(e) => {
                    const nextIP = e.target.value;
                    const prevGw = inferGatewayFromIP(editingCam.ip);
                    const nextGw = inferGatewayFromIP(nextIP);
                    if (!hwGateway || hwGateway === prevGw) {
                      setHwGateway(nextGw);
                    }
                    setEditingCam({ ...editingCam, ip: nextIP });
                  }}
                  placeholder="例如：192.168.1.202"
                />
                <Input
                  label="设备名称 (留空可自动读取)"
                  value={editingCam.name || ""}
                  onChange={(e) => setEditingCam({ ...editingCam, name: e.target.value })}
                  placeholder="留空则保存时自动探测名称"
                />
                {customGroupMode ? (
                  <div className="custom-group-field">
                    <Input
                      label="新建自定义分组"
                      value={editingCam.group || ""}
                      onChange={(e) => setEditingCam({ ...editingCam, group: e.target.value })}
                      placeholder="输入新分组名称，如：一楼大厅"
                    />
                    <button
                      type="button"
                      className="custom-group-switch-btn"
                      onClick={() => {
                        setCustomGroupMode(false);
                        if (!editingCam.group?.trim()) {
                          setEditingCam({
                            ...editingCam,
                            group: availableGroups[0] || DEFAULT_GROUP,
                          });
                        }
                      }}
                    >
                      选择已有分组
                    </button>
                  </div>
                ) : (
                  <MorphSelect
                    label="所属分组"
                    className="morph-select--field"
                    items={[
                      ...Array.from(
                        new Set([
                          ...availableGroups,
                          ...(editingCam.group?.trim() ? [editingCam.group.trim()] : []),
                        ])
                      ).map((g) => ({
                        value: g,
                        label: g,
                      })),
                      {
                        value: "__custom__",
                        label: "+ 新建自定义分组…",
                      },
                    ]}
                    value={editingCam.group?.trim() || DEFAULT_GROUP}
                    onValueChange={(val) => {
                      if (val === "__custom__") {
                        setCustomGroupMode(true);
                        setEditingCam({ ...editingCam, group: "" });
                      } else {
                        setEditingCam({ ...editingCam, group: val });
                      }
                    }}
                    searchable={false}
                    panelWidth={268}
                  />
                )}
                <Input
                  label="管理员账号"
                  value={editingCam.username || "admin"}
                  onChange={(e) => setEditingCam({ ...editingCam, username: e.target.value })}
                />
                <PasswordField
                  label="访问密码 (无密码则留空)"
                  value={editingCam.password || ""}
                  onChange={(e) =>
                    setEditingCam({
                      ...editingCam,
                      password: e.target.value,
                      no_password: e.target.value === "",
                    })
                  }
                  placeholder="输入密码，无密码请留空"
                  messages={{
                    showPassword: "显示密码",
                    hidePassword: "隐藏密码",
                  }}
                />
                <Input
                  label="主码流路径 (高清录像 / 放大)"
                  value={editingCam.main_stream_path || ""}
                  onChange={(e) =>
                    setEditingCam({ ...editingCam, main_stream_path: e.target.value })
                  }
                  placeholder="例如：/stream1"
                />
                <Input
                  label="子码流路径 (多宫格墙预览)"
                  value={editingCam.sub_stream_path || ""}
                  onChange={(e) =>
                    setEditingCam({ ...editingCam, sub_stream_path: e.target.value })
                  }
                  placeholder="例如：/stream2"
                />
              </div>

              {isEditMode && (
                <div className="hw-network-box">
                  <div className="hw-network-box__head">
                    <Switch
                      checked={syncHardwareIP}
                      onCheckedChange={setSyncHardwareIP}
                      label="同步写入摄像头机身硬件静态 IP（自动预检 IP 冲突并平滑迁移历史录像）"
                    />
                    <div className="hw-network-box__badges">
                      {loadingHwNet ? (
                        <Badge tone="neutral" size="sm">
                          正在读取机身网络…
                        </Badge>
                      ) : hwNetInfo ? (
                        <>
                          <Badge
                            tone={hwNetInfo.proto === "static" ? "info" : "warning"}
                            size="sm"
                          >
                            {hwNetInfo.proto === "static"
                              ? "当前机身: 静态 IP"
                              : "当前机身: DHCP 动态分配"}
                          </Badge>
                          {hwNetInfo.mac && (
                            <Badge tone="neutral" size="sm">
                              MAC: {hwNetInfo.mac}
                            </Badge>
                          )}
                        </>
                      ) : (
                        <Badge tone="warning" size="sm">
                          原 IP ({originalIP}) 暂未连通管理口
                        </Badge>
                      )}
                    </div>
                  </div>
                  {syncHardwareIP && (
                    <div className="hw-network-box__grid">
                      <Input
                        label="子网掩码 (静态 IP)"
                        value={hwNetmask}
                        onChange={(e) => setHwNetmask(e.target.value)}
                        placeholder="255.255.255.0"
                      />
                      <Input
                        label="默认网关 (静态 IP)"
                        value={hwGateway}
                        onChange={(e) => setHwGateway(e.target.value)}
                        placeholder={inferGatewayFromIP(editingCam.ip)}
                      />
                    </div>
                  )}
                </div>
              )}

              {modalProbeMsg && (
                <Alert
                  tone={modalProbeMsg.ok ? "success" : "warning"}
                  title={modalProbeMsg.text}
                  onDismiss={() => setModalProbeMsg(null)}
                />
              )}
              <div className="manual-cam-actions manual-cam-actions--split">
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  loading={probingModalCam}
                  onClick={() => void handleProbeModalCamera()}
                >
                  <Radar size={14} />
                  <span>验证并读取设备信息</span>
                </Button>
                <div className="manual-cam-actions__right">
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    disabled={savingManualCam}
                    onClick={() => {
                      setEditingCam(null);
                      setModalProbeMsg(null);
                    }}
                  >
                    取消
                  </Button>
                  <Button
                    type="submit"
                    variant="primary"
                    size="sm"
                    loading={savingManualCam}
                  >
                    <Save size={14} />
                    <span>
                      {isEditMode &&
                      syncHardwareIP &&
                      editingCam.ip?.trim() &&
                      editingCam.ip.trim() !== originalIP
                        ? `修改机身静态 IP 并保存`
                        : `保存摄像头配置`}
                    </span>
                  </Button>
                </div>
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
