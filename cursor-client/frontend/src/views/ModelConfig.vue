<template>
  <div class="model-config">
    <header class="page-header">
      <div>
        <div class="eyebrow">模型</div>
        <h2>模型配置</h2>
        <p>保存后会注入 Cursor 模型选择列表，默认使用本地 BYOK Agent 路由。</p>
      </div>
      <button class="btn btn-primary" @click="openCreate">
        <Plus class="icon-size" />
        新增模型
      </button>
    </header>

    <div v-if="notice.message" class="notice" :class="notice.type">
      <component :is="noticeIcon" class="notice-icon" />
      <span>{{ notice.message }}</span>
    </div>

    <section v-if="models.length === 0" class="empty-state">
      <Bot class="empty-icon" />
      <div>
        <h3>还没有模型</h3>
        <p>先添加一个 OpenAI 或 Anthropic 兼容模型。</p>
      </div>
      <button class="btn btn-primary" @click="openCreate">新增模型配置</button>
    </section>

    <section v-else class="models-grid">
      <article v-for="model in models" :key="model.catalogID || model.modelID" class="model-card">
        <div class="model-card-head">
          <div class="model-title">
            <div class="provider-icon" :class="model.type">
              <span v-if="model.type === 'anthropic'" class="ai-mark">AI</span>
              <Bot v-else />
            </div>
            <div>
              <div class="model-meta">
                <strong>{{ providerLabel(model.type) }}</strong>
                <span class="provider-pill" :class="model.type">启用</span>
              </div>
            </div>
          </div>
          <span class="enabled-pill">已注入</span>
        </div>

        <h3>{{ model.displayName || model.modelID }}</h3>
        <div class="model-id mono">{{ model.modelID }}</div>

        <div class="model-details">
          <div>
            <span>Base URL</span>
            <strong>{{ model.baseURL || '-' }}</strong>
          </div>
          <div>
            <span>Endpoint</span>
            <strong>{{ model.endpoint || defaultEndpoint(model.type) }}</strong>
          </div>
          <div>
            <span>推理强度</span>
            <strong>{{ displayReasoning(model) }}</strong>
          </div>
          <div>
            <span>Cursor ID</span>
            <strong>{{ model.catalogID || model.cursorModelID || '-' }}</strong>
          </div>
        </div>

        <div class="model-actions">
          <div class="left-actions">
            <button class="btn btn-secondary outline-blue" @click="editModel(model)">
              <Pencil class="icon-size" />
              编辑
            </button>
            <button class="btn btn-danger outline-red" @click="deleteModel(model.modelID)">
              <Trash2 class="icon-size" />
              删除
            </button>
          </div>
        </div>
      </article>
    </section>

    <Teleport to="body">
      <Transition name="modal-fade">
        <div v-if="showEditor" class="modal-overlay" @click="closeEditor">
          <section class="modal" @click.stop>
            <header class="modal-header">
              <div>
                <div class="modal-kicker">模型编辑</div>
                <h3>{{ editingModel ? '编辑模型配置' : '新增模型配置' }}</h3>
              </div>
              <div class="modal-actions">
                <button class="btn btn-ghost" @click="closeEditor">取消</button>
                <button class="btn btn-secondary" @click="saveAndTest" :disabled="saving || testing">
                  <FlaskConical class="icon-size" />
                  {{ testing ? '测试中' : '保存并测试' }}
                </button>
                <button class="btn btn-primary" @click="saveModel" :disabled="saving">
                  <Save class="icon-size" />
                  {{ saving ? '保存中' : '保存' }}
                </button>
                <button class="icon-btn" @click="closeEditor" aria-label="关闭">
                  <X class="icon-size" />
                </button>
              </div>
            </header>

            <div class="modal-body">
              <div class="provider-tabs">
                <button
                  class="provider-tab openai"
                  :class="{ active: formData.type === 'openai' }"
                  @click="selectProvider('openai')"
                >
                  <Bot class="icon-size" />
                  OpenAI
                </button>
                <button
                  class="provider-tab anthropic"
                  :class="{ active: formData.type === 'anthropic' }"
                  @click="selectProvider('anthropic')"
                >
                  <Sparkles class="icon-size" />
                  Anthropic
                </button>
              </div>

              <div class="form-section">
                <div class="section-title">
                  <h4>基础配置</h4>
                  <span>普通用户只需要填写这几项</span>
                </div>

                <div class="form-grid">
                  <label class="field">
                    <span>显示名称</span>
                    <input v-model.trim="formData.displayName" type="text" placeholder="例如：GPT-5.5" />
                  </label>

                  <label class="field">
                    <span>模型标识</span>
                    <input v-model.trim="formData.modelID" type="text" placeholder="例如：gpt-5.5" />
                  </label>

                  <label class="field">
                    <span>接口域名</span>
                    <input v-model.trim="formData.baseURL" type="text" :placeholder="defaultBaseURL(formData.type)" />
                  </label>

                  <label class="field">
                    <span>访问密钥</span>
                    <div class="password-wrap">
                      <input
                        v-model.trim="formData.apiKey"
                        :type="showKey ? 'text' : 'password'"
                        placeholder="sk-..."
                      />
                      <button type="button" class="btn btn-sm btn-secondary" @click="showKey = !showKey">
                        {{ showKey ? '隐藏' : '显示' }}
                      </button>
                    </div>
                  </label>

                  <label class="field">
                    <span>{{ formData.type === 'anthropic' ? '思考强度' : '推理强度' }}</span>
                    <select v-model="reasoningLevel">
                      <option value="low">低</option>
                      <option value="medium">中</option>
                      <option value="high">高</option>
                      <option v-if="formData.type === 'openai'" value="xhigh">极高</option>
                      <option v-if="formData.type === 'anthropic'" value="max">最大</option>
                    </select>
                  </label>

                  <label class="field">
                    <span>接口端点</span>
                    <select v-model="formData.endpoint">
                      <option :value="defaultEndpoint(formData.type)">{{ defaultEndpoint(formData.type) }}</option>
                      <option v-if="formData.type === 'openai'" value="/v1/chat/completions">/v1/chat/completions</option>
                    </select>
                  </label>
                </div>
              </div>

              <div class="advanced-box">
                <button class="advanced-toggle" @click="advancedOpen = !advancedOpen">
                  <ChevronDown class="icon-size" :class="{ rotated: advancedOpen }" />
                  高级参数
                </button>

                <Transition name="expand">
                  <div v-if="advancedOpen" class="form-grid advanced-grid">
                    <label class="field">
                      <span>上下文窗口</span>
                      <input v-model.number="formData.contextWindow" type="number" placeholder="留空使用默认值" />
                    </label>

                    <label class="field">
                      <span>最大输出 Token</span>
                      <input v-model.number="formData.maxTokens" type="number" placeholder="留空使用默认值" />
                    </label>

                    <label class="field">
                      <span>输入价格 / 1M</span>
                      <input v-model.number="formData.inputPricePer1M" type="number" step="0.000001" placeholder="用于费用估算" />
                    </label>

                    <label class="field">
                      <span>输出价格 / 1M</span>
                      <input v-model.number="formData.outputPricePer1M" type="number" step="0.000001" placeholder="用于费用估算" />
                    </label>

                    <div v-if="formData.type === 'openai'" class="field full-row">
                      <div class="field-title-row">
                        <span>额外参数 JSON</span>
                        <label class="inline-check">
                          <input v-model="extraParamsEnabled" type="checkbox" />
                          启用
                        </label>
                      </div>
                      <textarea v-model.trim="extraParamsJSON" spellcheck="false" placeholder="{&#10;  &quot;service_tier&quot;: &quot;priority&quot;&#10;}"></textarea>
                    </div>

                    <label class="field full-row">
                      <span>备注</span>
                      <textarea v-model.trim="note" placeholder="备注"></textarea>
                    </label>
                  </div>
                </Transition>
              </div>

              <div class="test-box" :class="{ success: testState === 'success', error: testState === 'error' }">
                <div>
                  <span>模型测试</span>
                  <strong>{{ testMessage || '尚未测试' }}</strong>
                </div>
              </div>
            </div>
          </section>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import {
  AlertCircle,
  Bot,
  CheckCircle2,
  ChevronDown,
  FlaskConical,
  Info,
  Pencil,
  Plus,
  Save,
  Sparkles,
  Trash2,
  X
} from 'lucide-vue-next'
import { bridge } from '../services/bridge'

