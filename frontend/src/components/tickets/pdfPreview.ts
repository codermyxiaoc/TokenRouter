import { getDocument, GlobalWorkerOptions, AnnotationMode } from 'pdfjs-dist'
import workerUrl from 'pdfjs-dist/build/pdf.worker.min.mjs?url'
import jpegFallbackUrl from 'pdfjs-dist/wasm/openjpeg_nowasm_fallback.js?url'
import jbigFallbackUrl from 'pdfjs-dist/wasm/jbig2_nowasm_fallback.js?url'

// 字体与字符映射随站点打包；只允许 PDF.js 请求这些固定资源，不使用文档提供的地址。
const resources = import.meta.glob<string>([
  '/node_modules/pdfjs-dist/cmaps/*.bcmap',
  '/node_modules/pdfjs-dist/standard_fonts/*.{pfb,ttf}',
], { eager: true, query: '?url', import: 'default' })

GlobalWorkerOptions.workerSrc = workerUrl
export const pdfAnnotationMode = AnnotationMode.DISABLE
export const pdfMaxCanvasPixels = 8_000_000

export function loadTicketPDF(data: Uint8Array, signal: AbortSignal) {
  // 禁用 WASM 时，JPEG2000/JBIG2 使用同源的官方 JS 解码器；构建保留它们的固定文件名。
  const jpegDirectory = new URL('.', new URL(jpegFallbackUrl, window.location.href)).href
  const jbigDirectory = new URL('.', new URL(jbigFallbackUrl, window.location.href)).href
  if (jpegDirectory !== jbigDirectory) throw new Error('PDF decoder resources unavailable')
  class LocalBinaryDataFactory {
    async fetch({ kind, filename }: { kind: string; filename: string }): Promise<Uint8Array> {
      const folder = kind === 'cMapUrl' ? 'cmaps' : kind === 'standardFontDataUrl' ? 'standard_fonts' : ''
      const url = folder && resources[`/node_modules/pdfjs-dist/${folder}/${filename}`]
      if (!url) throw new Error('Unsupported PDF resource')
      // Vite 的小文件可能内嵌为 data URL，直接解码可避免额外 CSP connect-src 权限。
      if (url.startsWith('data:')) {
        return Uint8Array.from(atob(url.slice(url.indexOf(',') + 1)), char => char.charCodeAt(0))
      }
      const response = await fetch(url, { signal, credentials: 'same-origin' })
      if (!response.ok) throw new Error('PDF resource unavailable')
      return new Uint8Array(await response.arrayBuffer())
    }
  }
  // 只绘制页面像素，不创建注释、脚本、表单或外部链接；无需放宽现有 CSP。
  return getDocument({
    data, useWorkerFetch: false, BinaryDataFactory: LocalBinaryDataFactory,
    wasmUrl: jpegDirectory,
    useWasm: false, enableXfa: false, disableFontFace: true, useSystemFonts: true,
    maxImageSize: 40_000_000, canvasMaxAreaInBytes: pdfMaxCanvasPixels * 4,
    stopAtErrors: true,
  })
}
