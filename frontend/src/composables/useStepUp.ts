/**
 * 敏感操作二次验证组合式函数。
 *
 * 当服务端返回 STEP_UP_REQUIRED 时打开 TOTP 对话框，取得短期授权后透明重试原操作。
 * 页面模板需要挂载 `<TotpStepUpDialog :controller="stepUp" />`。
 */
import { ref } from 'vue'

/** 服务端用于表示二次验证状态的错误码。 */
const STEP_UP_REQUIRED = 'STEP_UP_REQUIRED'
const STEP_UP_TOTP_NOT_ENABLED = 'STEP_UP_TOTP_NOT_ENABLED'
const STEP_UP_ADMIN_API_KEY_FORBIDDEN = 'STEP_UP_ADMIN_API_KEY_FORBIDDEN'

/** 用户关闭 TOTP 对话框时抛出的哨兵错误，调用方应静默处理。 */
export class StepUpCancelledError extends Error {
  readonly code = 'STEP_UP_CANCELLED'
  constructor() {
    super('step-up verification cancelled by user')
    this.name = 'StepUpCancelledError'
  }
}

export function isStepUpCancelled(err: unknown): boolean {
  return err instanceof StepUpCancelledError
}

interface ApiError {
  status?: number
  code?: string | number
  reason?: string
  message?: string
}

/** 从 code 或 reason 两种响应结构中提取语义错误码。 */
function markerOf(err: unknown): string {
  const e = (err ?? {}) as ApiError
  const candidates = [e.code, e.reason].map((v) => (typeof v === 'string' ? v : ''))
  return candidates.find((v) => v.startsWith('STEP_UP')) || ''
}

export function isStepUpRequired(err: unknown): boolean {
  return markerOf(err) === STEP_UP_REQUIRED
}

export function isStepUpBlocked(err: unknown): boolean {
  const m = markerOf(err)
  return m === STEP_UP_TOTP_NOT_ENABLED || m === STEP_UP_ADMIN_API_KEY_FORBIDDEN
}

export function stepUpBlockReason(err: unknown): string {
  return markerOf(err)
}

export type StepUpController = ReturnType<typeof useStepUp>

export function useStepUp() {
  const visible = ref(false)
  const blockedReason = ref<string>('')
  let resolver: ((ok: boolean) => void) | null = null

  /** 打开 TOTP 对话框，取得授权后返回 true。 */
  function prompt(): Promise<boolean> {
    visible.value = true
    return new Promise<boolean>((resolve) => {
      resolver = resolve
    })
  }

  function onVerified() {
    visible.value = false
    resolver?.(true)
    resolver = null
  }

  function onCancel() {
    visible.value = false
    resolver?.(false)
    resolver = null
  }

  /**
   * 执行敏感操作；需要二次验证时提示输入 TOTP，并在通过后重试一次。
   * 未启用 TOTP 或使用管理 API Key 时直接向调用方抛错；用户取消时抛出专用哨兵错误。
   */
  async function run<T>(action: () => Promise<T>): Promise<T> {
    try {
      return await action()
    } catch (err) {
      if (isStepUpBlocked(err)) {
        blockedReason.value = markerOf(err)
        throw err
      }
      if (!isStepUpRequired(err)) {
        throw err
      }
      const ok = await prompt()
      if (!ok) {
        throw new StepUpCancelledError()
      }
      // 当前会话已取得短期授权，只重试一次。
      return await action()
    }
  }

  return {
    visible,
    blockedReason,
    prompt,
    onVerified,
    onCancel,
    run
  }
}
