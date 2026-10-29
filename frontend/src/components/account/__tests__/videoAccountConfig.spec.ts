import { describe, expect, it } from 'vitest'
import {
  VIDEO_BUILTIN_PROTOCOLS, VIDEO_ENDPOINT_ADAPTERS, VIDEO_ENDPOINTS, VIDEO_LEGACY_CREDENTIAL_KEYS, VIDEO_VIDU_PATHS,
  createVideoAccountForm, legacyVideoProtocolRows, parseVideoProtocol, stripLegacyVideoCredentials,
  validateVideoAccountForm, videoAccountCredentials, videoProtocolPaths, videoProtocolRequiresPath,
  type VideoAccountForm, type VideoProtocolRow
} from '../videoAccountConfig'

const BASE = 'https://video.example.test'
const form = (rows: Partial<VideoProtocolRow>[], extra: Partial<VideoAccountForm> = {}): VideoAccountForm => ({
  rows: rows.map(row => ({ model: '*', protocol: '', path: '', baseUrl: '', ...row })),
  maxPendingTasks: 10, maxDurationSeconds: null, ...extra,
})

describe('parseVideoProtocol', () => {
  // 内置协议只接受同一端点下的方案，模板协议遵循服务端键名规则。
  it('accepts every builtin endpoint.adapter combination', () => {
    for (const endpoint of VIDEO_ENDPOINTS) {
      for (const adapter of VIDEO_ENDPOINT_ADAPTERS[endpoint]) {
        expect(parseVideoProtocol(`${endpoint}.${adapter}`)).toEqual({ kind: 'builtin', endpoint, adapter })
      }
    }
    // 内置协议只保留官方格式，厂商格式改为预置模板。
    expect(VIDEO_BUILTIN_PROTOCOLS).toEqual(['compat.openai_v1', 'openai_videos.openai_v1', 'seedance.seedance_v1', 'kling.kling_v1', 'wan.wan_v1', 'minimax.minimax_v2', 'vidu.vidu_v2'])
    // 已被取代的旧方案仍可解析，用于展示和试跑尚未迁移的账号。
    expect(parseVideoProtocol('seedance.kemei_v1')).toEqual({ kind: 'builtin', endpoint: 'seedance', adapter: 'kemei_v1' })
  })

  it.each(['compat.yezonghui_v1', 'kling.vidu_v2', 'unknown.openai_v1', 'openai_videos', '', 'profile:', 'profile:Bad', 'profile:-x', 'profile:x@0', 'profile:x@1.5', 'profile:x@'])('rejects %j', value => {
    expect(parseVideoProtocol(value)).toBeNull()
  })

  it('parses template references with optional versions', () => {
    expect(parseVideoProtocol('profile:vendor_x')).toEqual({ kind: 'profile', key: 'vendor_x' })
    expect(parseVideoProtocol(' profile:vendor-x@3 ')).toEqual({ kind: 'profile', key: 'vendor-x', version: 3 })
  })

  it('limits Kling paths by adapter and requires paths only for Kling/Vidu', () => {
    expect(videoProtocolPaths('kling.kling_v1')).toEqual(['/text-to-video/{model}', '/image-to-video/{model}'])
    expect(videoProtocolPaths('kling.legacy_v1')).toEqual(['/text-to-video/{model}', '/image-to-video/{model}'])
    expect(videoProtocolPaths('vidu.vidu_v2')).toEqual(VIDEO_VIDU_PATHS)
    expect(videoProtocolPaths('seedance.seedance_v1')).toEqual([])
    expect(videoProtocolRequiresPath('vidu.kemei_v1')).toBe(true)
    expect(videoProtocolRequiresPath('profile:vendor_x')).toBe(false)
  })
})

