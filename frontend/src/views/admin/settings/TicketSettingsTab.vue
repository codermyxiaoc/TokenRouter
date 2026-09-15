<template>
  <section class="card">
    <div class="flex items-start justify-between gap-4 border-b border-gray-100 px-6 py-4 dark:border-dark-700"><div><h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('tickets.settings.title') }}</h2><p class="mt-1 text-sm text-gray-500">{{ t('tickets.settings.description') }}</p></div><button type="button" class="btn btn-secondary btn-sm" :disabled="loading || saving" @click="load">{{ t('common.refresh') }}</button></div>
    <div v-if="loading" class="p-10 text-center text-gray-500" role="status">{{ t('common.loading') }}</div>
    <div v-else class="space-y-6 p-6">
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <template v-if="form">
        <div class="flex items-center justify-between gap-4"><div><label for="tickets-enabled" class="input-label">{{ t('tickets.settings.enabled') }}</label><p class="mt-1 text-sm text-gray-500">{{ t('tickets.settings.enabledHint') }}</p></div><Toggle id="tickets-enabled" v-model="form.enabled" :disabled="saving" :aria-label="t('tickets.settings.enabled')" /></div>
        <div class="grid gap-6 sm:grid-cols-2"><div v-for="field in numberFields" :key="field.key"><label class="input-label" :for="`ticket-setting-${field.key}`">{{ t(`tickets.settings.${field.key}`) }}</label><input :id="`ticket-setting-${field.key}`" v-model.number="form[field.key]" type="number" step="1" :min="field.min" :max="field.max" :disabled="saving" class="input w-full" /><p class="mt-2 text-xs text-gray-500">{{ t(`tickets.settings.${field.key}Hint`) }}</p></div></div>
        <!-- 沿用原配置键，统一控制客服回复、完成及撤销工单的邮件通知。 -->
        <div class="flex items-center justify-between gap-4 border-t border-gray-100 pt-6 dark:border-dark-700"><div><label for="tickets-notify" class="input-label">{{ t('tickets.settings.notify') }}</label><p class="mt-1 text-sm text-gray-500">{{ t('tickets.settings.notifyHint') }}</p></div><Toggle id="tickets-notify" v-model="form.notify_on_staff_reply" :disabled="saving" :aria-label="t('tickets.settings.notify')" /></div>
        <div class="flex justify-end"><button type="button" class="btn btn-primary" :disabled="saving || !valid" @click="save">{{ saving ? t('common.processing') : t('common.save') }}</button></div>
      </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import { ticketAPI, saveTicketSettings, type TicketSettings } from '@/api/tickets'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()
const form = ref<TicketSettings>()
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const numberFields = [
  { key: 'max_open_tickets', min: 1, max: 100 },
  { key: 'max_attachments', min: 0, max: 10 },
  { key: 'max_attachment_size_mb', min: 1, max: 20 },
  { key: 'auto_expire_hours', min: 0, max: 8760 },
] as const
const valid = computed(() => !!form.value && numberFields.every(({ key, min, max }) => Number.isInteger(form.value![key]) && form.value![key] >= min && form.value![key] <= max))

async function load() {
  loading.value = true
  error.value = ''
  try { form.value = await ticketAPI(true).config() }
  catch (err) { error.value = extractI18nErrorMessage(err, t, 'tickets.errors', t('tickets.loadFailed')) }
  finally { loading.value = false }
}
async function save() {
  if (!form.value || !valid.value || saving.value) return
  saving.value = true
  error.value = ''
  // 独立设置接口只写入本模块字段，避免系统设置的大表单覆盖工单配置。
  try {
    form.value = await saveTicketSettings({ ...form.value })
    await appStore.syncTicketModuleEnabled(form.value.enabled)
    appStore.showSuccess(t('tickets.settings.saved'))
  }
  catch (err) { error.value = extractI18nErrorMessage(err, t, 'tickets.errors', t('tickets.actionFailed')) }
  finally { saving.value = false }
}
onMounted(load)
</script>
