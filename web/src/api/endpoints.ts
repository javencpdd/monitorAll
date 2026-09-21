/**
 * REST 端点集合（契约来源：docs/ARCHITECTURE.md §7.3 接口表）。
 * 路径、请求体字段名与后端逐字对齐；此文件不做任何业务判断，只负责发请求。
 */
import { API_PREFIX, SAMPLE_TIMEOUT_MS, WS_PATH } from '@/utils/constants'
import { download, request } from './client'
import type {
  Card,
  Channel,
  ChannelRuntime,
  CreateCardReq,
  CreateChannelReq,
  CreateDashboardReq,
  CreateDataSourceReq,
  Dashboard,
  DashboardWithCards,
  DataSource,
  DeletedResp,
  Frame,
  HealthResp,
  LanInfo,
  LayoutPatchItem,
  MediaMTXPath,
  ReadyResp,
  RuntimeConfig,
  SaveLayoutReq,
  TestResult,
  UpdateCardReq,
  UpdateDashboardReq,
  UpdateDataSourceReq,
} from '@/types'

/* ——— 系统 ——— */

export function getRuntime(): Promise<RuntimeConfig> {
  return request<RuntimeConfig>(`${API_PREFIX}/system/runtime`)
}

export function getLan(): Promise<LanInfo> {
  return request<LanInfo>(`${API_PREFIX}/system/lan`)
}

export function getHealth(): Promise<HealthResp> {
  return request<HealthResp>('/healthz')
}

export function getReady(): Promise<ReadyResp> {
  return request<ReadyResp>('/readyz')
}

export function certUrl(): string {
  return `${API_PREFIX}/system/cert`
}

/** 下载自签证书（application/x-x509-ca-cert），供本地信任。 */
export function downloadCert(): Promise<Blob> {
  return download(certUrl())
}

/** WS 端点（同 REST 端口，带协议版本号）。 */
export function wsEndpoint(): string {
  return `${WS_PATH}?protocol=1`
}

/* ——— 数据源 ——— */

export function listDataSources(): Promise<DataSource[]> {
  return request<DataSource[]>(`${API_PREFIX}/datasources`)
}

export function createDataSource(body: CreateDataSourceReq): Promise<DataSource> {
  return request<DataSource>(`${API_PREFIX}/datasources`, { method: 'POST', body })
}

export function getDataSource(id: string): Promise<DataSource> {
  return request<DataSource>(`${API_PREFIX}/datasources/${encodeURIComponent(id)}`)
}

export function updateDataSource(id: string, body: UpdateDataSourceReq): Promise<DataSource> {
  return request<DataSource>(`${API_PREFIX}/datasources/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body,
  })
}

export function deleteDataSource(id: string): Promise<DeletedResp> {
  return request<DeletedResp>(`${API_PREFIX}/datasources/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
}

/** 测试连接：不落库，返回识别到的 payloadType 与一帧样例（决策 D4，纳入 MVP）。 */
export function testDataSource(body: CreateDataSourceReq): Promise<TestResult> {
  return request<TestResult>(`${API_PREFIX}/datasources/test`, { method: 'POST', body })
}

/* ——— 通道 ——— */

export function listChannels(dataSourceId: string, discover = false): Promise<ChannelRuntime[]> {
  return request<ChannelRuntime[]>(`${API_PREFIX}/datasources/${encodeURIComponent(dataSourceId)}/channels`, {
    query: discover ? { discover: 1 } : undefined,
  })
}

export function createChannel(dataSourceId: string, body: CreateChannelReq): Promise<Channel> {
  return request<Channel>(`${API_PREFIX}/datasources/${encodeURIComponent(dataSourceId)}/channels`, {
    method: 'POST',
    body,
  })
}

export function getChannel(id: string): Promise<ChannelRuntime> {
  return request<ChannelRuntime>(`${API_PREFIX}/channels/${encodeURIComponent(id)}`)
}

/** 拉取样例帧（卡片向导用）；无数据后端返回 50004。 */
export function getChannelSample(id: string, timeoutMs = SAMPLE_TIMEOUT_MS): Promise<Frame> {
  return request<Frame>(`${API_PREFIX}/channels/${encodeURIComponent(id)}/sample`, {
    query: { timeoutMs },
    timeoutMs: timeoutMs + 2000,
  })
}

export function deleteChannel(id: string): Promise<DeletedResp> {
  return request<DeletedResp>(`${API_PREFIX}/channels/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

/* ——— 看板 / 卡片 ——— */

export function listDashboards(): Promise<Dashboard[]> {
  return request<Dashboard[]>(`${API_PREFIX}/dashboards`)
}

export function createDashboard(body: CreateDashboardReq): Promise<DashboardWithCards> {
  return request<DashboardWithCards>(`${API_PREFIX}/dashboards`, { method: 'POST', body })
}

export function getDashboard(id: string): Promise<DashboardWithCards> {
  return request<DashboardWithCards>(`${API_PREFIX}/dashboards/${encodeURIComponent(id)}`)
}

/** 全量保存（cards 一并替换），revision 不匹配后端返回 40009。 */
export function updateDashboard(id: string, body: UpdateDashboardReq): Promise<DashboardWithCards> {
  return request<DashboardWithCards>(`${API_PREFIX}/dashboards/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body,
  })
}

export function deleteDashboard(id: string): Promise<DeletedResp> {
  return request<DeletedResp>(`${API_PREFIX}/dashboards/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

/** 轻量保存：仅改布局（拖拽后高频调用）。 */
export function saveLayout(id: string, revision: number, layouts: LayoutPatchItem[]): Promise<Dashboard> {
  const body: SaveLayoutReq = { revision, layouts }
  return request<Dashboard>(`${API_PREFIX}/dashboards/${encodeURIComponent(id)}/layout`, {
    method: 'PUT',
    body,
  })
}

export function listCards(dashboardId: string): Promise<Card[]> {
  return request<Card[]>(`${API_PREFIX}/dashboards/${encodeURIComponent(dashboardId)}/cards`)
}

export function createCard(dashboardId: string, body: CreateCardReq): Promise<Card> {
  return request<Card>(`${API_PREFIX}/dashboards/${encodeURIComponent(dashboardId)}/cards`, {
    method: 'POST',
    body,
  })
}

export function updateCard(id: string, body: UpdateCardReq): Promise<Card> {
  return request<Card>(`${API_PREFIX}/cards/${encodeURIComponent(id)}`, { method: 'PUT', body })
}

export function deleteCard(id: string): Promise<DeletedResp> {
  return request<DeletedResp>(`${API_PREFIX}/cards/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

/* ——— MediaMTX ——— */

export function listMediaMTXPaths(path?: string): Promise<MediaMTXPath[]> {
  return request<MediaMTXPath[]>(`${API_PREFIX}/mediamtx/paths`, { query: { path } })
}
