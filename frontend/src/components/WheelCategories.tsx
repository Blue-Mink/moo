import React, { useEffect, useRef } from 'react';

/**
 * 滚轮分类条 v2（2026-10-28 私人补丁）。
 *
 * 形态：固定宽度（容器 50%，最大 480px）的独立「滚轮」区块，
 * 选中项恒定钉在区块正中央、最大最醒目；左右相邻项依次变小变淡，
 * 像一个横置的滚轮/转盘。鼠标滚轮悬停 = 旋转（wheel 事件驱动），
 * 点击左右项 = 选中它并把它转到中心。
 *
 * 实现与 v1 同（scroll 驱动 + rAF 直写 style），差别：
 *  - 容器定宽（不是 flex-1 拉满）
 *  - wheel 事件 preventDefault + scrollLeft 步进（旋转手感）
 *  - 选中项 scrollIntoView 居中（保证「最醒目的在中间」）
 *  - 胶囊用 flex 居中，字体垂直水平都居中
 */

export interface WheelItem { key: string; node: React.ReactNode }

const WheelCategories: React.FC<{
  items: WheelItem[];
  activeKey: string | null;
  onSelect: (key: string) => void;
  /** 宽度占父容器比例（默认 0.5 = 一半） */
  widthRatio?: number;
}> = ({ items, activeKey, onSelect, widthRatio = 0.5 }) => {
  const scrollerRef = useRef<HTMLDivElement | null>(null);
  const itemRefs = useRef<Map<string, HTMLElement>>(new Map());
  const rafRef = useRef(0);


  const update = () => {
    const sc = scrollerRef.current;
    if (!sc) return;
    const cr = sc.getBoundingClientRect();
    const cx = cr.left + cr.width / 2;
    const half = Math.max(1, cr.width / 2);
    itemRefs.current.forEach((el) => {
      const r = el.getBoundingClientRect();
      const d = Math.min(1, Math.abs(r.left + r.width / 2 - cx) / half);
      const isActive = el.dataset.wheelActive === '1';
      const scale = 1 - d * 0.34 + (isActive ? 0.08 : 0);
      const opacity = 1 - d * 0.55 + (isActive ? 0.2 : 0);
      el.style.transform = `scale(${scale.toFixed(3)})`;
      el.style.opacity = Math.min(1, opacity).toFixed(3);
    });
  };

  const schedule = () => {
    if (rafRef.current) return;
    rafRef.current = requestAnimationFrame(() => {
      rafRef.current = 0;
      update();
    });
  };

  // 选中项滚到区块正中央（保证「最醒目的在中间」）
  const centerActive = (behavior: ScrollBehavior = 'smooth') => {
    const el = activeKey ? itemRefs.current.get(activeKey) : undefined;
    const sc = scrollerRef.current;
    if (!el || !sc) return;
    const er = el.getBoundingClientRect();
    const scr = sc.getBoundingClientRect();
    const delta = (er.left + er.width / 2) - (scr.left + scr.width / 2);
    sc.scrollBy({ left: delta, behavior });
  };

  useEffect(() => {
    const sc = scrollerRef.current;
    if (!sc) return;
    update();
    sc.addEventListener('scroll', schedule, { passive: true });
    const ro = new ResizeObserver(() => { schedule(); centerActive('auto'); });
    ro.observe(sc);
    window.addEventListener('resize', schedule);
    return () => {
      sc.removeEventListener('scroll', schedule);
      ro.disconnect();
      window.removeEventListener('resize', schedule);
      if (rafRef.current) cancelAnimationFrame(rafRef.current);
    };
  }, [items, activeKey]);

  // activeKey 变化 → 平滑滚到中心
  useEffect(() => { centerActive('smooth'); }, [activeKey]);

  // 鼠标滚轮 = 旋转：纵向滚轮量换算成横向滚动（每 notch ≈ 一个胶囊位）
  const onWheel = (e: React.WheelEvent) => {
    e.preventDefault();
    const sc = scrollerRef.current;
    if (!sc) return;
    const step = 150;
    const delta = Math.abs(e.deltaY) >= Math.abs(e.deltaX) ? e.deltaY : e.deltaX;
    sc.scrollLeft += Math.sign(delta) * Math.max(step, Math.abs(delta) / 3);
    e.stopPropagation();
  };

  return (
    <div
      ref={scrollerRef}
      onWheel={onWheel}
      className="flex items-stretch gap-2 overflow-x-auto pill-bar py-2 select-none"
      style={{
        width: `${widthRatio * 100}%`,
        maxWidth: 480,
        maskImage: 'linear-gradient(90deg, transparent 0%, black 12%, black 88%, transparent 100%)',
        WebkitMaskImage: 'linear-gradient(90deg, transparent 0%, black 12%, black 88%, transparent 100%)',
      }}
    >
      {items.map(({ key, node }) => (
        <div
          key={key}
          data-wheel-active={activeKey === key ? '1' : '0'}
          ref={(el) => {
            if (el) itemRefs.current.set(key, el);
            else itemRefs.current.delete(key);
          }}
          className="shrink-0 origin-center transition-[transform,opacity] duration-150 ease-out will-change-transform"
          onClick={() => onSelect(key)}
          role="button"
          tabIndex={0}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onSelect(key); }
          }}
        >
          {node}
        </div>
      ))}
    </div>
  );
};

export default WheelCategories;
