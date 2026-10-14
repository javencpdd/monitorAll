/**
 * 推流地址读取（publish 模式的共享实现）。
 *
 * ⚠️ 为什么要抽成共享 util：
 *   这段逻辑原先只存在于 `DataSourceWizard.vue` 的**局部函数**里，只服务于
 *   「创建成功后回显推流地址」。现在只读的**详情抽屉**也要展示同一份地址 ——
 *   若各写一遍，就出现了第二个真源：meta 字段名一改、或读取规则一改，
 *   必然改一处漏一处，两条路径显示的推流地址就会不一致（典型的"看起来都对、实际打架"）。
 *
 * 读取规则：publish 模式下，后端把 WHIP / RTMP 两条推流地址写进
 * **该数据源首个带 `meta.publishUrl` 的通道**里。
 */
import type { ChannelRuntime } from '@/types'

export interface PublishUrls {
  whip: string
  rtmp: string
}

/**
 * 从某数据源的通道列表里取出推流地址。
 *
 * @param channels 该数据源的通道（`datasourceStore.channelsBySource[sourceId] ?? []`）
 * @returns WHIP / RTMP 地址；未找到时两者均为 `''`（UI 自行决定如何呈现"未返回"）
 */
export function readPublishUrls(channels: readonly ChannelRuntime[]): PublishUrls {
  for (const ch of channels) {
    const whip = ch.meta?.publishUrl
    if (typeof whip === 'string' && whip.length > 0) {
      const rtmp = ch.meta?.publishRtmpUrl
      return { whip, rtmp: typeof rtmp === 'string' ? rtmp : '' }
    }
  }
  return { whip: '', rtmp: '' }
}
