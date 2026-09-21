import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

/**
 * MonitorAll 前端构建配置。
 * - `@` 指向 src
 * - 开发环境把 /api、/ws、健康检查代理到本地 Go 后端
 * - 构建产物输出到 web/dist（由后端 go:embed 直接消费）
 */
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    strictPort: false,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
        ws: true,
      },
      '/healthz': { target: 'http://127.0.0.1:8080', changeOrigin: true },
      '/readyz': { target: 'http://127.0.0.1:8080', changeOrigin: true },
    },
  },
  preview: {
    port: 4173,
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    target: 'es2020',
    chunkSizeWarningLimit: 2048,
    rollupOptions: {
      output: {
        manualChunks: {
          echarts: ['echarts/core', 'echarts/charts', 'echarts/components', 'echarts/renderers'],
          video: ['hls.js', 'mpegts.js'],
        },
      },
    },
  },
})
