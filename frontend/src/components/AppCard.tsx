import React from 'react';
import type { AppInfo, AppOperation } from '../api/client';
import { availableVersionLabel, installedVersionLabel, appWebUrl, appDownloadLabel, sourceLabel, effectiveMaintainer, descriptionPlainText } from '../api/client';
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import AppIcon from "./AppIcon";
import { DockerIcon } from "./DockerIcon";
import { cn, formatSpeed, formatProgress } from "@/lib/utils";
import { 
  Download, 
  RefreshCw, 
  Package,
  ArrowRight,
  X,
  BellOff,
  Globe,
  User,
  ExternalLink,
  Check,
  Star,
} from 'lucide-react';

interface AppCardProps {
  app: AppInfo;
  operation?: AppOperation;
  onInstall: (app: AppInfo) => void;
  onUpdate: (app: AppInfo) => void;
  /** false when this fnOS build cannot update apps without destroying them. */
  upgradeAllowed?: boolean;
  onUninstall?: (app: AppInfo) => void;
  onDetail?: (app: AppInfo) => void;
  onCancelOp?: (app: AppInfo) => void;
  /** 点击源名徽章 → 只看该源的应用 */
  onSourceFilter?: (source: string) => void;
  /** 点击开发者 → 只看该作者的应用 */
  onAuthorFilter?: (author: string) => void;
  /** 点击发布者 → 只看该发布者发布的应用 */
  onDistributorFilter?: (distributor: string) => void;
  /** 搜索框内当前词条（徽章词条叠加多选），命中者渲染选中态。 */
  activeTerms?: string[];
  /** 已安装应用启动/停用（与 fnOS 应用中心同步） */
  onControl?: (app: AppInfo, action: 'start' | 'stop') => void;
  /** 正在执行启停操作的应用名（显示转圈） */
  controlling?: string | null;
  /** 打开应用 Web UI（与 fnOS 应用中心"打开"按钮同目标） */
  onOpenApp?: (app: AppInfo) => void;
  /** 收藏状态（星标实心/空心） */
  isFavorite?: boolean;
  /** 切换收藏（列表/详情同端点） */
  onToggleFavorite?: (app: AppInfo) => void;
}

