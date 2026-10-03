import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { ThemeProvider } from 'next-themes'
import './index.css'
import App from './App.tsx'

// 图标 Service Worker：仅在安全上下文（https/localhost）可注册。
// http 明文（LAN IP）注册会被浏览器静默拒绝——功能自动降级为
// 纯 HTTP 缓存（max-age=86400），不影响任何现有行为。
function isSecureContext(): boolean {
  return window.isSecureContext || location.hostname === 'localhost' || location.hostname === '127.0.0.1'
}

if ('serviceWorker' in navigator && isSecureContext()) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('sw.js').catch(() => {})
  })
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem>
      <App />
    </ThemeProvider>
  </StrictMode>,
)

// 首帧 splash 移除：双 rAF 等 React 首帧真正绘制后再淡出（避免 splash 与
// 首帧内容同帧出现造成闪烁）。splash 本身零请求内联在 index.html，
// 在 JS 下载/执行期间提供正确底色 + 呼吸 logo（FN Connect 公网高延迟下
// 消除白屏首帧）。
requestAnimationFrame(() => {
  requestAnimationFrame(() => {
    const s = document.getElementById('moo-splash')
    if (!s) return
    s.style.opacity = '0'
    window.setTimeout(() => s.remove(), 350)
  })
})
