<template>
  <div class="dashboard">
    <header class="page-header">
      <div>
        <div class="eyebrow">控制台</div>
        <h2>仪表盘</h2>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary" @click="refreshAll" :disabled="refreshing">
          <RefreshCw class="icon-size" :class="{ spinning: refreshing }" />
          刷新
        </button>
        <button
          class="btn btn-primary state-action"
          :class="{ working: loading && actionName === 'start' }"
          @click="startProxy"
          :disabled="proxyState.running || loading"
        >
          <Power class="icon-size" />
          启动
        </button>
        <button
          class="btn btn-danger state-action"
          :class="{ working: loading && actionName === 'stop' }"
          @click="stopProxy"
          :disabled="!proxyState.running || loading"
        >
          <Square class="icon-size" />
          停止
        </button>
        <button class="btn btn-ghost" @click="restoreCursor" :disabled="loading">
          <RotateCcw class="icon-size" :class="{ spinning: loading && actionName === 'restore' }" />
          恢复官方通道
        </button>
      </div>
    </header>

    <div v-if="notice.message" class="notice" :class="notice.type">
      <component :is="noticeIcon" class="notice-icon" />
      <span>{{ notice.message }}</span>
    </div>

    <section class="service-band">
      <div class="service-main">
        <div class="status-orb" :class="{ active: proxyState.running, warning: !trust.trusted }">
          <Check class="status-icon" />
        </div>
        <div>
          <div class="service-title-row">
            <span class="service-title">{{ proxyState.running ? '本地代理运行中' : '本地代理已停止' }}</span>
            <span class="state-pill" :class="{ active: proxyState.running }">
              {{ proxyState.running ? 'Local Proxy' : 'Direct Ready' }}
            </span>
          </div>
          <div class="service-meta">
            <span class="mono">{{ proxyState.address || '127.0.0.1:18080' }}</span>
            <span>{{ trust.trusted ? 'CA 已信任' : 'CA 未信任' }}</span>
            <span>{{ modelCount }} 个模型</span>
          </div>
        </div>
      </div>

      <div class="service-side">
        <div>
          <span>最近模型</span>
          <strong>{{ stats.lastModel || '-' }}</strong>
        </div>
        <div>
          <span>最近请求</span>
          <strong>{{ formatDateTime(stats.lastRequest || proxyState.lastRequest) }}</strong>
        </div>
      </div>
    </section>

    <section class="metrics-grid">
      <article class="metric-card">
        <div class="metric-head">
          <MessageSquare class="metric-icon success" />
          <span>对话</span>
        </div>
        <strong>{{ formatNumber(dialogTotal) }}</strong>
        <div class="metric-foot">
          <span>成功 {{ formatNumber(stats.successfulDialogs || 0) }}</span>
          <span>失败 {{ formatNumber(stats.failedDialogs || 0) }}</span>
        </div>
      </article>

      <article class="metric-card">
        <div class="metric-head">
          <Gauge class="metric-icon info" />
          <span>Token</span>
        </div>
        <strong>{{ formatNumber(stats.totalTokens || 0) }}</strong>
        <div class="metric-foot">
          <span>输入 {{ formatNumber(stats.promptTokens || 0) }}</span>
          <span>输出 {{ formatNumber(stats.completionTokens || 0) }}</span>
        </div>
      </article>

      <article class="metric-card">
        <div class="metric-head">
          <CircleDollarSign class="metric-icon warning" />
          <span>价值估算</span>
        </div>
        <strong>{{ formatMoney(stats.estimatedCost) }}</strong>
        <div class="metric-foot">
          <span>BYOK {{ formatNumber(stats.byokRequests || 0) }}</span>
          <span>失败请求 {{ formatNumber(stats.failedRequests || 0) }}</span>
        </div>
      </article>

      <article class="metric-card">
        <div class="metric-head">
          <DatabaseZap class="metric-icon primary" />
          <span>缓存统计</span>
        </div>
        <strong>{{ cacheHitRate }}%</strong>
        <div class="metric-foot">
          <span>读 {{ formatNumber(stats.cacheReadTokens || 0) }}</span>
          <span>写 {{ formatNumber(stats.cacheWriteTokens || 0) }}</span>
        </div>
      </article>
    </section>

    <section class="panels-grid">
      <article class="panel">
        <div class="panel-header">
          <div>
            <h3>服务控制</h3>
            <p>代理、Cursor 设置和最近错误</p>
          </div>
          <span class="state-pill" :class="{ active: proxyState.running }">
            {{ proxyState.running ? '运行中' : '已停止' }}
          </span>
        </div>

        <div class="detail-list">
          <div class="detail-row">
            <span>代理地址</span>
            <strong class="mono">{{ proxyState.address || '127.0.0.1:18080' }}</strong>
          </div>
          <div class="detail-row">
            <span>Cursor 上游</span>
            <strong class="mono">{{ proxyState.baseURL || 'https://api2.cursor.sh' }}</strong>
          </div>
          <div class="detail-row">
            <span>拦截请求</span>
            <strong>{{ formatNumber(stats.totalRequests || proxyState.requestCount || 0) }}</strong>
          </div>
          <div class="detail-row">
            <span>模型注入</span>
            <strong>{{ formatNumber(stats.availableModelPatch || 0) }}</strong>
          </div>
          <div class="detail-row">
            <span>Cursor 设置</span>
            <strong class="path-value" :title="proxyState.cursorPath">{{ proxyState.cursorPath || '-' }}</strong>
          </div>
          <div class="detail-row error-row" v-if="lastError">
            <span>最近错误</span>
            <strong>{{ lastError }}</strong>
          </div>
        </div>
      </article>

      <article class="panel">
        <div class="panel-header">
          <div>
            <h3>接入诊断</h3>
            <p>证书、Cursor 设置和系统状态</p>
          </div>
          <span class="state-pill" :class="{ active: trust.trusted, warning: !trust.trusted }">
            {{ trust.trusted ? 'CA 已信任' : 'CA 未信任' }}
          </span>
        </div>

        <div class="detail-list">
          <div class="detail-row">
            <span>已配置模型</span>
            <strong>{{ modelCount }} 个</strong>
          </div>
          <div class="detail-row">
            <span>证书存储</span>
            <strong>{{ trust.store || '-' }}</strong>
          </div>
          <div class="detail-row">
            <span>证书指纹</span>
            <strong class="mono">{{ shortThumbprint || '-' }}</strong>
          </div>
          <div class="detail-row">
            <span>Cursor 代理设置</span>
            <strong :class="{ 'error-row': proxyState.running && !diagnostics.cursorProxySet }">
              {{ diagnostics.cursorProxySet ? '已配置' : '未配置' }}
            </strong>
          </div>
          <div class="detail-row">
            <span>Cursor 配置路径</span>
            <strong class="path-value" :title="diagnostics.cursorConfigPath">{{ diagnostics.cursorConfigPath || '-' }}</strong>
          </div>
          <div class="detail-row">
            <span>日志目录</span>
            <strong class="path-value" :title="diagnostics.logDir">{{ diagnostics.logDir || '-' }}</strong>
          </div>
          <div class="detail-row">
            <span>数据目录</span>
            <strong class="path-value" :title="proxyState.dataDir">{{ proxyState.dataDir || '-' }}</strong>
          </div>
          <div class="detail-row error-row" v-if="trust.error">
            <span>证书错误</span>
            <strong>{{ trust.error }}</strong>
          </div>
        </div>

        <div class="panel-actions">
          <button class="btn btn-primary" @click="fixIssues" :disabled="loadingFix || !hasIssues">
            <Wrench class="icon-size" />
            {{ loadingFix ? '修复中' : '一键修复' }}
          </button>
          <button class="btn btn-secondary" @click="installCertificate" :disabled="loadingTrust">
            <ShieldCheck class="icon-size" />
            {{ loadingTrust ? '安装中' : '安装证书' }}
          </button>
          <button class="btn btn-secondary" @click="exportCertificate">
            <Download class="icon-size" />
            导出证书
          </button>
        </div>
      </article>
    </section>

    <section class="request-panel">
      <div class="panel-header request-header">
        <div>
          <h3>请求日志</h3>
          <p>最近 {{ requestLogs.length }} 条</p>
        </div>
        <button class="btn btn-sm btn-ghost" @click="clearLogs" :disabled="loadingLogs || requestLogs.length === 0">
          <Trash2 class="icon-size" />
          清空
        </button>
      </div>

      <div v-if="requestLogs.length === 0" class="empty-log">
        <FileSearch class="empty-icon" />
        <span>暂无请求记录</span>
      </div>

      <div v-else class="log-table-wrap">
        <table class="log-table">
          <thead>
            <tr>
              <th>时间</th>
              <th>路由</th>
              <th>状态</th>
              <th>耗时</th>
              <th>方法</th>
              <th>路径</th>
              <th>模型</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="entry in requestLogs" :key="entry.id || `${entry.time}-${entry.path}`">
              <td>{{ formatTime(entry.time) }}</td>
              <td>
                <span class="route-badge" :class="{ byok: entry.byok, handled: entry.handled }">
                  {{ entry.route || 'unknown' }}
                </span>
              </td>
              <td>
                <span class="status-code" :class="{ error: entry.statusCode >= 400 || entry.error }">
                  {{ entry.statusCode || '-' }}
                </span>
              </td>
              <td>{{ entry.durationMs || 0 }}ms</td>
              <td>{{ entry.method || '-' }}</td>
              <td>
                <div class="path-cell" :title="`${entry.host || ''}${entry.path || ''}`">
                  <span class="host-text">{{ entry.host || '-' }}</span>
                  <span>{{ entry.path || '-' }}</span>
                  <span v-if="entry.error" class="error-line">{{ entry.error }}</span>
                </div>
              </td>
              <td>{{ entry.model || '-' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import {
  AlertCircle,
  Check,
  CheckCircle2,
  CircleDollarSign,
  DatabaseZap,
  Download,
  FileSearch,
  Gauge,
  Info,
  MessageSquare,
  Power,
  RefreshCw,
  RotateCcw,
  ShieldCheck,
  Square,
  Trash2,
  Wrench
} from 'lucide-vue-next'
import { bridge } from '../services/bridge'

const emptyStats = () => ({
  totalRequests: 0,
  byokRequests: 0,
  successfulDialogs: 0,
  failedDialogs: 0,
  failedRequests: 0,
  availableModelPatch: 0,
  cacheHits: 0,
  cacheMisses: 0,
  cacheReadTokens: 0,
  cacheWriteTokens: 0,
  promptTokens: 0,
  completionTokens: 0,
  totalTokens: 0,
  estimatedCost: 0,
  lastModel: '',
  lastError: '',
  lastRequest: ''
})

const emptyTrust = () => ({
  trusted: false,
  installed: false,
  store: '',
  subject: '',
  thumbprint: '',
  error: ''
})

const proxyState = ref({
  running: false,
  address: '127.0.0.1:18080',
  baseURL: 'https://api2.cursor.sh',
  cursorPath: '',
  dataDir: '',
  lastError: '',
  stats: emptyStats(),
  trust: emptyTrust()
})

const diagnostics = ref({
  proxyRunning: false,
  proxyAddress: '127.0.0.1:18080',
  proxyStartedAt: '',
  caInstalled: false,
  caCertPath: '',
  caExpiresAt: '',
  cursorProxySet: false,
  cursorConfigPath: '',
  dataDir: '',
  logDir: '',
  lastRequestAt: '',
  lastErrorAt: '',
  lastErrorMessage: '',
  totalRequests: 0,
  totalErrors: 0
})

const modelCount = ref(0)
const requestLogs = ref([])
const loading = ref(false)
const loadingLogs = ref(false)
const loadingTrust = ref(false)
const loadingFix = ref(false)
const refreshing = ref(false)
const actionName = ref('')
const notice = ref({ type: '', message: '' })

const stats = computed(() => proxyState.value.stats || emptyStats())
const trust = computed(() => proxyState.value.trust || emptyTrust())
const hasIssues = computed(() => {
  return !diagnostics.value.caInstalled ||
         (proxyState.value.running && !diagnostics.value.cursorProxySet)
})
const dialogTotal = computed(() => {
  const successful = stats.value.successfulDialogs || 0
  const failed = stats.value.failedDialogs || 0
  return successful + failed || stats.value.byokRequests || 0
})
const cacheSampleCount = computed(() => (stats.value.cacheHits || 0) + (stats.value.cacheMisses || 0))
const cacheHitRate = computed(() => {
  if (!cacheSampleCount.value) return 0
  return Math.round(((stats.value.cacheHits || 0) / cacheSampleCount.value) * 100)
})
const shortThumbprint = computed(() => {
  const value = trust.value.thumbprint || ''
  if (value.length <= 18) return value
  return `${value.slice(0, 8)}...${value.slice(-8)}`
})
const lastError = computed(() => proxyState.value.lastError || stats.value.lastError || '')
const noticeIcon = computed(() => {
  if (notice.value.type === 'error') return AlertCircle
  if (notice.value.type === 'success') return CheckCircle2
  return Info
})

const formatNumber = (num) => Number(num || 0).toLocaleString()
const formatMoney = (num) => `$${Number(num || 0).toFixed(4)}`
const wait = (ms) => new Promise((resolve) => window.setTimeout(resolve, ms))

const formatTime = (value) => {
  const date = parseDate(value)
  if (!date) return '-'
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

const formatDateTime = (value) => {
  const date = parseDate(value)
  if (!date) return '-'
  return date.toLocaleString([], { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}

const parseDate = (value) => {
  if (!value) return null
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return null
  return date
}

const showNotice = (message, type = 'info') => {
  notice.value = { message, type }
  window.clearTimeout(showNotice.timer)
  showNotice.timer = window.setTimeout(() => {
    notice.value = { type: '', message: '' }
  }, 4200)
}

const normalizeState = (state) => ({
  ...state,
  stats: state?.stats || emptyStats(),
  trust: state?.trust || emptyTrust()
})

const loadState = async () => {
  const state = await bridge.getState()
  proxyState.value = normalizeState(state)
}

const loadDiagnostics = async () => {
  try {
    const diag = await bridge.getDiagnostics()
    diagnostics.value = diag || diagnostics.value
  } catch (err) {
    console.warn('[Dashboard] Failed to load diagnostics:', err)
  }
}

const loadRequestLogs = async () => {
  requestLogs.value = await bridge.listRequestLogs()
}

const loadModels = async () => {
  const adapters = await bridge.listModelAdapters()
  modelCount.value = adapters.length
}

const refreshAll = async () => {
  refreshing.value = true
  const minimumVisible = wait(720)
  try {
    await Promise.all([loadState(), loadDiagnostics(), loadModels(), loadRequestLogs()])
  } catch (err) {
    showNotice(`刷新失败：${err.message}`, 'error')
  } finally {
    await minimumVisible
    refreshing.value = false
  }
}

const startProxy = async () => {
  loading.value = true
  actionName.value = 'start'
  try {
    const state = await bridge.startProxy()
    proxyState.value = normalizeState(state)
    showNotice('代理服务已启动', 'success')
    await refreshAll()
  } catch (err) {
    showNotice(`启动失败：${err.message}`, 'error')
  } finally {
    loading.value = false
    actionName.value = ''
  }
}

const stopProxy = async () => {
  loading.value = true
  actionName.value = 'stop'
  try {
    const state = await bridge.stopProxy()
    proxyState.value = normalizeState(state)
    showNotice('代理服务已停止', 'success')
  } catch (err) {
    showNotice(`停止失败：${err.message}`, 'error')
  } finally {
    loading.value = false
    actionName.value = ''
  }
}

const restoreCursor = async () => {
  loading.value = true
  actionName.value = 'restore'
  const minimumVisible = wait(720)
  try {
    const state = await bridge.restoreCursorSettings()
    proxyState.value = normalizeState(state)
    showNotice('已恢复 Cursor 官方通道', 'success')
  } catch (err) {
    showNotice(`恢复失败：${err.message}`, 'error')
  } finally {
    await minimumVisible
    loading.value = false
    actionName.value = ''
  }
}

const fixIssues = async () => {
  loadingFix.value = true
  try {
    const options = {
      fixCATrust: !diagnostics.value.caInstalled,
      fixCursorProxy: proxyState.value.running && !diagnostics.value.cursorProxySet,
      clearStatsigCache: true,
      clearAdminCache: true,
      restoreOfficial: false
    }

    const result = await bridge.fixIssues(options)

    // Refresh diagnostics and proxy state
    await Promise.all([
      loadDiagnostics(),
      loadState()
    ])

    // Show result
    const fixedCount = result.fixedIssues.filter(i => i.status === 'fixed').length
    const failedCount = result.failedIssues.length

    if (result.success && fixedCount > 0) {
      showNotice(`成功修复 ${fixedCount} 个问题`, 'success')
    } else if (failedCount > 0) {
      showNotice(`修复失败：${result.failedIssues[0].error}`, 'error')
    } else {
      showNotice('所有检查项均正常', 'success')
    }
  } catch (err) {
    console.error('[Dashboard] Fix issues failed:', err)
    showNotice(`修复失败：${err.message || err}`, 'error')
  } finally {
    loadingFix.value = false
  }
}

const installCertificate = async () => {
  loadingTrust.value = true
  try {
    const status = await bridge.ensureCACertificateTrusted()
    proxyState.value = { ...proxyState.value, trust: status || emptyTrust() }
    showNotice(status?.trusted ? 'CA 证书已信任' : `证书安装未完成：${status?.error || '未知状态'}`, status?.trusted ? 'success' : 'error')
  } catch (err) {
    showNotice(`证书安装失败：${err.message}`, 'error')
  } finally {
    loadingTrust.value = false
  }
}

const exportCertificate = async () => {
  try {
    const cert = await bridge.getCACertificate()
    const blob = new Blob([cert], { type: 'application/x-pem-file' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'cursor-assistant-ca.pem'
    a.click()
    URL.revokeObjectURL(url)
    showNotice('CA 证书已导出', 'success')
  } catch (err) {
    showNotice(`导出失败：${err.message}`, 'error')
  }
}

const clearLogs = async () => {
  loadingLogs.value = true
  try {
    await bridge.clearRequestLogs()
    requestLogs.value = []
    showNotice('请求日志已清空', 'success')
  } catch (err) {
    showNotice(`清空失败：${err.message}`, 'error')
  } finally {
    loadingLogs.value = false
  }
}

let refreshTimer = null

onMounted(() => {
  refreshAll()
  refreshTimer = window.setInterval(() => {
    Promise.allSettled([loadState(), loadDiagnostics(), loadRequestLogs()])
  }, 3000)
})

onUnmounted(() => {
  if (refreshTimer) window.clearInterval(refreshTimer)
  window.clearTimeout(showNotice.timer)
})
</script>

<style scoped>
.dashboard {
  width: min(1240px, 100%);
  margin: 0 auto;
  padding: 28px 28px 48px;
  animation: soft-enter var(--motion-slow) var(--ease-standard) both;
}

.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 18px;
  margin-bottom: 18px;
}

.eyebrow {
  display: none;
}

.page-header h2 {
  margin: 0;
  color: var(--text);
  font-size: 28px;
  font-weight: 760;
  letter-spacing: 0;
}

.header-actions,
.panel-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  justify-content: flex-end;
}

.spinning {
  animation: icon-spin 0.8s linear infinite !important;
  transform-box: fill-box;
  transform-origin: center;
  will-change: transform;
}

.state-action {
  min-width: 74px;
}

.state-action.working {
  filter: saturate(1.1);
}

.state-action.working .icon-size {
  animation: icon-pulse 0.9s ease-in-out infinite;
}

.notice {
  display: flex;
  align-items: center;
  gap: 9px;
  min-height: 38px;
  margin-bottom: 16px;
  padding: 9px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--panel);
  color: var(--text-muted);
  box-shadow: var(--shadow-soft);
  animation: soft-pop var(--motion-medium) var(--ease-standard) both;
}

.notice.success {
  border-color: var(--success-border);
  background: var(--success-bg);
  color: var(--success-text);
}

.notice.error {
  border-color: var(--danger-border);
  background: var(--danger-bg);
  color: var(--danger-text);
}

.notice-icon {
  width: 17px;
  height: 17px;
  flex: 0 0 auto;
}

.service-band {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  min-height: 0;
  margin-bottom: 14px;
  padding: 18px 20px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--panel);
  box-shadow: var(--shadow-soft);
  animation: soft-enter var(--motion-slow) var(--ease-standard) both;
  transition:
    border-color var(--motion-medium) var(--ease-standard),
    box-shadow var(--motion-medium) var(--ease-standard),
    transform var(--motion-medium) var(--ease-standard);
}

.service-band:hover {
  transform: translateY(-1px);
  box-shadow: var(--shadow);
}

.service-main {
  display: flex;
  align-items: center;
  gap: 14px;
  min-width: 0;
}

.status-orb {
  display: grid;
  width: 46px;
  height: 46px;
  flex: 0 0 auto;
  place-items: center;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: rgba(118, 118, 128, 0.08);
  color: var(--text-muted);
  transition:
    background var(--motion-medium) var(--ease-standard),
    border-color var(--motion-medium) var(--ease-standard),
    color var(--motion-medium) var(--ease-standard),
    box-shadow var(--motion-medium) var(--ease-standard),
    transform var(--motion-medium) var(--ease-spring);
}

.status-orb.active {
  border-color: var(--success-border);
  background: var(--success-bg);
  color: var(--color-success);
  box-shadow: 0 0 0 5px rgba(52, 199, 89, 0.08);
  animation: orb-breathe 2.4s ease-in-out infinite;
}

.status-orb.warning:not(.active) {
  border-color: var(--warning-border);
  background: var(--warning-bg);
  color: var(--color-warning);
}

.status-icon {
  width: 22px;
  height: 22px;
  stroke-width: 2.6;
}

.service-title-row {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.service-title {
  color: var(--text);
  font-size: 20px;
  font-weight: 760;
}

.service-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 4px;
  color: var(--text-muted);
  font-size: 13px;
}

.service-side {
  display: grid;
  grid-template-columns: repeat(2, minmax(120px, 1fr));
  gap: 10px;
  min-width: 300px;
}

.service-side div {
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--panel-muted);
  transition:
    background var(--motion-fast) var(--ease-standard),
    transform var(--motion-fast) var(--ease-standard);
}

.service-side div:hover {
  background: rgba(255, 255, 255, 0.82);
  transform: translateY(-1px);
}

.service-side span,
.detail-row span,
.metric-head span,
.panel-header p {
  color: var(--text-muted);
  font-size: 12px;
}

.service-side strong {
  display: block;
  margin-top: 3px;
  color: var(--text);
  font-size: 13px;
  overflow-wrap: anywhere;
}

.metrics-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 14px;
}

.metric-card,
.panel,
.request-panel {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--panel);
  box-shadow: var(--shadow-soft);
}

