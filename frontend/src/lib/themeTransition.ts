/**
 * 亮/暗主题「圆形展开」过渡 —— 参考开源 fn-knock（kci-lnk/fn-knock-turborepo）
 * 的 View Transitions 实现（packages/ui-vue/.../theme-toggle/useThemeMode.ts）。
 *
 * 切换时调用 document.startViewTransition() 对切前后两帧做过渡：
 * 新主题帧用圆形遮罩从屏幕中心向外展开（mask-size 0 → 200vmax），
 * 时长 1s，expo-out 自定义曲线（linear() 分段），旧帧不淡出、垫在下方
 * —— 视觉效果与 fn-knock 控制台一致。
 *
 * 降级（以下任一情况直接切换、不做动画）：
 *   - 用户开启系统「减少动态效果」（prefers-reduced-motion: reduce）
 *   - 浏览器无 View Transitions API（Safari <18.2、旧 WebView）
 *
 * 过渡期间临时挂 :root[data-theme-transitioning]，禁用页面其他
 * transition，避免各组件自带的颜色过渡干扰圆形展开。
 *
 * 注意：本模块只负责「怎么过渡」，主题状态本身仍由 next-themes 管理
 * （localStorage key=theme，.dark 类挂在 <html>），两者解耦。
 */

export type ResolvedThemeMode = 'light' | 'dark';

const STYLE_ID = 'new-store-theme-transition-style';
const THEME_TRANSITION_DURATION = '1s';
const THEME_TRANSITION_MASK =
  "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 40 40'%3E%3Ccircle cx='20' cy='20' r='20' fill='white'/%3E%3C/svg%3E\")";

type ViewTransitionLike = { finished: Promise<void> };

type StartViewTransitionDocument = Document & {
  startViewTransition?: (
    updateCallback: () => void | Promise<void>,
  ) => ViewTransitionLike;
};

export const prefersReducedMotion = (): boolean =>
  typeof window !== 'undefined' &&
  window.matchMedia('(prefers-reduced-motion: reduce)').matches;

/** 注入圆形展开过渡样式（幂等，仅首次生效）。 */
export const ensureThemeTransitionStyles = (): void => {
  if (typeof document === 'undefined') return;
  if (document.getElementById(STYLE_ID)) return;

  const style = document.createElement('style');
  style.id = STYLE_ID;
  style.textContent = `
:root {
  --new-store-theme-transition-duration: ${THEME_TRANSITION_DURATION};
  --new-store-theme-transition-mask: ${THEME_TRANSITION_MASK};
  --new-store-theme-expo-out: linear(
    0 0%, 0.1684 2.66%, 0.3165 5.49%, 0.446 8.52%,
    0.5581 11.78%, 0.6535 15.29%, 0.7341 19.11%,
    0.8011 23.3%, 0.8557 27.93%, 0.8962 32.68%,
    0.9283 38.01%, 0.9529 44.08%, 0.9711 51.14%,
    0.9833 59.06%, 0.9915 68.74%, 1 100%
  );
}

/* 移动端（飞牛 app 内嵌 WebView / 触屏 / 窄屏）：fn-knock 的 1s 节奏在手机上
   拖尾偏慢，缩短到 0.45s 跟手性更好；桌面浏览器保持 1s。 */
@media (max-width: 768px), (pointer: coarse) {
  :root {
    --new-store-theme-transition-duration: 0.45s;
  }
}

:root[data-theme-transitioning] *,
:root[data-theme-transitioning] *::before,
:root[data-theme-transitioning] *::after {
  transition-property: none !important;
}

::view-transition-group(root) {
  animation-timing-function: var(--new-store-theme-expo-out);
}

::view-transition-old(root),
.dark::view-transition-old(root) {
  animation: none;
  animation-fill-mode: both;
  z-index: -1;
}

::view-transition-new(root),
.dark::view-transition-new(root) {
  animation: new-store-theme-reveal var(--new-store-theme-transition-duration);
  animation-fill-mode: both;
  animation-timing-function: var(--new-store-theme-expo-out);
  -webkit-mask: var(--new-store-theme-transition-mask) center / 0 no-repeat;
  mask: var(--new-store-theme-transition-mask) center / 0 no-repeat;
}

@keyframes new-store-theme-reveal {
  to {
    -webkit-mask-size: 200vmax;
    mask-size: 200vmax;
  }
}
`;
  document.head.appendChild(style);
};

/**
 * 等待 <html> 的 .dark 类翻转到目标值（next-themes 在渲染副作用里
 * 落类，晚于 setTheme 调用；View Transition 必须等新帧 DOM 就位后
 * 再拍新快照，否则拍到的还是旧主题）。超时兜底，避免死等。
 */
const waitForThemeClass = (
  mode: ResolvedThemeMode,
  timeoutMs = 800,
): Promise<void> =>
  new Promise((resolve) => {
    if (typeof document === 'undefined') {
      resolve();
      return;
    }
    const started = Date.now();
    const tick = () => {
      const isDark = document.documentElement.classList.contains('dark');
      if ((mode === 'dark') === isDark || Date.now() - started > timeoutMs) {
        resolve();
        return;
      }
      setTimeout(tick, 16);
    };
    setTimeout(tick, 16);
  });

/**
 * 执行一次带圆形展开过渡的主题切换。
 * @param next 目标主题（light/dark）
 * @param applyTheme 切换动作（通常是 next-themes 的 setTheme）
 */
export const runThemeToggleTransition = async (
  next: ResolvedThemeMode,
  applyTheme: () => void,
): Promise<void> => {
  const startViewTransition =
    typeof document === 'undefined'
      ? undefined
      : (document as StartViewTransitionDocument).startViewTransition?.bind(
          document,
        );

  if (typeof document === 'undefined' || prefersReducedMotion() || !startViewTransition) {
    applyTheme();
    return;
  }

  ensureThemeTransitionStyles();

  const root = document.documentElement;
  root.dataset.themeTransition = next === 'dark' ? 'to-dark' : 'to-light';
  root.dataset.themeTransitioning = '';

  try {
    const transition = startViewTransition(async () => {
      applyTheme();
      await waitForThemeClass(next);
    });
    await transition.finished.catch(() => undefined);
  } finally {
    delete root.dataset.themeTransition;
    delete root.dataset.themeTransitioning;
  }
};

/** 进行中的一次切换（防止连点触发重叠过渡；同 fn-knock 的 activeThemeTransition）。 */
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

  activeThemeTransition = runThemeToggleTransition(next, () =>
    setTheme(next),
  ).finally(() => {
    activeThemeTransition = null;
  });

  return activeThemeTransition;
};
