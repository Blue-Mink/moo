/**
 * 页面「在场」跟踪 —— 「退出 Moo 再进来不应看到旧通知」的彻底修复。
 *
 * 两种「离开」机制：
 *  1) 页面真隐藏（后台 tab / 面板窗口最小化）：visibilitychange 触发，
 *     sonner 暂停 3 秒倒计时把通知「冻结」在屏幕上。
 *  2) 飞牛桌面「关闭应用窗口」= iframe 被隐藏（display:none）：
 *     visibilityState 不变、visibilitychange 根本不触发，
 *     旧的「focus / 9 秒后交互」启发式漏掉「9 秒内退出再进」的情况，
 *     且离开期间到达的通知（下载完成 / 源刷新 / 自动更新）照常弹出，
 *     回来第一眼就撞见「我没在时冒出来的通知」。
 *
 * 统一两个在场信号（任一判「离开」即为离开）：
 *  - visibilitychange：document.visibilityState !== 'visible'；
 *  - IntersectionObserver 观察 <html>：iframe 被 display:none 时根元素
 *    交叠变 0（isIntersecting=false），重新出现恢复 true；
 *    顶层 tab（非 iframe）里 documentElement 恒交叠，不受影响。
 *
 * 行为：
 *  - 离开 → 返回 转换：清空屏上全部通知（冻结残留 + 离开期间新弹的）；
 *  - 离开期间：吞掉所有新发起的通知（用户不在应用前，结果由按钮/列表状态承载）。
 */
import * as sonner from "sonner";

const t = sonner.toast;

type EmitFn = (message: unknown, data?: unknown) => string | number;

// 会被「离开期间吞掉」的发射方法（dismiss 永远放行）
const real: Record<string, EmitFn> = {
  success: t.success as EmitFn,
  error: t.error as EmitFn,
  info: t.info as EmitFn,
  warning: t.warning as EmitFn,
  message: t.message as EmitFn,
  loading: t.loading as EmitFn,
  custom: t.custom as EmitFn,
};

const state = {
  docHidden: typeof document !== "undefined" && document.visibilityState !== "visible",
  ioHidden: false,
  away: false,
};

let installed = false;

/** 当前是否处于「用户离开」状态（真隐藏或 iframe 被隐藏）。 */
export const isPageAway = (): boolean => state.away;

function recompute(): void {
  const next = state.docHidden || state.ioHidden;
  if (next === state.away) return;
  state.away = next;
  if (!next) {
    // 返回：屏上无论剩什么（冻结的 / 离开期间新弹的），一律清空。
    // 用户重新打开看到的是应用状态（按钮/列表/进度），不是旧通知。
    t.dismiss();
  }
}

/**
 * 安装在场跟踪（幂等）。返回的卸载函数恢复原始 toast 方法。
 * 在 App 根组件的 useEffect 中调用一次。
 */
export function installPresenceTracking(): () => void {
  if (installed) return () => {};
  installed = true;

  // ① 真隐藏 / 可见（后台 tab、窗口最小化、屏幕锁定）
  const onVisibility = () => {
    state.docHidden = document.visibilityState !== "visible";
    recompute();
  };
  document.addEventListener("visibilitychange", onVisibility);

  // ② iframe 被隐藏（飞牛桌面退出应用）——无 visibility 事件，靠交叠信号
  let io: IntersectionObserver | null = null;
  try {
    io = new IntersectionObserver(
      (entries) => {
        state.ioHidden = !entries.some((e) => e.isIntersecting);
        recompute();
      },
      { threshold: 0 },
    );
    io.observe(document.documentElement);
  } catch {
    // 环境无 IntersectionObserver：退化为仅 visibilitychange（旧行为）
  }

  // 离开期间吞掉新通知（正常在场时直通原实现，行为零变化）
  for (const k of Object.keys(real)) {
    const fn = real[k];
    (t as unknown as Record<string, unknown>)[k] = (message: unknown, data?: unknown) => {
      if (state.away) return;
      return fn(message, data);
    };
  }

  return () => {
    document.removeEventListener("visibilitychange", onVisibility);
    io?.disconnect();
    for (const k of Object.keys(real)) {
      (t as unknown as Record<string, unknown>)[k] = real[k];
    }
    state.docHidden = document.visibilityState !== "visible";
    state.ioHidden = false;
    state.away = state.docHidden;
    installed = false;
  };
}