describe('validateVideoAccountForm', () => {
  it('accepts a complete mixed configuration', () => {
    expect(validateVideoAccountForm(form([
      { model: 'kling-v2', protocol: 'kling.kling_v1', path: '/text-to-video/{model}' },
      { model: 'seedance-1', protocol: 'seedance.seedance_v1', baseUrl: 'https://ark.example/api/v3' },
      { model: 'vendor-x', protocol: 'profile:vendor_x' },
      { model: '*', protocol: 'openai_videos.openai_v1' },
    ]), BASE)).toBeNull()
  })

  it.each<[string, Partial<VideoProtocolRow>[], string]>([
    ['empty model', [{ model: ' ', protocol: 'compat.openai_v1' }], 'bindingModelRequired'],
    ['partial wildcard', [{ model: 'kling-*', protocol: 'compat.openai_v1' }], 'protocolModelWildcardInvalid'],
    ['question wildcard', [{ model: 'm?', protocol: 'compat.openai_v1' }], 'protocolModelWildcardInvalid'],
    ['duplicate model', [{ model: 'a', protocol: 'compat.openai_v1' }, { model: ' a ', protocol: 'wan.wan_v1' }], 'bindingDuplicate'],
    ['missing protocol', [{ model: '*' }], 'protocolRequired'],
    ['cross endpoint adapter', [{ protocol: 'compat.yezonghui_v1' }], 'protocolInvalid'],
    ['kling without path', [{ model: 'k', protocol: 'kling.kling_v1' }], 'protocolPathRequired'],
    ['vidu without path', [{ model: 'v', protocol: 'vidu.vidu_v2' }], 'protocolPathRequired'],
    ['path on other endpoint', [{ protocol: 'seedance.seedance_v1', path: '/ent/v2/img2video' }], 'protocolPathForbidden'],
    ['path on template', [{ protocol: 'profile:vendor_x', path: '/text-to-video/{model}' }], 'protocolPathForbidden'],
    ['kling_v1 omni', [{ model: 'k', protocol: 'kling.kling_v1', path: '/omni-video/{model}' }], 'adapterKlingPathInvalid'],
    ['legacy kling path without template', [{ model: 'k', protocol: 'kling.legacy_v1', path: '/v1/videos/text2video' }], 'protocolDeprecated'],
    ['unknown kling path', [{ model: 'k', protocol: 'kling.kling_v1', path: '/x' }], 'adapterKlingPathInvalid'],
    ['kling path on vidu', [{ model: 'v', protocol: 'vidu.vidu_v2', path: '/text-to-video/{model}' }], 'bindingPathInvalid'],
    ['bad base url', [{ protocol: 'compat.openai_v1', baseUrl: 'https://user:pw@x.test' }], 'baseUrlInvalid'],
  ])('rejects %s', (_, rows, error) => {
    expect(validateVideoAccountForm(form(rows), BASE)).toBe(error)
  })

  it('requires at least one row, a base URL and valid budgets', () => {
    expect(validateVideoAccountForm(form([]), BASE)).toBe('protocolsRequired')
    expect(validateVideoAccountForm(form([{ protocol: 'compat.openai_v1' }]), ' ')).toBe('baseUrlRequired')
    expect(validateVideoAccountForm(form([{ protocol: 'compat.openai_v1', baseUrl: BASE }]), '')).toBeNull()
    expect(validateVideoAccountForm(form([{ protocol: 'compat.openai_v1' }], { maxPendingTasks: 0 }), BASE)).toBe('pendingInvalid')
    expect(validateVideoAccountForm(form([{ protocol: 'compat.openai_v1' }], { maxDurationSeconds: -1 }), BASE)).toBe('budgetInvalid')
  })
})

