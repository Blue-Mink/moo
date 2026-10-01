import React, { useMemo } from 'react';
import type { AppInfo } from '../api/client';
import { availableVersionLabel, appDownloadLabel, descriptionPlainText } from '../api/client';
import { cn } from '../lib/utils';
import { ChevronRight, Flame, Clock } from 'lucide-react';
import AppIcon from './AppIcon';

interface FeaturedShowcaseProps {
  apps: AppInfo[];
  onDetail: (app: AppInfo) => void;
}

const GRADIENTS = ['hero-gradient-1', 'hero-gradient-2', 'hero-gradient-3'];

/** 取应用简介首行作为横幅副标题（HTML 描述先纯文本化，避免露出标签） */
const tagline = (app: AppInfo) => {
  const line = descriptionPlainText(app.description || '').split('\n').map(s => s.trim()).find(Boolean) || '';
  return line.length > 40 ? line.slice(0, 40) + '…' : line;
};

const HeroBanner: React.FC<{ app: AppInfo; className?: string; onDetail: (a: AppInfo) => void }> = ({ app, className, onDetail }) => (
  <button
    onClick={() => onDetail(app)}
    className={cn(
      'group relative overflow-hidden rounded-[20px] p-5 text-left text-white transition-transform duration-200 hover:scale-[1.01] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/70 h-full',
      className
    )}
  >
    <div className="flex items-start gap-4">
      {/* 统一走 AppIcon（外部源经本地代理+磁盘缓存，直连 raw 在国内不可靠） */}
      <AppIcon app={app} className="h-16 w-16 shadow-lg" iconClassName="h-8 w-8" />
      <div className="min-w-0 pt-1">
        <p className="text-[11px] font-medium uppercase tracking-widest text-white/70">推荐</p>
        <h3 className="mt-1 text-xl font-bold leading-tight truncate">{app.display_name}</h3>
        {tagline(app) && <p className="mt-1 text-[13px] text-white/80 line-clamp-2 leading-snug">{tagline(app)}</p>}
      </div>
    </div>
    <div className="mt-3 flex items-center gap-1 text-[12px] font-semibold text-white/85">
      查看详情 <ChevronRight className="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" />
    </div>
  </button>
);

const RowCard: React.FC<{ app: AppInfo; onDetail: (a: AppInfo) => void }> = ({ app, onDetail }) => (
  <button
    onClick={() => onDetail(app)}
    className="group w-[104px] shrink-0 text-left focus-visible:outline-none"
  >
    <AppIcon
      app={app}
      className="h-[72px] w-[72px] shadow-[0_2px_8px_rgb(0_0_0/0.10)]"
      iconClassName="h-9 w-9"
    />
    <p className="mt-2 text-[13px] font-medium leading-tight truncate">{app.display_name}</p>
    <p className="mt-0.5 text-[11px] text-muted-foreground truncate">
      {appDownloadLabel(app) ?? `v${availableVersionLabel(app)}`}
    </p>
  </button>
);

/**
 * 「发现」页顶部的 App Store Today 风格展示区：
 * 3 张渐变色横幅（编辑推荐：全目录随机 3 款）+ 「热门应用」「最近更新」横向滚动行。
 * 注意：应用名用 h3/p 而非可被 e2e 按 heading 命中的独立卡片结构，
 * 且容器不带 overflow-hidden，避免与 e2e 的 cardFor() 选择器冲突。
 */
const FeaturedShowcase: React.FC<FeaturedShowcaseProps> = ({ apps, onDetail }) => {
  // 编辑推荐：从整个目录随机挑 3 款。图标统一由 AppIcon 渲染（外部源
  // 经本地代理+磁盘缓存，缺失时占位兜底），不再按 icon_url 过滤候选。
  // 同一份目录内结果保持稳定（useMemo 只依赖 apps），刷新后重新洗牌。
  const featured = useMemo(() => {
    const arr = [...apps];
    const out: AppInfo[] = [];
    while (out.length < 3 && arr.length > 0) {
      const j = Math.floor(Math.random() * arr.length);
      out.push(arr.splice(j, 1)[0]);
    }
    return out;
  }, [apps]);
  const popular = useMemo(
    () => [...apps].sort((a, b) => (b.download_count ?? 0) - (a.download_count ?? 0)).slice(0, 12),
    [apps]
  );
  const recent = useMemo(
    () => [...apps].sort((a, b) => (b.updated_at || '').localeCompare(a.updated_at || '')).slice(0, 12),
    [apps]
  );

  if (featured.length === 0) return null;

  return (
    <div className="space-y-8">
      {/* 横幅区：首张占两列；3 张时第三张占右两列（col-start-2 错位），
          与首行 [2+1] 镜像成 [1空+2] 的锯齿布局，避免整行拉伸留大片空白 */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        {featured.map((app, i) => (
          <HeroBanner
            key={app.appname}
            app={app}
            onDetail={onDetail}
            className={cn(
              GRADIENTS[i % GRADIENTS.length],
              i === 0 && 'md:col-span-2',
              featured.length === 3 && i === 2 && 'md:col-span-2 md:col-start-2'
            )}
          />
        ))}
      </div>

      {/* 热门应用 */}
      <section>
        <div className="mb-3 flex items-center gap-2">
          <Flame className="h-4 w-4 text-orange-500" />
          <h2 className="text-lg font-bold tracking-tight">热门应用</h2>
        </div>
        <div className="flex gap-4 overflow-x-auto no-scrollbar pb-1 -mx-1 px-1">
          {popular.map(app => <RowCard key={app.appname} app={app} onDetail={onDetail} />)}
        </div>
      </section>

      {/* 最近更新 */}
      {recent.length > 0 && (
        <section>
          <div className="mb-3 flex items-center gap-2">
            <Clock className="h-4 w-4 text-primary" />
            <h2 className="text-lg font-bold tracking-tight">最近更新</h2>
          </div>
          <div className="flex gap-4 overflow-x-auto no-scrollbar pb-1 -mx-1 px-1">
            {recent.map(app => <RowCard key={app.appname} app={app} onDetail={onDetail} />)}
          </div>
        </section>
      )}
    </div>
  );
};

// memo：apps 引用与 onDetail 未变时跳过重渲染（搜索打字等高频场景）
export default React.memo(FeaturedShowcase);
