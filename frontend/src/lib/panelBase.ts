/**
 * 0.6.349（issue moo#7）：后端解析出的面板基址 → 浏览器可达地址。
 *
 * 后端拿到的是「本机回环」地址（http://127.0.0.1:<面板端口>），那是 Moo
 * 进程自己访问面板用的；浏览器里 127.0.0.1 指向用户自己的机器，必须换成
 * 当前页面主机名。协议与端口原样保留（自定义端口由此传导到授权页）。
 */
export const panelBaseForBrowser = (resolved: string, pageHost: string): string => {
  try {
    const u = new URL(resolved);
    const h = u.hostname.toLowerCase();
    const loopback = h === '127.0.0.1' || h === '::1' || h === '[::1]' || h === 'localhost';
    const pageIsLoopback = !pageHost || /^(127\.0\.0\.1|localhost|\[?::1\]?)$/i.test(pageHost);
    if (loopback && !pageIsLoopback) {
      u.hostname = pageHost;
    }
    return u.toString().replace(/\/$/, '');
  } catch {
    return resolved;
  }
};
