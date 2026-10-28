import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ArrowUpDown,
  Camera as CameraIcon,
  Compass,
  Film,
  Maximize2,
  Mic,
  Minimize2,
  PhoneCall,
  PhoneOff,
  Power,
  Radar,
  RefreshCw,
  Search,
  Settings,
  SlidersHorizontal,
  Sparkles,
  Square,
  Video,
  VideoOff,
  VolumeX,
  X,
} from "lucide-react";
import {
  cameraSupportsTalk,
  type Camera,
  type PTZPreset,
  type RecordingConfig,
  type RecordingState,
  type RecordingsPayload,
  type SystemSettingsTab,
  type UpdateStatus,
} from "./types";
import { TalkbackSession } from "./lib/audio";
import { StreamTile } from "./components/StreamTile";
import { PTZControlPanel } from "./components/PTZControlPanel";
import { PlaybackView } from "./components/PlaybackView";
import { DeviceManagerView } from "./components/DeviceManagerView";
import { SystemSettingsModal } from "./components/SystemSettingsModal";
import SegmentedControl from "./components/arc/segmented-control/segmented-control";
import { Tabs, TabsList, TabsTrigger } from "./components/arc/tabs/tabs";
import { Pagination } from "./components/arc/pagination/pagination";
import { AnimatedCounter } from "./components/arc/animated-counter/animated-counter";
import { ScrollArea } from "./components/arc/scroll-area/scroll-area";
import { Alert } from "./components/arc/alert/alert";
import { Button } from "./components/arc/button/button";
import { Badge } from "./components/arc/badge/badge";
import { SearchField } from "./components/arc/search-field/search-field";
import { EmptyState } from "./components/arc/empty-state/empty-state";
import { Dialog, DialogContent } from "./components/arc/dialog/dialog";
import { Switch } from "./components/arc/switch/switch";
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
      Boolean(window.chrome?.webview?.postMessage)
    );
  }, []);

  const sendWindowsControl = useCallback(
    (action: "minimize" | "tray" | "remove-tray" | "close") => {
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

  // Space-Maximizing Wall Modes: Floating Control Drawer, Immersive Auto-Hide & Command Palette
  const [controlDrawerOpen, setControlDrawerOpen] = useState<boolean>(false);
  const [commandModalOpen, setCommandModalOpen] = useState<boolean>(false);
  const [commandQuery, setCommandQuery] = useState<string>("");
  const [commandSelectedIndex, setCommandSelectedIndex] = useState<number>(0);
  const [immersiveMode, setImmersiveMode] = useState<boolean>(
    () => localStorage.getItem("focus_immersive_wall") === "true"
  );
  const [idleAutoHide, setIdleAutoHide] = useState<boolean>(
    () => localStorage.getItem("focus_idle_autohide") === "true"
  );
  const [headerHovered, setHeaderHovered] = useState<boolean>(false);
  const [isUserIdle, setIsUserIdle] = useState<boolean>(false);
  const drawerRef = useRef<HTMLDivElement | null>(null);
  const drawerTriggerRef = useRef<HTMLButtonElement | null>(null);
  const headerHoverTimeoutRef = useRef<number | undefined>(undefined);

  useEffect(() => {
    localStorage.setItem("focus_immersive_wall", String(immersiveMode));
  }, [immersiveMode]);

  useEffect(() => {
    localStorage.setItem("focus_idle_autohide", String(idleAutoHide));
  }, [idleAutoHide]);

  useEffect(() => {
    if (activeTab !== "live") {
      setControlDrawerOpen(false);
    }
  }, [activeTab]);

  // Light dismiss for floating control drawer
  useEffect(() => {
    if (!controlDrawerOpen) return;
    const onPointerDown = (e: PointerEvent) => {
      const target = e.target as Node | null;
      if (!target) return;
      if (drawerRef.current?.contains(target)) return;
      if (drawerTriggerRef.current?.contains(target)) return;
      setControlDrawerOpen(false);
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setControlDrawerOpen(false);
      }
    };
    window.addEventListener("pointerdown", onPointerDown);
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("pointerdown", onPointerDown);
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [controlDrawerOpen]);

  // Audio Monitoring & Two-way Talkback State
  const [audioEnabledMap, setAudioEnabledMap] = useState<Record<string, boolean>>({});
  const [talkCamera, setTalkCamera] = useState<Camera | null>(null);
  const [talkMode, setTalkMode] = useState<"toggle" | "ptt" | null>(null);
  const [talkState, setTalkState] = useState<"connecting" | "ready" | "error" | "closed">("closed");
  const [talkMessage, setTalkMessage] = useState<string>("");
  const [micLevel, setMicLevel] = useState<number>(0);
  const talkSessionRef = useRef<TalkbackSession | null>(null);
  const talkMetaRef = useRef<{ cameraId: string; mode: "toggle" | "ptt" } | null>(null);

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
  const [settingsModalOpen, setSettingsModalOpen] = useState<boolean>(false);
  const [settingsTab, setSettingsTab] = useState<SystemSettingsTab>("general");
  const [shutdownModalOpen, setShutdownModalOpen] = useState<boolean>(false);
  const [shuttingDown, setShuttingDown] = useState<boolean>(false);
  const [updateStatus, setUpdateStatus] = useState<UpdateStatus | null>(null);
  const [updateUrlInput, setUpdateUrlInput] = useState<string>("");
  const [checkingUpdate, setCheckingUpdate] = useState<boolean>(false);
  const [applyingUpdate, setApplyingUpdate] = useState<boolean>(false);

  const openSystemSettings = useCallback(
    (tab: SystemSettingsTab = "general") => {
      if (updateStatus?.update_url && !updateUrlInput) {
        setUpdateUrlInput(updateStatus.update_url);
      }
      setSettingsTab(tab);
      setSettingsModalOpen(true);
    },
    [updateStatus?.update_url, updateUrlInput]
  );
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
    if (!isBusy && !(settingsModalOpen && settingsTab === "update")) return;
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
  }, [updateStatus?.state, settingsModalOpen, settingsTab]);

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
    talkMetaRef.current = null;
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
      talkMetaRef.current = { cameraId: camera.id, mode };
      setTalkCamera(camera);
      setTalkMode(mode);
      setTalkState("connecting");
      setTalkMessage(`正在呼叫 ${camera.name} (${camera.ip})…`);

      const session = new TalkbackSession(camera.id, {
        onStatus: (st, msg) => {
          setTalkState(st);
          setTalkMessage(msg);
          if (st === "closed") {
            talkMetaRef.current = null;
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
      if (
        (talkMetaRef.current?.cameraId === camera.id && talkMetaRef.current?.mode === "toggle") ||
        (talkCamera?.id === camera.id && talkMode === "toggle")
      ) {
        return;
      }
      startTalkback(camera, "ptt");
    },
    [talkCamera, talkMode, startTalkback]
  );

  const handleStopPTT = useCallback(
    (camera: Camera) => {
      if (
        (talkMetaRef.current?.cameraId === camera.id && talkMetaRef.current?.mode === "ptt") ||
        (talkCamera?.id === camera.id && talkMode === "ptt")
      ) {
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

  // Idle auto-hide detection for Live Wall
  useEffect(() => {
    if (activeTab !== "live" || !idleAutoHide || cameras.length === 0) {
      setIsUserIdle(false);
      return;
    }
    let timer: number | undefined;
    const resetIdle = () => {
      setIsUserIdle(false);
      if (timer) window.clearTimeout(timer);
      timer = window.setTimeout(() => {
        setIsUserIdle(true);
      }, 3600);
    };
    resetIdle();
    window.addEventListener("pointermove", resetIdle, { passive: true });
    window.addEventListener("pointerdown", resetIdle, { passive: true });
    window.addEventListener("keydown", resetIdle);
    window.addEventListener("wheel", resetIdle, { passive: true });
    return () => {
      if (timer) window.clearTimeout(timer);
      window.removeEventListener("pointermove", resetIdle);
      window.removeEventListener("pointerdown", resetIdle);
      window.removeEventListener("keydown", resetIdle);
      window.removeEventListener("wheel", resetIdle);
    };
  }, [activeTab, idleAutoHide, cameras.length]);

  const anyModalOpen =
    Boolean(zoomCamera) ||
    commandModalOpen ||
    reorderModalOpen ||
    settingsModalOpen ||
    shutdownModalOpen;

  const isWallHeaderFloating =
    activeTab === "live" &&
    cameras.length > 0 &&
    (immersiveMode || (idleAutoHide && isUserIdle));

  const isWallHeaderVisible =
    !isWallHeaderFloating ||
    headerHovered ||
    controlDrawerOpen ||
    anyModalOpen;

  const revealHeader = useCallback(() => {
    if (headerHoverTimeoutRef.current) {
      window.clearTimeout(headerHoverTimeoutRef.current);
      headerHoverTimeoutRef.current = undefined;
    }
    setHeaderHovered(true);
  }, []);

  const scheduleHideHeader = useCallback(() => {
    if (headerHoverTimeoutRef.current) {
      window.clearTimeout(headerHoverTimeoutRef.current);
    }
    headerHoverTimeoutRef.current = window.setTimeout(() => {
      setHeaderHovered(false);
    }, 320);
  }, []);

  // Global keyboard shortcuts for Live Wall (Cmd/Ctrl+K, /, F, V, M, 1~5, [ / ])
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setCommandQuery("");
        setCommandSelectedIndex(0);
        setCommandModalOpen((prev) => !prev);
        return;
      }

      const target = e.target as HTMLElement | null;
      const isEditable =
        target instanceof HTMLInputElement ||
        target instanceof HTMLTextAreaElement ||
        target instanceof HTMLSelectElement ||
        Boolean(target?.isContentEditable);

      if (isEditable || anyModalOpen) return;

      if (e.key === "/") {
        e.preventDefault();
        setCommandQuery(searchText);
        setCommandSelectedIndex(0);
        setCommandModalOpen(true);
        return;
      }

      if (activeTab !== "live" || cameras.length === 0) return;

      if (e.metaKey || e.ctrlKey || e.altKey) return;

      switch (e.key.toLowerCase()) {
        case "f":
          e.preventDefault();
          setImmersiveMode((prev) => !prev);
          break;
        case "v":
          e.preventDefault();
          setControlDrawerOpen((prev) => !prev);
          break;
        case "m":
          e.preventDefault();
          setAudioEnabledMap({});
          break;
        case "1":
          e.preventDefault();
          setGridMode("all");
          setCurrentPage(0);
          break;
        case "2":
          e.preventDefault();
          setGridMode("36");
          setCurrentPage(0);
          break;
        case "3":
          e.preventDefault();
          setGridMode("40");
          setCurrentPage(0);
          break;
        case "4":
          e.preventDefault();
          setGridMode("16");
          setCurrentPage(0);
          break;
        case "5":
          e.preventDefault();
          setGridMode("9");
          setCurrentPage(0);
          break;
        case "[":
        case "pageup":
          if (gridMode !== "all" && totalPages > 1) {
            e.preventDefault();
            setCurrentPage((p) => Math.max(0, p - 1));
          }
          break;
        case "]":
        case "pagedown":
          if (gridMode !== "all" && totalPages > 1) {
            e.preventDefault();
            setCurrentPage((p) => Math.min(totalPages - 1, p + 1));
          }
          break;
        case "escape":
          if (controlDrawerOpen) {
            setControlDrawerOpen(false);
          } else if (immersiveMode) {
            setImmersiveMode(false);
          }
          break;
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [
    activeTab,
    anyModalOpen,
    cameras.length,
    controlDrawerOpen,
    gridMode,
    immersiveMode,
    searchText,
    totalPages,
  ]);

  const commandMatchedCameras = useMemo(() => {
    const q = commandQuery.trim().toLowerCase();
    if (!q) return cameras;
    return cameras.filter(
      (c) =>
        c.name.toLowerCase().includes(q) ||
        c.ip.toLowerCase().includes(q) ||
        (c.group || "").toLowerCase().includes(q)
    );
  }, [cameras, commandQuery]);

  const safeCommandIndex = Math.min(
    commandSelectedIndex,
    Math.max(0, commandMatchedCameras.length - 1)
  );

  const gridModeShortLabel = useMemo(() => {
    if (gridMode === "all") return `全览 (${filteredCameras.length})`;
    return `${gridMode}宫格`;
  }, [gridMode, filteredCameras.length]);

  const hasActiveFilter =
    selectedGroup !== "all" || searchText.trim().length > 0;

  return (
    <div
      className={`app-shell ${
        isWallHeaderFloating ? "app-shell--immersive-live" : ""
      } ${!isWallHeaderVisible ? "app-shell--header-hidden" : ""}`}
    >
      {/* Top-Edge Hover Sensor for Immersive TV Wall Mode */}
      {isWallHeaderFloating && !isWallHeaderVisible && (
        <div
          className="immersive-top-sensor"
          onPointerEnter={revealHeader}
          aria-hidden="true"
        />
      )}

      {/* Top Navigation Header */}
      <header
        className={`top-header ${
          isWallHeaderFloating ? "top-header--floating" : ""
        } ${!isWallHeaderVisible ? "top-header--hidden" : ""}`}
        onPointerEnter={revealHeader}
        onPointerLeave={scheduleHideHeader}
      >
        <div className="brand-area">
          <svg
            width="28"
            height="28"
            viewBox="0 0 64 64"
            fill="none"
            aria-hidden="true"
            className="brand-icon"
          >
            <defs>
              <linearGradient id="fc-hdr-bg" x1="32" y1="1.5" x2="32" y2="62.5" gradientUnits="userSpaceOnUse">
                <stop offset="0" stopColor="#364056" />
                <stop offset="0.48" stopColor="#1E2534" />
                <stop offset="1" stopColor="#0F131C" />
              </linearGradient>
              <linearGradient id="fc-hdr-bezel" x1="10" y1="10" x2="54" y2="54" gradientUnits="userSpaceOnUse">
                <stop offset="0" stopColor="#93A8C8" />
                <stop offset="0.5" stopColor="#47536B" />
                <stop offset="1" stopColor="#202636" />
              </linearGradient>
              <radialGradient id="fc-hdr-glass" cx="0" cy="0" r="1" gradientUnits="userSpaceOnUse" gradientTransform="translate(34.5 34.5) scale(19.5)">
                <stop offset="0" stopColor="#0C2147" />
                <stop offset="0.52" stopColor="#16438C" />
                <stop offset="0.86" stopColor="#297EF6" />
                <stop offset="1" stopColor="#0E2A5C" />
              </radialGradient>
              <radialGradient id="fc-hdr-pupil" cx="0" cy="0" r="1" gradientUnits="userSpaceOnUse" gradientTransform="translate(31 31) scale(9)">
                <stop offset="0" stopColor="#050812" />
                <stop offset="0.7" stopColor="#0B1736" />
                <stop offset="1" stopColor="#1F4690" />
              </radialGradient>
            </defs>
            <rect x="1.5" y="1.5" width="61" height="61" rx="14.5" fill="url(#fc-hdr-bg)" stroke="#8AB8E6" strokeOpacity="0.48" strokeWidth="1.5" />
            <path d="M7.5 15V10.8C7.5 8.97746 8.97746 7.5 10.8 7.5H15M7.5 49V53.2C7.5 55.0225 8.97746 56.5 10.8 56.5H15M56.5 49V53.2C56.5 55.0225 55.0225 56.5 53.2 56.5H49" stroke="#8AB8E6" strokeOpacity="0.34" strokeWidth="1.6" strokeLinecap="round" />
            <circle cx="32" cy="32" r="22.4" fill="url(#fc-hdr-bezel)" />
            <circle cx="32" cy="32" r="19.8" fill="#111622" />
            <circle cx="32" cy="32" r="17.6" fill="url(#fc-hdr-glass)" />
            <circle cx="32" cy="32" r="13.8" stroke="#76CCFF" strokeOpacity="0.38" strokeWidth="0.95" />
            <circle cx="32" cy="32" r="8.4" fill="url(#fc-hdr-pupil)" stroke="#62BAFF" strokeOpacity="0.55" strokeWidth="1.1" />
            <ellipse cx="24.8" cy="24.5" rx="6.2" ry="3.1" transform="rotate(-42 24.8 24.5)" fill="#FFFFFF" fillOpacity="0.52" />
            <circle cx="28.4" cy="28.4" r="1.5" fill="#FFFFFF" fillOpacity="0.9" />
            <circle cx="51.2" cy="12.8" r="4.1" fill="#0E121A" />
            <circle cx="51.2" cy="12.8" r="2.9" fill="#FF3B30" />
            <circle cx="50.4" cy="12" r="0.8" fill="#FFE5E5" />
          </svg>
          <div className="brand-text-row">
            <h1 className="brand-title">FocusCamera</h1>
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
                  <span>录制 </span>
                  <span className="inline-counter">
                    <AnimatedCounter value={activeRecordingCount} />
                  </span>
                  <span> 路</span>
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
              <TabsTrigger value="devices">设备管理</TabsTrigger>
            </TabsList>
          </Tabs>
        </nav>

        <div className="header-actions">
          {activeTab === "live" && cameras.length > 0 && (
            <div className="header-live-controls">
              {hasActiveFilter && (
                <button
                  type="button"
                  className="active-filter-chip"
                  onClick={() => {
                    setSelectedGroup("all");
                    setSearchText("");
                    setCurrentPage(0);
                  }}
                  title="点击一键清除当前分组与关键字筛选"
                >
                  <span>
                    {selectedGroup !== "all" ? selectedGroup : ""}
                    {selectedGroup !== "all" && searchText.trim() ? " · " : ""}
                    {searchText.trim() ? `"${searchText.trim()}"` : ""}
                  </span>
                  <X size={12} aria-hidden="true" />
                </button>
              )}

              {gridMode !== "all" && totalPages > 1 && (
                <div className="header-pagination-wrap">
                  <Pagination
                    page={safePage + 1}
                    pageCount={totalPages}
                    onPageChange={(p) => setCurrentPage(p - 1)}
                    label="监控画面分页导航"
                  />
                </div>
              )}

              <Button
                variant="secondary"
                size="sm"
                onClick={() => {
                  setCommandQuery(searchText);
                  setCommandSelectedIndex(0);
                  setCommandModalOpen(true);
                }}
                title="快速搜索设备或执行大屏指令（快捷键 ⌘K 或 /）"
              >
                <Search size={13} />
                <span>搜索</span>
                <kbd className="header-kbd">/</kbd>
              </Button>

              <Button
                ref={drawerTriggerRef}
                variant={controlDrawerOpen || hasActiveFilter ? "primary" : "secondary"}
                size="sm"
                onClick={() => setControlDrawerOpen((prev) => !prev)}
                title="展开或收起宫格切换、分组筛选与批量控制抽屉（快捷键 V）"
                aria-expanded={controlDrawerOpen}
              >
                <SlidersHorizontal size={13} />
                <span>{gridModeShortLabel}</span>
              </Button>

              <Button
                variant={immersiveMode ? "primary" : "ghost"}
                size="sm"
                onClick={() => setImmersiveMode((prev) => !prev)}
                title={
                  immersiveMode
                    ? "退出沉浸电视墙模式，恢复常驻顶栏（快捷键 F 或 Esc）"
                    : "开启沉浸电视墙模式：自动隐藏顶栏，鼠标触顶唤出，100% 满屏显示（快捷键 F）"
                }
              >
                {immersiveMode ? <Minimize2 size={14} /> : <Maximize2 size={14} />}
                <span>{immersiveMode ? "退出沉浸" : "沉浸"}</span>
              </Button>
            </div>
          )}

          {updateStatus?.has_update && (
            <Button
              variant="primary"
              size="sm"
              onClick={() => openSystemSettings("update")}
              title="发现可用新版本，点击进入系统设置执行一键受控热更新"
            >
              <Sparkles size={14} />
              <span>发现新版本 {updateStatus.latest_version}</span>
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            onClick={() => openSystemSettings("general")}
            title={`系统设置（开机自启 · 录像与存储策略 · 版本 ${updateStatus?.current_version || "v2.3.0"}）`}
          >
            <Settings size={14} />
            <span>系统设置</span>
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

      {/* Floating Glassmorphic Live Wall Control Popover (Zero Vertical Layout Shift) */}
      {activeTab === "live" && cameras.length > 0 && controlDrawerOpen && (
        <section
          ref={drawerRef}
          className="wall-control-drawer"
          aria-label="大屏视图与批量控制抽屉"
          onPointerEnter={revealHeader}
        >
          <div className="wall-control-drawer__top-bar">
            <div className="wall-search-box wall-search-box--flex">
              <SearchField
                label="搜索设备"
                placeholder="实时过滤名称、分组或 IP…"
                value={searchText}
                onValueChange={(val) => {
                  setSearchText(val);
                  setCurrentPage(0);
                }}
              />
            </div>

            <div className="wall-control-drawer__actions">
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
                title="关闭所有设备的声音监听（快捷键 M）"
              >
                <VolumeX size={14} />
                <span>全部静音 (M)</span>
              </Button>

              <Button
                variant="secondary"
                size="sm"
                onClick={() => {
                  setControlDrawerOpen(false);
                  setReorderModalOpen(true);
                }}
                title="打开 @uiarc/reorderable-list 列表视图调整监控顺序"
              >
                <ArrowUpDown size={14} />
                <span>排序</span>
              </Button>
            </div>
          </div>

          <div className="wall-control-drawer__field">
            <span className="wall-control-drawer__label">宫格布局 (1~5)</span>
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
          </div>

          <div className="wall-control-drawer__field">
            <span className="wall-control-drawer__label">设备分组筛选</span>
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

          <div className="wall-control-drawer__footer">
            <Switch
              checked={immersiveMode}
              onCheckedChange={setImmersiveMode}
              label="沉浸电视墙模式（隐藏顶栏，鼠标触顶唤出 · F）"
            />
            <Switch
              checked={idleAutoHide}
              onCheckedChange={setIdleAutoHide}
              label="空闲 4 秒自动隐身顶栏"
            />
          </div>
        </section>
      )}

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
                    title="尚未添加监控摄像头"
                    action={
                      <Button
                        variant="primary"
                        size="lg"
                        onClick={() => setActiveTab("devices")}
                      >
                        <Radar size={18} />
                        <span>扫描并添加摄像头</span>
                      </Button>
                    }
                  />
                </div>
              </div>
            ) : (
              <div className="live-wall-layout">
                {/* Camera Wall Grid (100% Space Utilization, No Permanent Second Toolbar Row) */}
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
            onOpenSettingsModal={() => openSystemSettings("recording")}
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
            onOpenSettingsModal={() => openSystemSettings("recording")}
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
            title={zoomCamera.name}
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
                  {cameraSupportsTalk(zoomCamera) && (
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
                            <span>挂断通话</span>
                          </>
                        ) : (
                          <>
                            <PhoneCall size={16} />
                            <span>双向通话</span>
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
                        onPointerDown={(e) => {
                          if (e.button !== 0) return;
                          e.currentTarget.setPointerCapture(e.pointerId);
                          handleStartPTT(zoomCamera);
                        }}
                        onPointerUp={(e) => {
                          if (e.currentTarget.hasPointerCapture(e.pointerId)) {
                            e.currentTarget.releasePointerCapture(e.pointerId);
                          }
                          handleStopPTT(zoomCamera);
                        }}
                        onPointerCancel={() => handleStopPTT(zoomCamera)}
                        onLostPointerCapture={() => handleStopPTT(zoomCamera)}
                      >
                        <Mic size={16} />
                        <span>
                          {talkCamera?.id === zoomCamera.id && talkMode === "ptt"
                            ? "对讲中…"
                            : "按住说话"}
                        </span>
                      </button>
                    </>
                  )}
                </div>

                <div className="zoom-intercom-bar__right">
                  <Button
                    variant={ptzPanelOpen ? "primary" : "secondary"}
                    size="md"
                    onClick={() => setPtzPanelOpen((v) => !v)}
                    title="展开或收起右侧云台控制面板"
                  >
                    <Compass size={16} />
                    <span>云台控制</span>
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
                        <span>{isMan ? "停止录制" : "手动录像"}</span>
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
                    <span>录像回放</span>
                  </Button>
                </div>
              </div>
            </div>
          </DialogContent>
        )}
      </Dialog>

      {/* Quick Search & Command Palette Dialog (Cmd/Ctrl+K or /) */}
      <Dialog open={commandModalOpen} onOpenChange={setCommandModalOpen}>
        {commandModalOpen && (
          <DialogContent
            className="command-dialog-content"
            title="快速搜索与大屏指令 (⌘K /)"
          >
            <div className="command-palette">
              <div className="command-palette__search">
                <SearchField
                  autoFocus
                  label="搜索监控设备"
                  placeholder="输入名称、IP 或分组（↑↓ 选择，回车直达 1080P 放大，Shift+回车筛选大屏）…"
                  value={commandQuery}
                  onValueChange={(val) => {
                    setCommandQuery(val);
                    setCommandSelectedIndex(0);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "ArrowDown") {
                      e.preventDefault();
                      setCommandSelectedIndex((i) =>
                        Math.min(
                          Math.max(0, commandMatchedCameras.length - 1),
                          i + 1
                        )
                      );
                    } else if (e.key === "ArrowUp") {
                      e.preventDefault();
                      setCommandSelectedIndex((i) => Math.max(0, i - 1));
                    } else if (e.key === "Enter") {
                      e.preventDefault();
                      if (e.shiftKey) {
                        setSearchText(commandQuery);
                        setCurrentPage(0);
                        setActiveTab("live");
                        setCommandModalOpen(false);
                        return;
                      }
                      const targetCam = commandMatchedCameras[safeCommandIndex];
                      if (targetCam) {
                        setCommandModalOpen(false);
                        setActiveTab("live");
                        handleOpenZoom(targetCam);
                      }
                    }
                  }}
                />
                {commandQuery.trim() !== searchText.trim() && (
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => {
                      setSearchText(commandQuery.trim());
                      setCurrentPage(0);
                      setActiveTab("live");
                      setCommandModalOpen(false);
                    }}
                  >
                    <span>在大屏中过滤 "{commandQuery.trim() || "全部"}"</span>
                  </Button>
                )}
              </div>

              <div className="command-palette__quick-bar">
                <span className="command-palette__section-label">快捷布局与模式</span>
                <div className="command-palette__pills">
                  {(
                    [
                      { value: "all", label: "1 · 全览" },
                      { value: "36", label: "2 · 36宫格" },
                      { value: "40", label: "3 · 40宫格" },
                      { value: "16", label: "4 · 16宫格" },
                      { value: "9", label: "5 · 9宫格" },
                    ] as const
                  ).map((opt) => (
                    <Button
                      key={opt.value}
                      variant={gridMode === opt.value ? "primary" : "secondary"}
                      size="sm"
                      onClick={() => {
                        setGridMode(opt.value);
                        setCurrentPage(0);
                        setActiveTab("live");
                        setCommandModalOpen(false);
                      }}
                    >
                      <span>{opt.label}</span>
                    </Button>
                  ))}

                  <Button
                    variant={immersiveMode ? "primary" : "secondary"}
                    size="sm"
                    onClick={() => {
                      setImmersiveMode((prev) => !prev);
                      setActiveTab("live");
                      setCommandModalOpen(false);
                    }}
                  >
                    {immersiveMode ? <Minimize2 size={13} /> : <Maximize2 size={13} />}
                    <span>F · {immersiveMode ? "退出沉浸" : "沉浸满屏"}</span>
                  </Button>
                </div>
              </div>

              {groups.length > 1 && (
                <div className="command-palette__quick-bar">
                  <span className="command-palette__section-label">分组快速切换</span>
                  <div className="command-palette__pills">
                    {groups.map((g) => (
                      <Button
                        key={g}
                        variant={selectedGroup === g ? "primary" : "ghost"}
                        size="sm"
                        onClick={() => {
                          setSelectedGroup(g);
                          setCurrentPage(0);
                          setActiveTab("live");
                          setCommandModalOpen(false);
                        }}
                      >
                        <span>{g === "all" ? "全部分组" : g}</span>
                      </Button>
                    ))}
                  </div>
                </div>
              )}

              <div className="command-palette__results">
                <div className="command-palette__results-hdr">
                  <span className="command-palette__section-label">
                    匹配摄像头 ({commandMatchedCameras.length})
                  </span>
                  <span className="command-palette__hint">
                    回车打开 1080P 高清画面 · Shift+回车过滤大屏
                  </span>
                </div>

                <ScrollArea
                  maxHeight="44vh"
                  className="command-palette__scroll"
                  label="快速搜索匹配设备列表"
                >
                  {commandMatchedCameras.length === 0 ? (
                    <div className="command-palette__empty">
                      未找到匹配的摄像头
                    </div>
                  ) : (
                    <div className="command-palette__list" role="listbox">
                      {commandMatchedCameras.map((cam, idx) => {
                        const isOnline = Boolean(onlineMap[cam.id]);
                        const isSelected = idx === safeCommandIndex;
                        return (
                          <div
                            key={cam.id}
                            role="option"
                            aria-selected={isSelected}
                            className={`command-camera-item ${
                              isSelected ? "command-camera-item--selected" : ""
                            }`}
                            onMouseEnter={() => setCommandSelectedIndex(idx)}
                            onClick={() => {
                              setCommandModalOpen(false);
                              setActiveTab("live");
                              handleOpenZoom(cam);
                            }}
                          >
                            <div className="command-camera-item__left">
                              <span
                                className={`status-dot status-dot--${
                                  isOnline ? "live" : "connecting"
                                }`}
                              />
                              <CameraIcon size={14} />
                              <strong className="command-camera-item__name">
                                {cam.name}
                              </strong>
                              <span className="command-camera-item__ip">
                                {cam.ip}
                              </span>
                              {cam.group && (
                                <Badge tone="neutral" size="sm">
                                  {cam.group}
                                </Badge>
                              )}
                            </div>
                            <div className="command-camera-item__right">
                              <Button
                                variant="ghost"
                                size="sm"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  setSearchText(cam.name);
                                  setCurrentPage(0);
                                  setActiveTab("live");
                                  setCommandModalOpen(false);
                                }}
                              >
                                <span>仅看此路</span>
                              </Button>
                              <Badge
                                tone={isOnline ? "success" : "neutral"}
                                size="sm"
                              >
                                1080P 放大 ↵
                              </Badge>
                            </div>
                          </div>
                        );
                      })}
                    </div>
                  )}
                </ScrollArea>
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

      {/* Unified System Settings Modal (@uiarc/dialog + @uiarc/tabs) */}
      <SystemSettingsModal
        open={settingsModalOpen}
        onOpenChange={setSettingsModalOpen}
        activeTab={settingsTab}
        onTabChange={setSettingsTab}
        recordingConfig={recordingsPayload?.config ?? DEFAULT_REC_CONFIG}
        activeRecordingCount={activeRecordingCount}
        onSaveRecordingSettings={async (cfg) => {
          await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(cfg),
          });
          await fetchRecordings();
        }}
        updateStatus={updateStatus}
        updateUrlInput={updateUrlInput}
        onUpdateUrlInputChange={setUpdateUrlInput}
        checkingUpdate={checkingUpdate}
        applyingUpdate={applyingUpdate}
        onCheckUpdate={handleCheckUpdate}
        onApplyUpdate={handleApplyUpdate}
      />

      {/* @uiarc/dialog Graceful Service Shutdown Confirmation Modal */}
      <Dialog open={shutdownModalOpen} onOpenChange={setShutdownModalOpen}>
        {shutdownModalOpen && (
          <DialogContent
            className="shutdown-dialog-content"
            title="关闭或最小化主窗口"
          >
            <div className="modal-form-stack">
              {activeRecordingCount > 0 && (
                <Alert
                  tone="warning"
                  title={`正在后台录制 ${activeRecordingCount} 路监控`}
                />
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
