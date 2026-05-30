<template>
  <div class="stage">
    <div class="app-window">
      <aside class="sidebar">
        <div class="logo">
          <div class="brand-mark" aria-hidden="true">
            <Network class="brand-icon" />
          </div>
          <div>
            <h1>Cursor 助手</h1>
            <p class="subtitle">本地 BYOK Agent</p>
          </div>
        </div>

        <nav class="nav">
          <router-link to="/" class="nav-item">
            <BarChart3 class="nav-icon" />
            <span>仪表盘</span>
          </router-link>
          <router-link to="/models" class="nav-item">
            <Box class="nav-icon" />
            <span>模型配置</span>
          </router-link>
        </nav>

        <div class="sidebar-spacer"></div>

        <div class="side-status">
          <div class="side-status-item">
            <span class="status-dot" :class="{ active: proxyRunning }"></span>
            <div>
              <strong>{{ proxyRunning ? '本地代理运行中' : '本地代理已停止' }}</strong>
              <small>{{ proxyAddress }}</small>
            </div>
          </div>
          <div class="side-status-item">
            <ShieldCheck class="side-icon" />
            <strong>{{ trustTrusted ? 'CA 已信任' : 'CA 未信任' }}</strong>
          </div>
          <div class="side-status-item">
            <Layers class="side-icon" />
            <strong>{{ modelCount }} 个模型</strong>
          </div>
        </div>

      </aside>

      <main class="main-content">
        <router-view v-slot="{ Component, route }">
          <Transition name="page-fade" mode="out-in">
            <component :is="Component" :key="route.name" />
          </Transition>
        </router-view>
      </main>
    </div>
  </div>
</template>

<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { BarChart3, Box, Layers, Network, ShieldCheck } from 'lucide-vue-next'
import { bridge } from './services/bridge'

const proxyRunning = ref(false)
const proxyAddress = ref('127.0.0.1:18080')
const trustTrusted = ref(false)
const modelCount = ref(0)

const loadSidebarState = async () => {
  try {
    const [state, models] = await Promise.all([bridge.getState(), bridge.listModelAdapters()])
    proxyRunning.value = Boolean(state?.running)
    proxyAddress.value = state?.address || '127.0.0.1:18080'
    trustTrusted.value = Boolean(state?.trust?.trusted)
    modelCount.value = Array.isArray(models) ? models.length : 0
  } catch (err) {
    console.warn('Failed to load sidebar state:', err)
  }
}

let sidebarTimer = null

onMounted(() => {
  loadSidebarState()
  sidebarTimer = window.setInterval(loadSidebarState, 5000)
})

onUnmounted(() => {
  if (sidebarTimer) window.clearInterval(sidebarTimer)
})
</script>

<style scoped>
.stage {
  min-height: 100vh;
  background: #f4f5f7;
}

.app-window {
  display: flex;
  height: 100vh;
  min-width: 0;
  overflow: hidden;
  border: 1px solid rgba(60, 60, 67, 0.28);
  border-radius: 0;
  background: var(--bg);
}

.sidebar {
  width: 232px;
  flex: 0 0 232px;
  background: var(--sidebar);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  backdrop-filter: blur(18px) saturate(1.15);
}

.logo {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 22px 18px 18px;
}

