<script setup lang="ts">
/**
 * 应用根组件：只负责挂载 NaiveProvider 与 RouterView。
 * 主题/全局状态在 stores/ui.ts 的 bootstrap 中初始化。
 */
import NaiveProvider from '@/components/base/NaiveProvider.vue'
</script>

<template>
  <naive-provider>
    <router-view />
  </naive-provider>
</template>
