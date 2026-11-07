<template>
  <span class="hidden" aria-hidden="true"></span>
  <TotpLoginModal
    v-if="totpTempToken"
    ref="totpModalRef"
    :temp-token="totpTempToken"
    :user-email-masked="totpUserEmailMasked"
    @verify="handleTotpVerify"
    @cancel="totpTempToken = ''"
  />
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import TotpLoginModal from '@/components/auth/TotpLoginModal.vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { exchangeGoogleOneTap, persistOAuthTokenContext } from '@/api/auth'
import { useAppStore, useAuthStore } from '@/stores'
import {
  cancelGoogleOneTap,
  isGoogleOneTapOriginSupported,
  showGoogleOneTap,
  type GoogleCredentialResponse
} from '@/utils/googleIdentity'
import {
  clearAllAffiliateCodes,
  resolveAffiliateCode,
  storeOAuthAffiliateCode
} from '@/utils/oauthAffiliate'
import { extractI18nErrorMessage } from '@/utils/apiError'

const props = defineProps<{
  enabled: boolean
  clientId: string
}>()

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()
let processingCredential = false
// 账号开启两步验证时的 2FA 临时会话与登录后跳转地址。
const totpTempToken = ref('')
const totpUserEmailMasked = ref('')
const totpModalRef = ref<InstanceType<typeof TotpLoginModal> | null>(null)
let totpRedirect = '/dashboard'

async function handleTotpVerify(code: string) {
  totpModalRef.value?.setVerifying(true)
  try {
    await authStore.login2FA(totpTempToken.value, code)
    totpTempToken.value = ''
    clearAllAffiliateCodes()
    appStore.showSuccess(t('auth.loginSuccess'))
    await router.push(totpRedirect)
  } catch (error: unknown) {
    totpModalRef.value?.setError(extractI18nErrorMessage(error, t, 'auth.errors', t('auth.loginFailed')))
  } finally {
    totpModalRef.value?.setVerifying(false)
  }
}

const canPrompt = computed(
  () =>
    props.enabled &&
    Boolean(props.clientId.trim()) &&
    !authStore.isAuthenticated &&
    isGoogleOneTapOriginSupported()
)

function sanitizeRedirectPath(value: unknown): string {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//')) {
    return '/dashboard'
  }
  if (value.includes('://') || value.includes('\n') || value.includes('\r')) {
    return '/dashboard'
  }
  return value
}

function routeQueryValue(value: unknown): string {
  const candidate = Array.isArray(value) ? value[0] : value
  return typeof candidate === 'string' ? candidate.trim() : ''
}

async function handleCredential(response: GoogleCredentialResponse): Promise<void> {
  const credential = response.credential?.trim()
  if (!credential || processingCredential) return

  processingCredential = true
  cancelGoogleOneTap(handleCredential)
  const redirect = sanitizeRedirectPath(route.query.redirect)
  const affiliateCode = resolveAffiliateCode(route.query.aff, route.query.aff_code)
  storeOAuthAffiliateCode(affiliateCode)

  try {
    const result = await exchangeGoogleOneTap({
      credential,
      redirect,
      aff_code: affiliateCode || undefined,
      promo_code: routeQueryValue(route.query.promo_code) || undefined
    })
    if (result.status === 'registration_required') {
      window.sessionStorage.setItem('email_oauth_pending_provider', 'google')
      await router.push('/auth/oauth/callback')
      return
    }
    if (result.status === 'totp_required') {
      totpRedirect = redirect
      totpUserEmailMasked.value = result.user_email_masked || ''
      totpTempToken.value = result.temp_token
      return
    }

    persistOAuthTokenContext(result)
    await authStore.setToken(result.access_token)
    clearAllAffiliateCodes()
    appStore.showSuccess(t('auth.loginSuccess'))
    await router.push(redirect)
  } catch (error: unknown) {
    appStore.showError(extractI18nErrorMessage(error, t, 'auth.errors', t('auth.loginFailed')))
  } finally {
    processingCredential = false
  }
}

function cancelPrompt(): void {
  cancelGoogleOneTap(handleCredential)
}

watch(
  [canPrompt, () => props.clientId],
  ([enabled]) => {
    if (!enabled) {
      cancelPrompt()
      return
    }
    // SDK 或浏览器拒绝展示时保持静默，页面上的常规登录入口仍然可用。
    void showGoogleOneTap(props.clientId, handleCredential).catch(() => undefined)
  },
  { immediate: true }
)

onBeforeUnmount(cancelPrompt)

defineExpose({ cancelPrompt })
</script>