.metric-card {
  min-width: 0;
  min-height: 116px;
  padding: 16px;
  animation: soft-enter var(--motion-slow) var(--ease-standard) both;
  transition:
    transform var(--motion-medium) var(--ease-spring),
    box-shadow var(--motion-medium) var(--ease-standard),
    border-color var(--motion-medium) var(--ease-standard),
    background var(--motion-medium) var(--ease-standard);
}

.metric-card:hover {
  transform: translateY(-3px);
  box-shadow: var(--shadow);
  border-color: rgba(0, 122, 255, 0.18);
}

.metric-card:nth-child(2) {
  animation-delay: 45ms;
}

.metric-card:nth-child(3) {
  animation-delay: 90ms;
}

.metric-card:nth-child(4) {
  animation-delay: 135ms;
}

.metric-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 10px;
}

.metric-icon {
  width: 32px;
  height: 32px;
  padding: 7px;
  border-radius: 999px;
  background: rgba(0, 122, 255, 0.09);
  transition: transform var(--motion-medium) var(--ease-spring);
}

.metric-card:hover .metric-icon {
  transform: scale(1.08) rotate(-2deg);
}

.metric-icon.success {
  color: var(--color-success);
}

.metric-icon.info {
  color: var(--color-info);
}

.metric-icon.warning {
  color: var(--color-warning);
}

