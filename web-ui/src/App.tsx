import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ArrowUpDown,
  Camera as CameraIcon,
  Compass,
  Download,
  Film,
  Mic,
  PhoneCall,
  PhoneOff,
  Power,
  Radar,
  RefreshCw,
  ShieldCheck,
  Sparkles,
  Square,
  Video,
  VideoOff,
  VolumeX,
} from "lucide-react";
import {
  cameraSupportsTalk,
  type Camera,
  type PTZPreset,
  type RecordingConfig,
  type RecordingState,
  type RecordingsPayload,
  type UpdateStatus,
} from "./types";
import { TalkbackSession } from "./lib/audio";
import { StreamTile } from "./components/StreamTile";
import { PTZControlPanel } from "./components/PTZControlPanel";
import { PlaybackView } from "./components/PlaybackView";
import { DeviceManagerView } from "./components/DeviceManagerView";
import SegmentedControl from "./components/arc/segmented-control/segmented-control";
import { Tabs, TabsList, TabsTrigger } from "./components/arc/tabs/tabs";
import { Pagination } from "./components/arc/pagination/pagination";
import { AnimatedCounter } from "./components/arc/animated-counter/animated-counter";
import { ScrollArea } from "./components/arc/scroll-area/scroll-area";
import { Input } from "./components/arc/input/input";
import { Alert } from "./components/arc/alert/alert";
import { Progress } from "./components/arc/progress/progress";
import { Button } from "./components/arc/button/button";
import { Badge } from "./components/arc/badge/badge";
import { SearchField } from "./components/arc/search-field/search-field";
import { EmptyState } from "./components/arc/empty-state/empty-state";
import { Dialog, DialogContent } from "./components/arc/dialog/dialog";
import { ThemeSwitch, type Theme } from "./components/arc/theme-switch/theme-switch";
import {
  ReorderableList,
  type ReorderableItem,
} from "./components/arc/reorderable-list/reorderable-list";

type NavTab = "live" | "playback" | "devices";
type GridMode = "all" | "36" | "40" | "16" | "9";

const DEFAULT_REC_CONFIG: RecordingConfig = {
  storage_dir: "./recordings",
  segment_minutes: 15,
  retention_days: 7,
  max_storage_gb: 500,
  schedule_enabled: false,
  schedule_start_time: "08:00",
  schedule_end_time: "23:00",
};