describe('video account credentials', () => {
  it('starts new accounts with one unselected * row and round-trips saved protocols', () => {
    expect(createVideoAccountForm().rows).toEqual([{ model: '*', protocol: '', path: '', baseUrl: '' }])
    const saved = {
      video_model_protocols: {
        '*': { protocol: 'openai_videos.openai_v1' },
        'kling-v2': { protocol: 'kling.kling_v1', path: '/text-to-video/{model}' },
        'seedance-1': { protocol: 'seedance.seedance_v1', base_url: 'https://ark.example/api/v3' },
        'vendor-x': { protocol: 'profile:vendor_x' },
      },
      video_max_pending_tasks: 8, video_max_duration_seconds: 15,
    }
    const loaded = createVideoAccountForm(saved)
    // 通配默认行排在最后，精确模型保持原顺序。
    expect(loaded.rows.map(row => row.model)).toEqual(['kling-v2', 'seedance-1', 'vendor-x', '*'])
    expect(validateVideoAccountForm(loaded, BASE)).toBeNull()
    expect(videoAccountCredentials(loaded)).toEqual(saved)
  })

  it('drops paths that the protocol cannot use and never emits legacy keys', () => {
    const credentials = videoAccountCredentials(form([{ protocol: 'compat.openai_v1', path: '/stale', baseUrl: ' ' }]))
    expect(credentials.video_model_protocols).toEqual({ '*': { protocol: 'compat.openai_v1' } })
    for (const key of VIDEO_LEGACY_CREDENTIAL_KEYS) expect(credentials).not.toHaveProperty(key)
    const existing: Record<string, unknown> = { api_key: 'k', video_endpoints: ['compat'], video_model_bindings: {}, video_openai_profile: 'metaso_h3' }
    expect(stripLegacyVideoCredentials(existing)).toEqual({ api_key: 'k' })
  })
})

describe('legacy conversion', () => {
  // 与服务端换算一致：openai_videos 优先，其次 compat，再其次第一个端点。
  it('prefers openai_videos, then compat, then the first endpoint for bound models and the * default', () => {
    const rows = legacyVideoProtocolRows({
      video_endpoints: ['seedance', 'compat', 'openai_videos'],
      video_model_bindings: { both: ['compat', 'openai_videos'], compat_only: ['seedance', 'compat'], native: 'seedance' },
      video_endpoint_adapters: { seedance: 'seedance_v1' },
      video_base_urls: { seedance: 'https://ark.example', compat: 'https://compat.example' },
      video_openai_profile: 'metaso_h3',
    })
    expect(rows).toEqual([
      // 旧厂商方案按服务端规则改绑为预置模板。
      { model: 'both', protocol: 'profile:metaso-h3', path: '', baseUrl: '' },
      { model: 'compat_only', protocol: 'profile:generic-flat', path: '', baseUrl: 'https://compat.example' },
      { model: 'native', protocol: 'seedance.seedance_v1', path: '', baseUrl: 'https://ark.example' },
      { model: '*', protocol: 'profile:metaso-h3', path: '', baseUrl: '' },
    ])
  })

  it('resolves model adapter overrides before endpoint defaults and adds exact rows for override-only models', () => {
    const rows = legacyVideoProtocolRows({
      video_endpoints: ['openai_videos'],
      video_endpoint_adapters: { openai_videos: 'openai_v1' },
      video_model_bindings: { relay: 'openai_videos' },
      video_model_adapters: { relay: { openai_videos: 'yezonghui_v1' }, unbound: { openai_videos: 'metaso_h3_v1' } },
    })
    expect(rows).toEqual([
      { model: 'relay', protocol: 'profile:yezonghui', path: '', baseUrl: '' },
      { model: 'unbound', protocol: 'profile:metaso-h3', path: '', baseUrl: '' },
      { model: '*', protocol: 'openai_videos.openai_v1', path: '', baseUrl: '' },
    ])
  })

  it('creates exact Kling/Vidu rows from path maps instead of a * default', () => {
    const kling = legacyVideoProtocolRows({
      video_endpoints: ['kling'], video_endpoint_adapters: { kling: 'kling_v1' },
      video_model_bindings: { bound: 'kling' },
      video_model_paths: { bound: '/text-to-video/{model}', unbound: '/image-to-video/{model}', bad: '/omni-video/{model}' },
    })
    // kling_v1 不支持 omni，无法通过新规则的旧绑定被丢弃；没有 * 默认行。
    expect(kling).toEqual([
      { model: 'bound', protocol: 'kling.kling_v1', path: '/text-to-video/{model}', baseUrl: '' },
      { model: 'unbound', protocol: 'kling.kling_v1', path: '/image-to-video/{model}', baseUrl: '' },
    ])
    const vidu = legacyVideoProtocolRows({
      video_endpoints: ['vidu'], video_endpoint_adapters: { vidu: 'vidu_v2' },
      video_vidu_model_paths: { q2: '/ent/v2/img2video' }, video_model_bindings: { nopath: 'vidu' },
    })
    expect(vidu).toEqual([{ model: 'q2', protocol: 'vidu.vidu_v2', path: '/ent/v2/img2video', baseUrl: '' }])
  })

  it('ignores Kling paths for override rows when the default endpoint is not Kling', () => {
    const rows = legacyVideoProtocolRows({
      video_endpoints: ['compat'], video_model_paths: { stray: '/text-to-video/{model}' },
    })
    expect(rows).toEqual([{ model: '*', protocol: 'profile:generic-flat', path: '', baseUrl: '' }])
  })

  it('uses saved video_model_protocols over legacy fields and falls back to an empty * row', () => {
    expect(createVideoAccountForm({ video_model_protocols: { m: { protocol: 'wan.wan_v1' } }, video_endpoints: ['compat'] }).rows)
      .toEqual([{ model: 'm', protocol: 'wan.wan_v1', path: '', baseUrl: '' }])
    expect(createVideoAccountForm({ video_endpoints: ['compat'] }).rows)
      .toEqual([{ model: '*', protocol: 'profile:generic-flat', path: '', baseUrl: '' }])
    expect(createVideoAccountForm({ video_endpoints: ['kling'] }).rows)
      .toEqual([{ model: '*', protocol: '', path: '', baseUrl: '' }])
  })
})

