import { describe, expect, it } from 'vitest'

import en from '../locales/en/admin/accounts'
import zh from '../locales/zh/admin/accounts'

describe('OpenAI WS mode locale descriptions', () => {
  it('separates account enablement from the global v2 routing requirement', () => {
    for (const locale of [zh, en]) {
      const ws = locale.accounts.openai
      expect(ws.wsModeDesc.toLowerCase()).toContain('off')
      expect(ws.wsModeRoutingDesc).toContain('mode_router_v2_enabled=true')
      expect(ws.wsModeRoutingDesc).toContain('false')
      expect(ws.wsModeRoutingDesc).toContain('http_bridge')
      expect(ws.wsModeHttpBridgeHint).toContain('HTTP/SSE')
    }
  })
})
