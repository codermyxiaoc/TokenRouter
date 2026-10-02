<template>
  <div v-if="endpoints?.length" class="mt-3 min-w-0 space-y-1.5" data-testid="model-video-endpoints">
    <p class="text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('marketplace.videoEndpoints.title') }}</p>
    <div class="space-y-1">
      <button
        v-for="endpoint in endpoints"
        :key="`${endpoint.method}:${endpoint.path}`"
        type="button"
        class="flex w-full min-w-0 items-start gap-2 rounded-lg border border-gray-200 bg-white/70 px-2 py-1.5 text-left transition hover:border-primary-300 hover:bg-primary-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:border-dark-700 dark:bg-dark-900 dark:hover:border-primary-500/50 dark:hover:bg-primary-500/10"
        :aria-label="t('marketplace.videoEndpoints.copy', { path: endpoint.path })"
        :title="t('marketplace.videoEndpoints.copy', { path: endpoint.path })"
        data-testid="model-video-endpoint"
        @click="copyToClipboard(endpoint.path)"
      >
        <span class="min-w-0 flex-1 space-y-1">
          <span class="block text-xs font-medium text-gray-800 dark:text-dark-100">{{ endpointTitle(endpoint.protocol) }}</span>
          <span class="flex min-w-0 items-start gap-2">
            <span class="shrink-0 rounded bg-emerald-50 px-1 py-0.5 font-mono text-[10px] font-semibold leading-4 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300">{{ endpoint.method }}</span>
            <code class="min-w-0 flex-1 break-all text-xs leading-5 text-gray-700 dark:text-dark-200">{{ endpoint.path }}</code>
          </span>
        </span>
        <Icon name="copy" size="xs" class="mt-1 shrink-0 text-gray-400 dark:text-dark-400" />
      </button>
    </div>
    <p class="text-[11px] leading-4 text-gray-400 dark:text-dark-500">{{ t('marketplace.videoEndpoints.hint') }}</p>
    <p v-if="endpoints.some(endpoint => endpoint.path.includes('{model}'))" class="text-[11px] leading-4 text-gray-400 dark:text-dark-500">{{ t('marketplace.videoEndpoints.modelPlaceholderHint', { placeholder: '{model}' }) }}</p>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useClipboard } from '@/composables/useClipboard'
import type { MarketplaceVideoEndpoint } from '@/types'

// 端点能力由后端按分组、模型和账号计算，前端不按名称猜测或补齐未支持的协议。
defineProps<{ endpoints?: MarketplaceVideoEndpoint[] }>()

const { t } = useI18n()
const { copyToClipboard } = useClipboard()

// 标题只解释后端提供的协议，不改变可调用端点集合；两种 OpenAI 路径沿用相同品牌名称。
const endpointTitleKeys: Record<string, string> = {
  compat: 'openai',
  openai_videos: 'openai',
  seedance: 'seedance',
  kling: 'kling',
  wan: 'wan',
  minimax: 'minimax',
  grok: 'grok',
}

function endpointTitle(protocol: string): string {
  const key = endpointTitleKeys[protocol]
  return key ? t(`marketplace.videoEndpoints.names.${key}`) : protocol
}
</script>
