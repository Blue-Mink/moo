import React, { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { AppInfo, AppOperation } from '../api/client';
import {
  sourceLabel, effectiveMaintainer, descriptionPlainText, appWebUrl,
  availableVersionLabel, installedVersionLabel, appDownloadLabel,
  hasActiveUpdate,
  downloadFpk, fetchFpkDownloadWizard, installFpkDownload,
} from '../api/client';
import { toast } from 'sonner';
import { META_PILL } from './AppDetailDialog';
import AppIcon from './AppIcon';
import { installedStateLabel } from '@/lib/appRunState';
import { DockerIcon } from './DockerIcon';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Progress } from '@/components/ui/progress';
import { cn } from '@/lib/utils';
import { useCoarsePointer } from '@/lib/hooks';
import { categoryLabel } from '@/lib/categories';
import {
  Bell, BellOff, Download, Globe, Loader2, Package,
  Play, RefreshCw, Square, Star, Tag, Trash2, User, X,
} from 'lucide-react';
import MoreMenuButton, { type MoreMenuItem } from './MoreMenu';
// 0.6.342：已停止 → 置灰「启动」按钮（与行列表/网格同一口径）
import { canStartInList } from '@/lib/appRunState';
import { loadFpkFiles, pickCachedFpk } from '@/lib/fpkCache';

/* 0.6.365：FPK 缓存清单与匹配规则抽到 lib/fpkCache.ts（卡面与移动行列表共用）。 */

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
  /** 0.6.319F：鼠标打开详情的习惯（仅细指针有效，触屏恒单击）。默认 double。 */
  clickMode?: 'single' | 'double';
  /** 0.6.342：已装应用启动（列表「已停止」→ 置灰「启动」按钮直接启动） */
  onControl?: (app: AppInfo, action: 'start' | 'stop') => void;
  /** 正在执行启停操作的应用名（按钮转圈） */
  controlling?: string | null;
  /** 0.6.360（用户定稿）：卡面「⋯ 更多」菜单的次要动作（卸载 / 忽略更新） */
  onUninstall?: (app: AppInfo) => void;
  onIgnoreUpdate?: (app: AppInfo) => void;
  onUnignoreUpdate?: (app: AppInfo) => void;
  /** 0.6.362：卡面装了本地缓存 FPK 之后，通知宿主刷新目录与缓存计数 */
  onInstallDone?: () => void;
}