const models = ref([])
const showEditor = ref(false)
const editingModel = ref(null)
const saving = ref(false)
const testing = ref(false)
const showKey = ref(false)
const advancedOpen = ref(false)
const note = ref('')
const reasoningLevel = ref('medium')
const extraParamsEnabled = ref(false)
const extraParamsJSON = ref('')
const testState = ref('')
const testMessage = ref('')
const notice = ref({ type: '', message: '' })

const newFormData = () => ({
  displayName: '',
  type: 'openai',
  modelID: '',
  cursorModelID: '',
  baseURL: '',
  apiKey: '',
  endpoint: '/v1/responses',
  maxTokens: 0,
  contextWindow: 0,
  temperature: 0.7,
  inputPricePer1M: 0,
  outputPricePer1M: 0,
  supportsThinking: true,
  supportsImages: false,
  supportsCmdK: true,
  supportsSandboxing: false,
  note: '',
  thinkingLevel: 'medium',
  extraParamsEnabled: false,
  extraParamsJSON: ''
})

const formData = ref(newFormData())
const noticeIcon = computed(() => {
  if (notice.value.type === 'error') return AlertCircle
  if (notice.value.type === 'success') return CheckCircle2
  return Info
})

const providerLabel = (type) => type === 'anthropic' ? 'Anthropic' : 'OpenAI'
const defaultBaseURL = (type) => type === 'anthropic' ? 'https://api.anthropic.com' : 'https://api.openai.com'
const defaultEndpoint = (type) => type === 'anthropic' ? '/v1/messages' : '/v1/responses'

