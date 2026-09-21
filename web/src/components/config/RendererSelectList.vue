<script setup lang="ts">
/**
 * 渲染器候选列表（CD-07）：按 payloadType 分段展示
 * 推荐（置顶）/ 兼容 / 不兼容（置灰 + tooltip 说明原因）。
 */
import { computed } from 'vue'
import type { PayloadType } from '@/types'
import { filterByPayloadType } from '@/renderers'
import { PAYLOAD_TYPE_TEXT } from '@/utils/constants'

const props = withDefaults(
  defineProps<{ payloadType?: PayloadType | undefined; modelValue?: string | undefined }>(),
  { payloadType: undefined, modelValue: undefined },
)

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

const grouped = computed(() => filterByPayloadType(props.payloadType))

const currentTypeLabel = computed<string>(() =>
  props.payloadType ? PAYLOAD_TYPE_TEXT[props.payloadType] : '未识别',
)

function pick(type: string): void {
  emit('update:modelValue', type)
}
</script>

<template>
  <div class="ma-rlist">
    <div class="ma-rlist__summary ma-text-xs ma-text-3">
      按数据类型 <b>{{ currentTypeLabel }}</b> 过滤：
    </div>

    <div v-if="grouped.recommended.length > 0" class="ma-rlist__group">
      <div class="ma-rlist__title">推荐</div>
      <button
        v-for="m in grouped.recommended"
        :key="m.type"
        class="ma-rlist__item"
        :class="{ 'ma-rlist__item--active': modelValue === m.type }"
        @click="pick(m.type)"
      >
        <span class="ma-rlist__name">{{ m.displayName }}</span>
        <span class="ma-rlist__tag ma-rlist__tag--rec">推荐</span>
      </button>
    </div>

    <div v-if="grouped.compatible.length > 0" class="ma-rlist__group">
      <div class="ma-rlist__title">兼容</div>
      <button
        v-for="m in grouped.compatible"
        :key="m.type"
        class="ma-rlist__item"
        :class="{ 'ma-rlist__item--active': modelValue === m.type }"
        @click="pick(m.type)"
      >
        <span class="ma-rlist__name">{{ m.displayName }}</span>
        <span class="ma-rlist__tag">需挑字段</span>
      </button>
    </div>

    <div v-if="grouped.incompatible.length > 0" class="ma-rlist__group">
      <div class="ma-rlist__title ma-text-3">不兼容</div>
      <div
        v-for="item in grouped.incompatible"
        :key="item.manifest.type"
        class="ma-rlist__item ma-rlist__item--disabled"
        :title="item.reason"
      >
        <span class="ma-rlist__name">{{ item.manifest.displayName }}</span>
        <span class="ma-rlist__tag ma-rlist__tag--bad">{{ item.reason }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ma-rlist {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 100%;
  min-width: 0;
}

.ma-rlist__summary b {
  color: var(--ma-text-1);
}

.ma-rlist__group {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.ma-rlist__title {
  font-size: var(--ma-font-xs);
  color: var(--ma-text-2);
}

.ma-rlist__item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-1);
  cursor: pointer;
  text-align: left;
  min-width: 0;
}

.ma-rlist__item:hover {
  border-color: var(--ma-border-strong);
  background: var(--ma-bg-hover);
}

.ma-rlist__item--active {
  border-color: var(--ma-accent);
  background: var(--ma-accent-dim);
}

.ma-rlist__item--disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.ma-rlist__name {
  flex: none;
  font-size: var(--ma-font-sm);
}

.ma-rlist__tag {
  flex: 1 1 auto;
  min-width: 0;
  text-align: right;
  font-size: var(--ma-font-xs);
  color: var(--ma-text-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ma-rlist__tag--rec {
  color: var(--ma-accent);
}

.ma-rlist__tag--bad {
  color: var(--ma-status-error);
}
</style>