const STATUS_DOT: Record<string, string> = {
  running: 'bg-emerald-500', stopped: 'bg-muted-foreground/50',
  starting: 'bg-amber-500', stopping: 'bg-amber-500',
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
  clickMode = 'double',
  onControl, controlling,
  onUninstall, onIgnoreUpdate, onUnignoreUpdate, onInstallDone,
}) => {
  const isInstalled = app.installed;
  /* 0.6.363（用户定稿）：主操作与「→ v新版本」都只认「有效更新」（未忽略）——
     已忽略的应用回落成「打开/启动」，与 ⋯ 里的「取消忽略更新」口径一致。 */
  const canUpdate = hasActiveUpdate(app);
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
  // 0.6.342（用户令）：已停止 → 卡面给置灰「启动」，点击即启动（不再先走「打开」隐式启动）
  const canStart = canStartInList(app) && !!onControl;
  const controlBusy = app.status === 'starting' || app.status === 'stopping';

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
  // 0.6.319F：细指针打开方式受「鼠标操作习惯」开关控制；触屏恒单击
  const singleOpen = coarse || clickMode === 'single';
  const openIfSurfaceTap = (e: React.MouseEvent<HTMLDivElement>) => {
    const t = e.target as HTMLElement;
    if (t.closest('button, a, input, select, [data-nodbl]')) return;
    onDetail?.(app);
  };

  /* ── 0.6.360（用户定稿）：卡面「⋯ 更多」菜单 ──────────────────────────
     用户原话：「在应用列表的页面也增加这个更多的按钮，不要被左边的徽章和应用简介
     遮挡，可以适当调整安装和这个更多按钮的间距」。卡面只保留一个主操作（安装/
     更新/启动/打开），次要动作（停用/启动、忽略更新、卸载）收进 ⋯ 菜单；
     菜单 portal 到 body（卡片 overflow-hidden 会裁切），因此不会被卡内徽章与
     简介遮挡。 */
  const canToggle = isInstalled && (app.start_stop ?? true) && app.status !== 'nostart' && !controlBusy;
  /* ── 0.6.362（用户定稿）：卡面也提供详情页 ⋯ 里的「下载 / 安装（本地缓存）」──
     只在应用有源时给（无源=自装/系统自带，没有可下载的 FPK，与详情页 noSource 同口径）。 */
  const hasSource = !!app.source;
  const [cachedFpk, setCachedFpk] = useState<string | null>(null);
  const [fpkChecked, setFpkChecked] = useState(false);
  const [fpkBusy, setFpkBusy] = useState(false);
  useEffect(() => {
    if (!hasSource) { setFpkChecked(true); setCachedFpk(null); return; }
    let cancelled = false;
    loadFpkFiles()
      .then((files) => { if (!cancelled) setCachedFpk(pickCachedFpk(files, app)?.name ?? null); })
      .finally(() => { if (!cancelled) setFpkChecked(true); });
    return () => { cancelled = true; };
  }, [app.appname, app.display_name, hasSource]); // eslint-disable-line react-hooks/exhaustive-deps
  const refreshCachedFpk = () =>
    loadFpkFiles(true).then((files) => setCachedFpk(pickCachedFpk(files, app)?.name ?? null));
  // 下载 FPK 到本地缓存（后台任务，进度见全局后台任务指示器；完成后再点=安装）
  const startFpkDownload = () => {
    if (fpkBusy || !hasSource) return;
    setFpkBusy(true);
    let doneMsg = '';
    downloadFpk(app.appname, (ev) => { if (ev.step === 'done' && ev.message) doneMsg = ev.message; })
      .promise
      .then(async () => {
        toast.success(doneMsg || 'FPK 下载完成');
        await refreshCachedFpk();
      })
      .catch((e: unknown) => {
        if (e instanceof DOMException && e.name === 'AbortError') return;
        toast.error(e instanceof Error ? e.message : 'FPK 下载失败');
      })
      .finally(() => setFpkBusy(false));
  };
  // 安装本地缓存里的 FPK；需要向导参数的交给详情页（卡面不弹向导）
  const installCachedFpk = () => {
    const name = cachedFpk;
    if (!name || fpkBusy) return;
    setFpkBusy(true);
    fetchFpkDownloadWizard(name)
      .then((wz) => {
        if (wz.has_wizard && (wz.content?.length ?? 0) > 0) {
          toast.info('该应用安装需要填写向导参数，请在详情页继续');
          onDetail?.(app);
          return;
        }
        return installFpkDownload(name, () => {}).promise.then(() => {
          toast.success(`已安装 ${app.display_name}`);
          onInstallDone?.();
        });
      })
      .catch((e: unknown) => {
        toast.error(e instanceof Error ? e.message : '安装失败');
      })
      .finally(() => setFpkBusy(false));
  };
  /* 0.6.361（用户定稿）：「更多按钮点开不是详情，详情直接点应用卡片不就行了」——
     菜单只放次要动作（与详情页 ⋯ 同一套语义：更新/忽略/启停/卸载），不再给「详情」入口。 */
  const moreItems: MoreMenuItem[] = [];
  /* 0.6.361（用户定稿）：「和应用详情页面的按钮一样的弹窗内容」——详情页菜单里有的
     更新/忽略/启停/卸载，卡面菜单同样给（卡面主操作是「更新」时也保留同一项，
     两处菜单内容保持一致，用户不必猜哪里有什么）。 */
  if (isInstalled && canUpdate && !app.update_ignored && onUpdate) {
    moreItems.push({
      key: 'update',
      icon: <RefreshCw className="h-3.5 w-3.5" />,
      label: '更新',
      hint: availableVersionLabel(app) ? `v${availableVersionLabel(app)}` : undefined,
      onClick: () => onUpdate(app),
    });
  }
  if (canUpdate && !app.update_ignored && onIgnoreUpdate) {
    moreItems.push({
      key: 'ignore',
      icon: <BellOff className="h-3.5 w-3.5" />,
      label: '忽略更新',
      onClick: () => onIgnoreUpdate(app),
    });
  }
  if (app.update_ignored && onUnignoreUpdate) {
    moreItems.push({
      key: 'unignore',
      icon: <Bell className="h-3.5 w-3.5" />,
      label: '取消忽略更新',
      onClick: () => onUnignoreUpdate(app),
    });
  }
  if (canToggle && onControl) {
    const running = app.status === 'running';
    moreItems.push({
      key: running ? 'stop' : 'start',
      icon: running ? <Square className="h-3 w-3.5 fill-current" /> : <Play className="h-3 w-3.5 fill-current" />,
      label: running ? '停用' : '启动',
      disabled: controlling === app.appname,
      onClick: () => onControl(app, running ? 'stop' : 'start'),
    });
  }
  const moreDangerItems: MoreMenuItem[] = [];
  if (hasSource) {
    // 0.6.362：与详情页 ⋯ 同一套语义——无缓存=下载、有缓存=安装（下载完成不显示 ✔）
    moreItems.push({
      key: 'fpk',
      icon: fpkBusy
        ? <Loader2 className="h-3.5 w-3.5 animate-spin" />
        : <Download className="h-3.5 w-3.5" />,
      /* 0.6.363（用户定稿）：有可用更新时，这里装的是**本地缓存的旧包**——
         不再笼统写「安装」（与上面的「更新」并列易误点），改标「安装本地包」
         只留四个字的标签（菜单宽 132px 是用户定稿，再加版本小字会把标签截成「安…」）。
         无缓存时仍是「下载」（下到的是源里的最新版）。 */
      label: !fpkChecked ? '检查本地缓存…' : fpkBusy ? '处理中…'
        : cachedFpk ? (canUpdate ? '安装本地包' : '安装') : '下载',
      disabled: !fpkChecked || fpkBusy,
      onClick: () => (cachedFpk ? installCachedFpk() : startFpkDownload()),
    });
  }
  if (isInstalled && onUninstall) {
    moreDangerItems.push({
      key: 'uninstall',
      icon: <Trash2 className="h-3.5 w-3.5" />,
      label: '卸载',
      danger: true,
      disabled: !!operation || controlling === app.appname,
      onClick: () => onUninstall(app),
    });
  }

  return (
    <div
      onClick={singleOpen ? openIfSurfaceTap : undefined}
      onDoubleClick={singleOpen ? undefined : onRootDoubleClick}
      title={singleOpen ? (coarse ? '点按打开应用详情' : '单击打开应用详情') : '双击打开应用详情'}
      className="h-[180px] flex flex-col overflow-hidden rounded-[18px] border border-white/10 bg-card/55 backdrop-blur-xl shadow-appstore transition-shadow duration-200 hover:shadow-appstore-hover touch-manipulation"
    >
      {/* 头部行：图标 + 收藏星 + 名称列（名/appname/版本·下载） + 右侧竖排动作列
          （主操作胶囊在上、⋯ 在下，各自独立占位 → 简介与徽章永远不会压到按钮上） */}
      <div className="flex-none flex items-start gap-2.5 px-3 pt-3">
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
          <div className="min-w-0">
            {/* 0.6.361（用户定稿）：「不要缩略掉左边的应用名称」——主操作与 ⋯ 改成竖排后
                名字列变宽（68 → 100px），再放开 2 行换行，长名不再变成「Eas…」；
                docker 图标改为行内尾缀，不再单独占 22px 把英文长名单词挤断
                （Mattermost → 「Mattermos / t」）。 */}
            <h3 className="font-semibold text-[14px] leading-tight line-clamp-2 break-words" title={app.display_name}>
              {app.display_name}
              {app.app_type === 'docker' && <DockerIcon className="ml-1 inline-block h-3.5 w-3.5 align-[-2px] text-primary" />}
            </h3>
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
        {/* 0.6.361（用户定稿）：右侧竖排动作列（主操作胶囊在上、⋯ 在下）
            0.6.364（用户实报「工具类应用没有打开按钮、只有更多，但它的 ⋯ 位置和
            有打开按钮的应用下面的 ⋯ 不在一个位置」）：
            ① 列固定最小宽 60px（= 2 字主操作胶囊实测宽），⋯ 在该宽度里居中 →
               有/无主操作时 ⋯ 的水平位置一致；
            ② 无主操作时用 h-8 空占位（aria-hidden）占住主操作位，gap 不变 →
               ⋯ 的垂直位置也一致（顶边 32+4=36px）。 */}
        <div className={cn(
          'shrink-0 flex flex-col items-center gap-1',
          // 只有「列里确实有东西」时占 60px 宽，避免空列白占名字空间
          (!!operation || !isInstalled || (canUpdate && !!onUpdate) || controlBusy || canStart || canOpen
            || moreItems.length > 0 || moreDangerItems.length > 0) && 'min-w-[60px]'
        )}>
        {operation ? (
          <button disabled className="h-8 px-3.5 rounded-full text-[13px] font-semibold inline-flex items-center gap-1.5 shrink-0 bg-muted/50 text-muted-foreground border border-border/50 opacity-80">
            <Loader2 className="h-3.5 w-3.5 animate-spin" />处理中
          </button>
        ) : !isInstalled ? (
          // 0.6.319F（用户定稿）：安装/打开胶囊去箭头图标，只留文字（与详情对话框同款）
          <Button onClick={() => onInstall(app)} className="h-8 px-4 rounded-full text-[13px] font-semibold shrink-0 shadow-none bg-muted/60 text-foreground border border-border/50 hover:bg-muted hover:opacity-100">
            安装
          </Button>
        ) : canUpdate && onUpdate ? (
          // 0.6.288：有更新→直接给「更新」胶囊（描边主色，与移动端同款），
          // 覆盖「打开」优先级：让用户第一时间看到并能一键更新，不必进详情
          <Button onClick={() => onUpdate(app)} variant="outline" disabled={!upgradeAllowed}
            title={upgradeAllowed ? undefined : '当前 fnOS 版本的更新通道会删除应用数据，请在系统应用中心手动安装 fpk'}
            // 0.6.307：更新胶囊=选中胶囊同款（浅蓝底+primary/40 描边，原描边无底）
            // 0.6.364（用户定稿「安装/打开/更新的蓝底视觉太吵，都去掉」）：改中性胶囊
            className="h-8 px-4 rounded-full text-[13px] font-semibold gap-1.5 shrink-0 border border-border/50 bg-muted/60 text-foreground hover:bg-muted disabled:border-muted disabled:bg-transparent disabled:text-muted-foreground">
            <RefreshCw className="h-3.5 w-3.5" />{upgradeAllowed ? '更新' : '需手动'}
          </Button>
        ) : controlBusy ? (
          <button disabled className="h-8 px-3 rounded-full text-[13px] font-semibold inline-flex items-center gap-1.5 shrink-0 border border-border/60 bg-muted/40 text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
            {app.status === 'starting' ? '启动中' : '停用中'}
          </button>
        ) : canStart ? (
          // 0.6.342（用户令）：置灰「启动」——点击即启动，不弹详情、不隐式打开
          <Button
            onClick={() => onControl?.(app, 'start')}
            disabled={controlling === app.appname}
            aria-label={`启动 ${app.display_name}`}
            title={`「${app.display_name}」已停止，点击启动`}
            className="h-8 px-4 rounded-full text-[13px] font-semibold shrink-0 gap-1.5 border border-border/60 bg-muted/60 text-muted-foreground shadow-none hover:bg-muted hover:opacity-100 disabled:opacity-60"
          >
            {controlling === app.appname
              ? <Loader2 className="h-3.5 w-3.5 animate-spin" />
              : <Play className="h-3.5 w-3.5 fill-current" />}
            启动
          </Button>
        ) : canOpen ? (
          <Button onClick={() => onOpenApp?.(app)} className="h-8 px-4 rounded-full text-[13px] font-semibold shrink-0 shadow-none bg-muted/60 text-foreground border border-border/50 hover:bg-muted hover:opacity-100">
            打开
          </Button>
        ) : null /* 0.6.365（用户令）：无主操作（工具类应用无 web 入口 / 无启停 / 无更新）
             时不再留 32px 空占位——⋯ 直接顶到主操作位（即「打开」按钮所在的位置），
             竖排列里它就是第一个元素（顶边 0），不再下移一行。 */}
        {(moreItems.length > 0 || moreDangerItems.length > 0) && (
          /* 0.6.361（用户定稿）：⋯ 从「主操作右侧」改为「主操作下方」——与主操作竖排，
             裸图标无白圈；不再与简介/徽章抢同一行横向空间 */
          <MoreMenuButton
            items={moreItems}
            dangerItems={moreDangerItems}
            portal
            className="h-7 w-7"
            ariaLabel="更多操作"
          />
        )}
        </div>
      </div>
      {/* 徽章行（状态在图标正下方 + 源/作者/发布/分类/已忽略；放不下自动换行，
          固定卡高下由简介区吸收行数差） */}
      <div className="mt-2 flex-none flex items-stretch gap-2 px-3">
        <div className="w-10 shrink-0 flex items-center justify-center">
          {isInstalled ? (
            <div className="flex items-center gap-1 whitespace-nowrap text-[11px] text-muted-foreground">
              <span className={cn('h-1.5 w-1.5 rounded-full shrink-0', STATUS_DOT[app.status] || 'bg-muted-foreground/50')} />
              {/* 0.6.348：文案统一走 appRunState（此前本地字典与移动端漂移，
                  同一应用桌面「运行中」/手机「已安装」） */}
              {installedStateLabel(app)}
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
            // 0.6.308（用户定稿）：选中态=移动端行徽章同款实心蓝（用户实看满意，网页端对齐）
// 0.6.319c（用户定稿）：胶囊字号统一=13px（原 11px 覆盖移除，line-height 17px 不变，行高零变化）
const pill = (active: boolean) => cn(META_PILL, 'shrink-0', active && 'bg-primary/15 text-primary border-primary/40');
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
                <button onClick={() => onDistributorFilter(app.distributor!)} className={pill(aDist)} title={`只看「${app.distributor}」发布的应用`}>
                  <Package className="h-3 w-3 mt-px shrink-0" /><span className="min-w-0 break-words">{app.distributor}</span>
                </button>
              )}
              {app.category && (
                <span className={cn(META_PILL, 'pointer-events-none shrink-0')} title={`分类：${categoryLabel(app.category)}`}>
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
          // 0.6.319e（用户定稿）：16px 太大影响整体风格，回 14px 常规 + foreground/70
          <p ref={descTextRef} style={{ WebkitLineClamp: descLines, display: descLines === 0 ? 'none' : undefined }} className="text-sm text-foreground/70 line-clamp-3 leading-relaxed" title={descriptionPlainText(app.description)}>
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