.metric-icon.primary {
  color: var(--color-primary);
}

.metric-card strong {
  display: block;
  color: var(--text);
  margin: 0;
  font-size: 24px;
  font-weight: 760;
  transition: color var(--motion-fast) var(--ease-standard);
}

.metric-foot {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 12px;
  margin-top: 5px;
  color: var(--text-muted);
  font-size: 12px;
}

.panels-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 14px;
  margin-bottom: 14px;
}

.panel {
  padding: 0;
  overflow: hidden;
  animation: soft-enter var(--motion-slow) var(--ease-standard) both;
  transition:
    transform var(--motion-medium) var(--ease-standard),
    box-shadow var(--motion-medium) var(--ease-standard),
    border-color var(--motion-medium) var(--ease-standard);
}

.panel:hover {
  transform: translateY(-1px);
  box-shadow: var(--shadow);
  border-color: rgba(60, 60, 67, 0.2);
}

.panel-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 0;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border);
}

.panel-header h3 {
  margin: 0;
  color: var(--text);
  font-size: 16px;
  font-weight: 720;
}

.panel-header p {
  margin: 3px 0 0;
}

.state-pill {
  display: inline-flex;
  align-items: center;
  min-height: 24px;
  padding: 3px 8px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: rgba(118, 118, 128, 0.08);
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 650;
  white-space: nowrap;
}

