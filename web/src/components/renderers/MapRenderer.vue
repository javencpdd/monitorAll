<script setup lang="ts">
/**
 * RD-06 地图渲染器（合规重点，ARCH §13 / T-27）。
 *
 * 合规红线实现要点：
 *   1. 底图**只**用高德 JS API 2.0（utils/amapLoader.ts 是唯一加载入口）
 *   2. 坐标转换**只**在 utils/geo.ts 用 gcoord 完成，本文件只调用 toGCJ02 / toGCJ02Batch
 *   3. sourceCRS 由用户在卡片配置里显式声明，优先级高于 payload.crs
 *   4. amapKey 为空时降级为「配置指引 + 纯坐标列表」，**绝不切换任何境外底图**
 *   5. 脚注/悬浮面板显示「坐标系 GCJ-02（源 WGS-84，已离线换算）」
 */
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import type { RendererProps } from '@/types'
import type { AMapMap, AMapMarker, AMapNamespace, AMapPolyline } from '@/utils/amapLoader'
import { loadAMap } from '@/utils/amapLoader'
import { describeCrs, isValidLngLat, toGCJ02, toGCJ02Batch } from '@/utils/geo'
import type { LngLat } from '@/utils/geo'
import { useFrameFeed } from '@/composables/useFrameFeed'
import { useUiStore } from '@/stores/ui'
import { MAP_DEFAULT_TRAIL, MAP_DEFAULT_ZOOM, MAP_THROTTLE_MS, DEFAULT_MAP_CENTER } from '@/utils/constants'
import { cssColor } from '@/styles/theme'
import { formatNumber } from '@/utils/format'
import type { CRS } from '@/types'
import { getByPath } from '@/utils/jsonpath'
import EmptyState from '@/components/base/EmptyState.vue'

type Pos = { lat: number; lon: number; yaw?: number; t: number; sourceCRS: CRS }

const props = defineProps<RendererProps>()
const ui = useUiStore()

const sourceCRS = computed<CRS>(() => (props.config.sourceCRS as CRS) ?? 'WGS-84')
const mapStyle = computed<string>(() => String(props.config.mapStyle ?? 'amap://styles/dark'))
const trailLength = computed<number>(() => Number(props.config.trailLength ?? MAP_DEFAULT_TRAIL))
const follow = computed<boolean>(() => props.config.follow !== false)
const showYaw = computed<boolean>(() => props.config.showYaw !== false)
const zoom = computed<number>(() => Number(props.config.zoom ?? MAP_DEFAULT_ZOOM))

const { latest, getBuffer, version } = useFrameFeed(() => props.card.channelId)

const container = ref<HTMLDivElement | null>(null)
const amap = shallowRef<AMapNamespace | null>(null)
const mapInstance = shallowRef<AMapMap | null>(null)
const poly = shallowRef<AMapPolyline | null>(null)
const marker = shallowRef<AMapMarker | null>(null)
const loadError = ref('')
const ready = ref(false)

/** 降级坐标列表：无 Key 时展示最近若干点。 */
const recentPoints = ref<Pos[]>([])

function extractPosition(frame: { payload: unknown; publishedTs: number } | undefined): Pos | null {
  if (!frame) return null
  const payload = frame.payload as Record<string, unknown> | undefined
  if (!payload) return null
  // geo_pose：lat/lon 直读
  const lat = typeof payload.lat === 'number' ? payload.lat : Number.NaN
  const lon = typeof payload.lon === 'number' ? payload.lon : Number.NaN
  if (Number.isFinite(lat) && Number.isFinite(lon)) {
    return {
      lat,
      lon,
      ...(typeof payload.yawDeg === 'number' ? { yaw: payload.yawDeg } : {}),
      t: typeof payload.t === 'number' ? payload.t : frame.publishedTs,
      sourceCRS: sourceCRS.value,
    }
  }
  // json 兜底：尝试常见字段路径
  const candidates = ['latitude', 'lat', 'gps.lat', 'pose.position.lat']
  const lonCandidates = ['longitude', 'lon', 'lng', 'gps.lon', 'gps.lng', 'pose.position.lon']
  const flatLat = firstNumberAt(payload, candidates)
  const flatLon = firstNumberAt(payload, lonCandidates)
  if (flatLat !== undefined && flatLon !== undefined) {
    return { lat: flatLat, lon: flatLon, t: frame.publishedTs, sourceCRS: sourceCRS.value }
  }
  return null
}

function firstNumberAt(payload: Record<string, unknown>, paths: string[]): number | undefined {
  for (const path of paths) {
    const value = getByPath(payload, path)
    if (typeof value === 'number' && Number.isFinite(value)) return value
  }
  return undefined
}

