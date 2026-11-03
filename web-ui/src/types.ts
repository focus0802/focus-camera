export interface PTZPreset {
  id: string;
  name: string;
  hw_token?: string;
  zoom?: number;
  pan_x?: number;
  pan_y?: number;
  hardware?: boolean;
}

export interface DigitalViewState {
  zoom: number;
  panX: number;
  panY: number;
}

export type PTZDirection =
  | "up"
  | "down"
  | "left"
  | "right"
  | "up_left"
  | "up_right"
  | "down_left"
  | "down_right";

export interface Camera {
  id: string;
  name: string;
  ip: string;
  group: string;
  username?: string;
  password?: string;
  no_password?: boolean;
  vendor?: string;
  main_stream_path?: string;
  sub_stream_path?: string;
  model?: string;
  flip_type?: string;
  ptz_invert_x?: boolean;
  ptz_invert_y?: boolean;
  rtsp_port?: number;
  talk_port?: number;
  supports_talk?: boolean;
  supports_ptz?: boolean;
  ptz_presets?: PTZPreset[];
  sub_stream?: boolean;
  auto_record?: boolean;
  sort_order?: number;
}

export interface CameraNetworkConfig {
  proto: "static" | "dhcp";
  ipaddr: string;
  netmask: string;
  gateway: string;
  mac?: string;
  dns?: string;
}

export type OSDPositionPreset =
  | "keep"
  | "top_left"
  | "bottom_left"
  | "top_right"
  | "bottom_right"
  | "custom";

export interface CameraOSDConfig {
  label_enabled: boolean;
  label_text: string;
  label_x?: string;
  label_y?: string;
  label2_enabled: boolean;
  label2_text: string;
  label2_x?: string;
  label2_y?: string;
  date_enabled: boolean;
  date_x?: string;
  date_y?: string;
  week_enabled: boolean;
  position_preset?: OSDPositionPreset;
  dev_alias?: string;
}

export interface CameraTimeStatus {
  camera_time: string;
  camera_unix: number;
  host_time: string;
  host_unix: number;
  drift_seconds: number;
  timezone: string;
  timing_mode: "ntp" | "manual" | string;
  ntp_server?: string;
}

export interface BatchItemResult {
  id: string;
  name: string;
  ip: string;
  ok: boolean;
  detail?: string;
  error?: string;
}

export interface BatchMaintResult {
  action: "sync_time" | "sync_osd_names" | string;
  total: number;
  succeeded: number;
  failed: number;
  items: BatchItemResult[];
}

export function cameraSupportsTalk(camera?: Camera | null): boolean {
  if (!camera) return false;
  if (camera.supports_talk === true || camera.talk_port === 8800) {
    return true;
  }
  if (typeof camera.supports_talk === "boolean") {
    return camera.supports_talk;
  }
  const v = (camera.vendor || "").toLowerCase();
  return v === "" || v === "tplink" || v === "mercury";
}

export interface DiscoveredCamera {
  ip: string;
  reachable: boolean;
  rtsp_open: boolean;
  talk_open: boolean;
  http_open: boolean;
  vendor?: string;
  main_stream_path?: string;
  sub_stream_path?: string;
  supports_talk?: boolean;
  supports_ptz?: boolean;
  auto_record?: boolean;
  device_model: string;
  hardware_ver: string;
  firmware_ver: string;
  mac: string;
  osd_label: string;
  suggested_name: string;
  suggested_group: string;
  auth_ok: boolean;
  password?: string;
  no_password?: boolean;
  error?: string;
  status: "new" | "existing" | "ip_shifted";
  existing_id?: string;
}

export interface RecordingConfig {
  storage_dir: string;
  segment_minutes: number;
  retention_days: number;
  max_storage_gb: number;
  schedule_enabled: boolean;
  schedule_start_time: string;
  schedule_end_time: string;
}

export interface RecordingState {
  camera_id: string;
  camera_name: string;
  active: boolean;
  mode: "manual" | "auto" | "";
  auto_enabled: boolean;
  current_file?: string;
  started_at?: string;
  duration_sec?: number;
  bytes_written?: number;
  last_error?: string;
}

export interface SegmentFile {
  id: string;
  camera_id: string;
  camera_name: string;
  date: string;
  start_time: string;
  end_time: string;
  start_sec: number;
  duration_sec: number;
  size_bytes: number;
  mode: "manual" | "auto";
  rel_path: string;
  active?: boolean;
}

export interface DateSummary {
  date: string;
  segment_count: number;
  auto_count: number;
  manual_count: number;
  active: boolean;
  total_bytes: number;
}

export interface RecordingsPayload {
  config: RecordingConfig;
  states: RecordingState[];
  dates: string[];
  date_summaries?: Record<string, DateSummary>;
  segments: SegmentFile[];
  total_bytes: number;
  segment_count: number;
}

export interface UpdateStatus {
  current_version: string;
  os_arch: string;
  executable_path: string;
  update_url: string;
  state:
    | "idle"
    | "checking"
    | "available"
    | "up_to_date"
    | "downloading"
    | "verifying"
    | "applying"
    | "restarting"
    | "error";
  has_update: boolean;
  latest_version?: string;
  published_at?: string;
  release_notes?: string;
  download_url?: string;
  expected_sha256?: string;
  downloaded_bytes: number;
  total_bytes: number;
  progress_pct: number;
  last_checked_at?: string;
  message?: string;
  error?: string;
}

export type AutoStartMode = "window" | "tray";

export interface AutoStartStatus {
  supported: boolean;
  enabled: boolean;
  os_registered: boolean;
  mode: AutoStartMode;
  executable_path: string;
  registered_cmd?: string;
  is_network_path?: boolean;
  warning?: string;
}

export type SystemSettingsTab = "general" | "recording" | "update";
