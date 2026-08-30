export interface AppInfo {
  name: string
  version: string
  official_site_url: string
}

export interface ConnectionInput {
  site_url: string
  gateway_url?: string
  api_key: string
  label?: string
}

export interface ConnectionSummary {
  configured: boolean
  auth_mode?: string
  site_url: string
  gateway_url: string
  label: string
  api_key_configured: boolean
  api_key_hint: string
  api_key_id?: number
  codex_api_key_id?: number
  claude_api_key_id?: number
  session_configured: boolean
  device_id?: string
  protection_level?: string
  scope?: string
  updated_at?: string
}

export interface ToolConfigInput {
  tool: 'codex' | 'claude'
  base_url?: string
  model?: string
}

export interface ToolConfigFile {
  path: string
  backup_path?: string
  changed: boolean
  contains_secret: boolean
}

export interface ToolLaunchPlan {
  tool: string
  environment_variable: string
  command: string
  shell: string
  note?: string
}

export interface ToolLaunchResult {
  tool: string
  executable: string
  pid: number
  environment_variable: string
  started_at: string
  message?: string
}

export interface ToolConfigResult {
  tool: string
  files: ToolConfigFile[]
  warnings?: string[]
  launch?: ToolLaunchPlan
  completed_at: string
}

export interface ToolConfigRestoreInput {
  tool: 'codex' | 'claude'
  backup_path: string
}

export interface ToolConfigRestoreResult {
  tool: string
  target_path: string
  previous_backup_path?: string
}

export interface DeviceAuthorizationView {
  request_id: string
  user_code: string
  verification_url: string
  verification_url_complete: string
  expires_in: number
  interval: number
  scope?: string
  audience?: string
}

export interface DeviceAuthorizationInput {
  device_name?: string
  scopes?: string[]
}

export interface DeviceAuthorizationStatus {
  request_id: string
  status: 'pending' | 'authorized' | 'denied' | 'expired' | 'error' | string
  message?: string
  expires_in?: number
  device_id?: string
  device_name?: string
}

export interface ProbeResult {
  reachable: boolean
  site_name?: string
  gateway_url?: string
  api_base_url?: string
  checked_at: string
  message?: string
}

export interface UsageSummary {
  mode: string
  status?: string
  plan_name?: string
  remaining: number
  balance: number
  unit: string
  valid: boolean
  stats_available?: boolean
  total_requests?: number
  total_tokens?: number
  total_cost?: number
  total_actual_cost?: number
  today_requests?: number
  today_tokens?: number
  today_cost?: number
  today_actual_cost?: number
}

// UsageOverview keeps account-level and selected-key figures separate.  The
// native binding may omit one of the snapshots when the corresponding
// credential/scope is unavailable, so both branches are nullable.
export interface UsageSnapshot extends UsageSummary {
  available?: boolean
  usage?: unknown
  quota?: unknown
  [key: string]: unknown
}

export interface UsageOverview {
  account?: UsageSnapshot | null
  selected_key?: UsageSnapshot | null
  account_ready?: boolean
  selected_key_ready?: boolean
  available?: boolean
  as_of?: string
}

export interface DeviceFlowCapabilities {
  grant_type: string
  expires_in_seconds: number
  poll_interval_seconds: number
  pkce_methods: string[]
  public_key_binding: string
  token_type: string
  dpop_algorithms: string[]
  public_key_curves: string[]
  proof_header: string
  nonce_required: boolean
  access_token_hash: string
}

export interface ClientCapabilities {
  protocol_version: string
  server_version?: string
  client_id: string
  audience: string
  api_base_url?: string
  scopes: string[]
  default_scopes?: string[]
  high_risk_scopes?: string[]
  features: Record<string, boolean>
  availability?: Record<string, string>
  backend_mode_enabled?: boolean
  async_images?: { enabled: boolean; pollable: boolean; reason?: string }
  endpoints: Record<string, string>
  device_flow: DeviceFlowCapabilities
}

