/**
 * GitHub 风格默认头像（identicon）生成工具。
 *
 * 根据用户 ID 等种子字符串确定性地生成 7x7 纵向镜像像素图案：
 * 相同种子永远得到相同头像，不同种子的图案与颜色基本不重复。
 * 全程同步纯函数，输出 SVG data URI，可直接作为 <img src> 使用。
 */

/** 图案网格边长：比 GitHub 经典 5x5 更细密的 7x7，避免头像较大时色块显得过于粗放 */
const GRID_SIZE = 7
/** 纵向镜像对称时只需计算左半部分列数（含中列） */
const HALF_COLUMNS = 4
/** 背景色：GitHub 同款中性浅灰，深浅主题下都协调 */
const BACKGROUND_COLOR = '#ececf0'
/** 前景固定饱和度，仅色相随种子变化 */
const FOREGROUND_SATURATION = 65
/** 前景固定亮度，50% 保证在浅灰底上可读 */
const FOREGROUND_LIGHTNESS = 50

/**
 * FNV-1a 32-bit 哈希。
 * 这里不需要密码学安全性，只要确定性和分布均匀；
 * 不选 SubtleCrypto SHA-256 是因为它是异步接口，无法用于 computed/模板。
 */
function fnv1a(input: string): number {
  let hash = 0x811c9dc5
  for (let i = 0; i < input.length; i++) {
    hash ^= input.charCodeAt(i)
    // 乘以 FNV 素数 16777619（用位运算避免精度问题）
    hash = Math.imul(hash, 0x01000193)
  }
  // 无符号化
  return hash >>> 0
}

/**
 * mulberry32 伪随机数生成器：由 32 位种子产生确定性的 [0, 1) 序列。
 */
function mulberry32(seed: number): () => number {
  let state = seed >>> 0
  return () => {
    state = (state + 0x6d2b79f5) >>> 0
    let t = state
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

/**
 * 根据种子生成默认头像的 SVG data URI。
 * 种子传入前会做归一化（去首尾空格、转小写），空串兜底为 '?'。
 */
export function identiconDataUri(seed: string): string {
  const normalized = seed.trim().toLowerCase() || '?'
  const random = mulberry32(fnv1a(normalized))

  // 前 3 列共 15 格逐格决定是否填充，第 4、5 列镜像第 2、1 列
  const filled: boolean[][] = []
  for (let column = 0; column < HALF_COLUMNS; column++) {
    const columnCells: boolean[] = []
    for (let row = 0; row < GRID_SIZE; row++) {
      columnCells.push(random() < 0.5)
    }
    filled.push(columnCells)
  }

  const hue = Math.floor(random() * 360)
  const foreground = `hsl(${hue}, ${FOREGROUND_SATURATION}%, ${FOREGROUND_LIGHTNESS}%)`

  const rects: string[] = [`<rect width="${GRID_SIZE}" height="${GRID_SIZE}" fill="${BACKGROUND_COLOR}"/>`]
  for (let column = 0; column < GRID_SIZE; column++) {
    // 右半部分列镜像取左半部分的填充状态
    const sourceColumn = column < HALF_COLUMNS ? column : GRID_SIZE - 1 - column
    for (let row = 0; row < GRID_SIZE; row++) {
      if (filled[sourceColumn][row]) {
        rects.push(`<rect x="${column}" y="${row}" width="1" height="1" fill="${foreground}"/>`)
      }
    }
  }

  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${GRID_SIZE} ${GRID_SIZE}" shape-rendering="crispEdges">${rects.join('')}</svg>`
  return `data:image/svg+xml,${encodeURIComponent(svg)}`
}
