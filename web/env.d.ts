/// <reference types="vite/client" />

/** 构建期可注入的高德 Key（兜底，正常情况下由后端 /api/v1/system/runtime 下发）。 */
interface ImportMetaEnv {
  readonly VITE_AMAP_KEY?: string
  /** 开发环境：是否抽样调用 AMap.convertFrom 做坐标系交叉校验，默认关闭。 */
  readonly VITE_AMAP_CROSSCHECK?: string
  /** 开发环境：覆盖后端 WS 地址。 */
  readonly VITE_WS_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