describe('deprecated protocol rebinding', () => {
  // 与服务端 videoDeprecatedProtocolReplacement 一致：厂商方案改为预置模板，原样协议在原生端点改为官方协议。
  it.each<[string, string, string]>([
    ['openai_videos.metaso_h3_v1', '', 'profile:metaso-h3'],
    ['openai_videos.yezonghui_v1', '', 'profile:yezonghui'],
    ['openai_videos.legacy_v1', '', 'profile:generic-flat-videos'],
    ['compat.legacy_v1', '', 'profile:generic-flat'],
    ['compat.kemei_v1', '', 'profile:kemei-compat'],
    ['minimax.kemei_v1', '', 'profile:kemei-minimax'],
    ['kling.kemei_v1', '/omni-video/{model}', 'profile:kemei-kling-omni'],
    ['vidu.kemei_v1', '/ent/v2/start-end2video', 'profile:kemei-vidu-start-end2video'],
    ['seedance.legacy_v1', '', 'seedance.seedance_v1'],
    ['kling.legacy_v1', '/image-to-video/{model}', 'kling.kling_v1'],
  ])('rebinds %s', (protocol, path, want) => {
    const rows = createVideoAccountForm({ video_model_protocols: { m: { protocol, path, base_url: 'https://b.example' } } }).rows
    expect(rows[0].protocol).toBe(want)
    expect(rows[0].baseUrl).toBe('https://b.example')
    expect(validateVideoAccountForm({ rows, maxPendingTasks: 10, maxDurationSeconds: null }, BASE)).toBeNull()
  })

  it('keeps legacy Kling paths without an equivalent template and asks for manual rebinding', () => {
    const rows = createVideoAccountForm({ video_model_protocols: { m: { protocol: 'kling.legacy_v1', path: '/v1/videos/omni-video' } } }).rows
    expect(rows[0].protocol).toBe('kling.legacy_v1')
    expect(validateVideoAccountForm({ rows, maxPendingTasks: 10, maxDurationSeconds: null }, BASE)).toBe('protocolDeprecated')
  })
})