const showNotice = (message, type = 'info') => {
  notice.value = { message, type }
  window.clearTimeout(showNotice.timer)
  showNotice.timer = window.setTimeout(() => {
    notice.value = { type: '', message: '' }
  }, 4200)
}

const loadModels = async () => {
  try {
    models.value = await bridge.listModelAdapters()
  } catch (err) {
    showNotice(`加载模型失败：${err.message}`, 'error')
  }
}

const openCreate = () => {
  editingModel.value = null
  formData.value = newFormData()
  note.value = ''
  reasoningLevel.value = 'medium'
  extraParamsEnabled.value = false
  extraParamsJSON.value = ''
  testState.value = ''
  testMessage.value = ''
  showKey.value = false
  advancedOpen.value = false
  showEditor.value = true
}

const editModel = (model) => {
  editingModel.value = model
  formData.value = {
    ...newFormData(),
    ...model,
    endpoint: model.endpoint || defaultEndpoint(model.type)
  }
  reasoningLevel.value = normalizeReasoningLevelForProvider(model.type, model.thinkingLevel || (model.supportsThinking ? 'medium' : 'low'))
  note.value = model.note || ''
  extraParamsEnabled.value = Boolean(model.extraParamsEnabled)
  extraParamsJSON.value = model.extraParamsJSON || ''
  testState.value = ''
  testMessage.value = ''
  showKey.value = false
  advancedOpen.value = Boolean(model.contextWindow || model.maxTokens || model.extraParamsEnabled || model.note)
  showEditor.value = true
}

const selectProvider = (type) => {
  const previousType = formData.value.type
  formData.value.type = type
  if (!formData.value.baseURL || formData.value.baseURL === defaultBaseURL(previousType)) {
    formData.value.baseURL = ''
  }
  formData.value.endpoint = defaultEndpoint(type)
  if (type === 'openai' && reasoningLevel.value === 'max') {
    reasoningLevel.value = 'xhigh'
  }
  if (type === 'anthropic' && reasoningLevel.value === 'xhigh') {
    reasoningLevel.value = 'max'
  }
}

const normalizeReasoningLevelForProvider = (type, level) => {
  if (type === 'anthropic' && ['xhigh', 'very_high', 'x_high', 'x-high'].includes(level)) {
    return 'max'
  }
  if (type === 'openai' && level === 'max') {
    return 'xhigh'
  }
  return level || 'medium'
}

