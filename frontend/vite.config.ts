import { defineConfig, type Plugin } from 'vite'
import type { OutputChunk, OutputAsset } from 'rollup'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from "path"

/**
 * 入口链路 modulepreload：构建后在 index.html 注入 <link rel="modulepreload">，
 * 覆盖入口 chunk 及其全部静态依赖（vendor-react、主 chunk 等）。
 *
 * 收益：浏览器解析完 HTML 即并行发起所有初始 JS 下载，不必等入口 module
 * 解析出 import 再逐个请求——FN Connect 公网高延迟隧道下省掉一跳串行
 * RTT（每跳 ~200-500ms）。动态 import 的 chunk 由 Vite 自带 preload，不重复。
 */
function preloadInitialChunks(): Plugin {
  let bundle: Record<string, OutputChunk | OutputAsset> = {}
  return {
    name: 'moo:preload-initial-chunks',
    apply: 'build',
    enforce: 'post',
    generateBundle: (_opts, b) => {
      bundle = b as Record<string, OutputChunk | OutputAsset>
    },
    transformIndexHtml: {
      order: 'post',
      handler: (html: string) => {
        const names = new Set<string>()
        for (const f of Object.values(bundle)) {
          if (f.type !== 'chunk' || !f.isEntry) continue
          names.add(f.fileName)
          for (const dep of f.imports ?? []) names.add(dep)
        }
        if (names.size === 0) return html
        const links = [...names]
          .sort()
          .map((n) => `    <link rel="modulepreload" href="./${n}" />`)
          .join('\n')
        return html.replace('</head>', `${links}\n  </head>`)
      },
    },
  }
}

// https://vite.dev/
export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    preloadInitialChunks(),
  ],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  // 相对 base：同一份产物在网关挂载点 /app/moo/ 与直连调试 / 下都能工作
  base: './',
  build: {
    outDir: '../web/dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // react 核心独立分包：与业务 chunk 并行下载，跨版本浏览器缓存命中
        manualChunks: {
          'vendor-react': ['react', 'react-dom'],
        },
      },
    },
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:38100',
        changeOrigin: true,
      },
    },
  },
})
