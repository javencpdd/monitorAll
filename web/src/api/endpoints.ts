/**
 * REST 端点集合（契约来源：docs/DEVELOPMENT.md §8 REST API 与错误码）。
 * 路径、请求体字段名与后端逐字对齐；此文件不做任何业务判断，只负责发请求。
 */
import { API_PREFIX, API_TIMEOUT_MS, SAMPLE_TIMEOUT_MS, WS_PATH } from '@/utils/constants'
import { AppError, download, request, type ApiResponse } from './client'
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
  LogoUploadResp,
  LayoutPatchItem,
  MediaMTXPath,
  ReadyResp,
  RuntimeConfig,
  SaveLayoutReq,
  SettingsPayload,
  TestResult,
  UpdateCardReq,
  UpdateDashboardReq,
  UpdateDataSourceReq,
  WeatherSnapshot,
} from '@/types'
import type {
  CreateMapNodeReq,
  MapBoardDetail,
  MapGeometryItem,
  MapGeometryResp,
  MapNodeMutationResp,
  MapNodeView,
  UpdateMapNodeReq,
} from '@/types/mapboard'

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

/* ——— 地图看板 / 节点（v3） ——— */

/** 地图看板聚合体：Dashboard + Cards + Nodes（一次请求拿全，避免 N+1）。 */
export function getMapBoard(id: string): Promise<MapBoardDetail> {
  return request<MapBoardDetail>(`${API_PREFIX}/mapboards/${encodeURIComponent(id)}`)
}

/** 建空地图看板（kind='map'）。 */
export function createMapBoard(name: string): Promise<MapBoardDetail> {
  return request<MapBoardDetail>(`${API_PREFIX}/mapboards`, { method: 'POST', body: { name } })
}

/** 全量保存（cards + globalConfig），带 revision 严格 CAS；40009 由调用方处理。 */
export function updateMapBoard(id: string, body: UpdateDashboardReq): Promise<MapBoardDetail> {
  return request<MapBoardDetail>(`${API_PREFIX}/mapboards/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body,
  })
}

/**
 * 轻量几何保存（C7）：**不进 revision 域**，不校验、不递增 revision。
 * 因此即使失败也**不重试** revision 冲突（40009 在此端点不应出现）。
 */
export function saveMapGeometry(id: string, items: MapGeometryItem[]): Promise<MapGeometryResp> {
  return request<MapGeometryResp>(`${API_PREFIX}/mapboards/${encodeURIComponent(id)}/geometry`, {
    method: 'PUT',
    body: { items },
  })
}

export function listMapNodes(boardId: string): Promise<MapNodeView[]> {
  return request<MapNodeView[]>(`${API_PREFIX}/mapboards/${encodeURIComponent(boardId)}/nodes`)
}

/** 新建节点；返回完整 node（含 id），便于本地乐观插入。 */
export function createMapNode(boardId: string, body: CreateMapNodeReq): Promise<MapNodeMutationResp> {
  return request<MapNodeMutationResp>(`${API_PREFIX}/mapboards/${encodeURIComponent(boardId)}/nodes`, {
    method: 'POST',
    body,
  })
}

export function updateMapNode(
  boardId: string,
  nodeId: string,
  body: UpdateMapNodeReq,
): Promise<MapNodeMutationResp> {
  return request<MapNodeMutationResp>(
    `${API_PREFIX}/mapboards/${encodeURIComponent(boardId)}/nodes/${encodeURIComponent(nodeId)}`,
    { method: 'PUT', body },
  )
}

/**
 * 删除节点。
 * @param cascade 'unbind' 解绑卡片保留（默认，C10）/ 'delete' 级联删卡
 */
export function deleteMapNode(
  boardId: string,
  nodeId: string,
  revision: number,
  cascade: 'unbind' | 'delete' = 'unbind',
): Promise<MapNodeMutationResp> {
  return request<MapNodeMutationResp>(
    `${API_PREFIX}/mapboards/${encodeURIComponent(boardId)}/nodes/${encodeURIComponent(nodeId)}`,
    { method: 'DELETE', query: { revision, cascade } },
  )
}

/* ——— MediaMTX ——— */

export function listMediaMTXPaths(path?: string): Promise<MediaMTXPath[]> {
  return request<MediaMTXPath[]>(`${API_PREFIX}/mediamtx/paths`, { query: { path } })
}

/* ——— 全局设置：品牌 + 天气 ——— */

/** 读取全局设置（整体读）。 */
export function getSettings(): Promise<SettingsPayload> {
  return request<SettingsPayload>(`${API_PREFIX}/settings`)
}

/** 整体写回（不支持局部 patch）。 */
export function saveSettings(body: SettingsPayload): Promise<SettingsPayload> {
  return request<SettingsPayload>(`${API_PREFIX}/settings`, { method: 'PUT', body })
}

/**
 * 上传品牌 Logo（multipart，字段名固定为 `file`）。
 *
 * ⚠️ **不能走通用 `request()`**：它在 `body !== undefined` 时无条件写
 * `Content-Type: application/json`，multipart 的 boundary 会被覆盖掉，
 * 后端解析出的 `file` 为空 —— 表现为「点了上传没反应」，是最典型的静默失败之一。
 * 这里直接发 FormData 且**不设** Content-Type（浏览器自动补 boundary），
 * 解包与错误语义仍与 `request()` 完全一致（code≠0 → AppError）。
 */
export async function uploadLogo(file: File): Promise<LogoUploadResp> {
  const form = new FormData()
  form.append('file', file)
  const controller = new AbortController()
  const timer = window.setTimeout(() => controller.abort(), API_TIMEOUT_MS)
  let res: Response
  try {
    res = await fetch(`${API_PREFIX}/settings/logo`, {
      method: 'POST',
      body: form,
      credentials: 'same-origin',
      signal: controller.signal,
    })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') {
      throw new AppError(-1, `Logo 上传超时（${API_TIMEOUT_MS}ms）`)
    }
    throw new AppError(-2, err instanceof Error ? err.message : '网络异常')
  } finally {
    window.clearTimeout(timer)
  }
  const json = (await res.json().catch(() => null)) as ApiResponse<LogoUploadResp> | null
  if (!json) throw new AppError(res.status, `响应解析失败（HTTP ${res.status}）`)
  if (json.code !== 0) throw new AppError(json.code, json.message || 'Logo 上传失败', json.traceId)
  if (!res.ok) throw new AppError(res.status, json.message || `HTTP ${res.status}`)
  return json.data as LogoUploadResp
}

/**
 * 清除已上传的 Logo。
 *
 * 响应体按后端当前 DELETE 惯例为 `{deleted:true}`；调用方**不读**其字段
 * （清除结果以「随后 logoUrl 是否为空」为准），故此处不额外定义类型。
 */
export function deleteLogo(): Promise<DeletedResp> {
  return request<DeletedResp>(`${API_PREFIX}/settings/logo`, { method: 'DELETE' })
}

/**
 * 当日天气快照。
 *
 * ⚠️ 返回 `ok:false` 时**不抛错**：HTTP 仍 200，原因码在 `reason`、中文说明在 `message`。
 * 调用方必须区分「后端明确说不可用」与「这次请求本身失败」——前者要展示原因，后者要展示重试入口。
 */
export function getWeather(): Promise<WeatherSnapshot> {
  return request<WeatherSnapshot>(`${API_PREFIX}/weather`)
}
