import { apiClient } from '../client'

export interface BackupS3Config {
  endpoint: string
  region: string
  bucket: string
  access_key_id: string
  secret_access_key?: string
  prefix: string
  force_path_style: boolean
  upload_mode: 'multipart' | 'spooled_put'
}

export type BackupStorageType = 'local' | 's3'

export interface BackupStorageConfig {
  type: BackupStorageType
  local_path: string
  s3: BackupS3Config
}

export interface BackupContentConfig {
  include_usage_records: boolean
	include_ops_logs: boolean
	include_audit_logs: boolean
	include_runtime_data: boolean
	excluded_table_data?: string[]
}

export interface BackupScheduleConfig {
  enabled: boolean
  cron_expr: string
  retain_days: number
  retain_count: number
  monthly_archive?: BackupMonthlyArchiveConfig
}

export interface BackupMonthlyArchiveConfig {
  enabled: boolean
  days: number[]
  include_month_end: boolean
  retain_count: number // 0 为永久保留
}

export interface BackupMonthlyArchive {
  dates: string[]
  retain_count: number
}

export interface BackupRecord {
  id: string
  status: 'pending' | 'running' | 'completed' | 'failed'
  backup_type: string
  storage_type?: BackupStorageType
  storage_key?: string
  file_name: string
  s3_key: string
  parts?: BackupPart[]
  size_bytes: number
  triggered_by: string
  error_message?: string
  started_at: string
  finished_at?: string
  expires_at?: string
  progress?: string
  restore_status?: string
  restore_started_at?: string
  restore_error?: string
  restored_at?: string
  monthly_archive?: BackupMonthlyArchive
}

export interface BackupPart {
  index: number
  storage_key?: string
  s3_key?: string
  size_bytes: number
  sha256?: string
}

export interface BackupDownloadPart {
  index: number
  size_bytes: number
  url: string
}

export interface BackupDownloadResponse {
  url?: string
  parts?: BackupDownloadPart[]
}

export interface CreateBackupRequest {
  expire_days?: number
}

export interface TestS3Response {
  ok: boolean
  message: string
}

// 异步图片可复用备份 S3 凭据，独立保存图片桶、路径和链接有效期。
export interface ImageStorageConfig {
  enabled: boolean
  reuse_backup_s3: boolean
  bucket: string
  prefix: string
  public_base_url: string
  presign_expiry_hours: number
  max_download_bytes: number
  endpoint: string
  region: string
  access_key_id: string
  secret_access_key?: string
  force_path_style: boolean
}

export interface ImageStorageConfigResponse {
  config: ImageStorageConfig
  secret_configured: boolean
}

export async function getImageStorageConfig(): Promise<ImageStorageConfigResponse> {
  const { data } = await apiClient.get<ImageStorageConfigResponse>('/admin/backups/image-storage')
  return data
}

export async function updateImageStorageConfig(config: ImageStorageConfig): Promise<ImageStorageConfig> {
  const { data } = await apiClient.put<ImageStorageConfig>('/admin/backups/image-storage', config)
  return data
}

export async function testImageStorageConnection(config: ImageStorageConfig): Promise<TestS3Response> {
  const { data } = await apiClient.post<TestS3Response>('/admin/backups/image-storage/test', config)
  return data
}

// 存储配置
export async function getStorageConfig(): Promise<BackupStorageConfig> {
  const { data } = await apiClient.get<BackupStorageConfig>('/admin/backups/storage-config')
  return data
}

export async function updateStorageConfig(config: BackupStorageConfig): Promise<BackupStorageConfig> {
  const { data } = await apiClient.put<BackupStorageConfig>('/admin/backups/storage-config', config)
  return data
}

export async function testStorageConnection(config: BackupStorageConfig): Promise<TestS3Response> {
  const { data } = await apiClient.post<TestS3Response>('/admin/backups/storage-config/test', config)
  return data
}

// 内容配置
export async function getContentConfig(): Promise<BackupContentConfig> {
  const { data } = await apiClient.get<BackupContentConfig>('/admin/backups/content-config')
  return data
}

export async function updateContentConfig(config: BackupContentConfig): Promise<BackupContentConfig> {
  const { data } = await apiClient.put<BackupContentConfig>('/admin/backups/content-config', config)
  return data
}

// S3 配置
export async function getS3Config(): Promise<BackupS3Config> {
  const { data } = await apiClient.get<BackupS3Config>('/admin/backups/s3-config')
  return data
}

export async function updateS3Config(config: BackupS3Config): Promise<BackupS3Config> {
  const { data } = await apiClient.put<BackupS3Config>('/admin/backups/s3-config', config)
  return data
}

export async function testS3Connection(config: BackupS3Config): Promise<TestS3Response> {
  const { data } = await apiClient.post<TestS3Response>('/admin/backups/s3-config/test', config)
  return data
}

// 定时备份
export async function getSchedule(): Promise<BackupScheduleConfig> {
  const { data } = await apiClient.get<BackupScheduleConfig>('/admin/backups/schedule')
  return data
}

export async function updateSchedule(config: BackupScheduleConfig): Promise<BackupScheduleConfig> {
  const { data } = await apiClient.put<BackupScheduleConfig>('/admin/backups/schedule', config)
  return data
}

// 备份操作
export async function createBackup(req?: CreateBackupRequest): Promise<BackupRecord> {
  const { data } = await apiClient.post<BackupRecord>('/admin/backups', req || {})
  return data
}

export async function listBackups(): Promise<{ items: BackupRecord[] }> {
  const { data } = await apiClient.get<{ items: BackupRecord[] }>('/admin/backups')
  return data
}

export async function getBackup(id: string): Promise<BackupRecord> {
  const { data } = await apiClient.get<BackupRecord>(`/admin/backups/${id}`)
  return data
}

export async function deleteBackup(id: string, deleteArchived = false): Promise<void> {
  await apiClient.delete(`/admin/backups/${id}`, deleteArchived ? { params: { delete_archived: true } } : undefined)
}

export async function getDownloadURL(id: string): Promise<BackupDownloadResponse> {
  const { data } = await apiClient.get<BackupDownloadResponse>(`/admin/backups/${id}/download-url`)
  return data
}

export async function downloadBackupFile(id: string): Promise<Blob> {
  const { data } = await apiClient.get<BlobPart>(`/admin/backups/${id}/download`, {
    responseType: 'blob',
  })
  return data instanceof Blob ? data : new Blob([data], { type: 'application/gzip' })
}

// 恢复操作
export async function restoreBackup(id: string, password: string): Promise<BackupRecord> {
  const { data } = await apiClient.post<BackupRecord>(`/admin/backups/${id}/restore`, { password })
  return data
}

export const backupAPI = {
  getImageStorageConfig,
  updateImageStorageConfig,
  testImageStorageConnection,
  getStorageConfig,
  updateStorageConfig,
  testStorageConnection,
  getContentConfig,
  updateContentConfig,
  getS3Config,
  updateS3Config,
  testS3Connection,
  getSchedule,
  updateSchedule,
  createBackup,
  listBackups,
  getBackup,
  deleteBackup,
  getDownloadURL,
  downloadBackupFile,
  restoreBackup,
}

export default backupAPI
