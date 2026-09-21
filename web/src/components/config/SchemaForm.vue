<script setup lang="ts">
/**
 * 由 RendererManifest.configSchema 动态生成的表单（CD-02 / T-23）。
 * 支持全部 9 种控件：number / slider / text / select / switch / color
 *                   / field-picker / multi-field / threshold-list。
 */
import { computed } from 'vue'
import type { ConfigField, FieldHint, ThresholdBand } from '@/types'
import FieldPicker from './FieldPicker.vue'

const props = withDefaults(
  defineProps<{
    schema: ConfigField[]
    modelValue: Record<string, unknown>
    /** 最近一帧的 schemaHint，供字段点选器生成候选树。 */
    hint?: Record<string, FieldHint> | undefined
    samplePayload?: unknown
  }>(),
  { hint: undefined, samplePayload: undefined },
)

const emit = defineEmits<{ 'update:modelValue': [value: Record<string, unknown>] }>()

function set(key: string, value: unknown): void {
  emit('update:modelValue', { ...props.modelValue, [key]: value })
}

function readNumber(key: string, fallback: number): number {
  const raw = props.modelValue[key]
  const n = typeof raw === 'number' ? raw : Number(raw)
  return Number.isFinite(n) ? n : fallback
}

function readString(key: string, fallback: string): string {
  const raw = props.modelValue[key]
  return typeof raw === 'string' ? raw : fallback
}

function readBool(key: string, fallback: boolean): boolean {
  const raw = props.modelValue[key]
  return typeof raw === 'boolean' ? raw : fallback
}

function readStringArray(key: string): string[] {
  const raw = props.modelValue[key]
  if (!Array.isArray(raw)) return []
  return raw.filter((v): v is string => typeof v === 'string')
}

function readBands(key: string, fallback: ThresholdBand[]): ThresholdBand[] {
  const raw = props.modelValue[key]
  if (!Array.isArray(raw)) return fallback
  return raw.map((item) => {
    const band = item as Partial<ThresholdBand>
    return {
      from: typeof band.from === 'number' ? band.from : 0,
      to: typeof band.to === 'number' ? band.to : 0,
      color: typeof band.color === 'string' ? band.color : 'var(--ma-accent)',
      ...(typeof band.label === 'string' ? { label: band.label } : {}),
    }
  })
}

const bandsFallback = computed<Map<string, ThresholdBand[]>>(() => {
  const map = new Map<string, ThresholdBand[]>()
  for (const field of props.schema) {
    if (field.type === 'threshold-list') map.set(field.key, field.default)
  }
  return map
})

function setBand(key: string, index: number, patch: Partial<ThresholdBand>): void {
  const fallback = bandsFallback.value.get(key) ?? []
  const bands = readBands(key, fallback)
  const next = bands.map((band, i) => (i === index ? { ...band, ...patch } : band))
  set(key, next)
}

function addBand(key: string): void {
  const fallback = bandsFallback.value.get(key) ?? []
  const bands = readBands(key, fallback)
  const last = bands[bands.length - 1]
  const from = last ? last.to : 0
  set(key, [...bands, { from, to: from + 1, color: 'var(--ma-accent)' }])
}

function removeBand(key: string, index: number): void {
  const fallback = bandsFallback.value.get(key) ?? []
  const bands = readBands(key, fallback)
  set(
    key,
    bands.filter((_, i) => i !== index),
  )
}
</script>

