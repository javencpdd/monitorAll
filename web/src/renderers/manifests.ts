/**
 * 7 个 P0 渲染器 Manifest 定义（契约来源：docs/ARCHITECTURE.md §9）。
 *
 * 字段含义：
 *   accepts      —— 声明能吃的 payloadType
 *   recommended  —— 在这些类型上打「推荐」并置顶（CD-07）
 *   configSchema —— 驱动 CardConfigDrawer 的动态表单 + 字段点选器（CD-09）
 *   defaultLayout/defaultConfig —— 新建卡片的一键可用默认值
 */
import type { Component } from 'vue'
import type { RendererManifest } from '@/types'

/** 异步组件工厂（保持懒加载，避免首屏打包全部渲染器）。 */
const lazy = (loader: () => Promise<Component>): (() => Promise<Component>) => loader

/* ——— RD-03 仪表（ARCH §9.3 原文抄录） ——— */
export const gaugeManifest: RendererManifest = {
  type: 'gauge',
  displayName: '仪表',
  icon: 'speedometer',
  accepts: ['scalar', 'json', 'time_series_sample'],
  recommended: ['scalar'],
  configSchema: [
    {
      key: 'field',
      label: '数值字段',
      type: 'field-picker',
      accept: 'number',
      payloadTypes: ['scalar', 'json', 'time_series_sample'],
      default: 'value',
      help: '从最近一帧中点选数值字段',
    },
    { key: 'min', label: '量程下限', type: 'number', step: 0.1, default: 0 },
    { key: 'max', label: '量程上限', type: 'number', step: 0.1, default: 30 },
    { key: 'decimals', label: '小数位', type: 'slider', min: 0, max: 4, step: 1, default: 2 },
    {
      key: 'bands',
      label: '阈值区段',
      type: 'threshold-list',
      unit: 'V',
      default: [
        { from: 0, to: 21, color: '#e5534b', label: '危险' },
        { from: 21, to: 23, color: '#f2c037', label: '警戒' },
        { from: 23, to: 30, color: '#63e2b7', label: '安全' },
      ],
    },
    { key: 'showPointer', label: '显示指针', type: 'switch', default: true },
    { key: 'showMinMax', label: '脚注显示 MIN/MAX', type: 'switch', default: true },
  ],
  defaultConfig: {
    field: 'value',
    min: 0,
    max: 30,
    decimals: 2,
    showPointer: true,
    showMinMax: true,
    bands: [
      { from: 0, to: 21, color: '#e5534b', label: '危险' },
      { from: 21, to: 23, color: '#f2c037', label: '警戒' },
      { from: 23, to: 30, color: '#63e2b7', label: '安全' },
    ],
  },
  defaultLayout: { w: 2, h: 4, minW: 2, minH: 3 },
  capabilities: { supportsPause: false, supportsExport: false },
  component: lazy(() => import('../components/renderers/GaugeRenderer.vue')),
}

/* ——— RD-04 XY 折线图（ARCH §9.3 原文抄录） ——— */
export const lineChartManifest: RendererManifest = {
  type: 'line-chart',
  displayName: 'XY 折线图',
  icon: 'trending-up',
  accepts: ['time_series_sample', 'scalar', 'json'],
  recommended: ['time_series_sample'],
  configSchema: [
    {
      key: 'fields',
      label: 'Y 轴字段（多选）',
      type: 'multi-field',
      accept: 'number',
      max: 8,
      help: '图例即字段开关，可实时勾选',
    },
    {
      key: 'windowSec',
      label: '时间窗口',
      type: 'select',
      options: [
        { label: '30 秒', value: '30' },
        { label: '60 秒', value: '60' },
        { label: '5 分钟', value: '300' },
        { label: '30 分钟', value: '1800' },
      ],
      default: '60',
    },
    {
      key: 'yAxisMode',
      label: 'Y 轴量程',
      type: 'select',
      options: [
        { label: '自动', value: 'auto' },
        { label: '固定', value: 'fixed' },
      ],
      default: 'auto',
    },
    { key: 'yMin', label: 'Y 轴下限（固定时）', type: 'number', step: 0.1, default: 0 },
    { key: 'yMax', label: 'Y 轴上限（固定时）', type: 'number', step: 0.1, default: 1 },
    {
      key: 'maxPoints',
      label: '缓冲点数上限',
      type: 'slider',
      min: 200,
      max: 5000,
      step: 100,
      default: 1200,
      help: '超出后环形缓冲自动淘汰最旧点',
    },
    {
      key: 'sampling',
      label: '降采样',
      type: 'select',
      options: [
        { label: 'LTTB', value: 'lttb' },
        { label: '关闭', value: 'none' },
      ],
      default: 'lttb',
    },
    { key: 'showLegend', label: '显示图例', type: 'switch', default: true },
  ],
  defaultConfig: {
    fields: [],
    windowSec: '60',
    yAxisMode: 'auto',
    yMin: 0,
    yMax: 1,
    maxPoints: 1200,
    sampling: 'lttb',
    showLegend: true,
  },
  defaultLayout: { w: 4, h: 5, minW: 2, minH: 3 },
  capabilities: { needsHistory: true, supportsPause: true, supportsExport: true, maxPoints: 1200 },
  component: lazy(() => import('../components/renderers/LineChartRenderer.vue')),
}

