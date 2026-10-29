<template>
  <!-- 视频协议模板 AI：配置 OpenAI 兼容模型，供模板页面把上游文档生成模板草稿 -->
  <section class="card" data-testid="video-protocol-ai-settings">
    <div class="flex items-start justify-between gap-4 border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <div>
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.videoProtocolAI.title') }}</h2>
        <p class="mt-1 text-sm text-gray-500">{{ t('admin.videoProtocolAI.description') }}</p>
      </div>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || saving" @click="load">{{ t('common.refresh') }}</button>
    </div>
    <div v-if="loading" class="p-10 text-center text-gray-500" role="status">{{ t('common.loading') }}</div>
    <div v-else class="space-y-6 p-6">
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <div class="flex items-center justify-between gap-4">
        <div>
          <label for="video-protocol-ai-enabled" class="input-label">{{ t('admin.videoProtocolAI.enabled') }}</label>
          <p class="mt-1 text-sm text-gray-500">{{ t('admin.videoProtocolAI.enabledHint') }}</p>
        </div>
        <Toggle id="video-protocol-ai-enabled" v-model="form.enabled" :disabled="saving" :aria-label="t('admin.videoProtocolAI.enabled')" />
      </div>
      <!-- 快捷使用本站：Base URL 取站点 API 地址，密钥从当前管理员自己的 API Key 中选择 -->
      <div class="border-t border-gray-100 pt-6 dark:border-dark-700" data-testid="video-protocol-ai-site">
        <label class="input-label">{{ t('admin.videoProtocolAI.useSite') }}</label>
        <p class="mt-1 text-sm text-gray-500">{{ t('admin.videoProtocolAI.useSiteHint') }}</p>
        <div class="mt-3 flex flex-wrap items-center gap-3">
          <button type="button" class="btn btn-secondary" data-testid="video-protocol-ai-site-base-url" @click="useSiteBaseUrl">{{ t('admin.videoProtocolAI.useSiteBaseUrl') }}</button>
          <div class="min-w-[16rem] flex-1">
            <Select v-model="siteKeyId" :options="siteKeyOptions" :placeholder="siteKeysLoading ? t('common.loading') : t('admin.videoProtocolAI.selectSiteKey')" data-testid="video-protocol-ai-site-key" />
          </div>
          <button type="button" class="btn btn-secondary" :disabled="siteKeyId === null" data-testid="video-protocol-ai-site-key-apply" @click="useSiteKey">{{ t('admin.videoProtocolAI.useSiteKey') }}</button>
        </div>
        <p v-if="siteKeysLoaded && !siteKeyOptions.length" class="mt-2 text-xs text-amber-600">{{ t('admin.videoProtocolAI.noSiteKeys') }}</p>
      </div>
      <div class="grid gap-6 border-t border-gray-100 pt-6 sm:grid-cols-2 dark:border-dark-700">
        <div>
          <label for="video-protocol-ai-base-url" class="input-label">{{ t('admin.videoProtocolAI.baseUrl') }}</label>
          <input id="video-protocol-ai-base-url" v-model="form.base_url" class="input w-full font-mono" placeholder="https://api.openai.com/v1" data-testid="video-protocol-ai-base-url" />
          <p class="mt-2 text-xs text-gray-500">{{ t('admin.videoProtocolAI.baseUrlHint') }}</p>
        </div>
        <div>
          <label for="video-protocol-ai-model" class="input-label">{{ t('admin.videoProtocolAI.model') }}</label>
          <input id="video-protocol-ai-model" v-model="form.model" class="input w-full font-mono" placeholder="gpt-5" data-testid="video-protocol-ai-model" />
          <p class="mt-2 text-xs text-gray-500">{{ t('admin.videoProtocolAI.modelHint') }}</p>
        </div>
        <div>
          <label for="video-protocol-ai-key" class="input-label">{{ t('admin.videoProtocolAI.apiKey') }}</label>
          <input id="video-protocol-ai-key" v-model="apiKey" type="password" autocomplete="new-password" class="input w-full font-mono" :placeholder="configured ? t('admin.videoProtocolAI.apiKeyConfigured') : 'sk-...'" data-testid="video-protocol-ai-key" />
          <label v-if="configured" class="mt-2 flex items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
            <input v-model="clearKey" type="checkbox" class="h-4 w-4" data-testid="video-protocol-ai-clear-key" />
            {{ t('admin.videoProtocolAI.clearApiKey') }}
          </label>
        </div>
        <div>
          <label for="video-protocol-ai-timeout" class="input-label">{{ t('admin.videoProtocolAI.timeout') }}</label>
          <input id="video-protocol-ai-timeout" v-model.number="form.timeout_seconds" type="number" min="10" max="600" step="1" class="input w-full" data-testid="video-protocol-ai-timeout" />
          <p class="mt-2 text-xs text-gray-500">{{ t('admin.videoProtocolAI.timeoutHint') }}</p>
        </div>
      </div>
      <div class="flex justify-end">
        <button type="button" class="btn btn-primary" :disabled="saving" data-testid="video-protocol-ai-save" @click="save">
          {{ saving ? t('common.processing') : t('common.save') }}
        </button>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import Select from '@/components/common/Select.vue'