/** 从环形缓冲取最近 N 个点（命令式读取）。 */
function collectTrail(): Pos[] {
  const buffer = getBuffer()
  if (!buffer) return []
  const take = Math.max(0, trailLength.value)
  const frames = take > 0 ? buffer.tail(Math.min(take, buffer.limit)) : buffer.tail(1)
  const out: Pos[] = []
  for (const frame of frames) {
    const pos = extractPosition(frame)
    if (pos && isValidLngLat(pos.lon, pos.lat)) out.push(pos)
  }
  return out
}

/** 单次转换跟着点数走：先按用户声明的坐标系整体换算到 GCJ-02。 */
function toProjected(points: readonly Pos[]): LngLat[] {
  const raw = points.map((p) => [p.lon, p.lat] as LngLat)
  return toGCJ02Batch(raw, sourceCRS.value)
}

/** 强调色（Canvas/图标需具体色值，换肤后重新解析）。 */
const accent = computed<string>(() => {
  void ui.themeKind
  return cssColor('--ma-accent', '#63e2b7')
})

/** 当前位置 Marker：朝向箭头 SVG data URI，避免引入外部图标依赖。 */
const yawIconImage = computed<string>(
  () =>
    'data:image/svg+xml;utf8,' +
    encodeURIComponent(
      '<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">' +
        `<circle cx="12" cy="12" r="9" fill="#0f1f1b" stroke="${accent.value}" stroke-width="1.6"/>` +
        `<path d="M12 4 L16.5 20 L12 16.6 L7.5 20 Z" fill="${accent.value}"/></svg>`,
    ),
)

let lastPaint = 0

function paint(force: boolean): void {
  const now = performance.now()
  if (!force && now - lastPaint < MAP_THROTTLE_MS) return
  lastPaint = now
  const points = collectTrail()
  recentPoints.value = points.slice(-8).reverse()
  const last = points[points.length - 1]
  if (!last) return

  if (!mapInstance.value || !amap.value) return
  const projected = toProjected(points)
  if (projected.length === 0) return

  if (!poly.value) {
    poly.value = new amap.value.Polyline({
      path: projected,
      strokeColor: accent.value,
      strokeWeight: MAP_TRAIL_WEIGHT,
      strokeOpacity: 0.9,
      lineJoin: 'round',
      lineCap: 'round',
      isOutline: false,
    })
    mapInstance.value.add(poly.value)
  } else {
    // 增量 setPath，避免每帧重建 polyline
    poly.value.setPath(projected)
  }

  const current = projected[projected.length - 1] as LngLat
  if (!marker.value) {
    marker.value = new amap.value.Marker({
      position: current,
      anchor: 'center',
      zIndex: 120,
      ...(showYaw.value ? { angle: last.yaw ?? 0 } : {}),
      icon: new amap.value.Icon({
        size: new amap.value.Size(MAP_MARKER_SIZE, MAP_MARKER_SIZE),
        image: yawIconImage.value,
        imageSize: new amap.value.Size(MAP_MARKER_SIZE, MAP_MARKER_SIZE),
      }),
    })
    mapInstance.value.add(marker.value)
  } else {
    marker.value.setPosition(current)
    if (showYaw.value && last.yaw !== undefined) marker.value.setAngle(last.yaw)
  }

  if (follow.value) mapInstance.value.setCenter(current)
}

/** 轨迹线宽与 Marker 尺寸（px）。 */
const MAP_TRAIL_WEIGHT = 3
const MAP_MARKER_SIZE = 24

watch(version, () => paint(false))
watch([trailLength, sourceCRS, follow, showYaw], () => paint(true))

async function init(): Promise<void> {
  const key = ui.amapKey
  if (key.length === 0 || !ui.amapReady) {
    loadError.value = '未配置高德 Key'
    return
  }
  const instance = await loadAMap(key)
  if (!instance) {
    loadError.value = '高德 JS API 加载失败（Key 无效或无法访问外网）'
    return
  }
  if (!container.value) return
  amap.value = instance
  const first = extractPosition(latest.value)
  const center: LngLat = first
    ? (toGCJ02(first.lon, first.lat, sourceCRS.value) as LngLat)
    : DEFAULT_MAP_CENTER
  const map = new instance.Map(container.value, {
    zoom: zoom.value,
    center,
    viewMode: '2D',
    mapStyle: mapStyle.value,
    resizeEnable: true,
  })
  mapInstance.value = map
  ready.value = true
  paint(true)
}

onMounted(() => {
  void init()
})

