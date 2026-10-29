import React, { useEffect, useId, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { MoreHorizontal } from 'lucide-react';
import { cn } from '@/lib/utils';

/* ── 0.6.360（用户定稿）：通用「⋯ 更多操作」钮 + 菜单 ─────────────────────
   用户要求（网页端）：①按钮不要外面的白色圆框（裸图标）②弹窗以按钮为中心
   居中展开（此前右缘对齐按钮，观感偏左）③选项文字居中对齐。
   同一个组件两处复用：
   - 详情页头部（portal=false）：菜单 absolute 挂按钮包裹层，CSS 居中
     （left-1/2 + -translate-x-1/2）。**不能 portal**：挂到 body 会被 Radix
     Dialog 判为「外部交互」直接关掉详情页。
   - 列表卡（portal=true）：卡片 overflow-hidden 会裁掉菜单，必须 portal 到
     body 用 fixed 定位（位置由按钮矩形现算，居中并夹在视口内）。 */

export interface MoreMenuItem {
  key: string;
  icon: React.ReactNode;
  label: string;
  hint?: string;
  onClick: () => void;
  danger?: boolean;
  disabled?: boolean;
}

interface Props {
  items: MoreMenuItem[];
  /** 分隔线之下的危险项（卸载） */
  dangerItems?: MoreMenuItem[];
  portal?: boolean;
  className?: string;
  ariaLabel?: string;
  /** 按钮右上角小蓝点（后台下载进行中提示） */
  dot?: boolean;
  menuWidth?: number;
  onOpenChange?: (open: boolean) => void;
}

const MoreMenuButton: React.FC<Props> = ({
  items, dangerItems = [], portal = false, className, ariaLabel = '更多操作',
  /* 0.6.361（用户定稿）：「弹窗不要这么宽」——168 → 132（最长项「取消忽略更新」
     6 字 + 图标 + 内边距实测 ≈ 122） */
  dot = false, menuWidth = 132, onOpenChange,
}) => {
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);
  /* 0.6.367（用户令）：非 portal（详情页头部）菜单的横向修正量——默认以按钮
     居中展开；右侧卡片内余量不足时整体左移到卡片右内边距处（「靠左显示」），
     总之不溢出卡片区域。 */
  const [dx, setDx] = useState(0);
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const btnRef = useRef<HTMLButtonElement | null>(null);
  const menuId = useId();

  useEffect(() => { onOpenChange?.(open); }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  // 点外部关闭：portal 菜单在 body 上（DOM 已离开 wrapRef），按 id 一并判定
  useEffect(() => {
    if (!open) return;
    const onDown = (e: Event) => {
      const t = e.target as Node;
      if (wrapRef.current?.contains(t)) return;
      if (document.getElementById(menuId)?.contains(t)) return;
      setOpen(false);
    };
    document.addEventListener('pointerdown', onDown, true);
    return () => document.removeEventListener('pointerdown', onDown, true);
  }, [open, menuId]);

  const toggle = () => {
    if (open) { setOpen(false); return; }
    if (portal && btnRef.current) {
      const r = btnRef.current.getBoundingClientRect();
      const rows = items.length + dangerItems.length;
      const h = rows * 34 + 8 + (items.length && dangerItems.length ? 9 : 0);
      const left = Math.max(8, Math.min(r.left + r.width / 2 - menuWidth / 2, window.innerWidth - menuWidth - 8));
      const top = Math.max(8, Math.min(r.bottom + 8, window.innerHeight - h - 8));
      setPos({ left, top });
    } else if (!portal && btnRef.current) {
      /* 0.6.367（用户令）：「有些会溢出卡片外」——右侧位置够就以按钮居中展开，
         右侧卡片内不够就整体左移（靠左显示），总之不越出卡片（详情对话框）
         内边距。以前纯居中在手机端 ⋯ 贴卡片右缘时右溢 ~40px 被 overflow-hidden 裁掉。 */
      const r = btnRef.current.getBoundingClientRect();
      const box = btnRef.current.closest('[role="dialog"]') as HTMLElement | null;
      const br = box ? box.getBoundingClientRect() : null;
      /* 12 = 详情页内容卡相对 dialog 的内缩（手机端列容器 px-3 / 极光面板 m-3），
         按 12 夹取后菜单右缘正好贴卡片内边而不是贴屏幕边。 */
      const pad = 12;
      const minLeft = (br ? br.left : 0) + pad;
      const maxLeft = (br ? br.right : window.innerWidth) - menuWidth - pad;
      const centered = r.left + r.width / 2 - menuWidth / 2;
      const left = maxLeft < minLeft ? minLeft : Math.min(Math.max(centered, minLeft), maxLeft);
      setDx(Math.round(left - centered));
    }
    setOpen(true);
  };

  const row = (it: MoreMenuItem) => (
    <button
      key={it.key}
      type="button"
      role="menuitem"
      disabled={it.disabled}
      onClick={() => { setOpen(false); it.onClick(); }}
      className={cn(
        // 0.6.360（用户定稿）：选项文字居中对齐（图标 + 文案作为一组居中）
        'flex w-full items-center justify-center gap-1.5 whitespace-nowrap rounded-lg px-2 py-2 text-center text-[13px] hover:bg-muted disabled:opacity-50',
        it.danger ? 'text-rose-500' : 'text-foreground',
      )}
    >
      <span className="shrink-0 opacity-80">{it.icon}</span>
      <span className="truncate">{it.label}</span>
      {it.hint && <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">{it.hint}</span>}
    </button>
  );

  const body = (
    <div
      id={menuId}
      role="menu"
      style={portal
        ? { left: pos?.left ?? -9999, top: pos?.top ?? -9999, width: menuWidth }
        /* 0.6.366（用户实报「弹窗位置在安装/打开按钮下面」）：0.6.365 加的视口夹取
           在手机端把 innerWidth 读小，菜单被误左移半个菜单宽 → 整块跑到左边按钮下方。
           0.6.367：改为「以按钮居中为基准 + 按卡片（dialog）内边界算修正量 dx」，
           dx=0 时就是 0.6.360 定稿的纯居中；只有会溢出卡片时才左移。
           ⚠️ Tailwind v4 的 -translate-x-1/2 编译成 CSS `translate` 属性（不是
           transform）→ 这里必须覆盖 `translate`；若写 transform 会与类叠加，
           再左移半个菜单宽（实测菜单右缘跑到按钮中心）。 */
        : { width: menuWidth, translate: `calc(-50% + ${dx}px)` }}
      className={cn(
        'z-[80] rounded-xl border border-border bg-popover p-1 shadow-lg',
        portal
          ? 'fixed'
          // 详情页头部：以按钮为中心展开（不再右缘对齐）
          : 'absolute left-1/2 top-[calc(100%+8px)] -translate-x-1/2',
      )}
    >
      {items.map(row)}
      {items.length > 0 && dangerItems.length > 0 && (
        <div aria-hidden className="my-1 h-px bg-border/70" />
      )}
      {dangerItems.map(row)}
    </div>
  );

  return (
    <div ref={wrapRef} className={cn('relative shrink-0', portal ? 'inline-flex' : '')}>
      <button
        ref={btnRef}
        type="button"
        onClick={toggle}
        aria-label={ariaLabel}
        aria-haspopup="menu"
        aria-expanded={open}
        title={ariaLabel}
        className={cn(
          // 0.6.360（用户定稿）：去掉外部白色圆框（原 bg-card/55 + border + blur）
          'relative inline-flex items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-card/70 hover:text-foreground focus:outline-none focus-visible:outline-none',
          open && 'bg-card/70 text-foreground',
          className,
        )}
      >
        <MoreHorizontal className="h-[18px] w-[18px]" />
        {dot && (
          <span aria-hidden className="absolute right-0.5 top-0.5 h-1.5 w-1.5 animate-pulse rounded-full bg-primary" />
        )}
      </button>
      {open && (portal ? createPortal(body, document.body) : body)}
    </div>
  );
};

export default MoreMenuButton;