import { adminAPI } from '@/api/admin'
import { keysAPI } from '@/api/keys'
import { authAPI } from '@/api/auth'
import type { ApiKey } from '@/types'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const app = useAppStore()
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const form = reactive({ enabled: false, base_url: '', model: '', timeout_seconds: 120 })
// 密钥只写不读：留空表示保留已保存的密钥。
const apiKey = ref('')
const clearKey = ref(false)
const configured = ref(false)
// 本站快捷配置：当前管理员自己的启用 API Key，选择后只填入密钥输入框，仍需点击保存。
const siteKeys = ref<ApiKey[]>([])
const siteKeyId = ref<string | number | boolean | null>(null)
const siteKeysLoading = ref(false)
const siteKeysLoaded = ref(false)
const siteKeyOptions = computed(() => siteKeys.value.map(key => ({ value: key.id, label: `${key.name} (${maskKey(key.key)})` })))

// 列表中只展示密钥首尾，避免完整密钥出现在选项文字里。
function maskKey(value: string) {
  return value.length > 12 ? `${value.slice(0, 6)}…${value.slice(-4)}` : '••••'
}

// 本站 API 地址：优先使用系统设置中的 API Base URL，否则使用当前访问域名；统一补齐 /v1。
async function useSiteBaseUrl() {
  let base = ''
  try {
    base = (await authAPI.getPublicSettings())?.api_base_url || ''
  } catch {
    base = ''
  }
  base = (base || window.location.origin).replace(/\/+$/, '')
  form.base_url = /\/v1$/.test(base) ? base : `${base}/v1`
}

async function loadSiteKeys() {
  siteKeysLoading.value = true
  try {
    const page = await keysAPI.list(1, 100, { status: 'active' })
    siteKeys.value = page.items ?? []
  } catch {
    siteKeys.value = []
  } finally {
    siteKeysLoading.value = false
    siteKeysLoaded.value = true
  }
}

function useSiteKey() {
  const selected = siteKeys.value.find(key => key.id === siteKeyId.value)
  if (!selected) return
  apiKey.value = selected.key
  clearKey.value = false
  app.showSuccess(t('admin.videoProtocolAI.siteKeyFilled'))
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const cfg = await adminAPI.videoProtocolProfiles.getAIConfig()
    Object.assign(form, { enabled: cfg.enabled, base_url: cfg.base_url, model: cfg.model, timeout_seconds: cfg.timeout_seconds || 120 })
    configured.value = cfg.api_key_configured
    apiKey.value = ''
    clearKey.value = false
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.videoProtocolAI.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  try {
    const cfg = await adminAPI.videoProtocolProfiles.updateAIConfig({
      enabled: form.enabled,
      base_url: form.base_url.trim(),
      model: form.model.trim(),
      timeout_seconds: Number(form.timeout_seconds) || 120,
      ...(apiKey.value.trim() ? { api_key: apiKey.value.trim() } : {}),
      ...(clearKey.value && !apiKey.value.trim() ? { clear_api_key: true } : {}),
    })
    configured.value = cfg.api_key_configured
    apiKey.value = ''
    clearKey.value = false
    app.showSuccess(t('admin.videoProtocolAI.saved'))
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.videoProtocolAI.saveFailed'))
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void load()
  void loadSiteKeys()
})
</script>