watch(
  () => ui.amapKey,
  () => {
    if (!mapInstance.value && ui.amapReady) void init()
  },
)

onBeforeUnmount(() => {
  marker.value = null
  poly.value = null
  mapInstance.value?.destroy()
  mapInstance.value = null
  ready.value = false
})

const crsText = computed<string>(() => describeCrs(sourceCRS.value))
const lastPoint = computed<Pos | undefined>(() => recentPoints.value[0])

/** 降级列表用的投影结果（转换仍在 geo.ts，本文件只调用）。 */
const recentRows = computed<
  { time: string; lng: number; lat: number; yaw?: number }[]
>(() =>
  recentPoints.value.map((p) => {
    const [lng, lat] = toGCJ02(p.lon, p.lat, p.sourceCRS)
    return {
      time: new Date(p.t).toLocaleTimeString('zh-CN', { hour12: false }),
      lng,
      lat,
      ...(p.yaw !== undefined ? { yaw: p.yaw } : {}),
    }
  }),
)
</script>

<template>
  <div class="ma-map">
    <!-- 有 Key：高德底图 -->
    <div v-show="ui.amapReady && !loadError" ref="container" class="ma-map__canvas"></div>

    <!-- 无 Key / 加载失败：合规降级为纯坐标列表，绝不切换境外底图 -->
    <div v-if="!ui.amapReady || loadError" class="ma-map__fallback ma-scroll-y">
      <empty-state icon="map" title="请配置高德地图 Key" :description="loadError || ''">
        <template #action>
          <p class="ma-map__guide ma-text-xs ma-text-2">
            在服务端 <code>config.yaml</code> 的 <code>web.amapKey</code> 中填入高德 Web 端 Key 后重启即可显示底图。<br />
            本产品不使用任何境外底图，未配置 Key 期间仅显示坐标数值。
          </p>
        </template>
      </empty-state>
      <table class="ma-map__tbl">
        <thead>
          <tr>
            <th>时间</th>
            <th>纬度 / 经度（GCJ-02）</th>
            <th>朝向</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, i) in recentRows" :key="i">
            <td>{{ row.time }}</td>
            <td>{{ formatNumber(row.lat, 6) }}, {{ formatNumber(row.lng, 6) }}</td>
            <td>{{ row.yaw === undefined ? '--' : `${formatNumber(row.yaw, 1)}°` }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 悬浮信息面板：显式声明当前生效坐标系 -->
    <div v-else-if="lastPoint" class="ma-map__hud ma-text-xs">
      <div>lat {{ formatNumber(lastPoint.lat, 6) }} · lon {{ formatNumber(lastPoint.lon, 6) }}</div>
      <div v-if="lastPoint.yaw !== undefined">yaw {{ formatNumber(lastPoint.yaw, 1) }}°</div>
      <div class="ma-map__crs">{{ crsText }}</div>
    </div>

    <div class="ma-map__foot ma-text-xs ma-text-3">
      <span>高德 JS API 2.0 · {{ crsText }}</span>
      <span v-if="ready">已投影 {{ recentPoints.length }}+ 点</span>
    </div>
  </div>
</template>

<style scoped>
.ma-map {
  position: relative;
  width: 100%;
  height: 100%;
  min-height: 0;
  overflow: hidden;
}

.ma-map__canvas {
  width: 100%;
  height: 100%;
}

.ma-map__fallback {
  height: 100%;
  padding: 8px;
}

.ma-map__tbl {
  width: 100%;
  border-collapse: collapse;
  font-family: 'JetBrains Mono', monospace;
  font-size: var(--ma-font-xs);
}

.ma-map__tbl th,
.ma-map__tbl td {
  padding: 2px 4px;
  text-align: left;
  border-bottom: 1px solid var(--ma-border);
  color: var(--ma-text-2);
}

.ma-map__guide {
  margin: 4px 0 0;
  line-height: 1.6;
}

.ma-map__hud {
  position: absolute;
  left: 8px;
  top: 8px;
  padding: 4px 8px;
  border: 1px solid var(--ma-border-strong);
  border-radius: var(--ma-radius);
  background: color-mix(in srgb, var(--ma-bg-elevated) 82%, transparent);
  color: var(--ma-text-1);
  font-family: 'JetBrains Mono', monospace;
  pointer-events: none;
}

.ma-map__crs {
  margin-top: 2px;
  color: var(--ma-accent);
}

.ma-map__foot {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  display: flex;
  justify-content: space-between;
  gap: 8px;
  padding: 1px 6px;
  background: color-mix(in srgb, var(--ma-bg-card) 78%, transparent);
  pointer-events: none;
}
</style>
