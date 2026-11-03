// 顶部通知层（sonner toast / 后台任务通知栏）判定（0.6.500）。
//
// 用户实报：设置页里点顶部通知栏 → 设置页被关掉、回到首页。
// 根因：这些通知渲染在 Radix Dialog 的 DOM 之外，指针按下被 DismissableLayer
// 当成「对话框外点」，默认行为直接关闭对话框。命中通知层时必须拦下，
// 让点击只作用于通知本身（toast 无操作即无反应，任务栏的「查看」照常工作）。
export function isNoticeLayerTarget(target: EventTarget | null | undefined): boolean {
  const el = target as Element | null;
  return !!el && typeof el.closest === 'function'
    && !!el.closest('[data-sonner-toaster],[data-moo-top-notice]');
}

// Radix onInteractOutside 的 event = CustomEvent<{ originalEvent: PointerEvent }>，
// 真实点击目标在 detail.originalEvent.target 上（外层 target 是 document/图层）。
export function isNoticeLayerInteract(e: { detail?: unknown; target?: EventTarget | null }): boolean {
  const orig = (e.detail as { originalEvent?: Event } | undefined)?.originalEvent;
  return isNoticeLayerTarget(orig?.target ?? e.target);
}
