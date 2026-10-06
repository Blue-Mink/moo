import React, { useLayoutEffect, useRef, useState } from 'react';
import type { AppInfo, AppOperation } from '../api/client';
import {
  sourceLabel, effectiveMaintainer, descriptionPlainText, appWebUrl,
  availableVersionLabel, installedVersionLabel, appDownloadLabel,
} from '../api/client';
import { META_PILL } from './AppDetailDialog';
import AppIcon from './AppIcon';
import { DockerIcon } from './DockerIcon';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Progress } from '@/components/ui/progress';
import { cn } from '@/lib/utils';
import { useCoarsePointer } from '@/lib/hooks';
import { categoryLabel } from '@/lib/categories';
import {
  BellOff, Download, ExternalLink, Globe, Loader2, Package,
  RefreshCw, Star, Tag, User, X,
} from 'lucide-react';

/* 0.6.285（用户定稿·第四轮）：首页卡面「只留一个动作」——未安装=安装、
   已安装=打开（头部 GET 位，统一主色）；更新/启停/卸载/忽略全部撤下卡面
   （颜色杂影响观感 + 易误触），操作进详情对话框（双击卡片即开）。
   卡片**固定等高** h-[180px]（284 自适应高度 182~230 错落 = 用户说的「割裂」），
   简介区 flex-1 + line-clamp-3 吸收内容差高，任何信息量下所有卡绝对等大。 */

interface WebAppDetailCardProps {
  app: AppInfo;
  operation?: AppOperation;
  onInstall: (app: AppInfo) => void;
  /** 0.6.288 用户定稿：网页卡也直接给「更新」胶囊，不必进详情对话框点 */
  onUpdate?: (app: AppInfo) => void;
  /** false when this fnOS build cannot update apps without destroying them. */
  upgradeAllowed?: boolean;
  onDetail?: (app: AppInfo) => void;
  onCancelOp?: (app: AppInfo) => void;
  onSourceFilter?: (source: string) => void;
  onAuthorFilter?: (author: string) => void;
  onDistributorFilter?: (source: string) => void;
  activeTerms?: string[];
  onOpenApp?: (app: AppInfo) => void;
  isFavorite?: boolean;
  onToggleFavorite?: (app: AppInfo) => void;
}

const STATUS_DOT: Record<string, string> = {
  running: 'bg-emerald-500', stopped: 'bg-muted-foreground/50',
  starting: 'bg-amber-500', stopping: 'bg-amber-500',
};
const STATUS_TEXT: Record<string, string> = {
  running: '运行中', stopped: '已停止', starting: '启动中', stopping: '停用中', nostart: '未启动',
};

const getStepText = (step: string): string => {
  switch (step) {
    case 'downloading': return '正在下载…';
    case 'pulling': return '正在拉取镜像…';
    case 'installing': return '正在安装…';
    case 'verifying': return '正在验证…';
    case 'starting': return '正在启动…';
    case 'stopping': return '正在停止…';
    case 'uninstalling': return '正在卸载…';
    default: return '处理中…';
  }
};

