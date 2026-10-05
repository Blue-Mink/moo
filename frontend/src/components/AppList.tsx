import React, { Suspense, useEffect, useRef, useState } from 'react';
import type { AppInfo, AppOperation } from '../api/client';
import { PackageSearch, CheckCircle2, RefreshCw, Search } from 'lucide-react';
import { Skeleton } from '@/components/ui/skeleton';
import { WebAppDetailCardSkeleton } from './webCardSkeleton';

// 详情卡片含 README markdown 渲染依赖，与详情对话框共分包（懒加载不进主包）。
const WebAppDetailCard = React.lazy(() => import('./WebAppDetailCard'));

// 渐进渲染批量：首批只渲染这么多卡，滚动接近底部再追加。
// 700+ 卡片一次全渲染（含数百个图标 <img> 同时加载/解码）在 WebView 里
// 首帧与滚动都会明显掉帧；分批后首屏只承担一小块 DOM 与图片。
const PAGE_SIZE = 48;

interface AppListProps {
  apps: AppInfo[];
  loading: boolean;
  onInstall: (app: AppInfo) => void;
  onUpdate: (app: AppInfo) => void;
  /** false when this fnOS build cannot update apps without destroying them. */
  upgradeAllowed?: boolean;
  onDetail: (app: AppInfo) => void;
  onCancelOp?: (app: AppInfo) => void;
  filterType?: string;
  appOperations?: Map<string, AppOperation>;
  searchQuery?: string;
  onSourceFilter?: (source: string) => void;
  onAuthorFilter?: (author: string) => void;
  onDistributorFilter?: (distributor: string) => void;
  /** 打开已安装应用的 Web UI（有 web 入口的应用才渲染按钮）。 */
  onOpenApp?: (app: AppInfo) => void;
  /** 搜索框内当前词条（徽章词条叠加多选），命中者渲染选中态。 */
  activeTerms?: string[];
  /** 收藏 key 集合（卡片星标） */
  favoriteSet?: Set<string>;
  /** 切换收藏（卡片星标） */
  onToggleFavorite?: (app: AppInfo) => void;
}

const getEmptyMessage = (filterType?: string) => {
  switch (filterType) {
    case 'installed':
      return { icon: CheckCircle2, text: '暂无已安装的应用' };
    case 'update_available':
      return { icon: RefreshCw, text: '所有应用都是最新版本' };
    default:
      return { icon: PackageSearch, text: '暂无可用应用' };
  }
};

const AppList: React.FC<AppListProps> = ({ apps, loading, onInstall, onUpdate, upgradeAllowed, onDetail, onCancelOp, filterType, appOperations, searchQuery, onSourceFilter, onAuthorFilter, onDistributorFilter, onOpenApp, activeTerms, favoriteSet, onToggleFavorite }) => {
  // 渐进渲染：apps 集合变化（搜索/筛选/刷新后首尾不同）时回到首批；
  // 内容相同的重复拉取（安装/启停后刷新）不重置，避免用户滚动位置被弹回。
  const [visible, setVisible] = useState(PAGE_SIZE);
  const sig = apps.length === 0 ? 'empty' : `${apps.length}:${apps[0].key}:${apps[apps.length - 1].key}`;
  const prevSigRef = useRef('');
  useEffect(() => {
    if (prevSigRef.current !== sig) {
      prevSigRef.current = sig;
      setVisible(PAGE_SIZE);
    }
  }, [sig]);
  const shown = apps.slice(0, visible);
  const sentinelRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const el = sentinelRef.current;
    if (!el || visible >= apps.length) return;
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          setVisible((v) => Math.min(v + PAGE_SIZE, apps.length));
        }
      },
      { rootMargin: '800px 0px' },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [apps.length, visible]);

  if (loading) {
    return (
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4 items-start">
        {[...Array(6)].map((_, i) => (
          <div key={i} className="flex flex-col space-y-3">
            <Skeleton className="h-[125px] w-full rounded-xl" />
            <div className="space-y-2">
              <Skeleton className="h-4 w-[250px]" />
              <Skeleton className="h-4 w-[200px]" />
            </div>
          </div>
        ))}
      </div>
    );
  }

  if (apps.length === 0) {
    if (searchQuery?.trim()) {
      return (
        <div className="flex flex-col items-center justify-center h-64 text-muted-foreground">
          <Search className="h-12 w-12 mb-4 opacity-40" />
          <p className="text-sm">未找到匹配「{searchQuery.trim()}」的应用</p>
        </div>
      );
    }
    const empty = getEmptyMessage(filterType);
    const Icon = empty.icon;
    return (
      <div className="flex flex-col items-center justify-center h-64 text-muted-foreground">
        <Icon className="h-12 w-12 mb-4 opacity-40" />
        <p className="text-sm">{empty.text}</p>
      </div>
    );
  }

  return (
    /* 0.6.285（用户定稿）：统一等高大卡网格（h-180 固定高）；卡面动作只剩
       安装/打开（头部 GET 位）；2xl 宽屏 5 列让卡片整体更小更匀。 */
    <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5 gap-4 items-start">
      {shown.map((app) => (
        <Suspense key={app.key || app.appname} fallback={<WebAppDetailCardSkeleton />}>
          <WebAppDetailCard
            app={app}
            operation={appOperations?.get(app.appname)}
            onInstall={onInstall}
            onUpdate={onUpdate}
            upgradeAllowed={upgradeAllowed}
            onDetail={onDetail}
            onCancelOp={onCancelOp}
            onSourceFilter={onSourceFilter}
            onAuthorFilter={onAuthorFilter}
            onDistributorFilter={onDistributorFilter}
            activeTerms={activeTerms}
            onOpenApp={onOpenApp}
            isFavorite={favoriteSet?.has(app.key || app.appname)}
            onToggleFavorite={onToggleFavorite}
          />
        </Suspense>
      ))}
      {visible < apps.length && <div ref={sentinelRef} className="h-px" />}
    </div>
  );
};

// memo：搜索输入（防抖前）/其他无关状态变化时，若 apps 引用与回调未变，
// 跳过整棵卡片树的重新渲染 —— 这是 WebView 输入流畅度的关键。
export default React.memo(AppList);