const displayReasoning = (model) => {
  const level = normalizeReasoningLevelForProvider(model.type, model.thinkingLevel || 'medium')
  const map = { low: '低', medium: '中', high: '高', xhigh: '极高', max: '最大' }
  return map[level] || level
}

const normalizeBaseURL = (value, type) => {
  let base = (value || '').trim()
  if (!base) return defaultBaseURL(type)
  if (!/^https?:\/\//i.test(base)) {
    base = `https://${base}`
  }
  return base.replace(/\/+$/, '')
}

const buildPayload = () => {
  const payload = { ...formData.value }
  payload.type = payload.type || 'openai'
  payload.baseURL = normalizeBaseURL(payload.baseURL, payload.type)
  payload.endpoint = payload.endpoint || defaultEndpoint(payload.type)
  payload.thinkingLevel = normalizeReasoningLevelForProvider(payload.type, reasoningLevel.value || 'medium')
  payload.supportsThinking = reasoningLevel.value !== 'low'
  payload.supportsCmdK = true
  payload.supportsImages = false
  payload.supportsSandboxing = false
  payload.temperature = payload.temperature || 0.7
  payload.contextWindow = Number(payload.contextWindow) || 0
  payload.maxTokens = Number(payload.maxTokens) || 0
  payload.inputPricePer1M = Number(payload.inputPricePer1M) || 0
  payload.outputPricePer1M = Number(payload.outputPricePer1M) || 0
  payload.note = note.value || ''
  payload.extraParamsEnabled = payload.type === 'openai' && extraParamsEnabled.value
  payload.extraParamsJSON = payload.extraParamsEnabled ? (extraParamsJSON.value || '') : ''
  return payload
}

const validatePayload = (payload) => {
  if (!payload.displayName) return '请填写显示名称'
  if (!payload.modelID) return '请填写模型标识'
  if (!payload.apiKey) return '请填写访问密钥'
  if (!payload.baseURL) return '请填写接口域名'
  if (payload.extraParamsEnabled && payload.extraParamsJSON) {
    try {
      JSON.parse(payload.extraParamsJSON)
    } catch (err) {
      return '额外参数 JSON 格式不正确'
    }
  }
  return ''
}

const saveModel = async () => {
  const payload = buildPayload()
  const validation = validatePayload(payload)
  if (validation) {
    showNotice(validation, 'error')
    return false
  }
  saving.value = true
  try {
    await bridge.addModelAdapter(payload)
    await loadModels()
    closeEditor()
    showNotice('模型配置已保存', 'success')
    return true
  } catch (err) {
    showNotice(`保存失败：${err.message}`, 'error')
    return false
  } finally {
    saving.value = false
  }
}

const saveAndTest = async () => {
  const payload = buildPayload()
  const validation = validatePayload(payload)
  if (validation) {
    showNotice(validation, 'error')
    return
  }
  saving.value = true
  testing.value = true
  testState.value = ''
  testMessage.value = '测试中...'
  try {
    await bridge.addModelAdapter(payload)
    testMessage.value = await bridge.testModelAdapter(payload)
    testState.value = 'success'
    await loadModels()
    showNotice('模型保存并测试通过', 'success')
  } catch (err) {
    testState.value = 'error'
    testMessage.value = err.message || '测试失败'
  } finally {
    saving.value = false
    testing.value = false
  }
}

const deleteModel = async (modelID) => {
  if (!window.confirm('确定要删除这个模型吗？')) return
  try {
    await bridge.removeModelAdapter(modelID)
    await loadModels()
    showNotice('模型已删除', 'success')
  } catch (err) {
    showNotice(`删除失败：${err.message}`, 'error')
  }
}

const closeEditor = () => {
  showEditor.value = false
  editingModel.value = null
}

onMounted(loadModels)
</script>

<style scoped>
.model-config {
  width: min(1180px, 100%);
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
}

.page-header p {
  margin: 4px 0 0;
  color: var(--text-muted);
  font-size: 13px;
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
}

.models-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
  gap: 14px;
}