.brand-mark {
  display: grid;
  width: 40px;
  height: 40px;
  place-items: center;
  border: 1px solid rgba(255, 255, 255, 0.18);
  border-radius: 12px;
  background:
    radial-gradient(circle at 35% 28%, rgba(255, 255, 255, 0.28), transparent 30%),
    linear-gradient(145deg, #343843, #090a0d);
  color: #ffffff;
  box-shadow: 0 8px 18px rgba(0, 0, 0, 0.16);
  transition: transform var(--motion-medium) var(--ease-spring);
}

.logo:hover .brand-mark {
  transform: scale(1.04) rotate(-1deg);
}

.brand-icon {
  width: 22px;
  height: 22px;
}

.logo h1 {
  margin: 0;
  font-size: 17px;
  font-weight: 750;
  color: var(--text);
  letter-spacing: 0;
}

.subtitle {
  margin: 2px 0 0;
  font-size: 12px;
  color: var(--text-muted);
}

.nav {
  padding: 0 12px;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 36px;
  padding: 0 12px;
  margin-bottom: 4px;
  border-radius: 8px;
  color: var(--text-muted);
  text-decoration: none;
  transition:
    background var(--motion-fast) var(--ease-standard),
    color var(--motion-fast) var(--ease-standard),
    transform var(--motion-fast) var(--ease-standard),
    box-shadow var(--motion-fast) var(--ease-standard);
  font-size: 13px;
  font-weight: 650;
}

.nav-item:hover {
  background: rgba(118, 118, 128, 0.1);
  color: var(--text);
  transform: translateX(2px);
}

.nav-item.router-link-active {
  background: rgba(0, 122, 255, 0.13);
  color: var(--color-primary);
  box-shadow: inset 0 0 0 1px rgba(0, 122, 255, 0.08);
}

.nav-icon {
  width: 17px;
  height: 17px;
}

.sidebar-spacer {
  flex: 1;
}

.side-status {
  display: grid;
  gap: 10px;
  margin: 0 14px 16px;
  padding: 12px;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: rgba(255, 255, 255, 0.48);
}

.side-status::before {
  content: "当前状态";
  color: var(--text-subtle);
  font-size: 11px;
  font-weight: 700;
}

.side-status-item {
  display: flex;
  align-items: center;
  gap: 10px;
  color: var(--text-muted);
}

.side-status-item strong {
  color: var(--text);
  font-size: 12px;
  font-weight: 620;
}

.side-status-item small {
  display: block;
  margin-top: 1px;
  color: var(--text-subtle);
  font-size: 11px;
}

.status-dot {
  width: 7px;
  height: 7px;
  flex: 0 0 auto;
  border-radius: 999px;
  background: var(--text-subtle);
}

.status-dot.active {
  background: var(--color-success);
  box-shadow: 0 0 0 3px var(--success-bg);
  animation: status-pulse 2.2s ease-out infinite;
}

.side-icon {
  width: 16px;
  height: 16px;
  color: #34a853;
}

.main-content {
  flex: 1;
  min-width: 0;
  overflow-y: auto;
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.58), rgba(245, 245, 247, 0) 160px),
    var(--bg);
}

.page-fade-enter-active,
.page-fade-leave-active {
  transition:
    opacity var(--motion-medium) var(--ease-standard),
    transform var(--motion-medium) var(--ease-standard),
    filter var(--motion-medium) var(--ease-standard);
}

.page-fade-enter-from {
  opacity: 0;
  transform: translateY(8px) scale(0.992);
  filter: blur(2px);
}

.page-fade-leave-to {
  opacity: 0;
  transform: translateY(-4px) scale(0.996);
  filter: blur(1px);
}

@keyframes status-pulse {
  0% {
    box-shadow: 0 0 0 0 rgba(52, 199, 89, 0.28);
  }
  70% {
    box-shadow: 0 0 0 7px rgba(52, 199, 89, 0);
  }
  100% {
    box-shadow: 0 0 0 0 rgba(52, 199, 89, 0);
  }
}

@media (max-width: 760px) {
  .app-window {
    flex-direction: column;
    border-radius: 0;
  }

  .sidebar {
    width: 100%;
    flex: 0 0 auto;
  }

  .logo {
    padding: 12px 14px 8px;
  }

  .nav {
    display: flex;
    gap: 6px;
    padding: 8px 10px 10px;
    overflow-x: auto;
  }

  .nav-item {
    flex: 0 0 auto;
    justify-content: center;
    min-width: 96px;
  }

  .side-status {
    display: none;
  }
}
</style>
