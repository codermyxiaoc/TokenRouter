import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const viewPath = resolve(dirname(fileURLToPath(import.meta.url)), '../CreativeStudioView.vue')
const viewSource = readFileSync(viewPath, 'utf8')

describe('CreativeStudioView 移动端视口布局', () => {
  it('使用 AppLayout 全屏视口模式', () => {
    expect(viewSource).toContain('<AppLayout full-viewport>')
  })

  it('stage 继续使用动态视口高度并从顶栏下方开始', () => {
    expect(viewSource).toContain('h-[calc(100dvh-3.5rem)]')
    expect(viewSource).toContain('md:-mt-5')
    expect(viewSource).not.toContain('h-[calc(100vh-')
  })
})
