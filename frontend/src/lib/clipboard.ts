/**
 * 0.6.314 B：共享剪贴板写入工具（不引入新依赖）
 *
 * 背景：网页端常以 http://192.168.x.x:38100 访问（非 secure context），
 * navigator.clipboard 为 undefined；部分 WebView / 老浏览器上
 * document.execCommand('copy') 也会静默失败（无选区、焦点丢失）。
 * 此前各调用点「两条路都不校验结果，无论成败都 toast.success」= 假成功，
 * 用户粘贴为空，体验即「复制不了」。
 *
 * 契约：返回值 = 是否**真实**写入剪贴板。
 *   true  → 调用方可以 toast.success；
 *   false → 调用方不得报成功，应走降级 UI（如展开文本供手动全选复制）。
 */
export async function copyTextToClipboard(text: string): Promise<boolean> {
  // 1) Clipboard API：存在则 await，resolve = 真成功；
  //    非 secure context（navigator.clipboard undefined / 访问即抛 TypeError）
  //    或权限被拒 → 落入 2)。
  if (typeof navigator !== 'undefined') {
    const cb = navigator.clipboard;
    if (cb && typeof cb.writeText === 'function') {
      try {
        await cb.writeText(text);
        return true;
      } catch {
        /* 被拒或环境不可用 → 走 execCommand 兜底 */
      }
    }
  }
  // 2) execCommand 兜底：先保证焦点 + 完整选区，再检查返回值。
  //    position:fixed + left/top:0 + opacity:0 避免滚动跳动与视觉干扰；
  //    select() 之外再 setSelectionRange(0, len) 覆盖部分 WebView 对
  //    多行/超长文本 select() 选不全的情况。
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.left = '0';
    ta.style.top = '0';
    ta.style.opacity = '0';
    ta.style.pointerEvents = 'none';
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    try {
      ta.setSelectionRange(0, text.length);
    } catch {
      /* 个别环境 textarea 不支持 setSelectionRange，select() 已尽力 */
    }
    const ok = document.execCommand('copy') === true;
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}
