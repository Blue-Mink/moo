import React, { useEffect, useRef } from 'react';

/**
 * 滚轮分类条（2026-10-28 私人补丁）。
 *
 * 视觉：横向滚动容器内的胶囊按「距容器中心的距离」缩放——
 * 中间最大最实、两端渐小渐淡，滚动时像滚轮在转。
 *
 * 实现：不拦截滚动/点击（原生 overflow-x 手势与滚动条都保留），
 * 只在 scroll 事件里按每项中心相对容器中心的距离算 scale/opacity，
 * 直接写 style（不经 React state，滚动期间零重渲染）。
 * rAF 节流；ResizeObserver 处理容器尺寸变化与首次布局。
 *
 * Props:
 *   items   — { key, node }：node 为每项的胶囊 JSX（样式由调用方定）
 *   activeKey — 选中项（选中项基础 scale 提升一档，保证醒目）
 *   onSelect  — 点击项回调（选中/取消 toggle 由调用方定）
 */
export interface WheelItem { key: string; node: React.ReactNode }

const WheelCategories: React.FC<{
  items: WheelItem[];
  activeKey: string | null;
  onSelect: (key: string) => void;
}> = ({ items, activeKey, onSelect }) => {
  const scrollerRef = useRef<HTMLDivElement | null>(null);
  const itemRefs = useRef<Map<string, HTMLElement>>(new Map());
  const rafRef = useRef(0);

  // 按「项中心 x − 容器中心 x」的距离衰减：
  //   d = 0       → scale 1.0, opacity 1   （正中心，最大最实）
  //   |d| ≥ 半宽  → scale 0.72, opacity 0.42（两端，最小最淡）
  // 选中项在同样距离下 scale +0.12（保证当前分类比邻居醒目）。
  const update = () => {
    const sc = scrollerRef.current;
    if (!sc) return;
    const cr = sc.getBoundingClientRect();
    const cx = cr.left + cr.width / 2;
    const half = Math.max(1, cr.width / 2);
    itemRefs.current.forEach((el) => {
      const r = el.getBoundingClientRect();
      const d = Math.abs(r.left + r.width / 2 - cx) / half; // 0~1+
      const clamped = Math.min(1, d);
      const isActive = el.dataset.wheelActive === '1';
      const scale = 1 - clamped * 0.28 + (isActive ? 0.12 : 0);
      const opacity = 1 - clamped * 0.58 + (isActive ? 0.25 : 0);
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

  useEffect(() => {
    const sc = scrollerRef.current;
    if (!sc) return;
    update();
    sc.addEventListener('scroll', schedule, { passive: true });
    const ro = new ResizeObserver(schedule);
    ro.observe(sc);
    window.addEventListener('resize', schedule);
    return () => {
      sc.removeEventListener('scroll', schedule);
      ro.disconnect();
      window.removeEventListener('resize', schedule);
      if (rafRef.current) cancelAnimationFrame(rafRef.current);
    };
  }, [items, activeKey]);

  // 选中变化后重算一次（active 徽标位次变化 → 缩放档变化）
  useEffect(() => { schedule(); }, [activeKey]);

  return (
    <div
      ref={scrollerRef}
      className="flex items-center gap-2 overflow-x-auto pill-bar py-1"
      style={{ perspective: '600px' }}
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
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault();
              onSelect(key);
            }
          }}
        >
          {node}
        </div>
      ))}
    </div>
  );
};

export default WheelCategories;
