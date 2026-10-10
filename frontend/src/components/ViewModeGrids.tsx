import React from 'react';
import type { AppInfo } from '../api/client';
import { descriptionPlainText } from '../api/client';
import { useCoarsePointer } from '../lib/hooks';
import { cn } from '../lib/utils';
import AppIcon from './AppIcon';

/**
 * 0.6.293 网页端三种预览模式中的「极简」「极光」两套网格（0.6.301 起移动端同享）。
 * 交互：细指针（鼠标）双击卡片进入应用详情（与标准模式双击卡面行为一致）；
 * 触屏（hover:none+pointer:coarse）单击即开详情——iOS 的 dblclick 会被系统
 * 「双击缩放」手势吞掉（viewport 可缩放时），触屏坚持双击 = 恒无反应。
 * 卡片挂 touch-action: manipulation，iOS 在卡面上不触发双击缩放。
 * 状态文案按用户定稿 = 「已安装」（已装）/「未安装」（未装），不显示下载量。
 * 0.6.319F：细指针的打开方式受「鼠标操作习惯」开关控制（clickMode prop）：
 * double=双击开详情（0.6.293 定稿，默认）/ single=单击；触屏恒单击不受控。
 */

interface GridProps {
  apps: AppInfo[];
  onDetail: (app: AppInfo) => void;
  /** 0.6.319F：鼠标打开详情的习惯（仅细指针有效，触屏恒单击）。默认 double。 */
  clickMode?: 'single' | 'double';
}

/** 取应用简介首行作为副标题（与 FeaturedShowcase 同规则） */
const tagline = (app: AppInfo) => {
  const line = descriptionPlainText(app.description || '').split('\n').map(s => s.trim()).find(Boolean) || '';
  return line.length > 40 ? line.slice(0, 40) + '…' : line;
};

// 0.6.319F（用户定稿）：「安装」→「已安装」（与未安装对仗，消除歧义）
const statusLabel = (app: AppInfo) => (app.installed ? '已安装' : '未安装');

/* ── 极简模式：App Store「热门应用」行卡同构 —— 只有图标 + 名称 + 状态 ── */
export const MinimalIconGrid: React.FC<GridProps> = ({ apps, onDetail, clickMode = 'double' }) => {
  const coarse = useCoarsePointer();
  // 0.6.319F：细指针打开方式受习惯开关控制；触屏恒单击
  const singleOpen = coarse || clickMode === 'single';
  return (
  <div className="grid grid-cols-[repeat(auto-fill,minmax(104px,1fr))] gap-x-3 gap-y-6 justify-items-center text-center">
    {apps.map(app => (
      <div
        key={app.key || app.appname}
        role="button"
        tabIndex={0}
        title={app.display_name}
        onClick={singleOpen ? () => onDetail(app) : undefined}
        onDoubleClick={singleOpen ? undefined : () => onDetail(app)}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') onDetail(app); }}
        className="group w-full cursor-pointer focus-visible:outline-none [touch-action:manipulation]"
      >
        <AppIcon
          app={app}
          className="mx-auto h-[72px] w-[72px] shadow-[0_2px_8px_rgb(0_0_0/0.10)] transition-transform duration-200 group-hover:scale-[1.04]"
          iconClassName="h-9 w-9"
        />
        <p className="mt-2 text-[13px] font-medium leading-tight truncate">{app.display_name}</p>
        {/* 0.6.319n（用户令）：两态颜色对调——已安装=primary 蓝（突出「我有的」），未安装=灰 */}
        <p className={cn('mt-0.5 text-[11px] truncate', app.installed ? 'text-primary' : 'text-muted-foreground')}>
          {statusLabel(app)}
        </p>
      </div>
    ))}
  </div>
  );
};

/* ── 极光模式（0.6.294 返工定稿；0.6.296 色板按推荐卡方法学重写）：机制照抄
   发现页 HeroBanner——单条 135° 高饱和三色线性渐变整卡斜铺（.aurora-1..12，
   见 index.css），无暗底无光晕无描边；3 张一排宽横幅与原推荐区同构。色板
   经 palette_gate.py 闸门（小步单调 + 中段饱和不坍塌 + 组间可辨），按应用名
   哈希稳定取色（同应用恒同色）。 ── */
const AURORAS = [
  'aurora-1', 'aurora-2', 'aurora-3', 'aurora-4',
  'aurora-5', 'aurora-6', 'aurora-7', 'aurora-8',
  'aurora-9', 'aurora-10', 'aurora-11', 'aurora-12',
];
export const auroraFor = (name: string) => {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) | 0;
  return AURORAS[Math.abs(h) % AURORAS.length];
};

export const AuroraGrid: React.FC<GridProps> = ({ apps, onDetail, clickMode = 'double' }) => {
  const coarse = useCoarsePointer();
  // 0.6.319F：细指针打开方式受习惯开关控制；触屏恒单击
  const singleOpen = coarse || clickMode === 'single';
  return (
  <div className="grid grid-cols-1 md:grid-cols-3 xl:grid-cols-4 gap-4">
    {apps.map(app => (
      <div
        key={app.key || app.appname}
        role="button"
        tabIndex={0}
        title={`${singleOpen ? (coarse ? '点按' : '单击') : '双击'}打开 ${app.display_name} 详情`}
        onClick={singleOpen ? () => onDetail(app) : undefined}
        onDoubleClick={singleOpen ? undefined : () => onDetail(app)}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') onDetail(app); }}
        className={cn(
          'group relative cursor-pointer overflow-hidden rounded-[20px] p-5 text-left text-white',
          'transition-transform duration-200 hover:scale-[1.01]',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/70',
          'touch-manipulation',
          auroraFor(app.appname || app.display_name),
        )}
      >
        <div className="flex items-start gap-4">
          <AppIcon app={app} className="h-16 w-16 shadow-lg" iconClassName="h-8 w-8" />
          <div className="min-w-0 pt-1">
            {/* 「推荐」位置 = 安装状态（用户定稿）；不渲染「查看详情」行 */}
            <p className="text-[11px] font-medium uppercase tracking-widest text-white/70">
              {statusLabel(app)}
            </p>
            <h3 className="mt-1 text-xl font-bold leading-tight truncate">{app.display_name}</h3>
            {tagline(app) && <p className="mt-1 text-[13px] text-white/80 line-clamp-2 leading-snug">{tagline(app)}</p>}
          </div>
        </div>
      </div>
    ))}
  </div>
  );
};

export const MinimalIconGridM = React.memo(MinimalIconGrid);
export const AuroraGridM = React.memo(AuroraGrid);