.state-pill.active {
  border-color: var(--success-border);
  background: var(--success-bg);
  color: var(--success-text);
}

.state-pill.warning {
  border-color: var(--warning-border);
  background: var(--warning-bg);
  color: var(--warning-text);
}

.detail-list {
  display: grid;
  gap: 0;
  overflow: hidden;
  border: 0;
  border-radius: 0;
  background: transparent;
}

.detail-row {
  display: grid;
  grid-template-columns: 112px minmax(0, 1fr);
  gap: 12px;
  align-items: center;
  min-height: 38px;
  padding: 9px 16px;
  border-bottom: 1px solid var(--border);
  background: transparent;
}

.detail-row:last-child {
  border-bottom: 0;
}

.detail-row strong {
  color: var(--text);
  font-size: 12px;
  font-weight: 600;
  text-align: right;
  overflow-wrap: anywhere;
}

.path-value {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.error-row strong,
.status-code.error,
.error-line {
  color: var(--danger-text);
}

.panel-actions {
  justify-content: flex-start;
  margin-top: 0;
  padding: 14px 16px 16px;
  border-top: 1px solid var(--border);
}

.request-panel {
  padding: 0;
  overflow: hidden;
  animation: soft-enter var(--motion-slow) var(--ease-standard) 90ms both;
}

.request-header {
  align-items: center;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border);
}

