import { useEffect, useState } from 'react';

/**
 * 防抖值：输入框每次按键只更新廉价的本地状态，debounced 值在停止输入
 * delayMs 毫秒后才变化 —— 列表/筛选等昂贵计算只依赖它，避免 WebView 里
 * 每个字符都重渲染数百张应用卡片导致输入延迟。
 */
export function useDebouncedValue<T>(value: T, delayMs = 150): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(t);
  }, [value, delayMs]);
  return debounced;
}

/**
 * 键盘布局状态 —— 底部 dock 的"钉在屏幕底边"方案（对齐 iOS App Store 观感）。
 *
 * 返回：
 *  - open：键盘是否打开（视口收缩超阈值）。搜索框自动收起等"键盘开/关沿"
 *    信号用它，不依赖 offsetPx（clip 型下 offsetPx 恒 0）。
 *  - mode：键盘形态（键盘未开 = null）：
 *      resize = 布局视口被整体压扁（飞牛 app 等 adjustResize 原生壳）
 *               → offsetPx = 键盘高度，dock 用 bottom:-offsetPx /
 *               translateY(+offsetPx) 钉在物理屏幕底边（被键盘盖住）；
 *      clip   = 布局视口不变、可视视口被裁剪（iOS Safari/微信/浏览器）
 *               → offsetPx = 0（不位移，否则会推到屏幕外、露出容器黑底）；
 *               消费方应把容器高度切到 100dvh（跟随可视视口），容器底边
 *               自然停在键盘上方、按钮就位于键盘之上；
 *      pan    = 壳平移型（视口完全不变）→ hidden 兜底。
 *  - offsetPx：键盘高度（px），仅 resize 型非 0。
 *  - hidden：pan 型壳兜底：聚焦输入框期间整体隐藏 dock，失焦恢复。
 *
 * 键盘高度测量（取两者较大）：
 *  - 视口收缩（adjustResize 型）：innerHeight 相对基线的收缩量
 *  - 可视视口差值（浏览器型/iframe 裁剪型）：visualViewport 相对基线的增量
 *
 * 基线维护：
 *  - 视口变高（地址栏收合）→ 抬高基线
 *  - 宽度变化 >80px（旋转屏幕）→ 重建基线
 *  - 键盘收起后若 WebView 没完全回到原高度（壳布局微调），残留收缩稳定
 *    400ms 后把基线校准到"新常态"，防止 dock 被永久压到视口外
 *
 * 壳模式自学习：输入框聚焦时记录视口参照；收缩 >40px ⇒ resize 型
 * （offset 权威）；1.5s 无变化 ⇒ pan 型（聚焦隐/失焦现）；pan 误判后
 * 出现大幅收缩会自动纠正回 resize。
 */
export interface KeyboardDockState {
  /** 键盘是否打开 */
  open: boolean;
  /** 键盘形态：resize（布局压扁）/ clip（可视裁剪）/ pan（平移）/ null（未开） */
  mode: 'resize' | 'clip' | 'pan' | null;
  /** 键盘高度（px），仅 resize 型非 0；dock 以 bottom: -offsetPx 钉底边 */
  offsetPx: number;
  /** pan 型壳兜底：聚焦输入框期间整体隐藏 dock */
  hidden: boolean;
}

