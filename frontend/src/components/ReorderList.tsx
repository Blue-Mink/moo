import React, { useRef, useState } from 'react';
import { Check, GripVertical } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface ReorderItem {
  key: string;
  label: string;
  icon?: React.ElementType;
  /** 角标（如「有更新」的红色数字），原样渲染 */
  badge?: React.ReactNode;
}

interface ReorderListProps {
  items: ReorderItem[];
  /** 落位后回调（keys = 新顺序全量） */
  onReorder: (keys: string[]) => void;
  /** 拖拽/保存进行中（外部禁用交互） */
  busy?: boolean;
}

/**
 * 通用抖动排序列表（0.6.122 dock 栏排序 / 设置 tab 排序）：
 * 交互对齐应用源抖动排序（0.6.98）——
 * 触控长按行 500ms 进入抖动模式（行抖动、禁滚动），按住行体或手柄拖拽；
 * 桌面鼠标在抖动模式下长按 150ms 抓住。抬起落位 → onReorder 全量 keys。
 * 点「完成」退出抖动（未落位变化 = 无保存）。
 * 折叠 / 恢复默认按钮在卡片头部（SettingsPage），本组件只管行与拖动。
 */
const ReorderList: React.FC<ReorderListProps> = ({ items, onReorder, busy }) => {
  const [wiggle, setWiggle] = useState(false);
  const [dragIdx, setDragIdx] = useState<number | null>(null);
  const [dragOffset, setDragOffset] = useState(0);
  const [targetIdx, setTargetIdx] = useState<number | null>(null);

  const rowElsRef = useRef<(HTMLDivElement | null)[]>([]);
  const pressRef = useRef<{ x: number; y: number; idx: number; pointerId: number; grabbed: boolean; moved: boolean; entered: boolean } | null>(null);
  const lpTimerRef = useRef<number | null>(null);
  const wiggleJustEnteredRef = useRef(false);

  const clearLpTimer = () => {
    if (lpTimerRef.current !== null) {
      clearTimeout(lpTimerRef.current);
      lpTimerRef.current = null;
    }
  };

  const exitWiggle = () => {
    clearLpTimer();
    pressRef.current = null;
    setWiggle(false);
    setDragIdx(null);
    setDragOffset(0);
    setTargetIdx(null);
  };

  // ── 行体指针（进入抖动 + 抓住拖拽）─────────────────────────────────
  const onRowPointerDown = (idx: number, e: React.PointerEvent<HTMLDivElement>) => {
    if (busy) return;
    if (e.pointerType === 'mouse' && e.button !== 0) return;
    clearLpTimer();
    pressRef.current = { x: e.clientX, y: e.clientY, idx, pointerId: e.pointerId, grabbed: false, moved: false, entered: false };
    if (wiggle) {
      // 抖动态：触控按行体 = 抓住拖拽；鼠标长按 150ms 抓住
      if (e.pointerType !== 'mouse') {
        const p = pressRef.current;
        p.grabbed = true;
        const el = rowElsRef.current[idx];
        if (el) { try { el.setPointerCapture(e.pointerId); } catch { /* 忽略 */ } }
        setDragIdx(idx);
        setDragOffset(0);
        setTargetIdx(idx);
      } else {
        lpTimerRef.current = window.setTimeout(() => {
          const p = pressRef.current;
          if (!p || p.idx !== idx || p.moved) return;
          p.grabbed = true;
          const el = rowElsRef.current[idx];
          if (el) { try { el.setPointerCapture(p.pointerId); } catch { /* 忽略 */ } }
          setDragIdx(idx);
          setDragOffset(0);
          setTargetIdx(idx);
        }, 150);
      }
      return;
    }
    // 非抖动态：触控长按 500ms 进入抖动并抓住该行（对齐 iOS 应用库）
    if (e.pointerType !== 'mouse') {
      lpTimerRef.current = window.setTimeout(() => {
        const p = pressRef.current;
        if (!p || p.idx !== idx || p.moved) return;
        p.grabbed = true;
        p.entered = true;
        wiggleJustEnteredRef.current = true;
        const el = rowElsRef.current[idx];
        if (el) { try { el.setPointerCapture(e.pointerId); } catch { /* 忽略 */ } }
        setWiggle(true);
        setDragIdx(idx);
        setDragOffset(0);
        setTargetIdx(idx);
      }, 500);
    } else {
      // 桌面：长按 500ms 直接进入抖动（不抓住，随后再按住拖）
      lpTimerRef.current = window.setTimeout(() => {
        const p = pressRef.current;
        if (!p || p.idx !== idx || p.moved) return;
        p.entered = true;
        wiggleJustEnteredRef.current = true;
        setWiggle(true);
      }, 500);
    }
  };

  const onRowPointerMove = (idx: number, e: React.PointerEvent<HTMLDivElement>) => {
    const p = pressRef.current;
    if (!p || p.idx !== idx) return;
    if (!p.moved && Math.abs(e.clientX - p.x) + Math.abs(e.clientY - p.y) > 10) {
      p.moved = true;
      if (!p.grabbed) clearLpTimer(); // 还没抓住就移动 = 滚动，取消长按判定
      return;
    }
    if (!p.grabbed) return;
    // 已抓住：按各行实际高度累计计算目标槽位
    const els = rowElsRef.current;
    const n = items.length;
    const heights: number[] = [];
    for (let i = 0; i < n; i++) {
      const el = els[i];
      heights.push((el ? el.offsetHeight : 56) + 8);
    }
    const offset = e.clientY - p.y;
    let acc = 0;
    for (let i = 0; i < idx; i++) acc += heights[i];
    const center = acc + heights[idx] / 2 + offset;
    let t = idx;
    acc = 0;
    for (let i = 0; i < n; i++) {
      if (center >= acc + heights[i] / 2) t = i;
      else break;
      acc += heights[i];
    }
    setDragOffset(offset);
    setTargetIdx(t);
  };

  const onRowPointerUp = (idx: number) => {
    const p = pressRef.current;
    pressRef.current = null;
    clearLpTimer();
    if (!wiggle) return;
    if (!p) return;
    if (!p.grabbed) return;
    // 落位：目标槽位变化 → 全量 keys 回调（未移动的抬落 to===idx 自然无动作；
    // 触控长按进入抖动的那次按压若拖动了同样落位——与第二次按压无差别）
    const to = targetIdx;
    setDragIdx(null);
    setDragOffset(0);
    setTargetIdx(null);
    if (to === null || to === idx) return;
    const keys = items.map((i) => i.key);
    const [moved] = keys.splice(idx, 1);
    keys.splice(to, 0, moved);
    onReorder(keys);
  };

  const onRowPointerCancel = () => {
    const p = pressRef.current;
    pressRef.current = null;
    clearLpTimer();
    if (p?.grabbed) {
      setDragIdx(null);
      setDragOffset(0);
      setTargetIdx(null);
    }
  };

  // 手柄按压：立即抓住（手柄上浏览器不会发起滚动）
  const onHandlePointerDown = (idx: number, e: React.PointerEvent<HTMLButtonElement>) => {
    if (!wiggle || busy) return;
    if (e.pointerType === 'mouse' && e.button !== 0) return;
    e.preventDefault();
    clearLpTimer();
    pressRef.current = { x: e.clientX, y: e.clientY, idx, pointerId: e.pointerId, grabbed: true, moved: false, entered: false };
    const el = rowElsRef.current[idx];
    if (el) { try { el.setPointerCapture(e.pointerId); } catch { /* 忽略 */ } }
    setDragIdx(idx);
    setDragOffset(0);
    setTargetIdx(idx);
  };

  return (
    <div>
      <div className={cn('space-y-2', wiggle && 'touch-none select-none')}>
        {items.map((item, idx) => {
          const isDragging = dragIdx === idx;
          let shift = 0;
          if (dragIdx !== null && targetIdx !== null && !isDragging) {
            const draggedEl = rowElsRef.current[dragIdx];
            const h = (draggedEl ? draggedEl.offsetHeight : 56) + 8;
            if (dragIdx < targetIdx && idx > dragIdx && idx <= targetIdx) shift = -h;
            else if (dragIdx > targetIdx && idx >= targetIdx && idx < dragIdx) shift = h;
          }
          const Icon = item.icon;
          return (
            <div
              key={item.key}
              data-reorder-row={idx}
              ref={(el) => { rowElsRef.current[idx] = el; }}
              onPointerDown={(e) => onRowPointerDown(idx, e)}
              onPointerMove={(e) => onRowPointerMove(idx, e)}
              onPointerUp={() => onRowPointerUp(idx)}
              onPointerCancel={onRowPointerCancel}
              onContextMenu={(e) => e.preventDefault()}
              className={cn(
                'flex items-center gap-2.5 rounded-xl border border-border/25 bg-card px-3 py-2.5',
                wiggle && !isDragging && 'animate-wiggle cursor-grab',
                isDragging && 'relative z-10 shadow-lg border-primary/40 cursor-grabbing'
              )}
              style={{
                transform: isDragging ? `translateY(${dragOffset}px)` : shift !== 0 ? `translateY(${shift}px)` : undefined,
                transition: isDragging ? 'none' : 'transform 130ms ease',
                animationDelay: wiggle && !isDragging ? `${idx * 40}ms` : undefined,
              }}
            >
              {wiggle && (
                <button
                  type="button"
                  onPointerDown={(e) => onHandlePointerDown(idx, e)}
                  onContextMenu={(e) => e.preventDefault()}
                  className="-ml-1.5 shrink-0 cursor-grab touch-none select-none rounded p-1 active:cursor-grabbing"
                  title="按住拖动排序"
                  aria-label={`拖动排序 ${item.label}`}
                >
                  <GripVertical className="h-4 w-4 text-muted-foreground" />
                </button>
              )}
              <div className={cn('flex min-w-0 flex-1 items-center gap-2.5', wiggle && !isDragging && 'pointer-events-none')}>
                {Icon && (
                  <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-muted/70">
                    <Icon className="h-4 w-4 text-muted-foreground" />
                  </span>
                )}
                <span className="truncate text-sm font-medium">{item.label}</span>
                {item.badge}
              </div>
              <span className="shrink-0 text-[11px] text-muted-foreground/60 tabular-nums">{idx + 1}</span>
            </div>
          );
        })}
      </div>
      {wiggle && (
        <div className="mt-2 flex items-center justify-end">
          <button
            type="button"
            onClick={exitWiggle}
            className="inline-flex h-7 items-center gap-1 rounded-lg bg-primary px-3 text-xs font-medium text-primary-foreground"
          >
            <Check className="h-3.5 w-3.5" />
            完成
          </button>
        </div>
      )}
    </div>
  );
};

export default ReorderList;
