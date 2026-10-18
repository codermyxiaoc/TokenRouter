import { fileURLToPath, URL } from 'node:url';
import { readFileSync } from 'node:fs';
import react from '@vitejs/plugin-react';
import tailwind from '@tailwindcss/vite';
import { defineConfig } from 'vite';

// 独立 iframe 隔离 React / Tailwind 4；构建只清理子目录，不触碰主站产物。
export default defineConfig({
  base: '/canvas-app/', plugins: [
    react(), tailwind(),
    {
      name: 'canvas-third-party-notices',
      generateBundle() {
        // 许可证随静态产物嵌入二进制和镜像，不能只留在源码仓库中。
        for (const fileName of ['LICENSE.infinite-canvas', 'NOTICE.md']) {
          this.emitFile({ type: 'asset', fileName, source: readFileSync(new URL(`./${fileName}`, import.meta.url), 'utf8') });
        }
      },
    },
  ],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: {host:'127.0.0.1',port:5174,strictPort:true},
  build: { outDir: '../../backend/internal/web/dist/canvas-app', emptyOutDir: true },
});
