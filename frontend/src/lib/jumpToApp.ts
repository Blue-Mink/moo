/**
 * 0.6.346：把列表/网格里 data-app-key 匹配的卡片滚到视口中央。
 *
 * 历史坑（0.6.321R7 起一直存在，0.6.345 实测复现）：安装完成后点顶部通知
 * 「查看」时，列表还在渐进渲染（shown 逐段追加）+ 卡片带 content-visibility:auto，
 * 单次 scrollIntoView({behavior:'smooth'}) 测量的是变化中的文档高度，会把视口
 * 直接甩到列表末尾（实测 scrollY 98132、目标卡跑到视口上方 5.7 万像素）。
 * 这里改为分帧重试 + 每次用 rect 差值即时校正，居中即停手（不和用户抢滚动）。
 */
export function centerAppRow(key: string, opts: { attempts?: number; delay?: number } = {}) {
  const attempts = opts.attempts ?? 14;
  const delay = opts.delay ?? 110;
  let n = 0;
  const tick = () => {
    const el = Array.from(document.querySelectorAll<HTMLElement>('[data-app-key]'))
      .find(e => e.dataset.appKey === key && e.getClientRects().length > 0);
    if (!el) {
      if (n++ < attempts) setTimeout(tick, delay);
      return;
    }
    const r = el.getBoundingClientRect();
    const center = r.top + r.height / 2;
    const delta = center - window.innerHeight / 2;
    if (Math.abs(delta) > 24) {
      window.scrollTo({ top: Math.max(0, window.scrollY + delta), behavior: 'auto' });
    }
    n++;
    if (n < attempts && Math.abs(delta) > 24) setTimeout(tick, delay);
  };
  setTimeout(tick, 50);
}
