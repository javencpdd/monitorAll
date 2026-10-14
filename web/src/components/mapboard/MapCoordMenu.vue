<script setup lang="ts">
/**
 * 地图取点菜单（右键）。
 *
 * 为什么同时给两套坐标：
 *   地图上看到的是高德底图，它的坐标是 **GCJ-02（国测局加密坐标）**；
 *   而飞控/GPS 设备通常用 **WGS-84（原始 GPS）**。两者在中国境内差 **300~500 米**。
 *   如果只给地图坐标，用户粘进设备后会整体偏移，而现象看起来像"设备不准"——
 *   极难排查。所以这里默认把 **WGS-84 放在第一行**（那是用户真正要用的），
 *   GCJ-02 放在第二行备查。
 *
 * ⚠️ 两套值都由 `utils/geo.ts` 的唯一换算入口得出（合规 C-05），本组件不做数学。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { formatLngLat, copyText } from '@/utils/clipboard'
import type { LngLat } from '@/utils/geo'

const props = defineProps<{
  /** 相对地图容器的像素位置（右键落点）。 */
  x: number
  y: number
  /** 容器尺寸，用于把面板钳制在可视区内。 */
  bounds: { w: number; h: number }
  /** 换算到看板声明的源坐标系后的值（用户填设备用的就是这一个）。 */
  src: LngLat
  /** 该值所属的坐标系名，用于标注（如 WGS-84 / BD-09）。 */
  srcCrs: string
  /** 地图坐标（GCJ-02），用于与高德底图上的位置对照。 */
  gcj: LngLat
}>()
const emit = defineEmits<{ (e: 'close'): void }>()

const PANEL_W = 268
const PANEL_H = 148

/** 落点靠右/靠下时向内翻转，避免面板被容器裁掉。 */
const style = computed(() => {
  const x = Math.max(4, Math.min(props.x, Math.max(4, props.bounds.w - PANEL_W - 4)))
  const y = Math.max(4, Math.min(props.y, Math.max(4, props.bounds.h - PANEL_H - 4)))
  return { left: `${x}px`, top: `${y}px` }
})

const copied = ref<'src' | 'gcj' | ''>('')
let timer = 0

async function doCopy(which: 'src' | 'gcj'): Promise<void> {
  const [lon, lat] = which === 'src' ? props.src : props.gcj
  const ok = await copyText(formatLngLat(lon, lat))
  copied.value = ok ? which : ''
  if (!ok) {
    // 复制失败必须说出来：静默失败会让用户以为"复制好了"然后粘出错误坐标
    copied.value = ''
    return
  }
  window.clearTimeout(timer)
  timer = window.setTimeout(() => {
    copied.value = ''
  }, 1600)
}

function onKey(e: KeyboardEvent): void {
  if (e.key === 'Escape') emit('close')
}

onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKey)
  window.clearTimeout(timer)
})
</script>

<template>
  <div class="ma-pick" :style="style" @pointerdown.stop @click.stop @contextmenu.prevent>
    <div class="ma-pick__head">
      <span>取点坐标</span>
      <button class="ma-pick__x" type="button" title="关闭（Esc）" @click="emit('close')">×</button>
    </div>

    <div class="ma-pick__row">
      <div class="ma-pick__meta">
        <span class="ma-pick__label">源坐标系 · {{ srcCrs }}（填设备用这个）</span>
        <code class="ma-pick__val">{{ formatLngLat(src[0], src[1]) }}</code>
      </div>
      <button class="ma-pick__btn" type="button" @click="doCopy('src')">
        {{ copied === 'src' ? '已复制' : '复制' }}
      </button>
    </div>

    <div class="ma-pick__row">
      <div class="ma-pick__meta">
        <span class="ma-pick__label">地图坐标 · GCJ-02</span>
        <code class="ma-pick__val">{{ formatLngLat(gcj[0], gcj[1]) }}</code>
      </div>
      <button class="ma-pick__btn" type="button" @click="doCopy('gcj')">
        {{ copied === 'gcj' ? '已复制' : '复制' }}
      </button>
    </div>

    <p class="ma-pick__hint">填节点/设备用第一行；第二行是底图实际坐标，仅供对照。</p>
  </div>
</template>

<style scoped>
.ma-pick {
  position: absolute;
  z-index: 400;
  width: 268px;
  padding: 8px 10px 6px;
  border: 1px solid var(--ma-border-strong, var(--ma-border));
  border-radius: var(--ma-radius);
  background: var(--ma-bg-card);
  box-shadow: 0 8px 24px rgb(0 0 0 / 40%);
  /* 面板自己吃事件，避免点它时触发底图手势或"点击空白关闭" */
  pointer-events: auto;
}

.ma-pick__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
  color: var(--ma-text-2);
  font-size: var(--ma-font-xs);
}

.ma-pick__x {
  border: none;
  background: transparent;
  color: var(--ma-text-3);
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
}

.ma-pick__x:hover {
  color: var(--ma-status-error, #e5484d);
}

.ma-pick__row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 4px 0;
}

.ma-pick__meta {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.ma-pick__label {
  color: var(--ma-text-3);
  font-size: 10px;
  line-height: 1.3;
}

.ma-pick__val {
  color: var(--ma-text-1);
  font-size: var(--ma-font-xs);
  font-family: var(--ma-font-mono, monospace);
  white-space: nowrap;
}

.ma-pick__btn {
  flex: none;
  padding: 2px 8px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-elevated);
  color: var(--ma-text-1);
  cursor: pointer;
  font-size: var(--ma-font-xs);
}

.ma-pick__btn:hover {
  border-color: var(--ma-accent);
  color: var(--ma-accent);
}

.ma-pick__hint {
  margin: 4px 0 0;
  color: var(--ma-text-3);
  font-size: 10px;
  line-height: 1.4;
}
</style>