export interface IntegrationProfileKey {
  id: number
  name: string
  status: string
  expires_at?: string
  available: boolean
}

export interface IntegrationProfile {
  id: string
  client_id?: string
  audience?: string
  auth: string
  base_path: string
  grant_type?: string
  refresh_grant_type?: string
  api_key_id?: number
  available: boolean
  async_capability?: string
  endpoints?: string[]
  configuration?: string[]
}

export interface IntegrationProfileResponse {
  key_specific: boolean
  api_key: IntegrationProfileKey
  profiles: IntegrationProfile[]
}

export interface ImageCapabilities {
  protocol_version: string
  endpoint: string
  requires_api_key: boolean
  operations: string[]
  models: Array<{ id: string; operations: string[]; enabled: boolean }>
  defaults: { model: string; n: number; size: string; quality: string; output_format: string; background: string; poll_after_seconds: number }
  limits: Record<string, number>
  security: Record<string, boolean | string[]>
  async?: { enabled: boolean; pollable: boolean; reason?: string }
  backend_mode_enabled?: boolean
  server_time: string
}

export interface APIKeySummary {
  id: number
  name: string
  status: string
  key_hint: string
  quota: number
  quota_used: number
  expires_at?: string
  current_concurrency: number
  usage_5h: number
  usage_1d: number
  usage_7d: number
}

export interface APIKeySelectionResult {
  selected: APIKeySummary
  connection: ConnectionSummary
}

export interface DeviceSummary {
  device_id: string
  client_id: string
  device_name: string
  scopes: string[]
  audience: string
  protection_level: string
  created_at: string
  last_seen_at: string
  revoked_at?: string
}

export interface CheckoutSessionInput {
  amount: number
  payment_type: string
  order_type?: string
  plan_id?: number
  upgrade_from_subscription_id?: number
}

export interface CheckoutSession {
  session_id: string
  status: string
  order_id?: number
  payment_type?: string
  order_type?: string
  plan_id?: number
  upgrade_from_subscription_id?: number
  result_type?: string
  amount?: number
  pay_amount?: number
  currency?: string
  browser_url?: string
  expires_at: string
  created_at: string
  poll_after_seconds: number
  status_url?: string
}

export interface ImageHistoryQueryInput {
  cursor?: string
  status?: string
  limit?: number
}

export interface ImageHistoryItem {
  id: string
  task_id: string
  object: string
  status: string
  http_status?: number
  platform?: string
  operation?: string
  model?: string
  image_count?: number
  result_count?: number
  result_urls?: string[]
  result?: unknown
  created_at: number
  completed_at?: number
  expires_at: number
  assets_available: boolean
  assets_expired?: boolean
  error?: unknown
}

export interface ImageHistoryPage {
  items: ImageHistoryItem[]
  next_cursor?: string
  has_more: boolean
  server_time: number
}

export interface ImageHistoryAsset {
  task_id: string
  asset_index: number
  url: string
  expires_at: number
}

export interface ImageTaskView {
  id: string
  task_id: string
  status: string
  poll_url?: string
  expires_at?: string
  assets?: Array<{ url: string; revised_prompt?: string }>
  error?: { code?: string; message?: string }
}

export interface ImageTaskSummary {
  id: string
  task_id: string
  api_key_id?: number
  status: string
  prompt?: string
  model?: string
  created_at?: string
  updated_at?: string
}

export interface ImageGenerateInput {
  model?: string
  prompt: string
  n?: number
  size?: string
  quality?: string
  background?: string
  output_format?: string
  output_compression?: number
}

export interface ImageEditUpload {
  name?: string
  content_type?: string
  data_url?: string
  file_handle?: string
  bytes?: number
}

export interface ImageFileHandle {
  id: string
  name: string
  content_type: string
  bytes: number
  expires_at: string
}

export interface ImageEditInput {
  model?: string
  prompt: string
  n?: number
  size?: string
  quality?: string
  background?: string
  output_format?: string
  output_compression?: number
  input_fidelity?: string
  images: ImageEditUpload[]
  mask?: ImageEditUpload
}

