<template>
  <BaseDialog :show="show" :title="t('admin.accounts.upstreamBilling.settingsTitle')" width="normal" @close="$emit('close')">
    <form id="upstream-billing-settings-form" class="space-y-5" @submit.prevent="save">
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accounts.upstreamBilling.settingsHint') }}</p>
      <p v-if="loadError" role="alert" class="text-sm text-red-600">{{ loadError }}</p>
      <UpstreamBillingToggle id="upstream-billing-global-enabled" v-model="form.enabled" :disabled="loading || saving || !!loadError" :label="t('admin.accounts.upstreamBilling.globalEnabled')" />
      <div>
        <label class="input-label" for="upstream-billing-interval">{{ t('admin.accounts.upstreamBilling.interval') }}</label>
        <input id="upstream-billing-interval" v-model.number="form.interval_minutes" type="number" min="5" max="1440" step="1" required class="input" :disabled="loading || saving || !!loadError" />
      </div>
    </form>
    <template #footer>
      <button type="button" class="btn btn-secondary" @click="$emit('close')">{{ t('common.cancel') }}</button>
      <button type="submit" form="upstream-billing-settings-form" class="btn btn-primary" :disabled="loading || saving || !!loadError">{{ t('common.save') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import UpstreamBillingToggle from './UpstreamBillingToggle.vue'
import type { UpstreamBillingProbeSettings } from '@/types'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ close: []; saved: [settings: UpstreamBillingProbeSettings] }>()
const { t } = useI18n()
const app = useAppStore()
const form = reactive<UpstreamBillingProbeSettings>({ enabled: true, interval_minutes: 30 })
const loading = ref(false)
const saving = ref(false)
const loadError = ref('')
let generation = 0

watch(() => props.show, async show => {
  const current = ++generation
  if (!show) return
  loading.value = true
  loadError.value = ''
  try {
    const result = await adminAPI.accounts.getUpstreamBillingProbeSettings()
    if (current === generation) Object.assign(form, result)
  } catch {
    if (current === generation) loadError.value = t('admin.accounts.upstreamBilling.settingsFailed')
  } finally {
    if (current === generation) loading.value = false
  }
}, { immediate: true })

async function save() {
  if (loading.value || saving.value || loadError.value) return
  if (!Number.isInteger(form.interval_minutes) || form.interval_minutes < 5 || form.interval_minutes > 1440) {
    app.showError(t('admin.accounts.upstreamBilling.intervalInvalid'))
    return
  }
  saving.value = true
  const current = generation
  try {
    const result = await adminAPI.accounts.updateUpstreamBillingProbeSettings({ ...form })
    // 异步保存结果不可关闭后来重新打开的设置弹窗。
    if (current !== generation) return
    emit('saved', result)
    app.showSuccess(t('admin.accounts.upstreamBilling.settingsSaved'))
    emit('close')
  } catch {
    if (current === generation) app.showError(t('admin.accounts.upstreamBilling.settingsSaveFailed'))
  } finally {
    saving.value = false
  }
}
</script>