/* ——— RD-06 地图（ARCH §9.3 原文抄录，合规红线相关字段不可删改） ——— */
export const mapManifest: RendererManifest = {
  type: 'map',
  displayName: '地图（GCJ-02）',
  icon: 'map',
  accepts: ['geo_pose', 'json'],
  recommended: ['geo_pose'],
  configSchema: [
    {
      key: 'sourceCRS',
      label: '输入坐标系（请确认）',
      type: 'select',
      options: [
        { label: 'WGS-84（GPS 原始 / ROS NavSatFix）', value: 'WGS-84' },
        { label: 'GCJ-02（已转换）', value: 'GCJ-02' },
        { label: 'BD-09（百度）', value: 'BD-09' },
      ],
      default: 'WGS-84',
      help: '⚠ 选错会产生 300–600m 偏移。转换在前端离线完成，不调用高德官方接口',
    },
    {
      key: 'mapStyle',
      label: '底图样式',
      type: 'select',
      options: [
        { label: '标准路网', value: 'amap://styles/normal' },
        { label: '暗色', value: 'amap://styles/dark' },
        { label: '卫星', value: 'amap://styles/satellite' },
      ],
      default: 'amap://styles/dark',
    },
    { key: 'trailLength', label: '轨迹尾迹点数', type: 'slider', min: 0, max: 2000, step: 50, default: 200 },
    {
      key: 'latPath',
      label: '纬度字段路径',
      type: 'text',
      default: '',
      placeholder: 'latitude',
      help: 'JSON 通道用：填字段路径，如 pose.latitude / data.pose.latitude。留空则按顺序尝试 latitude、lat、gps.lat、pose.position.lat',
    },
    {
      key: 'lonPath',
      label: '经度字段路径',
      type: 'text',
      default: '',
      placeholder: 'longitude',
      help: '如 pose.longitude / data.pose.longitude。留空则尝试 longitude、lon、lng、gps.lon、pose.position.lon',
    },
    {
      key: 'yawPath',
      label: '朝向字段路径（可选）',
      type: 'text',
      default: '',
      placeholder: 'yaw',
      help: '如 pose.yaw；留空则不画朝向',
    },
    {
      key: 'yawUnit',
      label: '朝向单位',
      type: 'select',
      options: [
        { label: '弧度 rad', value: 'rad' },
        { label: '角度 deg', value: 'deg' },
      ],
      default: 'rad',
    },
    { key: 'follow', label: '镜头跟随当前点', type: 'switch', default: true },
    { key: 'showYaw', label: '朝向箭头随 yaw 旋转', type: 'switch', default: true },
    { key: 'zoom', label: '初始缩放级别', type: 'slider', min: 3, max: 20, step: 1, default: 17 },
  ],
  defaultConfig: {
    sourceCRS: 'WGS-84',
    mapStyle: 'amap://styles/dark',
    trailLength: 200,
    latPath: '',
    lonPath: '',
    yawPath: '',
    yawUnit: 'rad',
    follow: true,
    showYaw: true,
    zoom: 17,
  },
  defaultLayout: { w: 6, h: 6, minW: 3, minH: 4 },
  capabilities: { needsHistory: true, requiresAMap: true, maxPoints: 2000 },
  component: lazy(() => import('../components/renderers/MapRenderer.vue')),
}

/* ——— RD-01 原始数据 JSON 树（ARCH §9.4） ——— */
export const jsonTreeManifest: RendererManifest = {
  type: 'json-tree',
  displayName: '原始数据',
  icon: 'code',
  accepts: ['json', 'scalar', 'time_series_sample', 'geo_pose', 'table', 'image', 'video_stream'],
  recommended: ['json'],
  configSchema: [
    { key: 'defaultExpandDepth', label: '默认展开层级', type: 'slider', min: 1, max: 6, step: 1, default: 3 },
    { key: 'showType', label: '显示类型标注', type: 'switch', default: true },
    { key: 'maxNodes', label: '最大节点数', type: 'slider', min: 500, max: 20000, step: 500, default: 5000, help: '超出后截断，防止超大 payload 卡死渲染' },
  ],
  defaultConfig: { defaultExpandDepth: 3, showType: true, maxNodes: 5000 },
  defaultLayout: { w: 3, h: 5, minW: 2, minH: 3 },
  capabilities: {},
  component: lazy(() => import('../components/renderers/JsonTreeRenderer.vue')),
}