const AppCard: React.FC<AppCardProps> = ({ app, operation, onInstall, onUpdate, onDetail, onCancelOp, upgradeAllowed = true, onSourceFilter, onAuthorFilter, onDistributorFilter, onOpenApp, activeTerms, isFavorite, onToggleFavorite }) => {
  const isInstalled = app.installed;
  const canUpdate = isInstalled && app.has_update;
  const downloadLabel = appDownloadLabel(app);
  // "打开"目标：daemon appServiceInfo；无 Web 入口的应用不渲染按钮
  const openUrl = isInstalled ? appWebUrl(app) : null;
  // "打开"药丸：运行中且有 Web 入口；或处于可启动状态
  //（停止时 daemon 不下发 web 字段，启动成功后由 handleOpenApp 重新解析目标）
  const canOpen = isInstalled && !!onOpenApp && (
    !!openUrl || ((app.start_stop ?? true) && app.status !== 'nostart' &&
      (app.status === 'stopped' || app.status === 'starting' || app.status === 'stopping'))
  );

  const getStepText = (step: string): string => {
    switch (step) {
      case 'downloading': return '正在下载...';
      case 'pulling': return '正在拉取镜像...';
      case 'installing': return '正在安装...';
      case 'verifying': return '正在验证...';
      case 'starting': return '正在启动...';
      case 'stopping': return '正在停止...';
      case 'uninstalling': return '正在卸载...';
      default: return '处理中...';
    }
  };

  return (
    <Card className={cn(
      // 0.6.284：移动端卡片与 Dock 统一毛玻璃材质（布局不变）
      "relative overflow-hidden border border-white/10 bg-card/55 backdrop-blur-xl shadow-appstore rounded-[18px] transition-all duration-200 hover:shadow-appstore-hover hover:-translate-y-0.5",
      operation && "border-primary/50"
    )}>
      <div className="p-4 flex flex-col h-full gap-3">

        <div className="flex items-start gap-3 cursor-pointer touch-manipulation" onClick={() => onDetail?.(app)}>
          <div className="shrink-0">
            <AppIcon app={app} className="w-14 h-14" />
          </div>

          {/* 收藏星标：图标旁（与详情头部同一位置语言），实心琥珀 = 已收藏 */}
          {onToggleFavorite && (
            <button
              onClick={(e) => { e.stopPropagation(); onToggleFavorite(app); }}
              className={cn(
                "shrink-0 -mt-0.5 p-1.5 rounded-full transition-colors focus:outline-none focus-visible:outline-none", /* 0.6.146 星标去焦点环 */
                /* 0.6.128 去圆圈底（hover 在触屏卡住会留常驻圆圈） */
                isFavorite ? "text-amber-500" : "text-muted-foreground/50 hover:text-amber-500"
              )}
              title={isFavorite ? '取消收藏' : '收藏'}
              aria-label={isFavorite ? `取消收藏 ${app.display_name}` : `收藏 ${app.display_name}`}
              aria-pressed={isFavorite}
            >
              <Star className={cn("h-5 w-5", isFavorite && "fill-amber-400 text-amber-500")} />
            </button>
          )}

          <div className="flex-1 min-w-0 flex flex-col gap-0.5">
            <div className="flex items-center justify-between gap-2">
              <div className="flex items-center gap-1.5 min-w-0">
                <h3 className="font-semibold text-[15px] leading-tight text-foreground truncate" title={app.display_name}>
                  {app.display_name}
                </h3>
                {/* 0.6.219：Docker 应用标记改为 Docker 官方鲸鱼（配色跟随主题色） */}
                {app.app_type === 'docker' && (
                  <DockerIcon className="h-4 w-4 text-primary" />
                )}
              </div>
              {/* 0.6.290（用户定稿）：「有更新」徽章撤下（同移动行/网页卡），名字优先完整显示 */}
              {app.update_ignored && (
                <Badge variant="secondary" className="bg-muted text-muted-foreground border-0 font-medium px-1.5 h-5 text-xs shrink-0 rounded-full gap-0.5">
                  <BellOff className="h-2.5 w-2.5" />
                  已忽略
                </Badge>
              )}
            </div>

            {/* 所有应用：appname 统一显示在应用名下面 */}
            {app.appname && (
              <span className="text-[13px] text-muted-foreground/80 truncate" title={app.appname}>
                {app.appname}
              </span>
            )}

            <div className="flex items-center flex-wrap gap-x-1.5 text-xs text-muted-foreground">
              <span>{isInstalled ? `v${installedVersionLabel(app) || '-'}` : (app.latest_version ? `v${app.latest_version}` : '-')}</span>
              {canUpdate && (
                <>
                  <ArrowRight className="h-3 w-3 text-muted-foreground/50" />
                  <span className="text-primary">
                    v{availableVersionLabel(app)}
                  </span>
                </>
              )}
              {downloadLabel && (
                <>
                  <span className="text-muted-foreground/30">·</span>
                  <span className="inline-flex items-center gap-0.5">
                    <Download className="h-3 w-3" />
                    {downloadLabel}
                  </span>
                </>
              )}
            </div>

            {/* 应用源/开发者/发布者（版本号之下）：三项统一同款蓝框徽章 + 小地球源图标，
                全部可点击过滤；长名称在框内换行不截断。
                安装状态标签（未安装/已安装）：放在应用图标下面、居中于图标中心，
                垂直位置 = 右侧徽章行的中线（绝对定位，不占徽章流）。
                min-h-6：无徽章的应用也预留一行徽章高度，卡高一致 */}
            <div className="relative min-h-6 min-w-0">
              <span
                className={cn(
                  "absolute top-1/2 -translate-y-1/2 w-14 text-center text-xs font-medium whitespace-nowrap",
                  isInstalled ? "text-emerald-600 dark:text-emerald-500" : "text-muted-foreground/50"
                )}
                style={{ left: -68 }}
                title={isInstalled ? '已安装' : '未安装'}
              >
                {isInstalled ? '已安装' : '未安装'}
              </span>
              <div className="flex items-start flex-wrap gap-1 min-w-0">
              {(() => {
                const src = sourceLabel(app);
                const author = effectiveMaintainer(app);
                const dist = app.distributor;
                // 徽章词条已在搜索框（多选叠加）→ 命中徽章渲染选中态（实心 + ✓）
                const aSrc = !!activeTerms && !!src && activeTerms.includes(src);
                const aAuth = !!activeTerms && !!author && activeTerms.includes(author);
                const aDist = !!activeTerms && !!dist && activeTerms.includes(dist);
                const pillBase = "inline-flex items-start gap-1 rounded-full px-2 py-[3px] max-w-full text-xs leading-[17px] font-medium transition-colors";
                const pillCls = (active: boolean) => cn(pillBase, active ? "bg-primary text-primary-foreground" : "bg-primary/10 text-primary hover:bg-primary/20");
                return (<>
                  {/* 所有应用都标注来源：官方→飞牛应用中心源、内置→fnos-store、外部源→显示名 */}
                  {src && onSourceFilter && (
                    <button
                      onClick={(e) => { e.stopPropagation(); onSourceFilter(src); }}
                      className={pillCls(aSrc)}
                      title={aSrc ? `正在筛选「${src}」源 · 点击清除` : `只看「${src}」源的应用`}
                    >
                      <Globe className="h-3 w-3 mt-px shrink-0" />
                      <span className="min-w-0 break-words">{src}</span>
                      {aSrc && <Check className="h-2.5 w-2.5 mt-px shrink-0" />}
                    </button>
                  )}
                  {author && onAuthorFilter && (
                    <button
                      onClick={(e) => { e.stopPropagation(); onAuthorFilter(author); }}
                      className={pillCls(aAuth)}
                      title={aAuth ? `正在筛选「${author}」· 点击清除` : `只看「${author}」开发的应用`}
                    >
                      <User className="h-3 w-3 mt-px shrink-0" />
                      <span className="min-w-0 break-words">{author}</span>
                      {aAuth && <Check className="h-2.5 w-2.5 mt-px shrink-0" />}
                    </button>
                  )}
                  {dist && dist !== author && onDistributorFilter && (
                    <button
                      onClick={(e) => { e.stopPropagation(); onDistributorFilter(dist); }}
                      className={pillCls(aDist)}
                      title={aDist ? `正在筛选「${dist}」· 点击清除` : `只看「${dist}」发布的应用`}
                    >
                      <Package className="h-3 w-3 mt-px shrink-0" />
                      <span className="min-w-0 break-words">{dist}</span>
                      {aDist && <Check className="h-2.5 w-2.5 mt-px shrink-0" />}
                    </button>
                  )}
                </>);
              })()}
              </div>
            </div>
          </div>
        </div>

        {/* 简介行恒占位（两行高）：无简介的应用卡高与有简介的一致（网格行对齐） */}
        <div className="min-h-10">
          {app.description && (
            <p
              className="text-xs text-muted-foreground line-clamp-2 leading-relaxed cursor-pointer hover:text-foreground transition-colors"
              onClick={() => onDetail?.(app)}
              title="点击查看详情"
            >
              {descriptionPlainText(app.description)}
            </p>
          )}
        </div>

        <div className="flex-1" />

        {operation ? (
          <div className="pt-2 border-t border-border/20 space-y-2">
            <Progress value={operation.progress} className="w-full h-1" />
            
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2 text-xs text-muted-foreground min-w-0 tabular-nums">
                <span className="shrink-0">{getStepText(operation.step)}</span>
                {operation.step === 'downloading' && operation.speed != null && operation.speed > 0 ? (
                  <>
                    <span className="shrink-0">{formatSpeed(operation.speed)}</span>
                    {operation.downloaded != null && operation.total != null && operation.total > 0 && (
                      <span className="truncate">{formatProgress(operation.downloaded, operation.total)}</span>
                    )}
                  </>
                ) : (
                  <span>{Math.round(operation.progress)}%</span>
                )}
              </div>
              
              {(operation.step === 'downloading' || operation.step === 'pulling') && operation.cancel && (
                <button
                  onClick={() => onCancelOp?.(app)}
                  className="shrink-0 p-0.5 rounded-full text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors"
                  title="取消"
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              )}
            </div>
          </div>
        ) : (() => {
          // 底栏只留主操作按钮（状态标签已移到徽章行）；无可操作按钮时整行不出
          if (!isInstalled || canUpdate || canOpen) {
            return (
              <div className="pt-2 border-t border-border/20">
                {!isInstalled ? (
                  <Button
                    onClick={() => onInstall(app)}
                    className="pill w-full bg-primary text-primary-foreground px-4 h-8 text-[13px] font-semibold shadow-sm hover:opacity-90"
                  >
                    <Download className="mr-1 h-3.5 w-3.5" />
                    安装
                  </Button>
                ) : canUpdate ? (
                  <Button
                    onClick={() => onUpdate(app)}
                    variant="outline"
                    disabled={!upgradeAllowed}
                    title={upgradeAllowed ? undefined : '当前 fnOS 版本的更新通道会删除应用数据，请在系统应用中心手动安装 fpk'}
                    className="pill w-full h-8 px-4 text-[13px] font-semibold border-primary/50 text-primary hover:bg-primary/10 hover:text-primary disabled:border-muted disabled:text-muted-foreground"
                  >
                    <RefreshCw className="mr-1 h-3.5 w-3.5" />
                    {upgradeAllowed ? '更新' : '需手动更新'}
                  </Button>
                ) : (
                  <Button
                    onClick={() => onOpenApp?.(app)}
                    aria-label={`打开 ${app.display_name}`}
                    className="pill w-full bg-primary text-primary-foreground px-4 h-8 text-[13px] font-semibold shadow-sm hover:opacity-90"
                  >
                    <ExternalLink className="mr-1 h-3.5 w-3.5" />
                    打开
                  </Button>
                )}
              </div>
            );
          }
          return null;
        })()}
      </div>
    </Card>
  );
};

export default AppCard;
