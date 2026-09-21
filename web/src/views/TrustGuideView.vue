<script setup lang="ts">
/**
 * /trust 自签证书信任指引页（OP-01 / T-31）。
 * 说明：HTTPS 模式下 MediaMTX 与平台共用同一张自签证书，需要分别信任两次；
 * 视频像素流不过后端，因此必须让浏览器信任 MediaMTX 的地址才能出画面。
 */
import { computed, ref } from 'vue'
import { certUrl, downloadCert, getLan } from '@/api/endpoints'
import type { LanInfo } from '@/types'
import { useUiStore } from '@/stores/ui'

const ui = useUiStore()

const lan = ref<LanInfo | null>(null)
const loading = ref(false)
const certHref = certUrl()

async function load(): Promise<void> {
  loading.value = true
  try {
    lan.value = await getLan()
  } catch (err) {
    ui.handleError(err, '获取 LAN 信息失败')
  } finally {
    loading.value = false
  }
}
void load()

async function downloadCertFile(): Promise<void> {
  try {
    const blob = await downloadCert()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'monitorall-ca.crt'
    a.style.display = 'none'
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    window.setTimeout(() => URL.revokeObjectURL(url), 0)
  } catch (err) {
    ui.handleError(err, '证书下载失败')
  }
}

const httpsHost = computed<string>(() => {
  const host = lan.value?.hosts?.[0]
  const port = lan.value?.httpsPort ?? 8443
  return host ? `https://${host}:${port}` : 'https://<局域网IP>:8443'
})

const httpHost = computed<string>(() => {
  const host = lan.value?.hosts?.[0]
  const port = lan.value?.httpPort ?? 8080
  return host ? `http://${host}:${port}` : 'http://<局域网IP>:8080'
})
</script>

<template>
  <div class="ma-trust">
    <div class="ma-trust__card">
      <h1 class="ma-trust__title">证书信任指引</h1>
      <p class="ma-trust__lead ma-text-2">
        MonitorAll 在局域网内使用<strong>自签名证书</strong>提供 HTTPS。浏览器首次访问会提示「您的连接不是私密连接」，
        按下面的步骤信任一次即可，之后 WebRTC 视频、HLS 播放与地图瓦片都能正常加载。
      </p>

      <section class="ma-trust__section">
        <h2>① 信任平台本体</h2>
        <ol class="ma-trust__steps">
          <li>打开 <code>{{ httpsHost }}</code>（或用目录下的局域网地址）。</li>
          <li>Chrome / Edge 点击「高级」→「继续前往...（不安全）」。</li>
          <li>如需彻底消除告警，下载证书后在系统钥匙串/证书管理器中设为「始终信任」。</li>
        </ol>
        <div class="ma-trust__actions">
          <button class="ma-trust__btn" @click="downloadCertFile">下载自签证书</button>
          <a class="ma-trust__link" :href="certHref">直接下载证书</a>
          <a class="ma-trust__link" :href="httpHost">改用 HTTP 访问（{{ httpHost }}）</a>
        </div>
      </section>

      <section class="ma-trust__section">
        <h2>② 信任 MediaMTX（视频流）</h2>
        <p class="ma-trust__note ma-text-2">
          视频像素流<strong>不经过后端</strong>，浏览器直连 MediaMTX。启用 TLS 时需再访问一次
          <code>https://&lt;局域网IP&gt;:8889/</code> 并接受证书，否则 WebRTC / HLS 会被浏览器拦截。
        </p>
        <ol class="ma-trust__steps">
          <li>新标签页打开 <code>https://{{ lan?.hosts?.[0] ?? '<局域网IP>' }}:8889/</code>。</li>
          <li>出现证书告警时同样选择「高级」→「继续前往」。</li>
          <li>回到看板页面，视频卡会自动重连（或手动点击重试）。</li>
        </ol>
      </section>

      <section class="ma-trust__section">
        <h2>③ 仍然无法播放？</h2>
        <ul class="ma-trust__steps">
          <li>确认 MediaMTX 已启动且 RTMP 推流端在线。</li>
          <li>在视频卡右上角「协议」下拉里手动切换到 HLS 试试。</li>
          <li>临时用 HTTP 访问（HTTP 永远可用，不会被强制跳转 HTTPS）。</li>
        </ul>
      </section>

      <footer class="ma-trust__foot">
        <router-link class="ma-trust__link" to="/">← 返回看板</router-link>
        <span v-if="loading" class="ma-text-xs ma-text-3">正在获取局域网地址…</span>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.ma-trust {
  height: 100%;
  overflow: auto;
  padding: 24px 16px;
  background: var(--ma-bg-base);
}

.ma-trust__card {
  max-width: 720px;
  margin: 0 auto;
  padding: 20px 22px;
  border: 1px solid var(--ma-border);
  border-radius: var(--ma-radius);
  background: var(--ma-bg-card);
}

.ma-trust__title {
  margin: 0 0 8px;
  font-size: 20px;
}

.ma-trust__lead {
  margin: 0 0 16px;
  font-size: var(--ma-font-md);
  line-height: 1.7;
}

.ma-trust__section {
  margin-bottom: 18px;
}

.ma-trust__section h2 {
  margin: 0 0 6px;
  font-size: var(--ma-font-lg);
  color: var(--ma-text-1);
}

.ma-trust__steps {
  margin: 0;
  padding-left: 20px;
  font-size: var(--ma-font-sm);
  color: var(--ma-text-2);
  line-height: 1.8;
}

.ma-trust__note {
  margin: 0 0 6px;
  font-size: var(--ma-font-sm);
  line-height: 1.7;
}

.ma-trust__actions {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 8px;
}

.ma-trust__btn {
  padding: 4px 12px;
  border: 1px solid var(--ma-accent);
  border-radius: var(--ma-radius);
  background: var(--ma-accent-dim);
  color: var(--ma-accent);
  cursor: pointer;
  font-size: var(--ma-font-sm);
}

.ma-trust__link {
  font-size: var(--ma-font-sm);
}

.ma-trust__foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding-top: 12px;
  border-top: 1px solid var(--ma-border);
}

code {
  padding: 1px 4px;
  border-radius: 3px;
  background: var(--ma-bg-elevated);
  color: var(--ma-accent);
  font-size: var(--ma-font-xs);
}
</style>
