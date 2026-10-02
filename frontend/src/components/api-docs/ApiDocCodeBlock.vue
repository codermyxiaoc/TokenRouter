<template>
  <div class="overflow-hidden rounded-xl border border-gray-200 bg-gray-50 dark:border-dark-600 dark:bg-dark-950">
    <div class="flex items-center justify-between gap-3 border-b border-gray-200 px-4 py-2 dark:border-dark-700">
      <span class="text-xs font-medium text-gray-500 dark:text-dark-300">{{ label || language }}</span>
      <button type="button" class="inline-flex items-center gap-1.5 text-xs text-gray-500 hover:text-primary-600 dark:text-dark-300 dark:hover:text-primary-300" @click="copyToClipboard(code)">
        <Icon :name="copied ? 'check' : 'clipboard'" size="sm" />
        {{ copied ? t('common.copied') : t('common.copy') }}
      </button>
    </div>
    <pre class="max-h-[36rem] overflow-auto p-4 text-xs leading-6 text-gray-800 dark:text-dark-100 sm:text-[13px]" tabindex="0"><code>{{ code }}</code></pre>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useClipboard } from '@/composables/useClipboard'

// 使用文本插值展示示例，文档代码与厂商响应均不作为 HTML 执行。
defineProps<{ code: string; language?: string; label?: string }>()
const { t } = useI18n()
const { copied, copyToClipboard } = useClipboard()
</script>
