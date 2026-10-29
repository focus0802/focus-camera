import { useEffect, useMemo, useRef, useState } from "react";
import {
  ChevronLeft,
  ChevronRight,
  Clock,
  Download,
  Film,
  FolderOpen,
  HardDrive,
  Play,
  Radio,
  RefreshCw,
  Trash2,
  Video,
} from "lucide-react";
import type { Camera, RecordingsPayload, SegmentFile } from "../types";
import type { CalendarDayMarker } from "./arc/calendar/calendar";
import { DatePicker } from "./arc/date-picker/date-picker";
import SegmentedControl from "./arc/segmented-control/segmented-control";
import {
  MorphSelect,
  type MorphSelectOption,
} from "./arc/morph-select/morph-select";
import { SearchField } from "./arc/search-field/search-field";
import { Progress } from "./arc/progress/progress";
import { AnimatedCounter } from "./arc/animated-counter/animated-counter";
import { ScrollArea } from "./arc/scroll-area/scroll-area";
import {
  SortableDataTable,
  type DataColumn,
  type SortState,
} from "./arc/sortable-data-table/sortable-data-table";
import { Pagination } from "./arc/pagination/pagination";
import { Alert } from "./arc/alert/alert";
import { Badge } from "./arc/badge/badge";
import { Button } from "./arc/button/button";
import { Dialog, DialogContent } from "./arc/dialog/dialog";
import { EmptyState } from "./arc/empty-state/empty-state";

interface PlaybackViewProps {
  cameras: Camera[];
  payload: RecordingsPayload | null;
  selectedCameraId: string;
  onSelectCameraId: (id: string) => void;
  selectedDate: string;
  onSelectDate: (date: string) => void;
  onRefresh: () => Promise<void>;
  onOpenFolder: () => Promise<void>;
  onDeleteSegment: (relPath: string) => Promise<void>;
  onOpenSettingsModal: () => void;
}

type TableRowItem = SegmentFile & {
  size_mb: number;
  sort_timestamp: string;
} & Record<string, unknown>;

const zhNumericCollator = new Intl.Collator("zh-CN", {
  numeric: true,
  sensitivity: "base",
});

function formatBytes(bytes: number): string {
  if (!bytes || bytes <= 0) return "0 MB";
  const mb = bytes / (1024 * 1024);
  if (mb < 1024) return `${mb.toFixed(1)} MB`;
  return `${(mb / 1024).toFixed(2)} GB`;
}

function formatDuration(sec: number): string {
  if (!sec || sec <= 0) return "00:00";
  const m = Math.floor(sec / 60);
  const s = Math.floor(sec % 60);
  return `${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

function formatSecToHM(secOfDay: number): string {
  const clamped = Math.max(0, Math.min(86400, Math.round(secOfDay)));
  const h = Math.floor(clamped / 3600);
  const m = Math.floor((clamped % 3600) / 60);
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`;
}

function parseYMD(ymd: string): Date | undefined {
  if (!ymd) return undefined;
  const parts = ymd.split("-").map(Number);
  if (parts.length !== 3 || parts.some(isNaN)) return undefined;
  return new Date(parts[0], parts[1] - 1, parts[2]);
}

function toYMD(date: Date): string {
  const y = date.getFullYear();
  const m = String(date.getMonth() + 1).padStart(2, "0");
  const d = String(date.getDate()).padStart(2, "0");
  return `${y}-${m}-${d}`;
}