export function App() {
  // Theme (Default dark for monitoring room comfort)
  const [theme, setTheme] = useState<Theme>(() => {
    const saved = localStorage.getItem("focus_theme");
    return saved === "light" ? "light" : "dark";
  });

  const isMacDesktop = useMemo(() => {
    if (typeof window === "undefined") return false;
    return (
      window.__FOCUS_PLATFORM__ === "macos" ||
      Boolean(window.webkit?.messageHandlers?.focusWindow)
    );
  }, []);

  const isWindowsDesktop = useMemo(() => {
    if (typeof window === "undefined") return false;
    const params = new URLSearchParams(window.location.search);
    return (
      window.__FOCUS_PLATFORM__ === "windows" ||
      params.get("desktop") === "windows" ||
      Boolean(window.chrome?.webview?.postMessage) ||
      (/Windows/i.test(navigator.userAgent) && !isMacDesktop)
    );
  }, [isMacDesktop]);

  const sendWindowsControl = useCallback(
    (action: "drag" | "minimize" | "tray" | "remove-tray" | "maximize" | "close") => {
      if (window.chrome?.webview?.postMessage) {
        window.chrome.webview.postMessage(
          JSON.stringify({
            id: 0,
            method: "focusWindowControl",
            params: [action],
          })
        );
        return;
      }
      if (window.focusWindowControl) {
        void window.focusWindowControl(action);
        return;
      }
      if (action === "close") {
        setShutdownModalOpen(true);
      }
    },
    []
  );

  useEffect(() => {
    const onRequestClose = () => {
      setShutdownModalOpen(true);
    };
    window.addEventListener("focus-request-close", onRequestClose);
    return () => window.removeEventListener("focus-request-close", onRequestClose);
  }, []);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("focus_theme", theme);
    window.webkit?.messageHandlers?.focusWindow?.postMessage(`theme:${theme}`);
    if (window.chrome?.webview?.postMessage) {
      window.chrome.webview.postMessage(
        JSON.stringify({
          id: 0,
          method: "focusSyncWindowTheme",
          params: [theme],
        })
      );
    } else {
      void window.focusSyncWindowTheme?.(theme);
    }
  }, [theme]);

  const handleHeaderMouseDown = useCallback(
    (e: React.MouseEvent<HTMLElement>) => {
      if (e.button !== 0) return;
      const target = e.target as HTMLElement | null;
      if (!target) return;
      if (
        target.closest(
          'button, a, input, select, textarea, [role="tab"], [role="button"], [role="switch"], [data-no-drag]'
        )
      ) {
        return;
      }
      window.webkit?.messageHandlers?.focusWindow?.postMessage("drag");
      if (e.detail === 2) {
        sendWindowsControl("maximize");
      } else {
        sendWindowsControl("drag");
      }
    },
    [sendWindowsControl]
  );

  // Primary Navigation
  const [activeTab, setActiveTab] = useState<NavTab>("live");

  // Core Data State
  const [cameras, setCameras] = useState<Camera[]>([]);
  const [defaultSubnet, setDefaultSubnet] = useState<string>("192.168.1");
  const [defaultUsername, setDefaultUsername] = useState<string>("admin");
  const [defaultPassword, setDefaultPassword] = useState<string>("");
  const [loadingCameras, setLoadingCameras] = useState<boolean>(true);
  const [onlineMap, setOnlineMap] = useState<Record<string, boolean>>({});

  // Recording & Playback State
  const [recordingsPayload, setRecordingsPayload] = useState<RecordingsPayload | null>(null);
  const [playbackCameraId, setPlaybackCameraId] = useState<string>("");
  const [playbackDate, setPlaybackDate] = useState<string>("");

  // Live Wall Filtering & Layout State
  const [gridMode, setGridMode] = useState<GridMode>("all");
  const [selectedGroup, setSelectedGroup] = useState<string>("all");
  const [searchText, setSearchText] = useState<string>("");
  const [currentPage, setCurrentPage] = useState<number>(0);

  // Audio Monitoring & Two-way Talkback State
  const [audioEnabledMap, setAudioEnabledMap] = useState<Record<string, boolean>>({});
  const [talkCamera, setTalkCamera] = useState<Camera | null>(null);
  const [talkMode, setTalkMode] = useState<"toggle" | "ptt" | null>(null);
  const [talkState, setTalkState] = useState<"connecting" | "ready" | "error" | "closed">("closed");
  const [talkMessage, setTalkMessage] = useState<string>("");
  const [micLevel, setMicLevel] = useState<number>(0);
  const talkSessionRef = useRef<TalkbackSession | null>(null);

  // 1080P Zoom Modal & PTZ State
  const [zoomCamera, setZoomCamera] = useState<Camera | null>(null);
  const [zoomAudioEnabled, setZoomAudioEnabled] = useState<boolean>(true);
  const [ptzPanelOpen, setPtzPanelOpen] = useState<boolean>(true);

  const handleOpenZoom = useCallback((camera: Camera) => {
    setZoomAudioEnabled(true);
    setZoomCamera(camera);
  }, []);

  const handlePresetsUpdated = useCallback(
    (cameraId: string, presets: PTZPreset[]) => {
      setCameras((prev) =>
        prev.map((c) => (c.id === cameraId ? { ...c, ptz_presets: presets } : c))
      );
      setZoomCamera((prev) =>
        prev && prev.id === cameraId ? { ...prev, ptz_presets: presets } : prev
      );
    },
    []
  );

  // @uiarc/reorderable-list Live Wall & Modal Reordering State
  const [reorderModalOpen, setReorderModalOpen] = useState<boolean>(false);
  const [devicePolicyModalOpen, setDevicePolicyModalOpen] = useState<boolean>(false);
  const [shutdownModalOpen, setShutdownModalOpen] = useState<boolean>(false);
  const [shuttingDown, setShuttingDown] = useState<boolean>(false);
  const [updateModalOpen, setUpdateModalOpen] = useState<boolean>(false);
  const [updateStatus, setUpdateStatus] = useState<UpdateStatus | null>(null);
  const [updateUrlInput, setUpdateUrlInput] = useState<string>("");
  const [checkingUpdate, setCheckingUpdate] = useState<boolean>(false);
  const [applyingUpdate, setApplyingUpdate] = useState<boolean>(false);
  const [movedCameraId, setMovedCameraId] = useState<string | null>(null);
  const tileElementsRef = useRef(new Map<string, HTMLElement>());
  const handleElementsRef = useRef(new Map<string, HTMLButtonElement>());
  const slotRectsRef = useRef<
    {
      left: number;
      top: number;
      right: number;
      bottom: number;
      width: number;
      height: number;
    }[]
  >([]);

  const fetchCameras = useCallback(async () => {
    try {
      const res = await fetch("/api/cameras");
      const data = await res.json();
      setCameras(data.cameras || []);
      if (data.default_subnet) {
        setDefaultSubnet(data.default_subnet);
      }
      if (data.config?.default_username) {
        setDefaultUsername(data.config.default_username);
      }
      if (typeof data.config?.default_password === "string") {
        setDefaultPassword(data.config.default_password);
      }
    } catch (err) {
      console.error("Failed to fetch cameras:", err);
    } finally {
      setLoadingCameras(false);
    }
  }, []);

  const fetchRecordings = useCallback(async () => {
    try {
      const params = new URLSearchParams();
      if (playbackCameraId) params.set("camera_id", playbackCameraId);
      if (playbackDate) params.set("date", playbackDate);
      const res = await fetch(`/api/recordings?${params.toString()}`);
      if (res.ok) {
        const data: RecordingsPayload = await res.json();
        setRecordingsPayload(data);
      }
    } catch (err) {
      console.error("Failed to fetch recordings:", err);
    }
  }, [playbackCameraId, playbackDate]);

  useEffect(() => {
    void fetchCameras();
  }, [fetchCameras]);

  const fetchUpdateStatus = useCallback(async () => {
    try {
      const res = await fetch("/api/update/status");
      if (res.ok) {
        const st: UpdateStatus = await res.json();
        setUpdateStatus(st);
        setUpdateUrlInput((prev) => (prev === "" && st.update_url ? st.update_url : prev));
      }
    } catch {
      // Ignore transient disconnection during self-update restart
    }
  }, []);

  useEffect(() => {
    void fetchUpdateStatus();
  }, [fetchUpdateStatus]);

  // Poll update progress faster while downloading / verifying / applying / restarting
  useEffect(() => {
    const st = updateStatus?.state;
    const isBusy =
      st === "checking" ||
      st === "downloading" ||
      st === "verifying" ||
      st === "applying" ||
      st === "restarting";
    if (!isBusy && !updateModalOpen) return;
    const timer = window.setInterval(async () => {
      try {
        const res = await fetch("/api/update/status");
        if (res.ok) {
          const next: UpdateStatus = await res.json();
          setUpdateStatus(next);
          if (st === "restarting" && next.state === "idle") {
            window.location.reload();
          }
        }
      } catch {
        // Server is restarting; keep polling until new process listens
      }
    }, isBusy ? 500 : 3000);
    return () => window.clearInterval(timer);
  }, [updateStatus?.state, updateModalOpen]);

  const handleCheckUpdate = useCallback(async () => {
    setCheckingUpdate(true);
    try {
      const res = await fetch("/api/update/check", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ update_url: updateUrlInput.trim() }),
      });
      const st: UpdateStatus = await res.json();
      setUpdateStatus(st);
    } finally {
      setCheckingUpdate(false);
    }
  }, [updateUrlInput]);

  const handleApplyUpdate = useCallback(async () => {
    setApplyingUpdate(true);
    try {
      const res = await fetch("/api/update/apply", { method: "POST" });
      const st: UpdateStatus = await res.json();
      setUpdateStatus(st);
    } finally {
      setApplyingUpdate(false);
    }
  }, []);

  useEffect(() => {
    void fetchRecordings();
    const timer = window.setInterval(() => {
      void fetchRecordings();
    }, 4000);
    return () => window.clearInterval(timer);
  }, [fetchRecordings]);

  const recordingStateMap = useMemo(() => {
    const map: Record<string, RecordingState> = {};
    for (const st of recordingsPayload?.states ?? []) {
      map[st.camera_id] = st;
    }
    return map;
  }, [recordingsPayload?.states]);

  const activeRecordingCount = useMemo(
    () => (recordingsPayload?.states ?? []).filter((s) => s.active).length,
    [recordingsPayload?.states]
  );

  // Talkback Session Management
  const stopTalkback = useCallback(() => {
    if (talkSessionRef.current) {
      talkSessionRef.current.stop();
      talkSessionRef.current = null;
    }
    setTalkCamera(null);
    setTalkMode(null);
    setTalkState("closed");
    setMicLevel(0);
  }, []);

  const startTalkback = useCallback(
    (camera: Camera, mode: "toggle" | "ptt") => {
      if (talkSessionRef.current) {
        talkSessionRef.current.stop();
        talkSessionRef.current = null;
      }
      setTalkCamera(camera);
      setTalkMode(mode);
      setTalkState("connecting");
      setTalkMessage(`正在呼叫 ${camera.name} (${camera.ip})…`);

      const session = new TalkbackSession(camera.id, {
        onStatus: (st, msg) => {
          setTalkState(st);
          setTalkMessage(msg);
          if (st === "closed") {
            setTalkCamera(null);
            setTalkMode(null);
            setMicLevel(0);
          }
        },
        onLevel: (lvl) => setMicLevel(lvl),
      });
      talkSessionRef.current = session;
      void session.start();
    },
    []
  );

  const handleToggleTalk = useCallback(
    (camera: Camera) => {
      if (talkCamera?.id === camera.id && talkMode === "toggle" && talkState !== "closed") {
        stopTalkback();
      } else {
        startTalkback(camera, "toggle");
      }
    },
    [talkCamera, talkMode, talkState, startTalkback, stopTalkback]
  );

  const handleStartPTT = useCallback(
    (camera: Camera) => {
      if (talkCamera?.id === camera.id && talkMode === "toggle") return;
      startTalkback(camera, "ptt");
    },
    [talkCamera, talkMode, startTalkback]
  );

  const handleStopPTT = useCallback(
    (camera: Camera) => {
      if (talkCamera?.id === camera.id && talkMode === "ptt") {
        stopTalkback();
      }
    },
    [talkCamera, talkMode, stopTalkback]
  );

  // Spacebar Push-To-Talk when 1080P Zoom Modal is open
  useEffect(() => {
    if (!zoomCamera || !cameraSupportsTalk(zoomCamera)) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (
        e.code === "Space" &&
        !e.repeat &&
        !(e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement)
      ) {
        e.preventDefault();
        handleStartPTT(zoomCamera);
      }
    };
    const onKeyUp = (e: KeyboardEvent) => {
      if (
        e.code === "Space" &&
        !(e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement)
      ) {
        e.preventDefault();
        handleStopPTT(zoomCamera);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    window.addEventListener("keyup", onKeyUp);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("keyup", onKeyUp);
    };
  }, [zoomCamera, handleStartPTT, handleStopPTT]);

  // Recording API Handlers
  const handleToggleManualRecord = useCallback(
    async (camera: Camera, currentlyManual: boolean) => {
      await fetch("/api/recordings/control", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          camera_id: camera.id,
          action: currentlyManual ? "stop_manual" : "start_manual",
        }),
      });
      await fetchRecordings();
    },
    [fetchRecordings]
  );

  const handleToggleCameraAutoRecord = useCallback(
    async (cameraId: string, enabled: boolean) => {
      await fetch("/api/recordings/control", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          camera_id: cameraId,
          action: "set_auto",
          enabled,
        }),
      });
      await Promise.all([fetchCameras(), fetchRecordings()]);
    },
    [fetchCameras, fetchRecordings]
  );

  const handleToggleAllAutoRecord = useCallback(
    async (enabled: boolean) => {
      await fetch("/api/recordings/control", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          action: enabled ? "all_auto_on" : "all_auto_off",
        }),
      });
      await Promise.all([fetchCameras(), fetchRecordings()]);
    },
    [fetchCameras, fetchRecordings]
  );

  const handleRebootCamera = useCallback(async (camera: Camera) => {
    await fetch("/api/cameras/reboot", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: camera.id, ip: camera.ip }),
    });
  }, []);

  // Filtering & Pagination for Live Wall
  const groups = useMemo(() => {
    const set = new Set<string>();
    cameras.forEach((c) => {
      if (c.group) set.add(c.group);
    });
    return ["all", ...Array.from(set)];
  }, [cameras]);

  const filteredCameras = useMemo(() => {
    const q = searchText.trim().toLowerCase();
    return cameras.filter((c) => {
      if (selectedGroup !== "all" && c.group !== selectedGroup) return false;
      if (
        q &&
        !c.name.toLowerCase().includes(q) &&
        !c.ip.toLowerCase().includes(q) &&
        !(c.group || "").toLowerCase().includes(q)
      ) {
        return false;
      }
      return true;
    });
  }, [cameras, selectedGroup, searchText]);

  const pageSize = useMemo(() => {
    if (gridMode === "all") return Math.max(filteredCameras.length, 1);
    return Number(gridMode) || 36;
  }, [gridMode, filteredCameras.length]);

  const totalPages = Math.max(1, Math.ceil(filteredCameras.length / pageSize));
  const safePage = Math.min(currentPage, totalPages - 1);

  const visibleCameras = useMemo(() => {
    if (gridMode === "all") return filteredCameras;
    const start = safePage * pageSize;
    return filteredCameras.slice(start, start + pageSize);
  }, [filteredCameras, gridMode, safePage, pageSize]);

  const onlineCount = useMemo(
    () => cameras.filter((c) => onlineMap[c.id]).length,
    [cameras, onlineMap]
  );

  const gridColumns = useMemo(() => {
    if (gridMode === "9") return 3;
    if (gridMode === "16") return 4;
    if (gridMode === "36") return 6;
    if (gridMode === "40") return 8;
    const n = visibleCameras.length;
    if (n <= 4) return 2;
    if (n <= 9) return 3;
    if (n <= 16) return 4;
    if (n <= 25) return 5;
    if (n <= 36) return 6;
    return 7;
  }, [gridMode, visibleCameras.length]);

  const gridRows = useMemo(() => {
    const n = Math.max(visibleCameras.length, 1);
    return Math.max(1, Math.ceil(n / gridColumns));
  }, [visibleCameras.length, gridColumns]);

  const handleStatusChange = useCallback((cameraId: string, online: boolean) => {
    setOnlineMap((prev) => {
      if (prev[cameraId] === online) return prev;
      return { ...prev, [cameraId]: online };
    });
  }, []);

  const camerasRef = useRef<Camera[]>(cameras);
  camerasRef.current = cameras;
  const visibleCamerasRef = useRef<Camera[]>(visibleCameras);
  visibleCamerasRef.current = visibleCameras;

  const persistCameraOrder = useCallback(async (ordered: Camera[]) => {
    try {
      await fetch("/api/cameras/reorder", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          ordered_ids: ordered.map((c) => c.id),
        }),
      });
    } catch (err) {
      console.error("Failed to persist camera order:", err);
    }
  }, []);

  // Keep focus on the grip handle after keyboard reordering (same as @uiarc/reorderable-list)
  useEffect(() => {
    if (!movedCameraId) return;
    const handle = handleElementsRef.current.get(movedCameraId);
    if (handle && document.activeElement !== handle) {
      handle.focus();
    }
    const timer = window.setTimeout(() => setMovedCameraId(null), 260);
    return () => window.clearTimeout(timer);
  }, [movedCameraId, cameras]);

  const handleKeyboardMove = useCallback(
    (cameraId: string, delta: number) => {
      const vis = visibleCamerasRef.current;
      const curVisIdx = vis.findIndex((c) => c.id === cameraId);
      const nextVisIdx = curVisIdx + delta;
      if (curVisIdx < 0 || nextVisIdx < 0 || nextVisIdx >= vis.length) return;
      const targetId = vis[nextVisIdx]?.id;
      if (!targetId) return;

      const all = camerasRef.current;
      const fromIdx = all.findIndex((c) => c.id === cameraId);
      const toIdx = all.findIndex((c) => c.id === targetId);
      if (fromIdx < 0 || toIdx < 0 || fromIdx === toIdx) return;

      const next = [...all];
      const [moved] = next.splice(fromIdx, 1);
      next.splice(toIdx, 0, moved);
      const reordered = next.map((c, idx) => ({
        ...c,
        sort_order: (idx + 1) * 10,
      }));
      camerasRef.current = reordered;
      setMovedCameraId(cameraId);
      setCameras(reordered);
      void persistCameraOrder(reordered);
    },
    [persistCameraOrder]
  );

  const handlePointerReorderStart = useCallback(() => {
    slotRectsRef.current = visibleCamerasRef.current.map((c) => {
      const el = tileElementsRef.current.get(c.id);
      if (!el) {
        return { left: 0, top: 0, right: 0, bottom: 0, width: 0, height: 0 };
      }
      const r = el.getBoundingClientRect();
      return {
        left: r.left + window.scrollX,
        top: r.top + window.scrollY,
        right: r.right + window.scrollX,
        bottom: r.bottom + window.scrollY,
        width: r.width,
        height: r.height,
      };
    });
  }, []);

  const handlePointerReorderMove = useCallback(
    (cameraId: string, clientX: number, clientY: number) => {
      const pageX = clientX + window.scrollX;
      const pageY = clientY + window.scrollY;
      const slots = slotRectsRef.current;
      if (!slots.length) return;

      let targetSlotIdx = -1;
      for (let i = 0; i < slots.length; i++) {
        const s = slots[i];
        if (s.width <= 0 || s.height <= 0) continue;
        const mx = s.width * 0.12;
        const my = s.height * 0.12;
        if (
          pageX >= s.left + mx &&
          pageX <= s.right - mx &&
          pageY >= s.top + my &&
          pageY <= s.bottom - my
        ) {
          targetSlotIdx = i;
          break;
        }
      }
      if (targetSlotIdx < 0) return;

      const vis = visibleCamerasRef.current;
      const curVisIdx = vis.findIndex((c) => c.id === cameraId);
      if (curVisIdx < 0 || curVisIdx === targetSlotIdx) return;

      const targetCamera = vis[targetSlotIdx];
      if (!targetCamera || targetCamera.id === cameraId) return;

      const all = camerasRef.current;
      const fromIdx = all.findIndex((c) => c.id === cameraId);
      const toIdx = all.findIndex((c) => c.id === targetCamera.id);
      if (fromIdx < 0 || toIdx < 0 || fromIdx === toIdx) return;

      const next = [...all];
      const [moved] = next.splice(fromIdx, 1);
      next.splice(toIdx, 0, moved);
      const reordered = next.map((c, idx) => ({
        ...c,
        sort_order: (idx + 1) * 10,
      }));
      camerasRef.current = reordered;
      setCameras(reordered);
    },
    []
  );

  const handlePointerReorderEnd = useCallback(() => {
    slotRectsRef.current = [];
    void persistCameraOrder(camerasRef.current);
  }, [persistCameraOrder]);

  const reorderListItems = useMemo<ReorderableItem[]>(
    () =>
      cameras.map((c) => ({
        id: c.id,
        title: c.name,
        detail: `${c.ip} · ${c.group || "未分组"}`,
      })),
    [cameras]
  );

  const handleReorderFromList = useCallback(
    (nextItems: ReorderableItem[]) => {
      const byId = new Map(camerasRef.current.map((c) => [c.id, c]));
      const reordered: Camera[] = [];
      for (const item of nextItems) {
        const cam = byId.get(item.id);
        if (cam) {
          reordered.push({
            ...cam,
            sort_order: (reordered.length + 1) * 10,
          });
        }
      }
      camerasRef.current = reordered;
      setCameras(reordered);
      void persistCameraOrder(reordered);
    },
    [persistCameraOrder]
  );

  return (
    <div className="app-shell">
      {/* Top Navigation Header */}
      <header className="top-header" onMouseDown={handleHeaderMouseDown}>
        <div className="brand-area">
          <svg
            width="24"
            height="24"
            viewBox="0 0 64 64"
            fill="none"
            aria-hidden="true"
            className="brand-icon"
          >
            <defs>
              <linearGradient id="fc-hdr-bg" x1="32" y1="4" x2="32" y2="60" gradientUnits="userSpaceOnUse">
                <stop offset="0" stopColor="#2E3546" />
                <stop offset="0.5" stopColor="#1B202C" />
                <stop offset="1" stopColor="#0E1118" />
              </linearGradient>
              <linearGradient id="fc-hdr-bezel" x1="16" y1="16" x2="48" y2="48" gradientUnits="userSpaceOnUse">
                <stop offset="0" stopColor="#7F91AD" />
                <stop offset="0.5" stopColor="#3E475B" />
                <stop offset="1" stopColor="#1E2330" />
              </linearGradient>
              <radialGradient id="fc-hdr-glass" cx="0" cy="0" r="1" gradientUnits="userSpaceOnUse" gradientTransform="translate(34 34) scale(16)">
                <stop offset="0" stopColor="#0A1A38" />
                <stop offset="0.55" stopColor="#12346E" />
                <stop offset="0.88" stopColor="#1F66C7" />
                <stop offset="1" stopColor="#0B2044" />
              </radialGradient>
              <radialGradient id="fc-hdr-pupil" cx="0" cy="0" r="1" gradientUnits="userSpaceOnUse" gradientTransform="translate(31 31) scale(7.5)">
                <stop offset="0" stopColor="#050812" />
                <stop offset="0.7" stopColor="#0A142E" />
                <stop offset="1" stopColor="#193875" />
              </radialGradient>
            </defs>
            <rect x="4" y="4" width="56" height="56" rx="13" fill="url(#fc-hdr-bg)" stroke="#94ADCF" strokeOpacity="0.32" strokeWidth="1.2" />
            <circle cx="32" cy="32" r="19" fill="url(#fc-hdr-bezel)" />
            <circle cx="32" cy="32" r="16.8" fill="#121722" />
            <circle cx="32" cy="32" r="14.8" fill="url(#fc-hdr-glass)" />
            <circle cx="32" cy="32" r="11.8" stroke="#6BC6FF" strokeOpacity="0.3" strokeWidth="0.9" />
            <circle cx="32" cy="32" r="7" fill="url(#fc-hdr-pupil)" stroke="#57B3FF" strokeOpacity="0.5" strokeWidth="1" />
            <ellipse cx="25.8" cy="25.6" rx="5.2" ry="2.6" transform="rotate(-42 25.8 25.6)" fill="#FFFFFF" fillOpacity="0.5" />
            <circle cx="29" cy="29" r="1.3" fill="#FFFFFF" fillOpacity="0.88" />
            <circle cx="49" cy="15" r="3.6" fill="#0F1219" />
            <circle cx="49" cy="15" r="2.5" fill="#FF3B30" />
          </svg>
          <div className="brand-text-row">
            <h1 className="brand-title">FocusCamera 高清监控与 NVR 中心</h1>
            <div className="brand-meta">
              <Badge tone={onlineCount > 0 ? "success" : "neutral"} size="sm">
                <span>在线 </span>
                <span className="inline-counter">
                  <AnimatedCounter value={onlineCount} />
                </span>
                <span> / {cameras.length} 台</span>
              </Badge>
              {activeRecordingCount > 0 && (
                <Badge tone="danger" size="sm">
                  <span>正在录制 </span>
                  <span className="inline-counter">
                    <AnimatedCounter value={activeRecordingCount} />
                  </span>
                  <span> 路 1080P+音频</span>
                </Badge>
              )}
              {talkCamera && (
                <Badge tone="warning" size="sm">
                  对讲中: {talkCamera.name}
                </Badge>
              )}
            </div>
          </div>
        </div>

        <nav className="main-nav" aria-label="主功能导航">
          <Tabs
            value={activeTab}
            onValueChange={(val) => setActiveTab(val as NavTab)}
          >
            <TabsList aria-label="主功能视图切换">
              <TabsTrigger value="live">实时监控 ({cameras.length})</TabsTrigger>
              <TabsTrigger value="playback">
                录像回放 ({recordingsPayload?.segment_count ?? 0})
              </TabsTrigger>
              <TabsTrigger value="devices">设备扫描与管理</TabsTrigger>
            </TabsList>
          </Tabs>
        </nav>

        <div className="header-actions">
          <Button
            variant={updateStatus?.has_update ? "primary" : "ghost"}
            size="sm"
            onClick={() => {
              if (updateStatus?.update_url && !updateUrlInput) {
                setUpdateUrlInput(updateStatus.update_url);
              }
              setUpdateModalOpen(true);
            }}
            title="查看当前软件版本、配置更新源并执行一键受控热更新"
          >
            {updateStatus?.has_update ? (
              <>
                <Sparkles size={14} />
                <span>发现新版本 {updateStatus.latest_version}</span>
              </>
            ) : (
              <>
                <RefreshCw size={14} />
                <span>{updateStatus?.current_version || "v2.2.0"}</span>
              </>
            )}
          </Button>
          <ThemeSwitch
            theme={theme}
            iconOnly
            onThemeChange={(next) => setTheme(next)}
          />
          {!isWindowsDesktop && !isMacDesktop && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setShutdownModalOpen(true)}
              title="安全关闭后台服务与所有录像句柄"
            >
              <Power size={15} />
              <span>退出</span>
            </Button>
          )}
          {isWindowsDesktop && (
            <div className="win-window-controls" data-no-drag aria-label="窗口控制">
              <button
                type="button"
                className="win-caption-btn"
                onClick={() => sendWindowsControl("minimize")}
                title="最小化"
                aria-label="最小化"
              >
                <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
                  <path
                    d="M1 5h8"
                    stroke="currentColor"
                    strokeWidth="1.2"
                    strokeLinecap="round"
                  />
                </svg>
              </button>
              <button
                type="button"
                className="win-caption-btn"
                onClick={() => sendWindowsControl("maximize")}
                title="最大化 / 还原"
                aria-label="最大化或还原"
              >
                <svg
                  className="win-caption-btn__icon--maximize"
                  width="10"
                  height="10"
                  viewBox="0 0 10 10"
                  aria-hidden="true"
                >
                  <rect
                    x="1.5"
                    y="1.5"
                    width="7"
                    height="7"
                    rx="1"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="1.2"
                  />
                </svg>
                <svg
                  className="win-caption-btn__icon--restore"
                  width="10"
                  height="10"
                  viewBox="0 0 10 10"
                  aria-hidden="true"
                >
                  <path
                    d="M3 3V1.8c0-.44.36-.8.8-.8h4.4c.44 0 .8.36.8.8v4.4c0 .44-.36.8-.8.8H7"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="1.15"
                  />
                  <rect
                    x="1"
                    y="3"
                    width="6"
                    height="6"
                    rx="0.9"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="1.15"
                  />
                </svg>
              </button>
              <button
                type="button"
                className="win-caption-btn win-caption-btn--close"
                onClick={() => setShutdownModalOpen(true)}
                title="关闭窗口"
                aria-label="关闭窗口"
              >
                <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
                  <path
                    d="M1.5 1.5l7 7M8.5 1.5l-7 7"
                    stroke="currentColor"
                    strokeWidth="1.2"
                    strokeLinecap="round"
                  />
                </svg>
              </button>
            </div>
          )}
        </div>
      </header>

      {/* Active Intercom Floating Status Bar */}
      {talkCamera && (
        <div className={`talk-banner talk-banner--${talkState}`} role="status">
          <div className="talk-banner__info">
            <Mic size={16} />
            <strong>{talkCamera.name}</strong>
            <span>{talkMessage}</span>
            <div className="mic-meter" aria-hidden="true">
              <div
                className="mic-meter__fill"
                style={{ width: `${Math.round(micLevel * 100)}%` }}
              />
            </div>
          </div>
          <Button variant="danger" size="sm" onClick={stopTalkback}>
            <PhoneOff size={14} />
            <span>挂断对讲</span>
          </Button>
        </div>
      )}

      {/* Main Content Area */}
      <main className="main-stage">
        {activeTab === "live" && (
          <>
            {!loadingCameras && cameras.length === 0 ? (
              <div className="empty-wall-wrapper">
                <div className="panel-card empty-wall-card">
                  <EmptyState
                    icon={<Radar width={28} height={28} strokeWidth={1.6} />}
                    title="尚未添加任何监控摄像头"
                    description="本系统不会预置写死任何摄像头列表。请点击下方按钮扫描当前局域网（如 192.168.1.x），系统将自动发现支持 RTSP / ONVIF 协议的多品牌摄像头（TP-LINK、海康威视、大华、Tapo、宇视等），供您勾选一键添加。"
                    action={
                      <Button
                        variant="primary"
                        size="lg"
                        onClick={() => setActiveTab("devices")}
                      >
                        <Radar size={18} />
                        <span>前往扫描并添加局域网摄像头</span>
                      </Button>
                    }
                  />
                </div>
              </div>
            ) : (
              <div className="live-wall-layout">
                {/* Live Wall Toolbar */}
                <section className="wall-toolbar">
                  <div className="wall-toolbar__left">
                    <SegmentedControl
                      label="画面布局切换"
                      value={gridMode}
                      onValueChange={(v) => {
                        setGridMode(v as GridMode);
                        setCurrentPage(0);
                      }}
                      options={[
                        { value: "all", label: `全览 (${filteredCameras.length})` },
                        { value: "36", label: "36宫格" },
                        { value: "40", label: "40宫格" },
                        { value: "16", label: "16宫格" },
                        { value: "9", label: "9宫格" },
                      ]}
                    />

                    <SegmentedControl
                      label="设备分组筛选"
                      value={selectedGroup}
                      onValueChange={(v) => {
                        setSelectedGroup(v);
                        setCurrentPage(0);
                      }}
                      options={groups.map((g) => ({
                        value: g,
                        label: g === "all" ? "全部分组" : g,
                      }))}
                    />
                  </div>

                  <div className="wall-toolbar__right">
                    <div className="wall-search-box">
                      <SearchField
                        label="搜索设备"
                        placeholder="搜名称或 IP…"
                        value={searchText}
                        onValueChange={(val) => {
                          setSearchText(val);
                          setCurrentPage(0);
                        }}
                      />
                    </div>

                    {gridMode !== "all" && totalPages > 1 && (
                      <Pagination
                        page={safePage + 1}
                        pageCount={totalPages}
                        onPageChange={(p) => setCurrentPage(p - 1)}
                        label="监控画面分页导航"
                      />
                    )}

                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() =>
                        void handleToggleAllAutoRecord(activeRecordingCount === 0)
                      }
                      title="一键开启或关闭所有摄像头的后台 1080P 自动录像"
                    >
                      {activeRecordingCount > 0 ? (
                        <>
                          <VideoOff size={14} />
                          <span>关闭全部录像</span>
                        </>
                      ) : (
                        <>
                          <Video size={14} />
                          <span>开启全部录像</span>
                        </>
                      )}
                    </Button>

                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => setAudioEnabledMap({})}
                      title="关闭所有设备的声音监听"
                    >
                      <VolumeX size={14} />
                      <span>全部静音</span>
                    </Button>

                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => setReorderModalOpen(true)}
                      title="打开 @uiarc/reorderable-list 列表视图调整监控顺序"
                    >
                      <ArrowUpDown size={14} />
                      <span>排序</span>
                    </Button>
                  </div>
                </section>

                {/* Camera Wall Grid */}
                <section
                  className={`camera-wall-grid ${
                    gridRows > 1 ? "camera-wall-grid--fit-viewport" : ""
                  }`}
                  style={{
                    gridTemplateColumns: `repeat(${gridColumns}, minmax(0, 1fr))`,
                    gridTemplateRows:
                      gridRows > 1
                        ? `repeat(${gridRows}, minmax(0, 1fr))`
                        : undefined,
                  }}
                  aria-label="实时监控画面矩阵"
                >
                  {visibleCameras.map((cam, idx) => (
                    <StreamTile
                      key={cam.id}
                      camera={cam}
                      stream="stream2"
                      paused={zoomCamera?.id === cam.id}
                      recordingState={recordingStateMap[cam.id]}
                      audioEnabled={
                        Boolean(audioEnabledMap[cam.id]) && !zoomCamera
                      }
                      onToggleAudio={(id) =>
                        setAudioEnabledMap((prev) => ({ ...prev, [id]: !prev[id] }))
                      }
                      talkActive={talkCamera?.id === cam.id && talkState !== "closed"}
                      talkMode={talkCamera?.id === cam.id ? talkMode : null}
                      onToggleTalk={handleToggleTalk}
                      onStartPTT={handleStartPTT}
                      onStopPTT={handleStopPTT}
                      onToggleManualRecord={handleToggleManualRecord}
                      onOpenZoom={handleOpenZoom}
                      onRebootCamera={handleRebootCamera}
                      onStatusChange={handleStatusChange}
                      draggableCard
                      index={idx}
                      count={visibleCameras.length}
                      gridColumns={gridColumns}
                      lifted={movedCameraId === cam.id}
                      tileRef={(el) => {
                        if (el) tileElementsRef.current.set(cam.id, el);
                        else tileElementsRef.current.delete(cam.id);
                      }}
                      handleRef={(el) => {
                        if (el) handleElementsRef.current.set(cam.id, el);
                        else handleElementsRef.current.delete(cam.id);
                      }}
                      onKeyboardMove={handleKeyboardMove}
                      onPointerReorderStart={handlePointerReorderStart}
                      onPointerReorderMove={handlePointerReorderMove}
                      onPointerReorderEnd={handlePointerReorderEnd}
                    />
                  ))}
                </section>
              </div>
            )}
          </>
        )}

        {activeTab === "playback" && (
          <PlaybackView
            cameras={cameras}
            payload={recordingsPayload}
            selectedCameraId={playbackCameraId}
            onSelectCameraId={setPlaybackCameraId}
            selectedDate={playbackDate}
            onSelectDate={setPlaybackDate}
            onRefresh={fetchRecordings}
            onOpenFolder={async () => {
              await fetch("/api/recordings/open-folder", { method: "POST" });
            }}
            onDeleteSegment={async (relPath) => {
              await fetch(`/api/recordings/file?path=${encodeURIComponent(relPath)}`, {
                method: "DELETE",
              });
              await fetchRecordings();
            }}
            onOpenSettingsModal={() => {
              setActiveTab("devices");
              setDevicePolicyModalOpen(true);
            }}
          />
        )}

        {activeTab === "devices" && (
          <DeviceManagerView
            cameras={cameras}
            recordingStates={recordingStateMap}
            recordingConfig={recordingsPayload?.config ?? DEFAULT_REC_CONFIG}
            defaultSubnet={defaultSubnet}
            defaultUsername={defaultUsername}
            defaultPassword={defaultPassword}
            policyModalOpen={devicePolicyModalOpen}
            onPolicyModalOpenChange={setDevicePolicyModalOpen}
            onOpenReorderModal={() => setReorderModalOpen(true)}
            onScanCompleteAndAdded={async () => {
              await Promise.all([fetchCameras(), fetchRecordings()]);
            }}
            onToggleCameraAutoRecord={handleToggleCameraAutoRecord}
            onToggleAllAutoRecord={handleToggleAllAutoRecord}
            onSaveCamera={async (cam, isEdit) => {
              await fetch("/api/cameras", {
                method: isEdit ? "PUT" : "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify(cam),
              });
              await Promise.all([fetchCameras(), fetchRecordings()]);
            }}
            onDeleteCamera={async (id) => {
              await fetch(`/api/cameras?id=${encodeURIComponent(id)}`, {
                method: "DELETE",
              });
              await Promise.all([fetchCameras(), fetchRecordings()]);
            }}
            onClearAllCameras={async () => {
              await fetch("/api/cameras/clear", { method: "POST" });
              await Promise.all([fetchCameras(), fetchRecordings()]);
            }}
            onRebootCamera={handleRebootCamera}
            onSaveSettings={async (cfg) => {
              await fetch("/api/settings", {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify(cfg),
              });
              await fetchRecordings();
            }}
          />
        )}
      </main>

      {/* 1080P HD Zoom, PTZ Control & Two-Way Intercom Modal Dialog */}
      <Dialog
        open={Boolean(zoomCamera)}
        onOpenChange={(open) => {
          if (!open) setZoomCamera(null);
        }}
      >
        {zoomCamera && (
          <DialogContent
            className={`zoom-dialog-content ${
              ptzPanelOpen ? "zoom-dialog-content--with-ptz" : ""
            }`}
            title={`${zoomCamera.name} · 1080P 高清主码流与物理云台控制`}
            description={
              cameraSupportsTalk(zoomCamera)
                ? `设备 IP: ${zoomCamera.ip} · 分组: ${
                    zoomCamera.group || "默认分组"
                  } · 方向键/WASD 控制物理云台旋转 · 按住空格键对讲`
                : `设备 IP: ${zoomCamera.ip} · 分组: ${
                    zoomCamera.group || "默认分组"
                  } · 方向键/WASD 控制物理云台旋转`
            }
          >
            <div className="zoom-modal-body">
              <div
                className={`zoom-stage-layout ${
                  ptzPanelOpen ? "zoom-stage-layout--split" : ""
                }`}
              >
                <div className="zoom-video-wrap">
                  <StreamTile
                    camera={zoomCamera}
                    stream="stream1"
                    recordingState={recordingStateMap[zoomCamera.id]}
                    audioEnabled={zoomAudioEnabled}
                    onToggleAudio={() =>
                      setZoomAudioEnabled((prev) => !prev)
                    }
                    talkActive={
                      talkCamera?.id === zoomCamera.id && talkState !== "closed"
                    }
                    talkMode={talkCamera?.id === zoomCamera.id ? talkMode : null}
                    onToggleTalk={handleToggleTalk}
                    onStartPTT={handleStartPTT}
                    onStopPTT={handleStopPTT}
                    onToggleManualRecord={handleToggleManualRecord}
                    onRebootCamera={handleRebootCamera}
                    isModalView
                  />
                </div>

                {ptzPanelOpen && (
                  <PTZControlPanel
                    camera={zoomCamera}
                    onPresetsUpdated={handlePresetsUpdated}
                  />
                )}
              </div>

              <div className="zoom-intercom-bar">
                <div className="zoom-intercom-bar__left">
                  {cameraSupportsTalk(zoomCamera) ? (
                    <>
                      <Button
                        variant={
                          talkCamera?.id === zoomCamera.id && talkMode === "toggle"
                            ? "danger"
                            : "primary"
                        }
                        size="md"
                        onClick={() => handleToggleTalk(zoomCamera)}
                      >
                        {talkCamera?.id === zoomCamera.id && talkMode === "toggle" ? (
                          <>
                            <PhoneOff size={16} />
                            <span>挂断持续双向通话</span>
                          </>
                        ) : (
                          <>
                            <PhoneCall size={16} />
                            <span>开启持续双向通话</span>
                          </>
                        )}
                      </Button>

                      <button
                        type="button"
                        className={`ptt-large-btn ${
                          talkCamera?.id === zoomCamera.id && talkMode === "ptt"
                            ? "ptt-large-btn--active"
                            : ""
                        }`}
                        onPointerDown={() => handleStartPTT(zoomCamera)}
                        onPointerUp={() => handleStopPTT(zoomCamera)}
                        onPointerLeave={() => {
                          if (talkCamera?.id === zoomCamera.id && talkMode === "ptt") {
                            handleStopPTT(zoomCamera);
                          }
                        }}
                      >
                        <Mic size={16} />
                        <span>
                          {talkCamera?.id === zoomCamera.id && talkMode === "ptt"
                            ? "正在对讲中（松开结束）…"
                            : "按住说话（或按住空格键）"}
                        </span>
                      </button>
                    </>
                  ) : (
                    <Badge tone="neutral" size="sm">
                      标准 RTSP 监控机位
                    </Badge>
                  )}
                </div>

                <div className="zoom-intercom-bar__right">
                  <Button
                    variant={ptzPanelOpen ? "primary" : "secondary"}
                    size="md"
                    onClick={() => setPtzPanelOpen((v) => !v)}
                    title="展开或收起右侧云台方向盘、预置位与电子变倍控制面板"
                  >
                    <Compass size={16} />
                    <span>{ptzPanelOpen ? "收起云台面板" : "展开云台控制"}</span>
                  </Button>

                  {(() => {
                    const recSt = recordingStateMap[zoomCamera.id];
                    const isMan = Boolean(recSt?.active && recSt?.mode === "manual");
                    return (
                      <Button
                        variant={isMan ? "danger" : "secondary"}
                        size="md"
                        onClick={() =>
                          void handleToggleManualRecord(zoomCamera, isMan)
                        }
                      >
                        {isMan ? <Square size={15} /> : <Video size={16} />}
                        <span>{isMan ? "停止手动录制" : "录制 1080P 视频+音频"}</span>
                      </Button>
                    );
                  })()}

                  <Button
                    variant="secondary"
                    size="md"
                    onClick={() => {
                      const targetId = zoomCamera.id;
                      setZoomCamera(null);
                      setPlaybackCameraId(targetId);
                      setActiveTab("playback");
                    }}
                  >
                    <Film size={16} />
                    <span>查看该设备录像回放</span>
                  </Button>
                </div>
              </div>
            </div>
          </DialogContent>
        )}
      </Dialog>

      {/* @uiarc/reorderable-list Quick Reorder Modal */}
      <Dialog open={reorderModalOpen} onOpenChange={setReorderModalOpen}>
        {reorderModalOpen && (
          <DialogContent
            className="reorder-dialog-content"
            title="调整监控画面排序"
            description="按住左侧手柄拖拽，或聚焦手柄后使用 ↑ / ↓ 方向键调整顺序，更改将实时同步到全览墙并自动保存。"
          >
            <ScrollArea
              maxHeight="68vh"
              className="reorder-modal-scroll"
              label="监控画面顺序列表"
            >
              <ReorderableList
                items={reorderListItems}
                onReorder={handleReorderFromList}
                label="监控画面顺序列表"
              />
            </ScrollArea>
          </DialogContent>
        )}
      </Dialog>

      {/* @uiarc/dialog Controlled One-Click Safe Self-Update Modal */}
      <Dialog open={updateModalOpen} onOpenChange={setUpdateModalOpen}>
        {updateModalOpen && (
          <DialogContent
            className="form-dialog-content"
            title="软件版本检查与受控安全热升级"
            description="本系统采用受控升级机制：不会在后台静默打断监控录像，仅在您确认后先校验 SHA-256 签名、安全刷盘所有 1080P 录像分片，再原子替换主程序并自动重启。"
          >
            <div className="modal-form-stack">
              <div className="update-meta-strip">
                <Badge tone="neutral" size="sm">
                  当前版本: {updateStatus?.current_version || "v2.0.0"}
                </Badge>
                {updateStatus?.os_arch && (
                  <Badge tone="neutral" size="sm">
                    运行平台: {updateStatus.os_arch}
                  </Badge>
                )}
                {updateStatus?.has_update && updateStatus.latest_version && (
                  <Badge tone="success" size="sm">
                    最新可用: {updateStatus.latest_version}
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
                  void handleCheckUpdate();
                }}
              >
                <div className="update-source-row__input">
                  <Input
                    label="更新源地址 (默认绑定 GitHub Releases 官方仓库，亦支持局域网共享目录)"
                    value={updateUrlInput}
                    onChange={(e) => setUpdateUrlInput(e.target.value)}
                    placeholder="https://api.github.com/repos/focus0802/focus-camera/releases/latest"
                    description="留空或使用默认地址将从 GitHub Releases (focus0802/focus-camera) 检查更新（内置国内加速镜像自动回退）"
                  />
                </div>
              </form>

              {updateStatus?.error && (
                <Alert tone="danger" title="检查或执行更新时遇到问题">
                  {updateStatus.error}
                </Alert>
              )}

              {!updateStatus?.error &&
                updateStatus?.state === "up_to_date" &&
                updateStatus.message && (
                  <Alert tone="success" title={updateStatus.message}>
                    当前运行程序已是最新稳定版本，无需升级。
                  </Alert>
                )}

              {!updateStatus?.error &&
                updateStatus?.state === "idle" &&
                updateStatus.message && (
                  <Alert tone="info" title="配置更新源说明">
                    {updateStatus.message}
                  </Alert>
                )}

              {updateStatus?.has_update && (
                <div className="update-release-card">
                  <div className="update-release-card__head">
                    <strong>
                      新版本 {updateStatus.latest_version}
                      {updateStatus.published_at ? ` (${updateStatus.published_at})` : ""}
                    </strong>
                    {updateStatus.expected_sha256 && (
                      <Badge tone="info" size="sm">
                        <ShieldCheck size={12} />
                        <span>含 SHA-256 签名校验</span>
                      </Badge>
                    )}
                  </div>
                  {updateStatus.release_notes && (
                    <pre className="update-release-notes">
                      {updateStatus.release_notes}
                    </pre>
                  )}
                  {activeRecordingCount > 0 && (
                    <p className="update-safe-note">
                      提示：当前有 {activeRecordingCount} 路 1080P 录像正在运行。点击下方「确认升级并重启」后，系统会先安全封存当前所有 MP4 录像分片，不会造成录像文件损坏。
                    </p>
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

              <div className="manual-cam-actions">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => setUpdateModalOpen(false)}
                >
                  关闭
                </Button>
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  loading={checkingUpdate || updateStatus?.state === "checking"}
                  disabled={
                    updateStatus?.state === "downloading" ||
                    updateStatus?.state === "applying" ||
                    updateStatus?.state === "restarting"
                  }
                  onClick={() => void handleCheckUpdate()}
                >
                  <RefreshCw size={14} />
                  <span>检查更新</span>
                </Button>
                {updateStatus?.has_update && (
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
                    onClick={() => void handleApplyUpdate()}
                  >
                    <Download size={14} />
                    <span>
                      确认安全升级并重启 ({updateStatus.latest_version})
                    </span>
                  </Button>
                )}
              </div>
            </div>
          </DialogContent>
        )}
      </Dialog>

      {/* @uiarc/dialog Graceful Service Shutdown Confirmation Modal */}
      <Dialog open={shutdownModalOpen} onOpenChange={setShutdownModalOpen}>
        {shutdownModalOpen && (
          <DialogContent
            className="shutdown-dialog-content"
            title="关闭或最小化主窗口"
            description={
              isMacDesktop
                ? "您可以将主窗口收起到右上角菜单栏托盘继续后台运行，或彻底关闭监控服务。"
                : "您可以将主窗口收起到右下角系统托盘继续后台运行，或彻底关闭监控服务。"
            }
          >
            <div className="modal-form-stack">
              {activeRecordingCount > 0 && (
                <Alert
                  tone="warning"
                  title={`正在后台录制 ${activeRecordingCount} 路 1080P+音频监控`}
                >
                  选择「最小化到系统托盘」不会中断任何录像任务，点击托盘图标即可随时恢复主窗口；若选择「彻底退出并停录」，系统将先安全封存当前录像分片再关闭服务。
                </Alert>
              )}
              <div className="manual-cam-actions manual-cam-actions--split">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  disabled={shuttingDown}
                  onClick={() => setShutdownModalOpen(false)}
                >
                  取消
                </Button>
                <div className="manual-cam-actions__right">
                  {(isWindowsDesktop || isMacDesktop) && (
                    <Button
                      type="button"
                      variant="primary"
                      size="sm"
                      disabled={shuttingDown}
                      onClick={() => {
                        setShutdownModalOpen(false);
                        window.webkit?.messageHandlers?.focusWindow?.postMessage("tray");
                        sendWindowsControl("tray");
                      }}
                    >
                      <span>最小化到系统托盘</span>
                    </Button>
                  )}
                  <Button
                    type="button"
                    variant="danger"
                    size="sm"
                    loading={shuttingDown}
                    onClick={async () => {
                      setShuttingDown(true);
                      sendWindowsControl("remove-tray");
                      await fetch("/api/shutdown", { method: "POST" }).catch(() => {});
                      window.close();
                    }}
                  >
                    <Power size={14} />
                    <span>彻底退出并停录</span>
                  </Button>
                </div>
              </div>
            </div>
          </DialogContent>
        )}
      </Dialog>
    </div>
  );
}

export default App;
