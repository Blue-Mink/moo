import React from 'react';
import type { AppInfo, AppOperation } from '../api/client';
import { Loader2, Play } from 'lucide-react';
import MoreMenuButton from './MoreMenu';
import { useAppMoreMenu } from '@/lib/useAppMoreMenu';

/* ── 0.6.365（用户令）：移动端行列表右侧动作列 ─────────────────────────
   用户原话：「移动端也做这样的适配，在应用列表页面增加更多按钮」——桌面卡面
   已有「主操作胶囊 + ⋯」竖排动作列，移动端行只有主操作药丸、次要动作
   （忽略更新 / 停用 / 卸载 / 下载·安装本地包）无处可点，只能先进详情页。
   这里补同一套动作列：**主操作在上、⋯ 在下**；没有主操作的应用（工具类无
   web 入口 / 无启停 / 无更新）⋯ 直接顶到主操作位（即「打开」按钮的位置），
   与桌面卡面同一落位规则。

   0.6.367（用户令，本轮改）：
   ①「收藏 / 安装·打开 / 更多」三钮**等间距**竖排同一列——此前收藏星标在
     AppRowList 里 absolute top-0、主操作与 ⋯ 是只有 4px 间距的居中组，
     星标↔主操作 29px 而 主操作↔⋯ 仅 4px，用户报「更多和上面的安装/打开
     太近、容易误触」；改为固定三槽（24/28/28 + gap-3=12px）后三处间距一致。
   ②收藏星标改由本组件承载（props.favorite）→ 天然与动作列同轴居中，
     无主操作的行不再出现「星标贴右缘、⋯ 居中」的 18px 错位。
   菜单内容由 useAppMoreMenu 统一构建（0.6.366 起与收藏卡共用同一份）。 */

interface Props {
  app: AppInfo;
  operation?: AppOperation;
  isInstalled: boolean;
  canUpdate: boolean;
  canOpen: boolean;
  canStart: boolean;
  controlBusy: boolean;
  upgradeAllowed?: boolean;
  controlling?: string | null;
  /** 0.6.367：收藏星标（与主操作/更多同一竖列、等间距；位置跨行统一） */
  favorite?: React.ReactNode;
  onInstall: (app: AppInfo) => void;
  onUpdate: (app: AppInfo) => void;
  onOpenApp?: (app: AppInfo) => void;
  onControl?: (app: AppInfo, action: 'start' | 'stop') => void;
  onDetail?: (app: AppInfo) => void;
  onUninstall?: (app: AppInfo) => void;
  onIgnoreUpdate?: (app: AppInfo) => void;
  onUnignoreUpdate?: (app: AppInfo) => void;
  onInstallDone?: () => void;
}

const AppRowActions: React.FC<Props> = ({
  app, operation, isInstalled, canUpdate, canOpen, canStart, controlBusy,
  upgradeAllowed = true, controlling, favorite, onInstall, onUpdate, onOpenApp,
  onControl, onDetail, onUninstall, onIgnoreUpdate, onUnignoreUpdate, onInstallDone,
}) => {
  const { items, dangerItems, fpkBusy } = useAppMoreMenu({
    app, operation, isInstalled, canUpdate, controlBusy, controlling,
    onUpdate, onControl, onUninstall, onIgnoreUpdate, onUnignoreUpdate,
    onInstalled: onInstallDone, onNeedDetail: onDetail,
  });
  const hasMore = items.length > 0 || dangerItems.length > 0;
  const hasPrimary = !operation && (!isInstalled || canUpdate || controlBusy || canStart || canOpen);

  const primaryBtn = !isInstalled ? (
    <button
      onClick={() => onInstall(app)}
      className="pill bg-muted/60 text-foreground border border-border/50 h-7 px-4 text-[13px] font-semibold active:bg-muted"
    >
      安装
    </button>
  ) : canUpdate ? (
    <button
      onClick={() => onUpdate(app)}
      disabled={!upgradeAllowed}
      className="pill h-7 px-3.5 text-[13px] font-semibold border border-border/50 bg-muted/60 text-foreground active:bg-muted disabled:border-muted disabled:bg-transparent disabled:text-muted-foreground"
    >
      {upgradeAllowed ? '更新' : '需手动'}
    </button>
  ) : controlBusy ? (
    <button
      disabled
      aria-label={app.status === 'starting' ? '启动中' : '停用中'}
      className="pill h-7 px-3 text-[13px] font-semibold border border-border/60 bg-muted/40 text-muted-foreground inline-flex items-center gap-1"
    >
      <Loader2 className="h-3 w-3 animate-spin" />
      {app.status === 'starting' ? '启动中' : '停用中'}
    </button>
  ) : canStart ? (
    <button
      onClick={() => onControl?.(app, 'start')}
      disabled={controlling === app.appname}
      aria-label={`启动 ${app.display_name}`}
      title={`「${app.display_name}」已停止，点击启动`}
      className="pill h-7 px-4 text-[13px] font-semibold border border-border/60 bg-muted/60 text-muted-foreground active:bg-muted disabled:opacity-60 inline-flex items-center gap-1"
    >
      {controlling === app.appname
        ? <Loader2 className="h-3 w-3 animate-spin" />
        : <Play className="h-3 w-3 fill-current" />}
      启动
    </button>
  ) : (
    <button
      onClick={() => onOpenApp?.(app)}
      aria-label={`打开 ${app.display_name}`}
      className="pill bg-muted/60 text-foreground border border-border/50 h-7 px-4 text-[13px] font-semibold active:bg-muted"
    >
      打开
    </button>
  );

  const moreBtn = hasMore ? (
    <MoreMenuButton
      items={items}
      dangerItems={dangerItems}
      portal
      className="h-7 w-7"
      ariaLabel="更多操作"
      dot={fpkBusy}
    />
  ) : null;

  return (
    <div
      className="relative shrink-0 self-stretch flex flex-col items-center justify-center gap-3 min-w-[60px]"
      onClick={(e) => e.stopPropagation()}
    >
      {favorite}
      {/* 主操作槽：无主操作时 ⋯ 占这一槽（0.6.365 定稿「放在打开那里」），
          保证「有/无主操作」两行类型的 ⋯ 落点完全同高 */}
      {hasPrimary ? primaryBtn : moreBtn}
      {/* ⋯ 槽恒占位（0.6.367）：三槽总高固定 ⇒ 收藏星标、主操作、⋯ 在
         有/无主操作的行里相对行中心偏移逐像素一致，跨行不跳位 */}
      <div className="h-7 w-7">{hasPrimary ? moreBtn : null}</div>
    </div>
  );
};

export default React.memo(AppRowActions);