// Local filesystem paths are deliberately omitted from the Wails Asset
// binding. The Go side keeps them private and accepts only opaque IDs for
// subsequent open/delete operations.
export interface LocalImageAssetSummary {
  id: string
  name: string
  mime_type: string
  bytes: number
  sha256?: string
  created_at: string
}

interface NativeApp {
  GetAppInfo(): Promise<AppInfo>
  GetCapabilities(): Promise<ClientCapabilities>
  GetIntegrationProfiles(apiKeyID: number): Promise<IntegrationProfileResponse>
  GetImageCapabilities(): Promise<ImageCapabilities>
  SaveConnection(input: ConnectionInput): Promise<ConnectionSummary>
  GetConnection(): Promise<ConnectionSummary>
  IntegrateToolConfig(input: ToolConfigInput): Promise<ToolConfigResult>
  LaunchTool(tool: 'codex' | 'claude'): Promise<ToolLaunchResult>
  GetToolConfigPaths(tool: string): Promise<Record<string, string>>
  RestoreToolConfig(input: ToolConfigRestoreInput): Promise<ToolConfigRestoreResult>
  ClearConnection(): Promise<void>
  BeginDeviceAuthorization(input: DeviceAuthorizationInput): Promise<DeviceAuthorizationView>
  OpenDeviceVerification(requestID: string): Promise<void>
  PollDeviceAuthorization(requestID: string): Promise<DeviceAuthorizationStatus>
  LogoutDevice(): Promise<void>
  ProbeConnection(): Promise<ProbeResult>
  GetUsage(): Promise<UsageSummary>
  GetUsageOverview(): Promise<UsageOverview>
  ListAPIKeys(): Promise<APIKeySummary[]>
  SelectAPIKey(id: number): Promise<APIKeySelectionResult>
  SelectAPIKeyForPurpose(purpose: 'images' | 'codex' | 'claude', id: number): Promise<APIKeySelectionResult>
  OpenKeysPage(): Promise<void>
  ListDevices(): Promise<DeviceSummary[]>
  RevokeDevice(deviceID: string): Promise<void>
  CreateCheckoutSession(input: CheckoutSessionInput): Promise<CheckoutSession>
  GetCheckoutSession(sessionID: string): Promise<CheckoutSession>
  OpenCheckout(sessionID: string): Promise<void>
  Checkin(): Promise<{ reward_amount: number; balance: number; message?: string }>
  GenerateImage(input: ImageGenerateInput): Promise<ImageTaskView>
  PickImageFiles(multiple: boolean): Promise<ImageFileHandle[]>
  EditImage(input: ImageEditInput): Promise<ImageTaskView>
  GetImageTask(taskID: string): Promise<ImageTaskView>
  MarkImageTaskAssetsDownloaded(taskID: string): Promise<void>
  ListImageTasks(): Promise<ImageTaskSummary[]>
  ListImageHistory(input: ImageHistoryQueryInput): Promise<ImageHistoryPage>
  GetImageHistory(taskID: string): Promise<ImageHistoryItem>
  GetImageHistoryAsset(taskID: string, index: number): Promise<ImageHistoryAsset>
  DeleteImageHistory(taskID: string): Promise<void>
  DownloadImage(sourceURL: string, name?: string): Promise<LocalImageAssetSummary>
  ImageLibrary(): Promise<Array<LocalImageAssetSummary>>
  DeleteImage(id: string): Promise<void>
}

declare global {
  interface Window {
    go?: {
      main?: {
        App?: NativeApp
      }
    }
  }
}

function bridge(): NativeApp {
  const app = window.go?.main?.App
  if (!app) {
    throw new Error('请在神奇AI助手桌面应用中运行此操作')
  }
  return app
}

export async function nativeCall<T>(operation: (app: NativeApp) => Promise<T>): Promise<T> {
  return operation(bridge())
}

export function nativeAvailable(): boolean {
  return Boolean(window.go?.main?.App)
}
