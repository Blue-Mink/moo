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
 *               → offsetPx = 视口收缩量（首页 dock 用 translateY(+offsetPx)
 *                 钉物理底边）；设置页保存 dock 用「不压槽 + liftPx 上抬」
 *                 固定在可见底边（键盘顶边），见 0.6.217；
 *      clip   = 布局视口不变、可视视口被裁剪（iOS Safari/微信/浏览器）
 *               → offsetPx = 0（不位移，否则会推到屏幕外、露出容器黑底）；
 *               消费方应把容器高度切到 100dvh（跟随可视视口），容器底边
 *               自然停在键盘上方、按钮就位于键盘之上；
 *      pan    = 壳平移型（视口完全不变）→ hidden 兜底。
 *  - offsetPx：键盘高度（px），仅 resize 型非 0。
 *  - liftPx：0.6.217 新增。resize 型「部分压扁」壳（飞牛 app 实测：窗口
 *    压扁后底边仍被键盘盖住一段）= 布局视口与可视视口的高度差
 *    （innerHeight − vv.height − vv.offsetTop，取非负）。完整压扁壳 = 0。
 *    消费方 translateY(−liftPx) 把 dock 上抬到可见底边（键盘顶边），
 *    让保存按钮在键盘弹出时仍固定可见、可点。
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
  /** 0.6.217：resize 型部分压扁壳中键盘仍盖住窗口底边的量（px）；
      dock 以 translateY(-liftPx) 上抬到可见底边。完整压扁壳 = 0。 */
  liftPx: number;
  /** pan 型壳兜底：聚焦输入框期间整体隐藏 dock */
  hidden: boolean;
  /** 0.6.221：可视视口几何（布局坐标；键盘未开或 vv 不可用时 vvHeight = 0）。
      消费方据此把容器**直接**钉在可见区域上——比 100dvh / liftPx 的间接推算可靠：
      resize（布局压扁）/ clip（可视裁剪）/ pan（壳平移）三种壳共用同一份几何，
      容器底边恒等于可见底边（键盘顶边）。 */
  vvTop: number;
  vvHeight: number;
  /** 0.6.225：键盘弹出前的「整屏」高度基线（布局视口 px）。
      消费方用它可以**保持键盘弹出前的布局**（容器高度 = 整屏），
      于是容器底边＝物理屏幕底边：键盘只是盖住它，按钮/布局都不位移。 */
  baseHeight: number;
}

export function useKeyboardDock(
  deadbandPx = 8,
  keyboardThresholdPx = 220,
  settleMs = 400
): KeyboardDockState {
  const [state, setState] = useState<KeyboardDockState>({ open: false, mode: null, offsetPx: 0, liftPx: 0, hidden: false, vvTop: 0, vvHeight: 0, baseHeight: 0 });
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

    const setAll = (open: boolean, mode: 'resize' | 'clip' | 'pan' | null, offsetPx: number, liftPx: number, hidden: boolean, vvTop: number, vvHeight: number, baseHeight: number) =>
      setState(prev =>
        prev.open === open && prev.mode === mode && prev.offsetPx === offsetPx && prev.liftPx === liftPx &&
        prev.hidden === hidden && prev.vvTop === vvTop && prev.vvHeight === vvHeight && prev.baseHeight === baseHeight
          ? prev
          : { open, mode, offsetPx, liftPx, hidden, vvTop, vvHeight, baseHeight }
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
      // 0.6.217：部分压扁壳（飞牛 app 实测：窗口压扁后底边仍被键盘盖住）
      // 的键盘残留覆盖量 = 布局视口高出可视视口的部分；dock 上抬该值后
      // 底边正好落在键盘顶边（可见底边）。完整压扁壳 vv.height==innerHeight
      // → 0，不位移；vv 不报告（恒等于布局视口）也 → 0（不劣化旧行为）。
      const liftPx =
        mode === 'resize' && vv ? Math.max(0, Math.round(window.innerHeight - vv.height - vv.offsetTop)) : 0;
      const hidden = shellMode === 'pan' && focusedEditable;
      // 0.6.221：把当前可视视口几何一并交给消费方（键盘未开时归零）。
      // 这是"保存按钮固定可见底边"的**权威**依据：无论壳是压扁布局、
      // 裁剪可视区还是整体平移，容器按 (top, height) 钉住即可。
      const vvTop = keyboardActive && vv ? Math.round(vv.offsetTop) : 0;
      const vvHeight = keyboardActive && vv ? Math.round(vv.height) : 0;
      setAll(keyboardActive, mode, offsetPx, liftPx, hidden, vvTop, vvHeight, Math.round(baseH));
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
    // pan 型壳靠 vv.offsetTop 变化暴露平移，必须监听 vv 的 scroll
    vv?.addEventListener('scroll', measure);
    document.addEventListener('focusin', onFocusIn);
    document.addEventListener('focusout', onFocusOut);
    return () => {
      clearTimeout(modeProbeTimer);
      clearTimeout(settleTimer);
      window.removeEventListener('resize', measure);
      vv?.removeEventListener('resize', measure);
      vv?.removeEventListener('scroll', measure);
      document.removeEventListener('focusin', onFocusIn);
      document.removeEventListener('focusout', onFocusOut);
    };
  }, [deadbandPx, keyboardThresholdPx, settleMs]);
  return state;
}

