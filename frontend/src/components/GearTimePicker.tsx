import { useCallback, useEffect, useRef } from 'react';
import { cn } from '@/lib/utils';

/**
 * GearTimePicker — 苹果时钟齿轮式时间选择框（0.6.148 加速源测速间隔）：
 * 两列纵向 snap 滚轮（时 0-23 / 分 0-59）+ 高亮格。
 * 交互：滚轮惯性滚动吸附到行；当前行加亮加粗；下方小字单位。
 *
 * 0.6.358（用户令）：「上下两格不要，只留中间那一格」——可见高度从 3 行(72px)
 * 收到 1 行(24px)，即高亮格本身；齿轮样式沿用 Moo 原样式（圆角描边 + 淡蓝高亮底），
 * 不改滚轮交互与数值映射。上下渐隐遮罩随之删除（单格时它会把整格糊掉）。
 */

// 0.6.358：单格视口的内高必须正好等于一行 —— 外框有 1px 描边，故
//   行高 22 + 上下描边 2 = 外框 24（与左列 24px 带同高，行不会被裁 2px）。
const ITEM_H = 22;
const BOX_H = ITEM_H + 2; // 外框：1px 描边 ×2 + 单行

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

  // 0.6.291（用户定稿）：滚轮减速——原生滚动一格冲好几行、很难停准。
  // 改为拦截 wheel：累积 deltaY 过阈值才走一格（一格=一个数字），平滑吸附。
  // ⚠ React 合成 onWheel 是 passive 的（不能 preventDefault），必须原生监听。
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    let acc = 0;
    const THRESHOLD = 50;
    const onWheel = (e: WheelEvent) => {
      if (e.ctrlKey) return; // 捏合缩放交还浏览器
      e.preventDefault();
      e.stopPropagation();
      acc += e.deltaY;
      if (acc > THRESHOLD) acc = THRESHOLD + 1;
      if (acc < -THRESHOLD) acc = -THRESHOLD - 1;
      if (Math.abs(acc) <= THRESHOLD) return;
      const step = acc > 0 ? 1 : -1;
      acc -= step * THRESHOLD;
      const next = Math.max(0, Math.min(max, valueRef.current + step));
      if (next !== valueRef.current) {
        el.scrollTo({ top: next * ITEM_H, behavior: 'smooth' });
      }
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => el.removeEventListener('wheel', onWheel);
  }, [max]);

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
          与标题/手动刷新/折叠按钮平齐在一条直线上（0.6.358 单格后仍成立：
          14 + 24/2 = 26 = 52/2） */}
      <div className="h-3.5" aria-hidden />
      <div
        className="relative overflow-hidden rounded-lg border border-border/30 bg-muted/40"
        style={{ width: 40, height: BOX_H }}
      >
        {/* 0.6.358：只留中间那一格 —— 高亮带现在等于整格（沿用原中心带配色，
            不换样式）；原上下渐隐遮罩已随多行一起删除 */}
        <div className="pointer-events-none absolute inset-0 z-10 rounded-lg border border-primary/25 bg-primary/10" />
        <div
          ref={ref}
          onScroll={onScroll}
          role="listbox"
          aria-label={label}
          className="relative h-full snap-y snap-mandatory overflow-y-auto overscroll-contain outline-none [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
        >
          {/* 0.6.358：去掉原首尾各一行垫片 —— 单格视口下 scrollTop = value*ITEM_H
              正好让该行铺满整格（原垫片是「让第 i 行落在 72px 视口中央」用的，
              单格时反而会把 0 顶出视口） */}
          {Array.from({ length: max + 1 }, (_, i) => (
            <div
              key={i}
              role="option"
              aria-selected={i === value}
              onClick={() => scrollToValue(i)}
              style={{ height: ITEM_H }}
              className={cn(
                'flex cursor-pointer snap-start items-center justify-center text-[13px] tabular-nums',
                i === value ? 'font-semibold text-foreground' : 'text-muted-foreground/50'
              )}
            >
              {i}
            </div>
          ))}
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