const WebAppDetailCard: React.FC<WebAppDetailCardProps> = ({
  app, operation, onInstall, onUpdate, upgradeAllowed = true, onDetail, onCancelOp,
  onSourceFilter, onAuthorFilter, onDistributorFilter,
  activeTerms, onOpenApp,
  isFavorite, onToggleFavorite,
}) => {
  const isInstalled = app.installed;
  const canUpdate = isInstalled && app.has_update;
  const downloadLabel = appDownloadLabel(app);

  // 0.6.287 动态整行截断：简介区实际高度 ÷ 行高 = 可完整显示的行数（clamp 值）。
  // 徽章换行、下载进度条出现等任何挤压简介区的情况都会在重排后重新量。
  const descBoxRef = useRef<HTMLDivElement>(null);
  const descTextRef = useRef<HTMLParagraphElement>(null);
  const [descLines, setDescLines] = useState(3);
  useLayoutEffect(() => {
    const box = descBoxRef.current;
    if (!box) return;
    const measure = () => {
      const p = descTextRef.current;
      if (!p) return;
      const lineH = parseFloat(getComputedStyle(p).lineHeight) || 19.5;
      const bs = getComputedStyle(box);
      const avail = box.clientHeight - parseFloat(bs.paddingTop) - parseFloat(bs.paddingBottom);
      // 连一整行都放不下（下载进度条挤占）→ 0 = 整段隐藏，宁可无简介也不出半个字
      const n = avail < lineH ? 0 : Math.floor((avail + 0.5) / lineH);
      setDescLines((prev) => (prev === n ? prev : n));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(box);
    return () => ro.disconnect();
  }, [app.description]);
  const openUrl = isInstalled ? appWebUrl(app) : null;
  const canOpen = isInstalled && !!onOpenApp && (!!openUrl ||
    ((app.start_stop ?? true) && app.status !== 'nostart' &&
      (app.status === 'stopped' || app.status === 'starting' || app.status === 'stopping')));

  // 双击非功能区域 → 详情对话框；功能区域 = 任意按钮/链接/输入（closest 拦截）
  const onRootDoubleClick = (e: React.MouseEvent<HTMLDivElement>) => {
    const t = e.target as HTMLElement;
    if (t.closest('button, a, input, select, [data-nodbl]')) return;
    onDetail?.(app);
  };

  // 0.6.301：触屏（hover:none+coarse）单击卡面即开详情——iOS 的 dblclick 被
  // 系统「双击缩放」手势吞掉（viewport 可缩放时），触屏坚持双击 = 恒无反应；
  // 桌面细指针保持双击（0.6.293 用户定稿），交互区拦截规则与双击一致。
  const coarse = useCoarsePointer();
  const openIfSurfaceTap = (e: React.MouseEvent<HTMLDivElement>) => {
    const t = e.target as HTMLElement;
    if (t.closest('button, a, input, select, [data-nodbl]')) return;
    onDetail?.(app);
  };

  return (
    <div
      onClick={coarse ? openIfSurfaceTap : undefined}
      onDoubleClick={coarse ? undefined : onRootDoubleClick}
      title={coarse ? '点按打开应用详情' : '双击打开应用详情'}
      className="h-[180px] flex flex-col overflow-hidden rounded-[18px] border border-white/10 bg-card/55 backdrop-blur-xl shadow-appstore transition-shadow duration-200 hover:shadow-appstore-hover touch-manipulation"
    >
      {/* 头部行：图标 + 收藏星 + 名称列（名/appname/版本·下载） + GET 动作位（安装/打开，唯一卡面动作） */}
      <div className="flex-none flex items-center gap-2.5 px-3 pt-3">
        <AppIcon app={app} className="w-10 h-10 rounded-[10px] shrink-0" />
        {onToggleFavorite && (
          <button
            onClick={() => onToggleFavorite(app)}
            className="shrink-0 p-1 rounded-full transition-colors focus:outline-none focus-visible:outline-none text-muted-foreground/50 hover:text-amber-500"
            title={isFavorite ? '取消收藏' : '收藏'}
            aria-label={isFavorite ? `取消收藏 ${app.display_name}` : `收藏 ${app.display_name}`}
            aria-pressed={isFavorite}
          >
            <Star className={cn('h-[18px] w-[18px]', isFavorite && 'fill-amber-400 text-amber-500')} />
          </button>
        )}
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-1.5 min-w-0">
            <h3 className="font-semibold text-[14px] leading-tight truncate" title={app.display_name}>{app.display_name}</h3>
            {app.app_type === 'docker' && <DockerIcon className="h-4 w-4 text-primary shrink-0" />}
            {/* 0.6.290（用户定稿）：「有更新」徽章撤下（同移动行列表由），名字优先完整显示，
                更新信息=版本行 v旧→v新 + 右侧「更新」胶囊 */}
          </div>
          {app.appname && (
            <div className="text-[11px] text-muted-foreground/80 truncate" title={app.appname}>{app.appname}</div>
          )}
          {/* 版本 · 下载量（旧版卡片同款信息） */}
          <div className="flex items-center flex-wrap gap-x-1.5 text-[11px] text-muted-foreground tabular-nums min-w-0">
            <span className="shrink-0">{isInstalled ? `v${installedVersionLabel(app) || '-'}` : (app.latest_version ? `v${app.latest_version}` : '-')}</span>
            {canUpdate && (
              <>
                <span className="text-muted-foreground/50">→</span>
                <span className="text-primary">v{availableVersionLabel(app)}</span>
              </>
            )}
            {downloadLabel && (
              <>
                <span className="text-muted-foreground/30">·</span>
                <span className="inline-flex items-center gap-0.5 min-w-0 truncate"><Download className="h-3 w-3 shrink-0" />{downloadLabel}</span>
              </>
            )}
          </div>
        </div>
        {operation ? (
          <button disabled className="h-8 px-3.5 rounded-full text-[13px] font-semibold inline-flex items-center gap-1.5 shrink-0 bg-primary text-primary-foreground opacity-80">
            <Loader2 className="h-3.5 w-3.5 animate-spin" />处理中
          </button>
        ) : !isInstalled ? (
          <Button onClick={() => onInstall(app)} className="h-8 px-4 rounded-full text-[13px] font-semibold gap-1.5 shrink-0 shadow-sm hover:opacity-90">
            <Download className="h-3.5 w-3.5" />安装
          </Button>
        ) : canUpdate && onUpdate ? (
          // 0.6.288：有更新→直接给「更新」胶囊（描边主色，与移动端同款），
          // 覆盖「打开」优先级：让用户第一时间看到并能一键更新，不必进详情
          <Button onClick={() => onUpdate(app)} variant="outline" disabled={!upgradeAllowed}
            title={upgradeAllowed ? undefined : '当前 fnOS 版本的更新通道会删除应用数据，请在系统应用中心手动安装 fpk'}
            className="h-8 px-4 rounded-full text-[13px] font-semibold gap-1.5 shrink-0 border-primary/50 text-primary hover:bg-primary/10 hover:text-primary disabled:border-muted disabled:text-muted-foreground">
            <RefreshCw className="h-3.5 w-3.5" />{upgradeAllowed ? '更新' : '需手动'}
          </Button>
        ) : canOpen ? (
          <Button onClick={() => onOpenApp?.(app)} className="h-8 px-4 rounded-full text-[13px] font-semibold gap-1.5 shrink-0 shadow-sm hover:opacity-90">
            <ExternalLink className="h-3.5 w-3.5" />打开
          </Button>
        ) : null}
      </div>
      {/* 徽章行（状态在图标正下方 + 源/作者/发布/分类/已忽略；放不下自动换行，
          固定卡高下由简介区吸收行数差） */}
      <div className="mt-2 flex-none flex items-stretch gap-2 px-3">
        <div className="w-10 shrink-0 flex items-center justify-center">
          {isInstalled ? (
            <div className="flex items-center gap-1 whitespace-nowrap text-[11px] text-muted-foreground">
              <span className={cn('h-1.5 w-1.5 rounded-full shrink-0', STATUS_DOT[app.status] || 'bg-muted-foreground/50')} />
              {STATUS_TEXT[app.status] || '已安装'}
            </div>
          ) : (
            <span className="text-[11px] text-muted-foreground/60">未安装</span>
          )}
        </div>
        <div className="flex-1 min-w-0 flex items-center gap-1.5 flex-wrap">
          {(() => {
            const src = sourceLabel(app);
            const author = effectiveMaintainer(app);
            const aSrc = !!activeTerms && !!src && activeTerms.includes(src);
            const aAuth = !!activeTerms && !!author && activeTerms.includes(author);
            const aDist = !!activeTerms && !!app.distributor && activeTerms.includes(app.distributor);
            const pill = (active: boolean) => cn(META_PILL, 'text-[11px] shrink-0', active && 'bg-primary text-primary-foreground');
            return (<>
              {src && onSourceFilter && (
                <button onClick={() => onSourceFilter(src)} className={pill(aSrc)} title={aSrc ? `正在筛选「${src}」源 · 点击清除` : `只看「${src}」源的应用`}>
                  <Globe className="h-3 w-3 mt-px shrink-0" /><span className="min-w-0 break-words">{src}</span>
                </button>
              )}
              {author && onAuthorFilter && (
                <button onClick={() => onAuthorFilter(author)} className={pill(aAuth)} title={aAuth ? `正在筛选「${author}」· 点击清除` : `只看「${author}」开发的应用`}>
                  <User className="h-3 w-3 mt-px shrink-0" /><span className="min-w-0 break-words">{author}</span>
                </button>
              )}
              {app.distributor && app.distributor !== author && onDistributorFilter && (
                <button onClick={() => onDistributorFilter(app.distributor!)} className={pill(aDist)} title={`发布：${app.distributor}`}>
                  <Package className="h-3 w-3 mt-px shrink-0" /><span className="min-w-0 break-words">发布：{app.distributor}</span>
                </button>
              )}
              {app.category && (
                <span className={cn(META_PILL, 'text-[11px] pointer-events-none shrink-0')} title={`分类：${categoryLabel(app.category)}`}>
                  <Tag className="h-3 w-3 mt-px shrink-0" /><span className="min-w-0 break-words">{categoryLabel(app.category)}</span>
                </span>
              )}
              {app.update_ignored && (
                <Badge variant="secondary" className="bg-muted text-muted-foreground border-0 font-medium px-1.5 h-5 text-[10px] rounded-full shrink-0 gap-0.5">
                  <BellOff className="h-2.5 w-2.5" />已忽略
                </Badge>
              )}
            </>);
          })()}
        </div>
      </div>
      {/* 简短简介：flex-1 吃掉剩余高度；0.6.287 动态整行截断——徽章换行/操作进度条
          都会压缩简介区，固定 line-clamp-3 时容器在半个字上硬切（用户实报「最底部
          文字只显示一半」）。改为实测简介区高度 → 能完整放下几行就 clamp 几行，
          任何信息量下永远按整行截断（省略号由 clamp 自带）。 */}
      <div ref={descBoxRef} className="min-h-0 flex-1 overflow-hidden px-3 pt-2 pb-3">
        {app.description && (
          <p ref={descTextRef} style={{ WebkitLineClamp: descLines, display: descLines === 0 ? 'none' : undefined }} className="text-xs text-muted-foreground line-clamp-3 leading-relaxed" title={descriptionPlainText(app.description)}>
            {descriptionPlainText(app.description)}
          </p>
        )}
      </div>
      {/* 0.6.285：卡面动作只剩头部 GET 位（安装/打开）。进行中的操作仍显示
          进度条 + 阶段文字（这是反馈不是按钮），完成后随刷新消失；
          更新/启停/卸载/忽略等操作全部移入详情对话框（双击卡片打开）。 */}
      {operation && (
        <div className="flex-none border-t border-border/40 px-3 py-2 space-y-1">
          <Progress value={operation.progress} className="w-full h-1" />
          <div className="flex items-center justify-between text-[11px] text-muted-foreground tabular-nums">
            <span className="min-w-0 truncate">{getStepText(operation.step)}{operation.step === 'downloading' ? ` · ${Math.round(operation.progress)}%` : ''}</span>
            {(operation.step === 'downloading' || operation.step === 'pulling') && operation.cancel && (
              <button onClick={() => onCancelOp?.(app)} className="shrink-0 p-0.5 rounded-full hover:text-destructive transition-colors" title="取消">
                <X className="h-3.5 w-3.5" />
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  );
};

export default WebAppDetailCard;