/**
 * 桌面断点钩子（0.6.284）：与 CSS md:（768px）一致。
 * 用途：收藏区等共享区块按端渲染不同卡片（移动端瀑布流卡 / 桌面详情卡），
 * 用真分支避免两套卡片同时挂载（双份 DOM + 详情/README 请求浪费）。
 */
/**
 * 0.6.301：触屏/粗指针设备判定（手机/平板/触屏）。
 * iOS 双击卡片 = 系统双击缩放手势（viewport 可缩放时 dblclick 常被吞），
 * 触屏设备统一用「单击/点按开详情」替代桌面双击。
 */
export function useCoarsePointer(): boolean {
  const [coarse, setCoarse] = useState(
    () =>
      typeof window !== 'undefined' &&
      window.matchMedia('(hover: none) and (pointer: coarse)').matches,
  );
  useEffect(() => {
    const mq = window.matchMedia('(hover: none) and (pointer: coarse)');
    const cb = (e: MediaQueryListEvent) => setCoarse(e.matches);
    mq.addEventListener('change', cb);
    return () => mq.removeEventListener('change', cb);
  }, []);
  return coarse;
}

export function useIsDesktop(): boolean {
  const [is, setIs] = useState(
    () => typeof window !== 'undefined' && window.matchMedia('(min-width: 768px)').matches,
  );
  useEffect(() => {
    const mq = window.matchMedia('(min-width: 768px)');
    const cb = (e: MediaQueryListEvent) => setIs(e.matches);
    mq.addEventListener('change', cb);
    return () => mq.removeEventListener('change', cb);
  }, []);
  return is;
}

/**
 * 0.6.316：「手机上的桌面模拟」判定（飞牛 app 桌面模式等壳）——
 * 物理屏宽 <700 CSS px 且布局视口 ≥768px。
 *
 * 背景：飞牛 app「桌面模式」本质仍是手机里的 iOS WebView，只是把布局视口
 * 撑到桌面宽度 + 桌面 UA。Moo 据此走整套桌面 CSS 分支，而桌面分支的
 * backdrop-filter 玻璃层在 WKWebView GPU 合成异常下「玻璃出得来、其上
 * 内容不出来」→ 设置/详情页整屏只剩毛玻璃（0.6.315 只修了移动分支）。
 *
 * 阈值依据：真桌面笔记本屏宽 ≥1366、iPad ≥1024，均 ≥700 不误判；
 * 手机竖屏视口 <768 也不命中。命中时对话框挂 .mds-sim 类（index.css
 * 去 backdrop-filter + 玻璃底提至 /95 近实心），真桌面浏览器零变化。
 */
export function useMobileDesktopSim(): boolean {
  const calc = () =>
    typeof window !== 'undefined' &&
    window.screen &&
    window.screen.width > 0 &&
    window.screen.width < 700 &&
    window.innerWidth >= 768;
  const [sim, setSim] = useState<boolean>(calc);
  useEffect(() => {
    const onResize = () => setSim(calc());
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);
  return sim;
}