.model-card,
.empty-state,
.modal,
.test-box {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--panel);
  box-shadow: var(--shadow-soft);
}

.model-card {
  padding: 16px;
  animation: soft-enter var(--motion-slow) var(--ease-standard) both;
  transition:
    transform var(--motion-medium) var(--ease-spring),
    box-shadow var(--motion-medium) var(--ease-standard),
    border-color var(--motion-medium) var(--ease-standard),
    background var(--motion-medium) var(--ease-standard);
}

.model-card:hover {
  transform: translateY(-3px);
  box-shadow: var(--shadow);
  border-color: rgba(0, 122, 255, 0.18);
}

.model-card:nth-child(2n) {
  animation-delay: 50ms;
}

.model-card:nth-child(3n) {
  animation-delay: 100ms;
}

.model-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
}

.model-title {
  display: flex;
  min-width: 0;
  gap: 10px;
  align-items: center;
}

.provider-icon {
  display: grid;
  width: 36px;
  height: 36px;
  flex: 0 0 auto;
  place-items: center;
  border-radius: 10px;
  background: var(--info-bg);
  color: var(--color-primary);
  transition: transform var(--motion-medium) var(--ease-spring);
}

.model-card:hover .provider-icon {
  transform: scale(1.08) rotate(-2deg);
}

.provider-icon.anthropic {
  background: var(--warning-bg);
  color: var(--color-warning);
}

.provider-icon svg {
  width: 20px;
  height: 20px;
}

.ai-mark {
  color: #1d1d1f;
  font-size: 14px;
  font-weight: 780;
}

.model-card h3 {
  margin: 0 0 4px;
  color: var(--text);
  font-size: 19px;
  font-weight: 760;
  overflow-wrap: anywhere;
}

.model-id {
  margin-bottom: 14px;
  color: var(--text-muted);
  font-size: 12px;
}

.model-meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  color: var(--text-muted);
  font-size: 12px;
}

.model-meta strong {
  color: var(--text);
  font-size: 13px;
}

.provider-pill,
.enabled-pill {
  display: inline-flex;
  align-items: center;
  min-height: 22px;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 650;
}

.provider-pill.openai {
  background: var(--info-bg);
  color: var(--info-text);
}

.provider-pill.anthropic {
  background: var(--warning-bg);
  color: var(--warning-text);
}

.enabled-pill {
  background: var(--success-bg);
  color: var(--success-text);
  white-space: nowrap;
}

.model-details {
  display: grid;
  overflow: hidden;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: rgba(248, 248, 250, 0.72);
}

.model-details div {
  display: grid;
  grid-template-columns: 92px minmax(0, 1fr);
  gap: 12px;
  min-height: 38px;
  align-items: center;
  padding: 8px 10px;
  border-bottom: 1px solid var(--border);
  background: transparent;
}

.model-details span {
  color: var(--text-muted);
  font-size: 12px;
}

.model-details strong {
  color: var(--text);
  font-size: 12px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-weight: 550;
  text-align: right;
  overflow-wrap: anywhere;
}

.model-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-top: 14px;
}

.left-actions {
  display: flex;
  gap: 8px;
}

.outline-blue {
  border-color: rgba(0, 122, 255, 0.42);
  color: var(--color-primary);
}

.outline-red {
  border-color: rgba(255, 59, 48, 0.42);
}

.empty-state {
  display: grid;
  justify-items: center;
  gap: 12px;
  padding: 46px 24px;
  text-align: center;
}

.empty-icon {
  width: 32px;
  height: 32px;
  color: var(--color-primary);
}

.empty-state h3 {
  margin: 0;
  color: var(--text);
  font-size: 18px;
}

.empty-state p {
  margin: 6px 0 0;
  color: var(--text-muted);
}

:global(.modal-overlay) {
  position: fixed;
  inset: 0;
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: rgba(111, 113, 118, 0.34);
  backdrop-filter: blur(18px) saturate(1.15);
  overflow: hidden;
}

:global(.modal-fade-enter-active),
:global(.modal-fade-leave-active) {
  transition:
    opacity var(--motion-medium) var(--ease-standard),
    backdrop-filter var(--motion-medium) var(--ease-standard);
}