export function useKeyboardDock(
  deadbandPx = 8,
  keyboardThresholdPx = 220,
  settleMs = 400
): KeyboardDockState {
  const [state, setState] = useState<KeyboardDockState>({ open: false, mode: null, offsetPx: 0, hidden: false });
  useEffect(() => {
    const vv = window.visualViewport;
    // 初始固有差（iframe 高于可视区、地址栏等）
    let vvBaseline = vv ? window.innerHeight - vv.height : 0;
    let baseH = window.innerHeight;
    let baseW = window.innerWidth;
    const isEditable = (el: EventTarget | null) =>
      el instanceof HTMLElement &&
      (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable);
    let focusedEditable = false;
    let shellMode: 'unknown' | 'resize' | 'pan' = 'unknown';
    let modeProbeTimer: ReturnType<typeof setTimeout> | undefined;
    let settleTimer: ReturnType<typeof setTimeout> | undefined;
    let focusRefH = window.innerHeight;
    let focusRefVv = vv ? vv.height : 0;
    let keyboardActive = false; // 视口收缩超过阈值 = 键盘开着

    const setAll = (open: boolean, mode: 'resize' | 'clip' | 'pan' | null, offsetPx: number, hidden: boolean) =>
      setState(prev =>
        prev.open === open && prev.mode === mode && prev.offsetPx === offsetPx && prev.hidden === hidden
          ? prev
          : { open, mode, offsetPx, hidden }
      );

    const measure = () => {
      const h = window.innerHeight;
      const w = window.innerWidth;
      if (Math.abs(w - baseW) > 80) {
        // 旋转/大幅布局变化：重建基线，不当作键盘
        baseH = h;
        baseW = w;
        if (vv) vvBaseline = window.innerHeight - vv.height;
      } else if (h > baseH + 8) {
        baseH = h; // 视口变高（地址栏收合等）→ 抬高基线
      }
      const innerShrink = baseH - h;
      const vvShrink = vv ? window.innerHeight - vv.height - vvBaseline : 0;
      const rawOffset = Math.max(innerShrink, vvShrink);

      // 壳模式自学习：聚焦期间视口相对参照收缩 >40px ⇒ 立即判定 resize 型
      if (shellMode === 'unknown' && focusedEditable) {
        const dh = Math.abs(window.innerHeight - focusRefH);
        const dv = vv ? Math.abs(vv.height - focusRefVv) : 0;
        if (dh > 40 || dv > 40) {
          shellMode = 'resize';
          clearTimeout(modeProbeTimer);
        }
      }

      if (
        shellMode === 'pan' &&
        focusedEditable &&
        rawOffset > keyboardThresholdPx
      ) {
        // 慢弹键盘边界：1.5s 内没等到变化被误判 pan，随后视口才大幅收缩 ——
        // pan 壳的视口从不这样动，纠正为 resize（改走 offset，解除焦点隐藏）
        shellMode = 'resize';
        clearTimeout(modeProbeTimer);
      }

      // 形态判定：布局视口（innerHeight）也收缩 = resize 型（adjustResize
      // 壳，offset 权威）；只有可视视口收缩 = clip 型（iOS/微信/浏览器，
      // 不位移——位移会把 dock 推到屏幕外、露出容器黑底）。
      let mode: 'resize' | 'clip' | 'pan' | null = null;
      if (rawOffset > keyboardThresholdPx) {
        // 键盘打开：resize 型 offset 全程跟踪（dock 钉在屏幕底边、被键盘盖住）
        keyboardActive = true;
        mode = innerShrink > keyboardThresholdPx ? 'resize' : 'clip';
        clearTimeout(settleTimer);
      } else if (rawOffset > deadbandPx) {
        // 阈值以下的小残留：键盘已收起（可能 WebView 没回到原高度 / 壳
        // 布局微调）。稳定 settleMs 后把基线校准到新常态，避免 dock 被
        // 永久压到视口外
        if (keyboardActive) keyboardActive = false; // 收起确认
        clearTimeout(settleTimer);
        settleTimer = setTimeout(() => {
          const cur = Math.max(baseH - window.innerHeight, vv ? window.innerHeight - vv.height - vvBaseline : 0);
          if (!keyboardActive && cur > deadbandPx) {
            baseH = window.innerHeight;
            if (vv) vvBaseline = window.innerHeight - vv.height;
            measure();
          }
        }, settleMs);
      } else {
        keyboardActive = false;
        clearTimeout(settleTimer);
      }

      // 0.6.210：offsetPx 仅 resize 型有意义（clip 型位移会把 dock 推出
      // 屏幕、露出容器黑底——真机 0.6.209 大黑框根因）；clip 型由消费方
      // 把容器高度切 100dvh 跟随可视视口。
      const offsetPx = mode === 'resize' && rawOffset > deadbandPx ? Math.round(rawOffset) : 0;
      const hidden = shellMode === 'pan' && focusedEditable;
      setAll(keyboardActive, mode, offsetPx, hidden);
    };

    const onFocusIn = () => {
      if (!isEditable(document.activeElement)) return;
      focusedEditable = true;
      focusRefH = window.innerHeight;
      focusRefVv = vv ? vv.height : 0;
      // 1.5s 内视口毫无变化 ⇒ pan 型（resize 型由 measure 抢先判定）
      if (shellMode === 'unknown') {
        clearTimeout(modeProbeTimer);
        modeProbeTimer = setTimeout(() => {
          if (shellMode !== 'unknown') return;
          shellMode = 'pan';
          measure();
        }, 1500);
      }
      measure();
    };

    const onFocusOut = () => {
      // focusout 时新焦点可能尚未落定，延迟一拍再判定
      setTimeout(() => {
        if (!isEditable(document.activeElement)) {
          focusedEditable = false;
          measure();
        }
      }, 0);
    };

    measure();
    window.addEventListener('resize', measure);
    vv?.addEventListener('resize', measure);
    document.addEventListener('focusin', onFocusIn);
    document.addEventListener('focusout', onFocusOut);
    return () => {
      clearTimeout(modeProbeTimer);
      clearTimeout(settleTimer);
      window.removeEventListener('resize', measure);
      vv?.removeEventListener('resize', measure);
      document.removeEventListener('focusin', onFocusIn);
      document.removeEventListener('focusout', onFocusOut);
    };
  }, [deadbandPx, keyboardThresholdPx, settleMs]);
  return state;
}