.empty-log {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  min-height: 118px;
  color: var(--text-muted);
  background: transparent;
}

.empty-icon {
  width: 18px;
  height: 18px;
}

.log-table-wrap {
  overflow: auto;
  border: 0;
  border-radius: 0;
  background: transparent;
}

.log-table {
  width: 100%;
  min-width: 920px;
  border-collapse: collapse;
}

.log-table th,
.log-table td {
  padding: 9px 16px;
  border-bottom: 1px solid var(--border);
  color: var(--text-muted);
  font-size: 12px;
  text-align: left;
  vertical-align: top;
}

.log-table th {
  position: sticky;
  top: 0;
  background: rgba(248, 248, 250, 0.92);
  color: var(--text-subtle);
  font-weight: 700;
}

.log-table tbody tr:last-child td {
  border-bottom: 0;
}

.log-table tbody tr:hover td {
  background: rgba(0, 122, 255, 0.035);
}

.log-table tbody td {
  transition: background var(--motion-fast) var(--ease-standard);
}

@keyframes orb-breathe {
  0%,
  100% {
    transform: scale(1);
  }
  50% {
    transform: scale(1.035);
  }
}

@keyframes icon-spin {
  to {
    transform: rotate(360deg);
  }
}

@keyframes icon-pulse {
  0%,
  100% {
    transform: scale(1);
    opacity: 1;
  }
  50% {
    transform: scale(0.86);
    opacity: 0.78;
  }
}