:global(.modal-fade-enter-active .modal),
:global(.modal-fade-leave-active .modal) {
  transition:
    opacity var(--motion-medium) var(--ease-standard),
    transform var(--motion-medium) var(--ease-spring),
    filter var(--motion-medium) var(--ease-standard);
}

:global(.modal-fade-enter-from),
:global(.modal-fade-leave-to) {
  opacity: 0;
  backdrop-filter: blur(0) saturate(1);
}

:global(.modal-fade-enter-from .modal),
:global(.modal-fade-leave-to .modal) {
  opacity: 0;
  transform: translateY(14px) scale(0.965);
  filter: blur(4px);
}

:global(.modal) {
  display: flex;
  flex-direction: column;
  width: min(860px, calc(100vw - 48px));
  max-height: calc(100vh - 48px);
  overflow: hidden;
  box-shadow: var(--shadow);
  background: rgba(255, 255, 255, 0.96);
  border-radius: 16px;
}

:global(.modal-header) {
  flex: 0 0 auto;
  z-index: 2;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border);
  background: rgba(255, 255, 255, 0.9);
  backdrop-filter: blur(16px) saturate(1.1);
}

:global(.modal-kicker) {
  color: var(--text-subtle);
  font-size: 12px;
  font-weight: 650;
}

:global(.modal-header h3) {
  margin: 5px 0 0;
  color: var(--text);
  font-size: 20px;
  font-weight: 730;
}

:global(.modal-actions) {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  justify-content: flex-end;
}

:global(.icon-btn) {
  display: grid;
  width: 34px;
  height: 34px;
  place-items: center;
  border-radius: 9px;
  background: rgba(118, 118, 128, 0.1);
  color: var(--text-muted);
  cursor: pointer;
  transition:
    background var(--motion-fast) var(--ease-standard),
    color var(--motion-fast) var(--ease-standard),
    transform var(--motion-fast) var(--ease-spring);
}

:global(.icon-btn:hover) {
  background: rgba(118, 118, 128, 0.16);
  color: var(--text);
  transform: scale(1.04);
}

:global(.icon-btn:active) {
  transform: scale(0.96);
}

:global(.modal-body) {
  flex: 1;
  min-height: 0;
  overflow: auto;
  max-height: calc(100vh - 146px);
  padding: 18px 20px 20px;
}

:global(.provider-tabs) {
  width: min(430px, 100%);
  display: flex;
  gap: 8px;
  margin: 0 0 16px;
  padding: 5px;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: rgba(118, 118, 128, 0.08);
}

:global(.provider-tab) {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-height: 36px;
  padding: 0 13px;
  border: 1px solid transparent;
  border-radius: 9px;
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  font-weight: 650;
  transition:
    background var(--motion-fast) var(--ease-standard),
    border-color var(--motion-fast) var(--ease-standard),
    color var(--motion-fast) var(--ease-standard),
    transform var(--motion-fast) var(--ease-spring),
    box-shadow var(--motion-fast) var(--ease-standard);
}

:global(.provider-tab:hover) {
  transform: translateY(-1px);
  background: rgba(255, 255, 255, 0.52);
}

:global(.provider-tab.active.openai) {
  border-color: var(--border);
  background: #ffffff;
  color: var(--info-text);
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.08);
}

:global(.provider-tab.active.anthropic) {
  border-color: var(--border);
  background: #ffffff;
  color: var(--warning-text);
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.08);
}

:global(.form-section),
:global(.advanced-box) {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: rgba(255, 255, 255, 0.62);
}

:global(.form-section) {
  padding: 14px;
}