export function PlaybackView({
  cameras,
  payload,
  selectedCameraId,
  onSelectCameraId,
  selectedDate,
  onSelectDate,
  onRefresh,
  onOpenFolder,
  onDeleteSegment,
  onOpenSettingsModal,
}: PlaybackViewProps) {
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const tableScrollWrapRef = useRef<HTMLDivElement | null>(null);
  const [modeFilter, setModeFilter] = useState<string>("all");
  const [keyword, setKeyword] = useState<string>("");
  const [activeSegment, setActiveSegment] = useState<SegmentFile | null>(null);
  const [playbackRate, setPlaybackRate] = useState<string>("1");
  const [refreshing, setRefreshing] = useState<boolean>(false);
  const [timelineScale, setTimelineScale] = useState<"auto" | "24h">("auto");
  const [sortState, setSortState] = useState<SortState>({
    key: "sort_timestamp",
    direction: "desc",
  });
  const [page, setPage] = useState<number>(1);
  const [pageSize, setPageSize] = useState<number>(50);
  const [pendingDeleteSeg, setPendingDeleteSeg] = useState<SegmentFile | null>(null);
  const [deletingSeg, setDeletingSeg] = useState<boolean>(false);
  const [exportingRelPath, setExportingRelPath] = useState<string | null>(null);
  const [exportNotice, setExportNotice] = useState<{
    tone: "success" | "danger";
    title: string;
    savedPath?: string;
  } | null>(null);

  const segments = useMemo(() => {
    const raw = payload?.segments ?? [];
    const q = keyword.trim().toLowerCase();
    return raw.filter((s) => {
      if (modeFilter === "completed" && s.active) return false;
      if (modeFilter === "recording" && !s.active) return false;
      if (modeFilter === "manual" && s.mode !== "manual") return false;
      if (modeFilter === "auto" && s.mode !== "auto") return false;
      if (q) {
        const hay = `${s.camera_name} ${s.date} ${s.start_time} ${s.end_time}`.toLowerCase();
        if (!hay.includes(q)) return false;
      }
      return true;
    });
  }, [payload?.segments, modeFilter, keyword]);

  // Reset to page 1 when filter criteria or page size changes
  useEffect(() => {
    setPage(1);
  }, [selectedCameraId, selectedDate, modeFilter, keyword, pageSize]);

  // Global sort across all matching segments BEFORE pagination slicing
  const sortedRows = useMemo<TableRowItem[]>(() => {
    const mapped: TableRowItem[] = segments.map((s) => ({
      ...s,
      size_mb: Number((s.size_bytes / (1024 * 1024)).toFixed(2)),
      sort_timestamp: `${s.date} ${s.start_time}`,
    }));
    if (!sortState) return mapped;
    return mapped
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
  }, [segments, sortState]);

  const pageCount = Math.max(1, Math.ceil(sortedRows.length / pageSize));
  const safePage = Math.min(Math.max(1, page), pageCount);

  const pagedRows = useMemo<TableRowItem[]>(() => {
    const startIdx = (safePage - 1) * pageSize;
    return sortedRows.slice(startIdx, startIdx + pageSize);
  }, [sortedRows, safePage, pageSize]);

  // Select a segment and automatically jump to its page and scroll row into view
  function selectSegmentAndSyncPage(seg: SegmentFile) {
    setActiveSegment(seg);
    const idx = sortedRows.findIndex((r) => r.rel_path === seg.rel_path);
    if (idx >= 0) {
      const targetPage = Math.floor(idx / pageSize) + 1;
      if (targetPage !== safePage) {
        setPage(targetPage);
      }
    }
    requestAnimationFrame(() => {
      const activeBtn = tableScrollWrapRef.current?.querySelector<HTMLElement>(
        "[data-active-play='true']"
      );
      activeBtn?.closest("tr")?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    });
  }

  // Keep activeSegment synchronized with fresh poll data (duration, size, end_time, active flag)
  useEffect(() => {
    if (sortedRows.length > 0) {
      const matched = activeSegment
        ? sortedRows.find((s) => s.rel_path === activeSegment.rel_path)
        : undefined;
      if (matched) {
        if (
          matched.duration_sec !== activeSegment?.duration_sec ||
          matched.size_bytes !== activeSegment?.size_bytes ||
          matched.end_time !== activeSegment?.end_time ||
          matched.active !== activeSegment?.active
        ) {
          setActiveSegment(matched);
        }
      } else {
        // Prefer the newest segment that already has at least 15s of video so we don't auto-pick a 2s file
        const preferred =
          sortedRows.find((s) => !s.active && s.duration_sec >= 10) ||
          sortedRows.find((s) => s.duration_sec >= 15) ||
          sortedRows[0];
        selectSegmentAndSyncPage(preferred);
      }
    } else {
      setActiveSegment(null);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sortedRows, activeSegment]);

  useEffect(() => {
    if (videoRef.current) {
      videoRef.current.playbackRate = Number(playbackRate) || 1;
    }
  }, [playbackRate, activeSegment?.rel_path]);

  const activeRecordingCount = useMemo(
    () => (payload?.states ?? []).filter((s) => s.active).length,
    [payload?.states]
  );

  const availableDates = payload?.dates ?? [];

  const calendarDayMarkers = useMemo<Record<string, CalendarDayMarker>>(() => {
    const map: Record<string, CalendarDayMarker> = {};
    const summaries = payload?.date_summaries;
    if (summaries && Object.keys(summaries).length > 0) {
      for (const [d, ds] of Object.entries(summaries)) {
        if (!ds || ds.segment_count <= 0) continue;
        const tone: CalendarDayMarker["tone"] =
          ds.manual_count > 0 && ds.auto_count > 0
            ? "both"
            : ds.manual_count > 0
              ? "manual"
              : "auto";
        const modeDesc =
          ds.manual_count > 0 && ds.auto_count > 0
            ? `自动 ${ds.auto_count} / 手动 ${ds.manual_count}`
            : ds.manual_count > 0
              ? `手动 ${ds.manual_count} 段`
              : `自动 ${ds.auto_count} 段`;
        const activeTag = ds.active ? " · 正在录制" : "";
        map[d] = {
          tone,
          active: ds.active,
          count: ds.segment_count,
          tooltip: `${d} · 共 ${ds.segment_count} 段录像 (${modeDesc} · ${formatBytes(ds.total_bytes)})${activeTag}`,
        };
      }
      return map;
    }
    for (const d of availableDates) {
      map[d] = {
        tone: "auto",
        count: 1,
        tooltip: `${d} · 有录像分片`,
      };
    }
    return map;
  }, [payload?.date_summaries, availableDates]);

  const maxSelectableDate = useMemo(() => new Date(), []);

  const isCalendarDateDisabled = useMemo(
    () => (date: Date) => {
      const ymd = toYMD(date);
      if (selectedDate && ymd === selectedDate) return false;
      if (availableDates.length === 0) return false;
      return !calendarDayMarkers[ymd];
    },
    [selectedDate, availableDates.length, calendarDayMarkers]
  );

  // Scope the timeline to the active segment's date & camera (or selected filters)
  const timelineDate = useMemo(() => {
    if (selectedDate) return selectedDate;
    if (activeSegment?.date) return activeSegment.date;
    return availableDates[0] || "";
  }, [selectedDate, activeSegment?.date, availableDates]);

  const timelineCameraId = useMemo(() => {
    if (selectedCameraId) return selectedCameraId;
    return activeSegment?.camera_id || "";
  }, [selectedCameraId, activeSegment?.camera_id]);

  const timelineCameraName = useMemo(() => {
    if (activeSegment?.camera_id === timelineCameraId && activeSegment?.camera_name) {
      return activeSegment.camera_name;
    }
    return cameras.find((c) => c.id === timelineCameraId)?.name || "全部机位";
  }, [activeSegment, timelineCameraId, cameras]);

  const timelineSegments = useMemo(() => {
    return segments.filter((s) => {
      if (timelineDate && s.date !== timelineDate) return false;
      if (timelineCameraId && s.camera_id !== timelineCameraId) return false;
      return true;
    });
  }, [segments, timelineDate, timelineCameraId]);

  // Compute adaptive timeline bounds so recordings covering 1-2 hours don't collapse into a single dot
  const timelineBounds = useMemo(() => {
    if (timelineScale === "24h" || timelineSegments.length === 0) {
      return {
        startSec: 0,
        endSec: 86400,
        spanSec: 86400,
        ticks: [0, 3, 6, 9, 12, 15, 18, 21, 24].map((h) => h * 3600),
      };
    }
    let minS = 86400;
    let maxS = 0;
    for (const seg of timelineSegments) {
      if (seg.start_sec < minS) minS = seg.start_sec;
      const endS = seg.start_sec + Math.max(seg.duration_sec, 60);
      if (endS > maxS) maxS = endS;
    }
    // Pad by 15 mins on each side and snap to 15-min (900s) boundaries, minimum 1 hour (3600s) span
    let startSec = Math.max(0, Math.floor((minS - 900) / 900) * 900);
    let endSec = Math.min(86400, Math.ceil((maxS + 900) / 900) * 900);
    if (endSec - startSec < 3600) {
      const mid = (startSec + endSec) / 2;
      startSec = Math.max(0, Math.floor((mid - 1800) / 900) * 900);
      endSec = Math.min(86400, startSec + 3600);
    }
    const spanSec = Math.max(3600, endSec - startSec);
    const tickCount = 6;
    const ticks: number[] = [];
    for (let i = 0; i <= tickCount; i++) {
      ticks.push(startSec + (spanSec * i) / tickCount);
    }
    return { startSec, endSec, spanSec, ticks };
  }, [timelineScale, timelineSegments]);

  const activeIndex = useMemo(() => {
    if (!activeSegment) return -1;
    return sortedRows.findIndex((s) => s.rel_path === activeSegment.rel_path);
  }, [sortedRows, activeSegment]);

  async function handleRefresh() {
    setRefreshing(true);
    try {
      await onRefresh();
    } finally {
      setRefreshing(false);
    }
  }

  async function handleExportSegment(seg: SegmentFile) {
    if (exportingRelPath) return;
    setExportingRelPath(seg.rel_path);
    try {
      const safeTime = seg.start_time.replace(/:/g, "");
      const suggestedName = `${seg.camera_name}_${seg.date}_${safeTime}.mp4`;
      const res = await fetch("/api/recordings/export", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          path: seg.rel_path,
          suggested_name: suggestedName,
        }),
      });
      const rawText = await res.text();
      let data: {
        ok?: boolean;
        cancelled?: boolean;
        path?: string;
        error?: string;
      } = {};
      try {
        data = JSON.parse(rawText) as typeof data;
      } catch {
        if (res.status === 404) {
          setExportNotice({
            tone: "danger",
            title: "导出失败：当前运行的客户端程序尚未包含新版导出接口，请重启 FocusCamera 客户端后再试",
          });
          return;
        }
        setExportNotice({
          tone: "danger",
          title: `导出失败：服务端返回异常响应 (${res.status})`,
        });
        return;
      }
      if (!res.ok || !data.ok) {
        setExportNotice({
          tone: "danger",
          title: `导出失败：${data.error || "无法写入目标文件"}`,
        });
        return;
      }
      if (data.cancelled) {
        return;
      }
      if (data.path) {
        setExportNotice({
          tone: "success",
          title: `已导出 MP4 至：${data.path}`,
          savedPath: data.path,
        });
      }
    } catch (err) {
      setExportNotice({
        tone: "danger",
        title: `导出失败：${err instanceof Error ? err.message : "网络请求异常"}`,
      });
    } finally {
      setExportingRelPath(null);
    }
  }

  async function handleRevealExportedFile(filePath: string) {
    await fetch("/api/recordings/open-folder", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ select_file: filePath }),
    });
  }

  const columns: DataColumn<TableRowItem>[] = [
    {
      key: "camera_name",
      label: "设备 / 日期",
      sortable: true,
      width: "22%",
      render: (_val, row) => (
        <div className="table-cell-primary">
          <div className="table-cell-room-row">
            <strong>{row.camera_name}</strong>
            {row.active && (
              <span className="segment-live-dot" title="当前分片正在实时写入" />
            )}
          </div>
          <span className="table-cell-sub">{row.date}</span>
        </div>
      ),
    },
    {
      key: "sort_timestamp",
      label: "时间段 / 类型",
      sortable: true,
      width: "24%",
      render: (_val, row) => (
        <div className="table-cell-time">
          <span className="mono-text">
            {row.start_time} - {row.end_time}
          </span>
          <span
            className={`segment-mode-tag ${
              row.mode === "manual" ? "segment-mode-tag--manual" : "segment-mode-tag--auto"
            }`}
          >
            {row.mode === "manual" ? "手动录制" : "自动录像"}
          </span>
        </div>
      ),
    },
    {
      key: "duration_sec",
      label: "时长",
      sortable: true,
      numeric: true,
      width: "11%",
      render: (_val, row) => <span className="mono-text">{formatDuration(row.duration_sec)}</span>,
    },
    {
      key: "size_mb",
      label: "大小",
      sortable: true,
      numeric: true,
      width: "13%",
      render: (_val, row) => <span className="mono-text">{formatBytes(row.size_bytes)}</span>,
    },
    {
      key: "actions",
      label: "操作",
      sortable: false,
      width: "30%",
      render: (_val, row) => {
        const isCurrent = activeSegment?.rel_path === row.rel_path;
        const isExporting = exportingRelPath === row.rel_path;
        const rowDesc = `${row.camera_name} ${row.date} ${row.start_time}`;
        return (
          <div className="table-actions">
            <button
              type="button"
              data-play-trigger="true"
              data-active-play={isCurrent ? "true" : undefined}
              className={`table-action-btn ${isCurrent ? "table-action-btn--active" : ""}`}
              onClick={() => setActiveSegment(row)}
              aria-label={`回放 ${rowDesc}`}
            >
              <Play size={12} />
              <span>{isCurrent ? "当前" : "回放"}</span>
            </button>
            <button
              type="button"
              className="table-action-btn"
              disabled={Boolean(exportingRelPath)}
              onClick={() => void handleExportSegment(row)}
              aria-label={`导出 ${rowDesc} MP4`}
              title="选择目标文件夹导出标准 1080P MP4 录像文件"
            >
              <Download size={12} />
              <span>{isExporting ? "导出中…" : "导出"}</span>
            </button>
            <button
              type="button"
              className="table-action-btn table-action-btn--danger"
              onClick={() => setPendingDeleteSeg(row)}
              aria-label={`删除 ${rowDesc} 录像分片`}
              title="删除此录像分片"
            >
              <Trash2 size={12} />
            </button>
          </div>
        );
      },
    },
  ];

  const storageUsedStr = formatBytes(payload?.total_bytes ?? 0);
  const maxStorageGB = payload?.config.max_storage_gb ?? 500;
  const usedStorageGB = Number(
    ((payload?.total_bytes ?? 0) / (1024 * 1024 * 1024)).toFixed(2)
  );
  const rangeStart = sortedRows.length === 0 ? 0 : (safePage - 1) * pageSize + 1;
  const rangeEnd = Math.min(safePage * pageSize, sortedRows.length);

  const cameraSelectItems = useMemo<MorphSelectOption[]>(
    () => [
      {
        value: "all",
        label: `全部机位 (${cameras.length} 台)`,
        keywords: "all 全部",
      },
      ...cameras.map((cam) => ({
        value: cam.id,
        label: cam.name,
        meta: cam.ip,
        keywords: `${cam.name} ${cam.ip} ${cam.group || ""}`,
      })),
    ],
    [cameras]
  );

  return (
    <div className="view-container playback-view-container">
      {/* True Single-Row Top Bar: Filters on Left, Storage Telemetry + Actions on Right */}
      <section className="panel-card playback-header-card" aria-label="录像检索与存储概览">
        <div className="playback-header-filters">
          <div className="playback-filters-left">
            <div className="morph-select-inline">
              <MorphSelect
                label="机位"
                hideLabel
                className="morph-select--compact"
                items={cameraSelectItems}
                value={selectedCameraId || "all"}
                onValueChange={(val) => onSelectCameraId(val === "all" ? "" : val)}
                searchPlaceholder="搜索设备名称或 IP…"
                panelWidth={264}
                maxListHeight={280}
              />
            </div>

            <div className="playback-date-group">
              <div className="date-picker-wrap date-picker-wrap--compact">
                <DatePicker
                  label="日期"
                  locale="zh-CN"
                  showToday
                  showMarkerLegend
                  maxDate={maxSelectableDate}
                  disabledDates={isCalendarDateDisabled}
                  dayMarkers={calendarDayMarkers}
                  value={parseYMD(selectedDate)}
                  onChange={(date) => {
                    onSelectDate(date ? toYMD(date) : "");
                  }}
                  placeholder="日历选日"
                />
              </div>

              {availableDates.length > 0 && (
                <div className="date-pills" role="group" aria-label="快捷日期筛选">
                  <button
                    type="button"
                    className={`date-pill ${selectedDate === "" ? "date-pill--active" : ""}`}
                    onClick={() => onSelectDate("")}
                  >
                    全部
                  </button>
                  {availableDates.slice(0, 4).map((d) => (
                    <button
                      key={d}
                      type="button"
                      title={calendarDayMarkers[d]?.tooltip || d}
                      className={`date-pill ${selectedDate === d ? "date-pill--active" : ""}`}
                      onClick={() => onSelectDate(d)}
                    >
                      {d.slice(5)}
                    </button>
                  ))}
                </div>
              )}
            </div>

            <div className="segmented-field segmented-field--inline">
              <SegmentedControl
                label="录像状态与类型筛选"
                value={modeFilter}
                onValueChange={setModeFilter}
                options={[
                  { value: "all", label: "全部" },
                  { value: "completed", label: "已完结" },
                  { value: "recording", label: "录制中" },
                  { value: "manual", label: "手动" },
                ]}
              />
            </div>
          </div>

          <div className="playback-header-right">
            <div className="playback-kpi-strip" aria-label="存储与录制状态">
              <div
                className="playback-stat-chip"
                title={`已归档 ${payload?.segment_count ?? 0} 段 · 本地目录: ${
                  payload?.config.storage_dir || "./recordings"
                } (单段 ${payload?.config.segment_minutes ?? 15} 分钟 · 满额自动循环覆盖)`}
              >
                <HardDrive size={13} className="playback-kpi-pill__icon" />
                <strong>
                  {storageUsedStr} / {maxStorageGB} GB
                </strong>
                <div className="storage-quota-bar">
                  <Progress
                    value={usedStorageGB}
                    max={maxStorageGB}
                    aria-label="录像存储空间使用率"
                  />
                </div>
                <span className="playback-stat-chip__sub">
                  (
                  <span className="inline-counter">
                    <AnimatedCounter value={payload?.segment_count ?? 0} />
                  </span>
                  段)
                </span>
              </div>
              <span
                className="playback-stat-chip"
                title={`当前正在后台录制 ${activeRecordingCount} 路 1080P+音频 MP4`}
              >
                <Radio
                  size={13}
                  className={`playback-kpi-pill__icon ${
                    activeRecordingCount > 0 ? "playback-kpi-pill__icon--recording" : ""
                  }`}
                />
                <strong>
                  <span className="inline-counter">
                    <AnimatedCounter value={activeRecordingCount} />
                  </span>{" "}
                  路录制中
                </strong>
              </span>
            </div>

            <div className="toolbar-btn-row">
              <Button variant="secondary" size="sm" loading={refreshing} onClick={handleRefresh}>
                <RefreshCw size={13} />
                <span>刷新</span>
              </Button>
              <Button variant="secondary" size="sm" onClick={() => void onOpenFolder()}>
                <FolderOpen size={13} />
                <span>录像目录</span>
              </Button>
              <Button variant="secondary" size="sm" onClick={onOpenSettingsModal}>
                <Clock size={13} />
                <span>策略设置</span>
              </Button>
            </div>
          </div>
        </div>
      </section>

      {exportNotice && (
        <Alert
          open={Boolean(exportNotice)}
          tone={exportNotice.tone}
          title={exportNotice.title}
          onDismiss={() => setExportNotice(null)}
        >
          {exportNotice.savedPath ? (
            <span className="export-alert-actions">
              <button
                type="button"
                className="table-action-btn"
                onClick={() => void handleRevealExportedFile(exportNotice.savedPath!)}
              >
                <FolderOpen size={12} />
                <span>在文件夹中显示</span>
              </button>
            </span>
          ) : undefined}
        </Alert>
      )}

      {/* Main Playback Split: Left (1080P Player + Smart Timeline + Controls) | Right (Full-Height Scrollable Paged Table) */}
      {segments.length === 0 && !keyword && modeFilter === "all" ? (
        <div className="panel-card">
          <EmptyState
            icon={<Video width={24} height={24} strokeWidth={1.6} />}
            title="暂无录像分片"
            action={
              <Button variant="primary" size="md" onClick={handleRefresh}>
                刷新录像列表
              </Button>
            }
          />
        </div>
      ) : (
        <div className="playback-workbench">
          {/* Left: 1080P + Audio HTML5 Video Player + Integrated Smart Timeline */}
          <section className="panel-card player-card">
            <div className="player-card__header">
              <div className="player-card__title-group">
                <h3>
                  {activeSegment
                    ? `${activeSegment.camera_name} · ${activeSegment.date} ${activeSegment.start_time} - ${activeSegment.end_time}`
                    : "未选择录像分片"}
                </h3>
              </div>
              {activeSegment && (
                <div className="player-card__badges">
                  <Badge tone="neutral" size="sm">
                    {formatDuration(activeSegment.duration_sec)} · {formatBytes(activeSegment.size_bytes)}
                  </Badge>
                  {activeSegment.active && (
                    <Badge tone="warning" size="sm">
                      实时写入中
                    </Badge>
                  )}
                  <Badge tone={activeSegment.mode === "manual" ? "danger" : "success"} size="sm">
                    {activeSegment.mode === "manual" ? "手动录制" : "自动录像"}
                  </Badge>
                </div>
              )}
            </div>

            <div className="player-stage">
              {activeSegment ? (
                <video
                  ref={videoRef}
                  key={activeSegment.rel_path}
                  className="player-video"
                  src={`/api/recordings/file?path=${encodeURIComponent(activeSegment.rel_path)}`}
                  controls
                  autoPlay
                  playsInline
                />
              ) : (
                <div className="player-empty">请选择一个录像分片</div>
              )}
            </div>

            {/* Integrated Timeline scoped to current camera & date with Auto-Zoom */}
            <div className="player-timeline-box" aria-label="当前机位录像时间轴">
              <div className="timeline-header">
                <div className="timeline-header__title">
                  <Film size={13} />
                  <span className="timeline-title-text">
                    {timelineCameraName} · {timelineDate || "今日"} (
                    {timelineSegments.length} 段)
                  </span>
                </div>
                <div className="timeline-legend">
                  <div className="timeline-scale-switch" role="group" aria-label="时间轴缩放模式">
                    <button
                      type="button"
                      className={`timeline-scale-btn ${
                        timelineScale === "auto" ? "timeline-scale-btn--active" : ""
                      }`}
                      onClick={() => setTimelineScale("auto")}
                    >
                      自适应区间
                    </button>
                    <button
                      type="button"
                      className={`timeline-scale-btn ${
                        timelineScale === "24h" ? "timeline-scale-btn--active" : ""
                      }`}
                      onClick={() => setTimelineScale("24h")}
                    >
                      24h全天
                    </button>
                  </div>
                  <span className="legend-item">
                    <i className="legend-dot legend-dot--manual" />
                    手动
                  </span>
                  <span className="legend-item">
                    <i className="legend-dot legend-dot--auto" />
                    自动
                  </span>
                </div>
              </div>

              <div className="timeline-track-wrap">
                <div className="timeline-track">
                  {timelineBounds.ticks.map((tickSec, idx) => {
                    const pct =
                      ((tickSec - timelineBounds.startSec) / timelineBounds.spanSec) * 100;
                    return (
                      <div
                        key={idx}
                        className="timeline-tick"
                        style={{ left: `${pct}%` }}
                      >
                        <span>{formatSecToHM(tickSec)}</span>
                      </div>
                    );
                  })}

                  {timelineSegments.map((seg) => {
                    const rawLeft =
                      ((seg.start_sec - timelineBounds.startSec) / timelineBounds.spanSec) * 100;
                    const leftPct = Math.max(0, Math.min(99.2, rawLeft));
                    const rawWidth =
                      (Math.max(seg.duration_sec, 45) / timelineBounds.spanSec) * 100;
                    const widthPct = Math.max(1.1, Math.min(100 - leftPct, rawWidth));
                    const isSelected = activeSegment?.rel_path === seg.rel_path;
                    return (
                      <button
                        key={seg.rel_path}
                        type="button"
                        className={`timeline-segment timeline-segment--${seg.mode} ${
                          isSelected ? "timeline-segment--active" : ""
                        }`}
                        style={{ left: `${leftPct}%`, width: `${widthPct}%` }}
                        onClick={() => selectSegmentAndSyncPage(seg)}
                        aria-label={`定位播放 ${seg.camera_name} ${seg.date} ${seg.start_time} 至 ${seg.end_time}`}
                        title={`${seg.camera_name} | ${seg.date} ${seg.start_time} - ${seg.end_time} (${formatDuration(
                          seg.duration_sec
                        )})`}
                      />
                    );
                  })}
                </div>
              </div>
            </div>

            <div className="player-controls-bar">
              <div className="player-nav-group">
                <button
                  type="button"
                  className="table-action-btn"
                  disabled={activeIndex <= 0}
                  onClick={() => {
                    if (activeIndex > 0) {
                      selectSegmentAndSyncPage(sortedRows[activeIndex - 1]);
                    }
                  }}
                >
                  <ChevronLeft size={14} />
                  <span>上一段</span>
                </button>
                <span className="player-nav-pos mono-text">
                  {activeIndex >= 0 ? `${activeIndex + 1} / ${sortedRows.length}` : `0 / 0`}
                </span>
                <button
                  type="button"
                  className="table-action-btn"
                  disabled={activeIndex < 0 || activeIndex >= sortedRows.length - 1}
                  onClick={() => {
                    if (activeIndex >= 0 && activeIndex < sortedRows.length - 1) {
                      selectSegmentAndSyncPage(sortedRows[activeIndex + 1]);
                    }
                  }}
                >
                  <span>下一段</span>
                  <ChevronRight size={14} />
                </button>
              </div>

              <div className="speed-control">
                <SegmentedControl
                  label="回放倍速"
                  value={playbackRate}
                  onValueChange={setPlaybackRate}
                  options={[
                    { value: "0.5", label: "0.5x" },
                    { value: "1", label: "1.0x" },
                    { value: "1.5", label: "1.5x" },
                    { value: "2", label: "2.0x" },
                    { value: "4", label: "4.0x" },
                    { value: "8", label: "8.0x" },
                  ]}
                />
              </div>

              {activeSegment && (
                <button
                  type="button"
                  disabled={Boolean(exportingRelPath)}
                  onClick={() => void handleExportSegment(activeSegment)}
                  className="export-mp4-link"
                  title="选择文件夹并导出当前 1080P MP4 录像分片"
                >
                  <Download size={14} />
                  <span>
                    {exportingRelPath === activeSegment.rel_path ? "正在导出…" : "导出 MP4"}
                  </span>
                </button>
              )}
            </div>
          </section>

          {/* Right: Full-Height Scrollable Arc SortableDataTable + Arc Pagination Footer */}
          <section className="panel-card segments-table-card">
            <div className="segments-table-card__header">
              <div className="segments-table-card__title">
                <h3>
                  录像分片明细 ({sortedRows.length}
                  {payload && payload.segment_count > sortedRows.length
                    ? ` / ${payload.segment_count}`
                    : ""}
                  )
                </h3>
              </div>

              <div className="wall-search-box">
                <SearchField
                  label="搜索录像分片"
                  value={keyword}
                  onValueChange={setKeyword}
                  placeholder="搜索设备或时间…"
                />
              </div>
            </div>

            <ScrollArea
              viewportRef={tableScrollWrapRef}
              className="segments-table-scroll"
              fade={0}
              label="1080P 录像分片列表"
              onClick={(e) => {
                const target = e.target as HTMLElement;
                if (target.closest("a, button, th")) return;
                const rowEl = target.closest("tr");
                rowEl?.querySelector<HTMLButtonElement>("[data-play-trigger]")?.click();
              }}
            >
              <SortableDataTable
                rows={pagedRows}
                columns={columns}
                rowKey="rel_path"
                caption="1080P 录像分片列表"
                emptyMessage="暂无符合条件的录像文件"
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
                  第 {rangeStart}–{rangeEnd} 条 / 共 {sortedRows.length} 条
                </span>
                <SegmentedControl
                  label="每页显示录像分片条数"
                  value={String(pageSize)}
                  onValueChange={(v) => setPageSize(Number(v) || 50)}
                  options={[
                    { value: "30", label: "30条/页" },
                    { value: "50", label: "50条/页" },
                    { value: "100", label: "100条/页" },
                  ]}
                />
              </div>

              <Pagination
                page={safePage}
                pageCount={pageCount}
                onPageChange={setPage}
                label="录像分片分页导航"
              />
            </div>
          </section>
        </div>
      )}

      {/* @uiarc/dialog Delete Segment Confirmation Modal */}
      <Dialog
        open={Boolean(pendingDeleteSeg)}
        onOpenChange={(open) => {
          if (!open && !deletingSeg) setPendingDeleteSeg(null);
        }}
      >
        {pendingDeleteSeg && (
          <DialogContent
            title="删除录像分片"
            description={`确定删除「${pendingDeleteSeg.camera_name}」(${pendingDeleteSeg.date} ${pendingDeleteSeg.start_time} - ${pendingDeleteSeg.end_time}) 吗？`}
          >
            <div className="manual-cam-actions">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={deletingSeg}
                onClick={() => setPendingDeleteSeg(null)}
              >
                取消
              </Button>
              <Button
                type="button"
                variant="danger"
                size="sm"
                loading={deletingSeg}
                onClick={async () => {
                  setDeletingSeg(true);
                  try {
                    await onDeleteSegment(pendingDeleteSeg.rel_path);
                    setPendingDeleteSeg(null);
                  } finally {
                    setDeletingSeg(false);
                  }
                }}
              >
                <Trash2 size={14} />
                <span>确认永久删除</span>
              </Button>
            </div>
          </DialogContent>
        )}
      </Dialog>
    </div>
  );
}
