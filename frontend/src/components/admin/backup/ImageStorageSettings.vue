<template>
  <section class="card p-6" data-testid="image-storage-settings">
    <div class="mb-4">
      <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('imageStorage.title') }}</h3>
      <p class="mt-1 text-sm leading-6 text-gray-500 dark:text-gray-400">{{ t('imageStorage.description') }}</p>
    </div>
    <p v-if="loadError" role="alert" class="mb-4 text-sm text-red-600">{{ loadError }} <button type="button" class="underline" @click="load">{{ t('common.refresh') }}</button></p>
    <form @submit.prevent="save">
      <fieldset :disabled="loading || !loaded || saving || testing" class="space-y-4 disabled:opacity-60">
        <label class="flex items-center gap-2 text-sm"><input v-model="form.enabled" type="checkbox" data-testid="image-storage-enabled" /><span>{{ t('imageStorage.enabled') }}</span></label>
        <label class="flex items-center gap-2 text-sm"><input v-model="form.reuse_backup_s3" type="checkbox" data-testid="image-storage-reuse" /><span>{{ t('imageStorage.reuseBackupS3') }}</span></label>
        <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
          <div><label class="input-label" for="image-storage-bucket">{{ t('admin.backup.s3.bucket') }}</label><input id="image-storage-bucket" v-model="form.bucket" class="input w-full" :required="form.enabled && !form.reuse_backup_s3" :placeholder="form.reuse_backup_s3 ? t('imageStorage.bucketInherited') : ''" /></div>
          <div><label class="input-label" for="image-storage-prefix">{{ t('admin.backup.s3.prefix') }}</label><input id="image-storage-prefix" v-model="form.prefix" class="input w-full" placeholder="images/" /></div>
          <template v-if="!form.reuse_backup_s3">
            <div><label class="input-label" for="image-storage-endpoint">{{ t('admin.backup.s3.endpoint') }}</label><input id="image-storage-endpoint" v-model="form.endpoint" class="input w-full" placeholder="https://s3.example.com" /></div>
            <div><label class="input-label" for="image-storage-region">{{ t('admin.backup.s3.region') }}</label><input id="image-storage-region" v-model="form.region" class="input w-full" placeholder="auto" /></div>
            <div><label class="input-label" for="image-storage-access-key">{{ t('admin.backup.s3.accessKeyId') }}</label><input id="image-storage-access-key" v-model="form.access_key_id" class="input w-full" autocomplete="off" :required="form.enabled" /></div>
            <div><label class="input-label" for="image-storage-secret">{{ t('admin.backup.s3.secretAccessKey') }}</label><input id="image-storage-secret" v-model="form.secret_access_key" type="password" class="input w-full" autocomplete="new-password" :required="form.enabled && !secretConfigured" :placeholder="secretConfigured ? t('admin.backup.s3.secretConfigured') : ''" /></div>
            <label class="flex items-center gap-2 text-sm md:col-span-2"><input v-model="form.force_path_style" type="checkbox" /><span>{{ t('admin.backup.s3.forcePathStyle') }}</span></label>
          </template>
          <div><label class="input-label" for="image-storage-public-url">{{ t('imageStorage.publicBaseUrl') }}</label><input id="image-storage-public-url" v-model="form.public_base_url" class="input w-full" placeholder="https://images.example.com" /><p class="mt-1 text-xs text-gray-500">{{ t('imageStorage.publicBaseUrlHint') }}</p></div>
          <div><label class="input-label" for="image-storage-expiry">{{ t('imageStorage.presignExpiryHours') }}</label><input id="image-storage-expiry" v-model.number="form.presign_expiry_hours" type="number" min="1" step="1" required class="input w-full" /></div>
          <div><label class="input-label" for="image-storage-max-bytes">{{ t('imageStorage.maxDownloadBytes') }}</label><input id="image-storage-max-bytes" v-model.number="form.max_download_bytes" type="number" min="1" step="1" required class="input w-full" /></div>
        </div>
        <p class="text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('imageStorage.lifecycleHint') }}</p>
        <div class="flex flex-wrap gap-2">
          <button type="button" class="btn btn-secondary btn-sm h-9" data-testid="image-storage-test" @click="test">{{ testing ? t('common.loading') : t('admin.backup.storage.testConnection') }}</button>
          <button type="submit" class="btn btn-primary btn-sm h-9" data-testid="image-storage-save">{{ saving ? t('common.loading') : t('common.save') }}</button>
        </div>
      </fieldset>
    </form>
    <TotpStepUpDialog :controller="stepUp" />
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import { useAppStore } from '@/stores'
import type { ImageStorageConfig } from '@/api/admin/backup'
import { useStepUp, isStepUpBlocked, isStepUpCancelled, stepUpBlockReason } from '@/composables/useStepUp'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()
const stepUp = useStepUp()
const form = ref<ImageStorageConfig>({
  enabled: false, reuse_backup_s3: true, bucket: '', prefix: 'images/', public_base_url: '',
  presign_expiry_hours: 24, max_download_bytes: 33554432,
  endpoint: '', region: 'auto', access_key_id: '', secret_access_key: '', force_path_style: false,
})
const secretConfigured = ref(false)
const loading = ref(false)
const loaded = ref(false)
const loadError = ref('')
const saving = ref(false)
const testing = ref(false)

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const result = await adminAPI.backup.getImageStorageConfig()
    // 密钥只写不读；服务端掩码与已配置标志都不能成为下次提交的新密钥。
    form.value = { ...result.config, secret_access_key: '' }
    // 复用模式返回的是备份密钥状态，不能把它当成独立存储已保存的密钥。
    secretConfigured.value = !result.config.reuse_backup_s3 && result.secret_configured
    loaded.value = true
  } catch (error) {
    loaded.value = false
    loadError.value = extractApiErrorMessage(error, t('errors.networkError'))
  } finally { loading.value = false }
}
function payload(): ImageStorageConfig {
  const { secret_access_key, ...config } = form.value
  return secret_access_key ? { ...config, secret_access_key } : config
}
function reportError(error: unknown) {
  if (isStepUpCancelled(error)) return
  if (isStepUpBlocked(error)) {
    appStore.showError(t(stepUpBlockReason(error) === 'STEP_UP_ADMIN_API_KEY_FORBIDDEN' ? 'stepUp.adminApiKeyForbidden' : 'stepUp.notEnabled'))
    return
  }
  appStore.showError(extractApiErrorMessage(error, t('errors.networkError')))
}
async function save() {
  if (!loaded.value || saving.value || testing.value) return
  saving.value = true
  try {
    const config = payload()
    await stepUp.run(() => adminAPI.backup.updateImageStorageConfig(config))
    form.value.secret_access_key = ''
    appStore.showSuccess(t('imageStorage.saved'))
    await load()
  } catch (error) { reportError(error) }
  finally { saving.value = false }
}
async function test() {
  if (!loaded.value || testing.value || saving.value) return
  testing.value = true
  try {
    const config = payload()
    const result = await stepUp.run(() => adminAPI.backup.testImageStorageConnection(config))
    if (result.ok) appStore.showSuccess(result.message || t('admin.backup.storage.testSuccess'))
    else appStore.showError(result.message || t('admin.backup.storage.testFailed'))
  } catch (error) { reportError(error) }
  finally { testing.value = false }
}
onMounted(load)
</script>