.route-badge {
  display: inline-flex;
  max-width: 128px;
  min-height: 22px;
  align-items: center;
  padding: 2px 7px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: rgba(118, 118, 128, 0.08);
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.route-badge.handled {
  border-color: var(--info-border);
  background: var(--info-bg);
  color: var(--info-text);
}

.route-badge.byok {
  border-color: var(--success-border);
  background: var(--success-bg);
  color: var(--success-text);
}

.status-code {
  color: var(--text);
}

.path-cell {
  display: grid;
  gap: 2px;
  max-width: 560px;
}

.host-text {
  color: var(--text);
}

.error-line {
  overflow-wrap: anywhere;
}

@media (max-width: 1120px) {
  .metrics-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .panels-grid,
  .service-band {
    grid-template-columns: 1fr;
  }

  .service-band {
    align-items: stretch;
    flex-direction: column;
  }

  .service-side {
    min-width: 0;
  }
}

@media (max-width: 760px) {
  .dashboard {
    padding: 16px;
  }

  .page-header {
    flex-direction: column;
    align-items: stretch;
  }

  .header-actions {
    justify-content: flex-start;
  }

  .metrics-grid,
  .panels-grid,
  .service-side {
    grid-template-columns: 1fr;
  }

  .detail-row {
    grid-template-columns: 1fr;
    gap: 3px;
  }

  .detail-row strong {
    text-align: left;
  }
}
</style>
