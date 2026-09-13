export type CodexImageToolMode = 'inherit' | 'enabled' | 'disabled' | 'block'

export const CODEX_IMAGE_GENERATION_BRIDGE_KEY = 'codex_image_generation_bridge'
export const LEGACY_CODEX_IMAGE_GENERATION_BRIDGE_KEY = 'codex_image_generation_bridge_enabled'
export const CODEX_IMAGE_GENERATION_POLICY_KEY = 'codex_image_generation_explicit_tool_policy'

type CodexImageToolCleanupMode = 'delete' | 'null'

// 显式 strip 的限制最强，读取时必须优先于新旧桥接开关。
export function readCodexImageToolMode(extra: Record<string, unknown> | null | undefined): CodexImageToolMode {
  if (extra?.[CODEX_IMAGE_GENERATION_POLICY_KEY] === 'strip') {
    return 'block'
  }

  const bridgeValue = typeof extra?.[CODEX_IMAGE_GENERATION_BRIDGE_KEY] === 'boolean'
    ? extra[CODEX_IMAGE_GENERATION_BRIDGE_KEY]
    : extra?.[LEGACY_CODEX_IMAGE_GENERATION_BRIDGE_KEY]
  if (bridgeValue === true) return 'enabled'
  if (bridgeValue === false) return 'disabled'
  return 'inherit'
}

// 完整对象保存可以删除键；JSONB 合并保存则写 null，使运行时把旧覆盖视为未设置。
export function applyCodexImageToolMode(
  extra: Record<string, unknown>,
  mode: CodexImageToolMode,
  cleanupMode: CodexImageToolCleanupMode = 'delete'
): Record<string, unknown> {
  const clear = (key: string) => {
    if (cleanupMode === 'null') {
      extra[key] = null
      return
    }
    delete extra[key]
  }

  clear(CODEX_IMAGE_GENERATION_BRIDGE_KEY)
  clear(LEGACY_CODEX_IMAGE_GENERATION_BRIDGE_KEY)
  clear(CODEX_IMAGE_GENERATION_POLICY_KEY)

  if (mode === 'enabled' || mode === 'disabled') {
    extra[CODEX_IMAGE_GENERATION_BRIDGE_KEY] = mode === 'enabled'
  } else if (mode === 'block') {
    extra[CODEX_IMAGE_GENERATION_POLICY_KEY] = 'strip'
  }

  return extra
}