:global(.section-title) {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

:global(.section-title h4) {
  margin: 0;
  color: var(--text);
  font-size: 16px;
}

:global(.section-title span) {
  color: var(--text-subtle);
  font-size: 12px;
}

:global(.form-grid) {
  display: grid;
  grid-template-columns: 1fr;
  gap: 9px;
}

:global(.field) {
  display: grid;
  grid-template-columns: 118px minmax(0, 1fr);
  align-items: center;
  gap: 12px;
  min-width: 0;
}

:global(.field.full-row) {
  grid-column: auto;
  align-items: start;
}

:global(.field span),
:global(.field-title-row span) {
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 650;
}

:global(.field input),
:global(.field select),
:global(.field textarea) {
  width: 100%;
  min-height: 34px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-sm);
  background: rgba(255, 255, 255, 0.78);
  color: var(--text);
  font-size: 13px;
  padding: 8px 10px;
  transition:
    border-color var(--motion-fast) var(--ease-standard),
    box-shadow var(--motion-fast) var(--ease-standard),
    background var(--motion-fast) var(--ease-standard);
}

:global(.field textarea) {
  min-height: 84px;
  resize: vertical;
}

:global(.field input:focus),
:global(.field select:focus),
:global(.field textarea:focus) {
  border-color: rgba(0, 122, 255, 0.55);
  box-shadow: 0 0 0 3px rgba(0, 122, 255, 0.12);
  outline: none;
}

:global(.password-wrap) {
  display: flex;
  gap: 8px;
}

:global(.password-wrap input) {
  flex: 1;
  min-width: 0;
}

:global(.field-title-row) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

:global(.inline-check) {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--text-muted);
  font-size: 12px;
}

:global(.inline-check input) {
  width: auto;
  min-height: auto;
  accent-color: var(--color-primary);
}

:global(.advanced-box) {
  margin-top: 12px;
}

:global(.advanced-toggle) {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-height: 38px;
  padding: 0 14px;
  background: transparent;
  color: var(--text);
  cursor: pointer;
  font-weight: 700;
  transition:
    background var(--motion-fast) var(--ease-standard),
    color var(--motion-fast) var(--ease-standard);
}

:global(.advanced-toggle:hover) {
  background: rgba(118, 118, 128, 0.06);
}

:global(.advanced-toggle .rotated) {
  transform: rotate(180deg);
  transition: transform var(--motion-medium) var(--ease-spring);
}

:global(.advanced-grid) {
  padding: 0 14px 14px;
  overflow: hidden;
  transform-origin: top center;
}

:global(.expand-enter-active),
:global(.expand-leave-active) {
  transition:
    opacity var(--motion-medium) var(--ease-standard),
    transform var(--motion-medium) var(--ease-standard),
    max-height var(--motion-medium) var(--ease-standard);
  max-height: 560px;
}

:global(.expand-enter-from),
:global(.expand-leave-to) {
  opacity: 0;
  transform: translateY(-6px);
  max-height: 0;
}

:global(.test-box) {
  margin-top: 12px;
  padding: 12px 14px;
  background: var(--panel-muted);
  min-height: 64px;
  display: flex;
  align-items: center;
  transition:
    background var(--motion-medium) var(--ease-standard),
    border-color var(--motion-medium) var(--ease-standard),
    transform var(--motion-medium) var(--ease-spring);
}

:global(.test-box span) {
  display: block;
  color: var(--text-muted);
  font-size: 12px;
}

:global(.test-box strong) {
  display: block;
  margin-top: 3px;
  color: var(--text);
  font-size: 13px;
  font-weight: 650;
}

:global(.test-box.success) {
  border-color: var(--success-border);
  background: var(--success-bg);
}

:global(.test-box.success strong) {
  color: var(--success-text);
}

:global(.test-box.error) {
  border-color: var(--danger-border);
  background: var(--danger-bg);
}

:global(.test-box.error strong) {
  color: var(--danger-text);
}

@media (max-width: 760px) {
  .model-config {
    padding: 16px;
  }

  .page-header,
  :global(.modal-header) {
    flex-direction: column;
    align-items: stretch;
  }

  :global(.modal-overlay) {
    padding: 10px;
  }

  :global(.modal) {
    width: 100%;
    max-height: calc(100vh - 20px);
  }

  :global(.modal-body) {
    max-height: calc(100vh - 130px);
  }

  .form-grid,
  .models-grid {
    grid-template-columns: 1fr;
  }

  .model-details div {
    grid-template-columns: 1fr;
    gap: 3px;
  }

  .model-details strong {
    text-align: left;
  }
}
</style>
