<template>
  <div class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
    <p class="text-sm text-gray-600 dark:text-gray-300">{{ platform === 'typesafe' ? 'System One · /v1/systemone · jev-latest' : platform === 'cline' ? 'Chat Completions · /chat/completions' : 'Command Code' }}</p>
    <template v-if="platform === 'command_code'">
      <label class="input-label">{{ t('admin.accounts.cnProviders.apiProtocol.title') }}</label>
      <Select v-model="form.protocol" :options="protocolOptions" />
      <label class="input-label">{{ t('admin.accounts.cnProviders.apiProtocol.endpoints') }}</label>
      <div v-for="item in nativeProtocols" :key="item.value">
        <label class="input-label text-xs">{{ item.label }}</label>
        <input v-model="form.baseUrls[item.value]" class="input" :placeholder="t('admin.accounts.additionalProviders.inheritBaseUrl')" />
      </div>
      <template v-if="form.protocol === 'adaptive'">
        <label class="flex items-center gap-2 text-sm"><input v-model="form.customRules" type="checkbox" class="checkbox" />{{ t('admin.accounts.additionalProviders.customRules') }}</label>
        <p class="input-hint">{{ t('admin.accounts.additionalProviders.catalogHint') }}</p>
        <div v-if="form.customRules" class="space-y-2">
          <div v-for="(row, index) in form.rules" :key="rowKey(row)" class="flex flex-wrap gap-2">
            <input v-model="row.pattern" class="input min-w-0 flex-1" placeholder="anthropic/claude-*" />
            <Select v-model="row.protocol" :options="nativeProtocols" class="w-48" />
            <button type="button" class="btn btn-danger" :aria-label="t('common.delete')" @click="form.rules.splice(index, 1)">×</button>
          </div>
          <button type="button" class="btn btn-secondary" @click="form.rules.push({ pattern: '', protocol: 'chat_completions' })">{{ t('common.add') }}</button>
        </div>
      </template>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import { createStableObjectKeyResolver } from '@/utils/stableObjectKey'
import type { AdditionalProviderForm, CnNativeApiProtocol, OpenCodeGoProtocolRule } from './credentialsBuilder'

defineProps<{ platform: string }>()
const form = defineModel<AdditionalProviderForm>({ required: true })
const { t } = useI18n()
const rowKey = createStableObjectKeyResolver<OpenCodeGoProtocolRule>('provider-protocol-rule')
// 固定端点仍保留可独立覆盖的地址，自适应默认交给后端实时目录识别。
const nativeProtocols = computed<Array<{ value: CnNativeApiProtocol; label: string }>>(() => [
  { value: 'chat_completions', label: 'Chat Completions' },
  { value: 'responses', label: 'Responses' },
  { value: 'anthropic', label: 'Anthropic Messages' }
])
const protocolOptions = computed(() => [{ value: 'adaptive', label: t('admin.accounts.cnProviders.apiProtocol.adaptive') }, ...nativeProtocols.value])
</script>
