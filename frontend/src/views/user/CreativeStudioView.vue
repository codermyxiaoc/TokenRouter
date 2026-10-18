<template>
  <AppLayout full-viewport>
    <div class="relative -mx-4 -mb-4 -mt-4 h-[calc(100dvh-3.5rem)] md:-mx-6 md:-mb-6 md:-mt-5 lg:-mx-8 lg:-mb-8 lg:-mt-4">
      <!-- 同源独立文档隔离 React/Tailwind 样式，用户变化销毁整个编辑器运行环境。 -->
      <iframe
        v-if="auth.user && auth.token"
        :key="auth.user.id"
        :data-user-id="auth.user.id"
        ref="frame"
        src="/canvas-app/index.html"
        :title="t('creative.title')"
        class="h-full w-full border-0"
        allow="fullscreen; clipboard-write"
        referrerpolicy="same-origin"
        @load="syncSessionAfterFrameLoad"
      />
      <p v-if="expired" role="alert" class="absolute bottom-4 left-1/2 -translate-x-1/2 rounded-lg bg-red-700 px-4 py-2 text-white">
        {{ t('creative.sessionExpired') }}
      </p>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useAuthStore } from '@/stores/auth'
import { useTheme } from '@/composables/useTheme'
import { isCanvasBridgeMessage } from '@/utils/creativeCanvasBridge'

// @project-doc docs/domains/cloud_canvas.md#session_boundary
const auth = useAuthStore()
const { t, locale } = useI18n()
const { isDark } = useTheme()
const frame = ref<HTMLIFrameElement | null>(null)
const expired = ref(false)
let sessionSyncTimer: ReturnType<typeof setInterval> | null = null

function stopSessionSync(): void {
  if (sessionSyncTimer !== null) {
    clearInterval(sessionSyncTimer)
    sessionSyncTimer = null
  }
}

function sendSession(): void {
  if (!auth.user || !auth.token) return
  // 切换账号的 DOM 更新前，旧画布仍可能发 ready；禁止把新用户令牌送入旧窗口。
  if (frame.value?.dataset.userId !== String(auth.user.id)) return
  frame.value?.contentWindow?.postMessage({
    namespace: 'tokenrouter-canvas',
    type: 'session',
    userId: auth.user.id,
    token: auth.token,
    locale: locale.value === 'en' ? 'en' : 'zh-CN',
    theme: isDark.value ? 'dark' : 'light',
  }, window.location.origin)
  expired.value = false
}

// iframe 首次加载与 React 会话桥初始化存在竞态；收到 ready 前短暂重发同源会话，
// 避免直接从登录后进入创作台时停留在“等待登录会话”。
function syncSessionAfterFrameLoad(): void {
  stopSessionSync()
  let attempts = 0
  const send = () => {
    sendSession()
    attempts += 1
    if (attempts >= 30) stopSessionSync()
  }
  send()
  sessionSyncTimer = setInterval(send, 100)
}

function receiveMessage(event: MessageEvent): void {
  if (!isCanvasBridgeMessage(event, window.location.origin, frame.value?.contentWindow ?? null)) return
  if (event.data.type === 'ready') {
    sendSession()
    stopSessionSync()
  }
  if (event.data.type === 'expired') expired.value = true
}

// 同一用户刷新 JWT 只更新会话，不重建画布、更不重发正在运行的生成请求。
watch(() => [auth.user?.id, auth.token, locale.value, isDark.value], syncSessionAfterFrameLoad, { flush: 'post' })
onMounted(() => {
  window.addEventListener('message', receiveMessage)
  syncSessionAfterFrameLoad()
})
onBeforeUnmount(() => {
  stopSessionSync()
  frame.value?.contentWindow?.postMessage({ namespace: 'tokenrouter-canvas', type: 'logout' }, window.location.origin)
  window.removeEventListener('message', receiveMessage)
})
</script>
