<script setup lang="ts">
/**
 * 四态角标（ARCH §10.3 / PRD §5.3）：
 * 绿=在线 灰=离线 黄闪=重连中 红=错误；视频降级态=橙色 degraded。
 * hover 显示 lastError。
 */
import type { CardVisualStatus } from '@/types'

const props = withDefaults(
  defineProps<{
    status: CardVisualStatus
    /** 显示文字（空则只显示圆点）。 */
    text?: string
    /** hover 详情（通常是 lastError）。 */
    tip?: string
    /** 重连倒计时（ms）。 */
    reconnectInMs?: number
    size?: 'small' | 'medium'
  }>(),
  { text: '', tip: '', reconnectInMs: undefined, size: 'medium' },
)

const pulse = (status: CardVisualStatus): boolean => status === 'reconnecting' || status === 'connecting' || status === 'loading'

const countdown = (): string => {
  if (props.reconnectInMs === undefined) return ''
  return `（${Math.max(1, Math.ceil(props.reconnectInMs / 1000))}s）`
}
</script>

<template>
  <span
    class="ma-badge"
    :class="[`ma-badge--${status}`, `ma-badge--${size}`, { 'ma-badge--pulse': pulse(status) }]"
    :title="tip || text"
  >
    <i class="ma-badge__dot"></i>
    <span v-if="text" class="ma-badge__text ma-ellipsis">{{ text }}{{ countdown() }}</span>
  </span>
</template>

<style scoped>
.ma-badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  max-width: 100%;
  min-width: 0;
  font-size: var(--ma-font-xs);
  color: var(--ma-text-2);
  white-space: nowrap;
}

.ma-badge__dot {
  flex: none;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--badge-color, var(--ma-status-offline));
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--badge-color, var(--ma-status-offline)) 22%, transparent);
}

.ma-badge--small .ma-badge__dot {
  width: 6px;
  height: 6px;
}

.ma-badge__text {
  min-width: 0;
  overflow: hidden;
}

.ma-badge--online {
  --badge-color: var(--ma-status-online);
}
.ma-badge--offline,
.ma-badge--idle {
  --badge-color: var(--ma-status-offline);
}
.ma-badge--reconnecting,
.ma-badge--connecting,
.ma-badge--loading {
  --badge-color: var(--ma-status-reconnecting);
}
.ma-badge--error,
.ma-badge--renderError {
  --badge-color: var(--ma-status-error);
}
.ma-badge--degraded {
  --badge-color: var(--ma-status-degraded);
}
.ma-badge--waiting,
.ma-badge--unconfigured {
  --badge-color: var(--ma-text-3);
}

.ma-badge--pulse .ma-badge__dot {
  animation: ma-pulse 1.2s ease-in-out infinite;
}

@keyframes ma-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.25;
  }
}
</style>