/* ——— RD-02 表格（ARCH §9.4） ——— */
export const tableManifest: RendererManifest = {
  type: 'table',
  displayName: '表格',
  icon: 'table',
  accepts: ['table', 'json', 'time_series_sample'],
  recommended: ['table'],
  configSchema: [
    { key: 'columns', label: '显示列（留空 = 自动）', type: 'multi-field', accept: 'any', help: '按最近一帧自动拍平出的字段点选' },
    { key: 'maxRows', label: '最大行数', type: 'slider', min: 100, max: 5000, step: 100, default: 1000 },
    { key: 'sortable', label: '允许排序', type: 'switch', default: true },
    { key: 'freezeFirst', label: '冻结首列', type: 'switch', default: false },
  ],
  defaultConfig: { columns: [], maxRows: 1000, sortable: true, freezeFirst: false },
  defaultLayout: { w: 4, h: 5, minW: 2, minH: 3 },
  capabilities: { supportsExport: true },
  component: lazy(() => import('../components/renderers/TableRenderer.vue')),
}

/* ——— RD-05 图像（ARCH §9.4） ——— */
export const imageManifest: RendererManifest = {
  type: 'image',
  displayName: '图像',
  icon: 'image',
  accepts: ['image'],
  recommended: ['image'],
  configSchema: [
    {
      key: 'fit',
      label: '缩放方式',
      type: 'select',
      options: [
        { label: '保持宽高比（contain）', value: 'contain' },
        { label: '裁剪填满（cover）', value: 'cover' },
        { label: '拉伸铺满（stretch）', value: 'stretch' },
      ],
      default: 'contain',
    },
    { key: 'showTimestamp', label: '显示时间戳水印', type: 'switch', default: true },
    { key: 'maxFps', label: '最大帧率', type: 'slider', min: 1, max: 30, step: 1, default: 10, help: '降低 CPU 与带宽占用（默认 10Hz）' },
  ],
  defaultConfig: { fit: 'contain', showTimestamp: true, maxFps: 10 },
  defaultLayout: { w: 4, h: 5, minW: 2, minH: 3 },
  capabilities: {},
  component: lazy(() => import('../components/renderers/ImageRenderer.vue')),
}

/* ——— RD-07 视频播放器（ARCH §9.4 + 决策 D1 降级链） ——— */
export const videoManifest: RendererManifest = {
  type: 'video',
  displayName: '视频播放器',
  icon: 'videocam',
  accepts: ['video_stream'],
  recommended: ['video_stream'],
  configSchema: [
    {
      key: 'preferredProtocol',
      label: '首选协议',
      type: 'select',
      options: [
        { label: 'WebRTC（低延迟 ≤0.5s）', value: 'webrtc' },
        { label: 'HLS（兼容优先 2-3s）', value: 'hls' },
        { label: 'HTTP-FLV（需外部网关 1-2s）', value: 'flv' },
      ],
      default: 'webrtc',
      help: 'WebRTC 不可用时按 autoDowngrade 自动降级到下一个可用协议',
    },
    { key: 'autoplay', label: '自动播放', type: 'switch', default: true },
    { key: 'muted', label: '静音启动', type: 'switch', default: true },
    { key: 'showControls', label: '悬浮控制条', type: 'switch', default: true },
    { key: 'autoDowngrade', label: '自动降级', type: 'switch', default: true },
    { key: 'retryPreferredSec', label: '重试首选协议间隔', type: 'slider', min: 10, max: 120, step: 5, default: 30, help: '降级后后台探测首选协议是否恢复' },
  ],
  defaultConfig: {
    preferredProtocol: 'webrtc',
    autoplay: true,
    muted: true,
    showControls: true,
    autoDowngrade: true,
    retryPreferredSec: 30,
  },
  defaultLayout: { w: 4, h: 6, minW: 2, minH: 4 },
  capabilities: {},
  component: lazy(() => import('../components/renderers/VideoRenderer.vue')),
}

/** 全部 P0 manifest（注册顺序即候选列表的基础顺序）。 */
export const P0_MANIFESTS: RendererManifest[] = [
  gaugeManifest,
  lineChartManifest,
  mapManifest,
  jsonTreeManifest,
  tableManifest,
  imageManifest,
  videoManifest,
]
