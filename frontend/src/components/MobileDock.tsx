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
 * 移动端底部 Dock（0.6.273 A档：与 PC 端 Desktop Dock 同款材质）——
 * 悬浮居中毛玻璃胶囊：bg-card/55 + backdrop-blur-2xl + 24px 大圆角 +
 * 0.5px 级 hairline 描边 + dock 投影（与 App.tsx 末尾桌面 Dock 完全一致），
 * 距底 10px + 刘海屏安全区。激活 = iOS 蓝图标+文字+浅色底，未激活 = 灰色；
 * 「有更新」带红色角标。
 */
const MobileDock: React.FC<MobileDockProps> = ({ active, onSelect, updateCount, order, bottomOffset = 0 }) => {
  const tabs = orderTabs(order);
  return (
  <nav
    // 静止：距底 10px + safe-area（与 PC Dock bottom-6 同一视觉节奏）。
    // bottom: -bottomOffset → 键盘弹出时 dock 下移键盘高度，钉在物理屏幕
    // 底边被键盘盖住；收起时 offset 归 0，dock 已在位（无回弹位移）
    style={bottomOffset > 0
      ? { bottom: `-${bottomOffset}px` }
      : { bottom: 'calc(10px + env(safe-area-inset-bottom))' }}
    // 0.6.274（用户反馈「太短很拥挤」）：按钮 70px 加宽。
    // 0.6.275（用户仍反馈「不够长」）：胶囊改近全宽 —— w-[calc(100%-24px)]
    //（左右各留 12px）+ max-w-[380px] 防平板宽屏下胶囊过宽，
    // 按钮改 flex-1 均匀铺满胶囊（375px 屏 ≈75px/钮，<360px 小屏自动收缩防溢出）
    className="md:hidden fixed left-1/2 -translate-x-1/2 z-30 flex w-[calc(100%-24px)] max-w-[380px] items-end rounded-[24px] border border-white/10 bg-card/55 px-3 py-2.5 shadow-2xl shadow-black/40 backdrop-blur-2xl"
    aria-label="主导航"
  >
    <div className="flex w-full items-end gap-2">
      {tabs.map(t => {
        const Icon = t.icon;
        const isActive = active === t.key;
        return (
          <button
            key={t.key}
            onClick={() => onSelect(t.key)}
            className={cn(
              // 与 PC Dock 按钮同构：图标 24px + 10px 标签 + 按下缩放反馈
              // 0.6.275：flex-1 均分胶囊宽度（随屏宽自适应，替代固定 70px）
              "relative flex flex-1 flex-col items-center gap-1 rounded-2xl px-2 py-1.5",
              // 0.6.232（用户反馈「点击响应有点慢」）：加**按下缩放反馈** ——
              // 手指落下即刻有视觉回应，不必等内容切换完才"看起来有反应"
              "transition-[background-color,color,transform] duration-100 active:scale-90",
              isActive ? 'bg-primary/15 text-primary' : 'text-muted-foreground'
            )}
            aria-current={isActive ? 'page' : undefined}
          >
            <span className="relative">
              <Icon className="h-6 w-6" strokeWidth={isActive ? 2.2 : 1.8} />
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
