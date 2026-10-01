// stableApps 列表数据稳定合并：新拉取的目录与旧列表逐项比对，
// 内容未变的条目**复用旧对象引用**——AppRow/AppCard 是 React.memo，
// 引用不变即跳过重渲染。
//
// 背景：页面加载后「官方描述回填轮询」每 4s 全量拉 /api/apps（1.2MB），
// 4G 手机上 JSON.parse + 1700+ 行整表重渲染每次占主线程 200-600ms；
// 点击恰好落在该窗口就会被 JS 任务队列排队 → 用户感知「点应用卡一下」。
// 合并后：无变化的刷新只花 ~2ms 比对，列表零重渲染。
import type { AppInfo } from '../api/client';

function sameValue(av: unknown, bv: unknown): boolean {
  if (av === bv) return true;
  if (typeof av !== 'object' || typeof bv !== 'object' || av === null || bv === null) {
    return false;
  }
  if (Array.isArray(av) && Array.isArray(bv)) {
    if (av.length !== bv.length) return false;
    for (let i = 0; i < av.length; i++) {
      if (!sameValue(av[i], bv[i])) return false;
    }
    return true;
  }
  return false;
}

function sameApp(a: AppInfo, b: AppInfo): boolean {
  const A = a as unknown as Record<string, unknown>;
  const B = b as unknown as Record<string, unknown>;
  for (const k in A) {
    if (!sameValue(A[k], B[k])) return false;
  }
  for (const k in B) {
    if (!sameValue(B[k], A[k])) return false;
  }
  return true;
}

// 按 key（appname@源名）匹配新旧条目；长度不一致或顺序大改时直接换全量
// （调用方会重洗牌，重渲染成本一次性付清，换取逻辑简单）。
export function stableApps(
  prev: AppInfo[] | undefined,
  next: AppInfo[],
): { list: AppInfo[]; changed: boolean } {
  if (!prev || prev.length !== next.length) {
    return { list: next, changed: true };
  }
  const byKey = new Map<string, AppInfo>();
  for (const p of prev) {
    byKey.set(p.key || p.appname, p);
  }
  let changed = 0;
  const list = next.map((n) => {
    const p = byKey.get(n.key || n.appname);
    if (p && sameApp(p, n)) {
      return p;
    }
    changed++;
    return n;
  });
  return { list, changed: changed > 0 };
}
