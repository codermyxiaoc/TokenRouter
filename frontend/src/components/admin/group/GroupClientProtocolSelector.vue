<template>
  <section class="border-t border-gray-200 pt-4 dark:border-dark-400">
    <div class="mb-3">
      <h4 class="text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t('admin.groups.clientProtocols.title') }}
      </h4>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.groups.clientProtocols.hint') }}
      </p>
    </div>

    <div
      data-testid="client-protocol-list"
      class="divide-y divide-gray-100 dark:divide-dark-700"
    >
      <div
        v-for="protocol in protocols"
        :key="protocol"
        class="flex min-h-14 items-center justify-between gap-4 py-2.5"
      >
        <div class="min-w-0">
          <span class="block text-sm font-medium text-gray-800 dark:text-gray-200">
            {{ t(`admin.groups.clientProtocols.labels.${protocol}`) }}
          </span>
          <code
            :data-protocol-endpoint="protocol"
            class="mt-0.5 block break-all font-mono text-xs text-gray-500 dark:text-gray-400"
          >{{ protocolEndpoints[protocol] }}</code>
        </div>

        <Toggle
          :model-value="isEnabled(protocol)"
          :data-protocol="protocol"
          :aria-label="t(`admin.groups.clientProtocols.labels.${protocol}`)"
          @update:model-value="toggle(protocol)"
        />
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import type { GroupClientProtocol, GroupPlatform } from '@/types'
import {
  hasGroupClientProtocol,
  setGroupClientProtocol,
  supportedGroupClientProtocols
} from '@/utils/groupClientProtocols'

const props = defineProps<{
  modelValue: GroupClientProtocol[]
  platform: GroupPlatform
}>()

const emit = defineEmits<{
  (event: 'update:modelValue', value: GroupClientProtocol[]): void
}>()

const { t } = useI18n()
const protocols = computed(() => supportedGroupClientProtocols(props.platform))

// 展示各协议最常用的标准入口，兼容别名仍由网关路由处理。
const protocolEndpoints: Record<GroupClientProtocol, string> = {
  anthropic_messages: '/v1/messages',
  openai_responses: '/v1/responses',
  openai_chat_completions: '/v1/chat/completions',
  gemini_generate_content: '/v1beta/models/{model}:generateContent'
}

const isEnabled = (protocol: GroupClientProtocol) => hasGroupClientProtocol(props.modelValue, protocol)

const toggle = (protocol: GroupClientProtocol) => {
  emit(
    'update:modelValue',
    setGroupClientProtocol(props.platform, props.modelValue, protocol, !isEnabled(protocol))
  )
}
</script>
