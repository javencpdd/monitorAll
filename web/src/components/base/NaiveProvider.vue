<script setup lang="ts">
/**
 * Naive UI 适配层：业务组件不直接用 naive-ui 的类型/主题，统一从这里下发。
 * 主题与 themeOverrides 全部取自 CSS 变量（styles/variables.css）。
 */
import { computed } from 'vue'
import { NConfigProvider, NDialogProvider, NGlobalStyle, NMessageProvider, NNotificationProvider, NLoadingBarProvider } from 'naive-ui'
import type { GlobalThemeOverrides } from 'naive-ui'
import { buildThemeOverrides, resolveNaiveTheme } from '@/styles/theme'
import { useUiStore } from '@/stores/ui'

const ui = useUiStore()

const naiveTheme = computed(() => resolveNaiveTheme(ui.themeKind))
const overrides = computed<GlobalThemeOverrides>(() => buildThemeOverrides())
</script>

<template>
  <n-config-provider :theme="naiveTheme" :theme-overrides="overrides" class="ma-provider">
    <n-global-style />
    <n-loading-bar-provider>
      <n-dialog-provider>
        <n-notification-provider>
          <n-message-provider>
            <slot />
          </n-message-provider>
        </n-notification-provider>
      </n-dialog-provider>
    </n-loading-bar-provider>
  </n-config-provider>
</template>

<style scoped>
.ma-provider {
  height: 100%;
}
</style>
