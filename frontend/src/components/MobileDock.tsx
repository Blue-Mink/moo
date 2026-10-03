import React from 'react';
import { Compass, LayoutGrid, CheckCircle2, RefreshCw } from 'lucide-react';
import { cn } from '@/lib/utils';

export type MobileTabKey = 'recommended' | 'all' | 'installed' | 'update_available';

const TABS: { key: MobileTabKey; label: string; icon: React.ElementType }[] = [
  { key: 'recommended', label: '发现', icon: Compass },
  { key: 'all', label: '全部', icon: LayoutGrid },
  { key: 'installed', label: '已安装', icon: CheckCircle2 },
  { key: 'update_available', label: '有更新', icon: RefreshCw },
];

interface MobileDockProps {
  active: MobileTabKey;
  onSelect: (key: MobileTabKey) => void;
  updateCount: number;
  /** 用户自定义顺序（系统设置「Dock 栏排序」；缺省 = TABS 默认序）。 */
  order?: string[];
  /**
   * 键盘高度（px）：dock 下移该值，钉在物理屏幕底边 —— 键盘弹出时 dock
   * 停在屏幕最底被键盘盖住（不跟键盘上移），收起时已就位于视口底边，
   * 全程不重挂载、无"回弹"位移（对齐 iOS App Store tab bar 观感）。
   */
  bottomOffset?: number;
}

/** 按自定义顺序排列 TABS（缺 key 追加末尾，防旧配置丢项）。 */
function orderTabs(order?: string[]): typeof TABS {
  if (!order || order.length === 0) return TABS;
  const byKey = new Map(TABS.map((t) => [t.key as string, t]));
  const out: typeof TABS = [];
  for (const k of order) {
    const t = byKey.get(k);
    if (t) {
      out.push(t);
      byKey.delete(k);
    }
  }
  for (const t of byKey.values()) out.push(t);
  return out;
}

/**
 * iOS App Store 风格底部标签栏：
 * 激活 = iOS 蓝图标+文字（无底色），未激活 = 灰色；
 * 「有更新」带红色角标。固定底部，适配刘海屏安全区。
 */
const MobileDock: React.FC<MobileDockProps> = ({ active, onSelect, updateCount, order, bottomOffset = 0 }) => {
  const tabs = orderTabs(order);
  return (
  <nav
    // bottom: -bottomOffset → 键盘弹出时 dock 下移键盘高度，钉在物理屏幕
    // 底边被键盘盖住；收起时 offset 归 0，dock 已在位（无回弹位移）
    style={bottomOffset > 0 ? { bottom: `-${bottomOffset}px` } : undefined}
    className="md:hidden fixed bottom-0 inset-x-0 z-30 bg-card/90 backdrop-blur-xl border-t border-border/60 pb-[max(0px,env(safe-area-inset-bottom))]"
    aria-label="主导航"
  >
    <div className="grid grid-cols-4">
      {tabs.map(t => {
        const Icon = t.icon;
        const isActive = active === t.key;
        return (
          <button
            key={t.key}
            onClick={() => onSelect(t.key)}
            className={cn(
              "flex flex-col items-center justify-center gap-[3px] pt-1.5 pb-1.5",
              // 0.6.232（用户反馈「点击响应有点慢」）：加**按下缩放反馈** ——
              // 手指落下即刻有视觉回应，不必等内容切换完才"看起来有反应"
              "transition-[color,transform] duration-100 active:scale-90",
              isActive ? "text-primary" : "text-muted-foreground"
            )}
            aria-current={isActive ? 'page' : undefined}
          >
            <span className="relative">
              <Icon className="h-[26px] w-[26px]" strokeWidth={isActive ? 2.2 : 1.9} />
              {t.key === 'update_available' && updateCount > 0 && (
                <span
                  className={cn(
                    "absolute -top-1 -right-2.5 min-w-[17px] h-[17px] px-1 rounded-full text-[10px] font-semibold flex items-center justify-center",
                    isActive ? "bg-primary text-primary-foreground" : "bg-destructive text-destructive-foreground"
                  )}
                >
                  {updateCount > 99 ? '99+' : updateCount}
                </span>
              )}
            </span>
            <span className={cn("text-[10px] leading-none", isActive && "font-semibold")}>{t.label}</span>
          </button>
        );
      })}
    </div>
  </nav>
  );
};

export default MobileDock;
