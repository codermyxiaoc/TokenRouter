import { describe, expect, it } from 'vitest'

import {
  applyCodexImageToolMode,
  readCodexImageToolMode,
  type CodexImageToolMode,
} from '@/utils/codexImageToolMode'

describe('Codex 图片工具策略', () => {
  it.each<[string, Record<string, unknown> | undefined, CodexImageToolMode]>([
    ['空配置跟随渠道', undefined, 'inherit'],
    ['读取新桥接开关 true', { codex_image_generation_bridge: true }, 'enabled'],
    ['读取新桥接开关 false', { codex_image_generation_bridge: false }, 'disabled'],
    ['兼容旧桥接开关', { codex_image_generation_bridge_enabled: true }, 'enabled'],
    [
      '新桥接开关优先于旧键',
      { codex_image_generation_bridge: false, codex_image_generation_bridge_enabled: true },
      'disabled',
    ],
    [
      'strip 策略优先于桥接开关',
      { codex_image_generation_explicit_tool_policy: 'strip', codex_image_generation_bridge: true },
      'block',
    ],
    [
      '非布尔和非字符串覆盖视为未设置',
      {
        codex_image_generation_bridge: null,
        codex_image_generation_bridge_enabled: 'true',
        codex_image_generation_explicit_tool_policy: null,
      },
      'inherit',
    ],
  ])('%s', (_name, extra, expected) => {
    expect(readCodexImageToolMode(extra)).toBe(expected)
  })

  it.each<[CodexImageToolMode, Record<string, unknown>]>([
    ['inherit', {}],
    ['enabled', { codex_image_generation_bridge: true }],
    ['disabled', { codex_image_generation_bridge: false }],
    ['block', { codex_image_generation_explicit_tool_policy: 'strip' }],
  ])('完整对象以删除方式保存 %s', (mode, expected) => {
    const extra: Record<string, unknown> = {
      untouched: 'keep',
      codex_image_generation_bridge: true,
      codex_image_generation_bridge_enabled: false,
      codex_image_generation_explicit_tool_policy: 'strip',
    }

    expect(applyCodexImageToolMode(extra, mode)).toEqual({ untouched: 'keep', ...expected })
  })

  it.each<[CodexImageToolMode, Record<string, unknown>]>([
    [
      'inherit',
      {
        codex_image_generation_bridge: null,
        codex_image_generation_bridge_enabled: null,
        codex_image_generation_explicit_tool_policy: null,
      },
    ],
    [
      'enabled',
      {
        codex_image_generation_bridge: true,
        codex_image_generation_bridge_enabled: null,
        codex_image_generation_explicit_tool_policy: null,
      },
    ],
    [
      'disabled',
      {
        codex_image_generation_bridge: false,
        codex_image_generation_bridge_enabled: null,
        codex_image_generation_explicit_tool_policy: null,
      },
    ],
    [
      'block',
      {
        codex_image_generation_bridge: null,
        codex_image_generation_bridge_enabled: null,
        codex_image_generation_explicit_tool_policy: 'strip',
      },
    ],
  ])('JSONB 合并以 null 方式保存 %s', (mode, expected) => {
    expect(applyCodexImageToolMode({}, mode, 'null')).toEqual(expected)
  })
})
