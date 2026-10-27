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

export interface RecordingsPayload {
  config: RecordingConfig;
  states: RecordingState[];
  dates: string[];
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