<template>
  <div class="ma-schema">
    <div v-for="field in schema" :key="field.key" class="ma-schema__row">
      <label class="ma-schema__label" :title="field.help || field.label">
        {{ field.label }}
        <span v-if="field.help" class="ma-schema__help" :title="field.help">?</span>
      </label>

      <div class="ma-schema__control">
        <!-- number -->
        <template v-if="field.type === 'number'">
          <input
            class="ma-schema__input"
            type="number"
            :step="field.step ?? 1"
            :value="readNumber(field.key, field.default)"
            @input="set(field.key, Number(($event.target as HTMLInputElement).value))"
          />
          <span v-if="field.unit" class="ma-schema__unit ma-text-xs ma-text-3">{{ field.unit }}</span>
        </template>

        <!-- slider -->
        <template v-else-if="field.type === 'slider'">
          <input
            class="ma-schema__range"
            type="range"
            :min="field.min"
            :max="field.max"
            :step="field.step"
            :value="readNumber(field.key, field.default)"
            @input="set(field.key, Number(($event.target as HTMLInputElement).value))"
          />
          <span class="ma-schema__range-val ma-text-xs">{{ readNumber(field.key, field.default) }}</span>
        </template>

        <!-- text -->
        <template v-else-if="field.type === 'text'">
          <input
            class="ma-schema__input"
            type="text"
            :placeholder="field.placeholder ?? ''"
            :value="readString(field.key, field.default)"
            @input="set(field.key, ($event.target as HTMLInputElement).value)"
          />
        </template>

        <!-- select -->
        <template v-else-if="field.type === 'select'">
          <select
            class="ma-schema__select"
            :value="readString(field.key, field.default)"
            @change="set(field.key, ($event.target as HTMLSelectElement).value)"
          >
            <option v-for="opt in field.options" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
        </template>

        <!-- switch -->
        <template v-else-if="field.type === 'switch'">
          <button
            class="ma-schema__switch"
            :class="{ 'ma-schema__switch--on': readBool(field.key, field.default) }"
            type="button"
            @click="set(field.key, !readBool(field.key, field.default))"
          >
            <span class="ma-schema__knob"></span>
          </button>
        </template>

        <!-- color -->
        <template v-else-if="field.type === 'color'">
          <input
            class="ma-schema__color"
            type="color"
            :value="readString(field.key, field.default)"
            @input="set(field.key, ($event.target as HTMLInputElement).value)"
          />
        </template>

        <!-- field-picker / multi-field -->
        <template v-else-if="field.type === 'field-picker'">
          <field-picker
            :hint="hint"
            :payload="samplePayload"
            :accept="field.accept"
            :model-value="readString(field.key, field.default ?? '')"
            @update:model-value="set(field.key, $event)"
          />
        </template>

        <template v-else-if="field.type === 'multi-field'">
          <field-picker
            :hint="hint"
            :payload="samplePayload"
            :accept="field.accept"
            :max="field.max"
            multiple
            :model-value="readStringArray(field.key)"
            @update:model-value="set(field.key, $event)"
          />
        </template>

        <!-- threshold-list -->
        <template v-else-if="field.type === 'threshold-list'">
          <div class="ma-schema__bands">
            <div v-for="(band, idx) in readBands(field.key, bandsFallback.get(field.key) ?? field.default)" :key="idx" class="ma-schema__band">
              <input
                class="ma-schema__num"
                type="number"
                step="0.1"
                :value="band.from"
                @input="setBand(field.key, idx, { from: Number(($event.target as HTMLInputElement).value) })"
              />
              <span class="ma-text-xs ma-text-3">→</span>
              <input
                class="ma-schema__num"
                type="number"
                step="0.1"
                :value="band.to"
                @input="setBand(field.key, idx, { to: Number(($event.target as HTMLInputElement).value) })"
              />
              <input
                class="ma-schema__color"
                type="color"
                :value="band.color.startsWith('#') ? band.color : '#63e2b7'"
                @input="setBand(field.key, idx, { color: ($event.target as HTMLInputElement).value })"
              />
              <input
                class="ma-schema__input ma-schema__input--sm"
                type="text"
                placeholder="标签"
                :value="band.label ?? ''"
                @input="setBand(field.key, idx, { label: ($event.target as HTMLInputElement).value })"
              />
              <button class="ma-schema__del" title="删除区段" @click="removeBand(field.key, idx)">✕</button>
            </div>
            <button class="ma-schema__add" @click="addBand(field.key)">+ 增加区段</button>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ma-schema {
  display: flex;
  flex-direction: column;
  gap: 10px;
  width: 100%;
  min-width: 0;
}

.ma-schema__row {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.ma-schema__label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: var(--ma-font-sm);
  color: var(--ma-text-2);
}

.ma-schema__help {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 13px;
  height: 13px;
  border: 1px solid var(--ma-border-strong);
  border-radius: 50%;
  font-size: 9px;
  color: var(--ma-text-3);
  cursor: help;
}

.ma-schema__control {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.ma-schema__input,
.ma-schema__select {
  flex: 1 1 auto;
  min-width: 0;
  height: 26px;
  padding: 0 6px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-base);
  color: var(--ma-text-1);
  font-size: var(--ma-font-sm);
}

.ma-schema__input--sm {
  flex: 0 1 90px;
}

.ma-schema__num {
  width: 62px;
  height: 24px;
  padding: 0 4px;
  border: 1px solid var(--ma-border);
  border-radius: 4px;
  background: var(--ma-bg-base);
  color: var(--ma-text-1);
  font-size: var(--ma-font-xs);
}

.ma-schema__input:focus,
.ma-schema__select:focus,
.ma-schema__num:focus {
  outline: none;
  border-color: var(--ma-accent);
}

.ma-schema__unit {
  flex: none;
}

.ma-schema__range {
  flex: 1 1 auto;
  min-width: 0;
  accent-color: var(--ma-accent);
}

.ma-schema__range-val {
  flex: none;
  min-width: 38px;
  text-align: right;
  color: var(--ma-text-2);
}

.ma-schema__switch {
  position: relative;
  width: 34px;
  height: 18px;
  border: 1px solid var(--ma-border-strong);
  border-radius: 9px;
  background: var(--ma-bg-base);
  cursor: pointer;
  padding: 0;
}

.ma-schema__switch--on {
  background: var(--ma-accent-dim);
  border-color: var(--ma-accent);
}

.ma-schema__knob {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 12px;
  height: 12px;
  border-radius: 50%;
  background: var(--ma-text-3);
  transition: transform 0.14s ease;
}

.ma-schema__switch--on .ma-schema__knob {
  transform: translateX(16px);
  background: var(--ma-accent);
}

.ma-schema__color {
  width: 30px;
  height: 24px;
  padding: 0;
  border: 1px solid var(--ma-border);
  border-radius: 4px;
  background: var(--ma-bg-base);
  cursor: pointer;
}

.ma-schema__bands {
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: 100%;
}

.ma-schema__band {
  display: flex;
  align-items: center;
  gap: 4px;
}

.ma-schema__del,
.ma-schema__add {
  border: 1px solid var(--ma-border-strong);
  border-radius: 4px;
  background: var(--ma-bg-elevated);
  color: var(--ma-text-2);
  cursor: pointer;
  font-size: var(--ma-font-xs);
  padding: 2px 6px;
}

.ma-schema__del:hover {
  color: var(--ma-status-error);
  border-color: var(--ma-status-error);
}

.ma-schema__add {
  align-self: flex-start;
}

.ma-schema__add:hover {
  color: var(--ma-accent);
  border-color: var(--ma-accent);
}
</style>
