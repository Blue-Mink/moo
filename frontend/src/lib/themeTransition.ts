/**
 * 亮/暗主题切换过渡 —— 0.6.302 起全引擎统一「即时切换 + 单层遮罩交叉淡切」。
 *
 * 演进史：
 *   0.6.293  View Transitions 圆形展开（照抄 fn-knock）
 *   0.6.301  WebKit（iOS/Mac Safari）改 150ms 全元素颜色淡切（绕开 VT 快照
 *            在毛玻璃/fixed 层上的「卡半屏」缺陷）
 *   0.6.302  两路都废弃，统一为遮罩淡切。原因（用户实报「卡顿/延迟/不丝滑」）：
 *            ① VT 路（Chromium/安卓/桌面网页）：
 *               a. 延迟——动画要等 applyTheme 回调 + 等 <html> 的 .dark 类
 *                  落上（next-themes 在 React effect 里提交，几十 ms 起）
 *                  才开始，点击后画面先「冻」一下；
 *               b. 卡顿——对 1800 卡 + 毛玻璃重页拍「旧帧/新帧」两张全页
 *                  快照本身就是昂贵的合成操作；
 *               c. 不丝滑——1s expo-out 尾部 40% 时长只推进 2%，观感拖沓。
 *            ② WebKit 淡切路（iOS）：对全部元素强制 150ms 颜色 transition，
 *               数千节点同时做 paint 级属性动画 + backdrop-filter 毛玻璃层
 *               每帧重模糊 → 掉帧 = 卡顿。
 *
 * 0.6.302 方案（所有引擎一致）：
 *   点击 → 主题即时切换（一次整页重绘，被不透明遮罩盖住、不可见）
 *        → 单层全屏遮罩（背景=旧主题底色）opacity 1→0 约 200ms 淡出，
 *          新主题在遮罩下逐渐显露 = 平滑交叉淡切。
 *   全程只动画一个元素的 opacity = 纯合成器（GPU）动画，零 paint、
 *   零快照、零等待 → 结构上不可能卡顿；无引擎差异、无快照缺陷。
 *   prefers-reduced-motion = 直接即时切换（无遮罩）。
 *
 * 主题状态仍由 next-themes 管理（localStorage key=theme，.dark 挂 <html>）。
 */

export type ResolvedThemeMode = 'light' | 'dark';

const CROSSFADE_MS = 200;

export const prefersReducedMotion = (): boolean =>
  typeof window !== 'undefined' &&
  window.matchMedia('(prefers-reduced-motion: reduce)').matches;

/** 取当前（切换前）页面底色：body → html → --background 变量 → 白。 */
const currentBgColor = (): string => {
  try {
    const b = getComputedStyle(document.body).backgroundColor;
    if (b && b !== 'rgba(0, 0, 0, 0)') return b;
    const h = getComputedStyle(document.documentElement).backgroundColor;
    if (h && h !== 'rgba(0, 0, 0, 0)') return h;
    const v = getComputedStyle(document.documentElement)
      .getPropertyValue('--background')
      .trim();
    if (v) return v;
  } catch {
    /* SSR/异常 → 兜底 */
  }
  return '#ffffff';
};

/**
 * 执行一次主题切换（0.6.302 遮罩交叉淡切）。
 * @param applyTheme 切换动作（通常是 next-themes 的 setTheme）
 */
export const runThemeToggleTransition = (
  applyTheme: () => void,
): void => {
  if (typeof document === 'undefined' || prefersReducedMotion()) {
    applyTheme();
    return;
  }

  const root = document.documentElement;
  const ov = document.createElement('div');
  ov.setAttribute('data-theme-overlay', '');
  ov.style.cssText =
    'position:fixed;inset:0;z-index:2147483000;pointer-events:none;' +
    `background:${currentBgColor()};opacity:1;` +
    `transition:opacity ${CROSSFADE_MS}ms ease-out;`;
  root.appendChild(ov);

  // 即时切换（重绘被不透明遮罩盖住，用户不可见）
  applyTheme();

  // 等重绘完成（双 rAF + 60ms 预算，吸收整页重绘的掉帧）再开始淡出，
  // 淡出期间零 paint（只有合成器 opacity 动画）= 丝滑
  window.setTimeout(() => {
    ov.style.opacity = '0';
    window.setTimeout(() => ov.remove(), CROSSFADE_MS + 100);
  }, 60);
};

/** 进行中的一次切换（防止连点触发重叠；同 fn-knock 的 activeThemeTransition）。 */
let activeThemeTransition: Promise<void> | null = null;

/**
 * 主题切换（React 侧入口）：当前是 dark 就切 light，反之亦然。
 * 过渡进行中再次点击 = 等当前过渡结束（丢弃本次点击），避免叠加。
 */
export const toggleThemeWithTransition = (
  current: ResolvedThemeMode,
  setTheme: (mode: ResolvedThemeMode) => void,
): Promise<void> | undefined => {
  const next: ResolvedThemeMode = current === 'dark' ? 'light' : 'dark';

  if (activeThemeTransition) {
    return activeThemeTransition;
  }

  activeThemeTransition = (async () => {
    runThemeToggleTransition(() => setTheme(next));
    // 淡切总时长 ≈ 60ms 等待 + 200ms 淡出，锁 320ms 防连点
    await new Promise((r) => setTimeout(r, 320));
  })();

  activeThemeTransition.finally(() => {
    activeThemeTransition = null;
  });

  return activeThemeTransition;
};
