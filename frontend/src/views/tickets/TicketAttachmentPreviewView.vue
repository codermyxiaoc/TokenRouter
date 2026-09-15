<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-4">
      <RouterLink :to="`${base}/${route.params.id}`" class="text-sm text-primary-600 hover:underline dark:text-primary-400">← {{ t('tickets.preview.back') }}</RouterLink>
      <TicketAttachmentPreview :ticket-id="Number(route.params.id)" :attachment-id="Number(route.params.attachmentId)" :admin="admin" />
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TicketAttachmentPreview from '@/components/tickets/TicketAttachmentPreview.vue'

// 保留已有预览地址的兼容入口，工单里的附件改由详情弹窗打开。
const props = withDefaults(defineProps<{ admin?: boolean }>(), { admin: false })
const route = useRoute()
const { t } = useI18n()
const base = computed(() => props.admin ? '/admin/tickets' : '/tickets')
</script>
