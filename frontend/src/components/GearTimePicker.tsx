import { useCallback, useEffect, useRef } from 'react';
import { cn } from '@/lib/utils';

/**
 * GearTimePicker — 苹果时钟齿轮式时间选择框（0.6.148 加速源测速间隔）：
 * 两列纵向 snap 滚轮（时 0-23 / 分 0-59）+ 中心高亮带 + 上下渐隐遮罩。
 * 交互：滚轮惯性滚动吸附到行；中心行加亮加粗；下方小字单位。
 */

const ITEM_H = 24; // 每行高度（h-6）
const VISIBLE = 3; // 可见行数
const BOX_H = ITEM_H * VISIBLE;

interface GearColumnProps {
  value: number;
  max: number; // 上限（时 23 / 分 59）
  unit: string;
  label: string;
  onChange: (v: number) => void;
}

function GearColumn({ value, max, unit, label, onChange }: GearColumnProps) {
  const ref = useRef<HTMLDivElement>(null);
  const raf = useRef(0);
  const valueRef = useRef(value);
  valueRef.current = value;

  // 外部值变化（加载设置/重置）→ 同步滚动位置
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const target = value * ITEM_H;
    if (Math.abs(el.scrollTop - target) > 2) {
      el.scrollTop = target;
    }
  }, [value]);

  useEffect(() => () => cancelAnimationFrame(raf.current), []);

  const onScroll = useCallback(() => {
    cancelAnimationFrame(raf.current);
    raf.current = requestAnimationFrame(() => {
      const el = ref.current;
      if (!el) return;
      const idx = Math.max(0, Math.min(max, Math.round(el.scrollTop / ITEM_H)));
      if (idx !== valueRef.current) onChange(idx);
    });
  }, [max, onChange]);

  // 点击某行 → 平滑滚动到中心（触摸设备友好，也是无滚轮环境的选择方式）
  const scrollToValue = (i: number) => {
    ref.current?.scrollTo({ top: i * ITEM_H, behavior: 'smooth' });
  };

  return (
    <div className="flex flex-col items-center">
      {/* 0.6.149：顶部留白与底部单位小字对称（4px gap + 10px 单位 = 14px），
          使滚轮中心与组件中心重合——头部行 items-center 时对齐轴即滚轮中心线，
          与标题/手动刷新/折叠按钮平齐在一条直线上 */}
      <div className="h-3.5" aria-hidden />
      <div
        className="relative overflow-hidden rounded-lg border border-border/30 bg-muted/40"
        style={{ width: 40, height: BOX_H }}
      >
        {/* 苹果式中心高亮带 */}
        <div className="pointer-events-none absolute inset-x-0 top-1/2 z-10 h-6 -translate-y-1/2 rounded-md border border-primary/25 bg-primary/10" />
        {/* 上下渐隐遮罩（滚轮感） */}
        <div className="pointer-events-none absolute inset-x-0 top-0 z-10 h-5 bg-gradient-to-b from-card to-transparent" />
        <div className="pointer-events-none absolute inset-x-0 bottom-0 z-10 h-5 bg-gradient-to-t from-card to-transparent" />
        <div
          ref={ref}
          onScroll={onScroll}
          role="listbox"
          aria-label={label}
          className="h-full snap-y snap-mandatory overflow-y-auto overscroll-contain outline-none [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
        >
          {/* 0.6.151：顶垫补 snap-start——scrollTop=0（值 0 居中）必须是合法吸附点，
              否则 snap-mandatory 在滚到顶时回弹到 24px（值 1），0 永远设不上；
              外部同步 scrollTop=0（如 0h5m 加载）同样会被弹走造成显示/状态不一致 */}
          <div style={{ height: ITEM_H }} className="snap-start" aria-hidden />
          {Array.from({ length: max + 1 }, (_, i) => (
            <div
              key={i}
              role="option"
              aria-selected={i === value}
              onClick={() => scrollToValue(i)}
              className={cn(
                'flex h-6 cursor-pointer snap-start items-center justify-center text-[13px] tabular-nums',
                i === value ? 'font-semibold text-foreground' : 'text-muted-foreground/50'
              )}
            >
              {i}
            </div>
          ))}
          <div style={{ height: ITEM_H }} aria-hidden />
        </div>
      </div>
      <span className="mt-1 text-[10px] leading-none text-muted-foreground">{unit}</span>
    </div>
  );
}

export default function GearTimePicker({
  hours,
  minutes,
  onChange,
  disabled,
  ariaLabel,
}: {
  hours: number;
  minutes: number;
  onChange: (h: number, m: number) => void;
  disabled?: boolean;
  ariaLabel?: string;
}) {
  return (
    <div
      className={cn('flex items-center gap-1', disabled && 'pointer-events-none opacity-50')}
      role="group"
      aria-label={ariaLabel || '自动测速间隔'}
    >
      <GearColumn
        value={hours}
        max={23}
        unit="时"
        label="测速间隔·小时"
        onChange={(h) => onChange(h, minutes)}
      />
      {/* 0.6.149：随容器 items-center 居中，与滚轮中心线重合（原 pt-[26px] 手写偏移废弃） */}
      <span className="text-[13px] leading-none text-muted-foreground">:</span>
      <GearColumn
        value={minutes}
        max={59}
        unit="分"
        label="测速间隔·分钟"
        onChange={(m) => onChange(hours, m)}
      />
    </div>
  );
}
