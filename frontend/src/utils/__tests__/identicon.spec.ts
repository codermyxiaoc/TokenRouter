import { identiconDataUri } from '../identicon'

/** 解码 data URI 拿到 SVG 文本，便于断言结构 */
function decodeSvg(dataUri: string): string {
  return decodeURIComponent(dataUri.replace(/^data:image\/svg\+xml,/, ''))
}

describe('identiconDataUri', () => {
  it('相同种子生成完全相同的头像', () => {
    expect(identiconDataUri('alice@example.com')).toBe(identiconDataUri('alice@example.com'))
  })

  it('种子归一化：忽略大小写与首尾空格', () => {
    expect(identiconDataUri('  Alice@Example.COM ')).toBe(identiconDataUri('alice@example.com'))
  })

  it('空种子兜底为固定图案', () => {
    expect(identiconDataUri('')).toBe(identiconDataUri('?'))
    expect(identiconDataUri('   ')).toBe(identiconDataUri('?'))
  })

  it('不同种子生成互不相同的头像', () => {
    const seeds = [
      'alice@example.com',
      'bob@example.com',
      'carol@example.com',
      'dave@example.com',
      'erin@example.com',
    ]
    const outputs = new Set(seeds.map(identiconDataUri))
    expect(outputs.size).toBe(seeds.length)
  })

  it('输出为 7x7 网格的 SVG data URI', () => {
    const dataUri = identiconDataUri('alice@example.com')
    expect(dataUri.startsWith('data:image/svg+xml,')).toBe(true)
    const svg = decodeSvg(dataUri)
    expect(svg).toContain('viewBox="0 0 7 7"')
    expect(svg).toContain('shape-rendering="crispEdges"')
  })

  it('图案保持纵向镜像对称', () => {
    const svg = decodeSvg(identiconDataUri('alice@example.com'))
    // 提取所有前景色块坐标（背景 rect 不带 x/y 属性）
    const cells = new Set(
      [...svg.matchAll(/<rect x="(\d)" y="(\d)" width="1" height="1" fill="hsl\(/g)]
        .map((match) => `${match[1]},${match[2]}`),
    )
    expect(cells.size).toBeGreaterThan(0)
    for (const cell of cells) {
      const [x, y] = cell.split(',').map(Number)
      expect(cells.has(`${6 - x},${y}`)).toBe(true)
    }
  })
})
