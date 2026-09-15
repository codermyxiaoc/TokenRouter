<template>
  <div class="space-y-2">
    <label class="input-label" :for="id">{{ t('tickets.attachments') }}</label>
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ settings.max_attachments === 0 ? t('tickets.attachmentsDisabled') : t('tickets.attachmentHint', { count: settings.max_attachments, size: settings.max_attachment_size_mb }) }}</p>
    <input v-if="settings.max_attachments > 0" :id="id" type="file" multiple :accept="attachmentAccept" :disabled="disabled" class="block w-full text-sm text-gray-500 file:mr-3 file:rounded-lg file:border-0 file:bg-primary-50 file:px-4 file:py-2 file:text-primary-700 dark:file:bg-dark-700 dark:file:text-gray-200" @change="addFiles" />
    <ul v-if="modelValue.length" class="space-y-1">
      <li v-for="(file, index) in modelValue" :key="index" class="flex items-center justify-between gap-2 rounded-lg bg-gray-50 px-3 py-2 text-sm dark:bg-dark-800">
        <span class="min-w-0 truncate">{{ file.name }} <span class="text-gray-500">({{ formatAttachmentSize(file.size) }})</span></span>
        <button type="button" class="shrink-0 text-red-600" :disabled="disabled" :aria-label="t('tickets.removeAttachment', { name: file.name })" @click="emit('update:modelValue', modelValue.filter((_, i) => i !== index))">{{ t('common.remove') }}</button>
      </li>
    </ul>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { TicketSettings } from '@/api/tickets'
import { attachmentAccept, formatAttachmentSize, validateTicketFiles } from './attachments'

const props = withDefaults(defineProps<{ modelValue: File[]; settings: TicketSettings; disabled?: boolean; id?: string }>(), { disabled: false, id: 'ticket-attachments' })
const emit = defineEmits<{ (e: 'update:modelValue', files: File[]): void }>()
const { t } = useI18n()
const error = ref('')

function addFiles(event: Event) {
  const input = event.target as HTMLInputElement
  const files = [...props.modelValue, ...Array.from(input.files ?? [])]
  input.value = ''
  const reason = validateTicketFiles(files, props.settings)
  error.value = reason ? t(`tickets.fileErrors.${reason}`, { count: props.settings.max_attachments, size: props.settings.max_attachment_size_mb }) : ''
  // 整次选择通过才更新，避免用户误以为所有选择的附件都已提交。
  if (!reason) emit('update:modelValue', files)
}
</script>
