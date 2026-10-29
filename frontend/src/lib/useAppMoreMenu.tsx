import { useEffect, useState } from 'react';
import type { AppInfo, AppOperation } from '../api/client';
import { availableVersionLabel, downloadFpk, fetchFpkDownloadWizard, installFpkDownload } from '../api/client';
import { toast } from 'sonner';
import { Bell, BellOff, Download, Loader2, Play, RefreshCw, Square, Trash2 } from 'lucide-react';
import type { MoreMenuItem } from '@/components/MoreMenu';
import { loadFpkFiles, pickCachedFpk } from '@/lib/fpkCache';

/* ── 0.6.366（用户令）：一份「⋯ 更多」菜单语义，三处复用 ────────────────
   0.6.365 把菜单写进了移动行（AppRowActions）；用户接着要求「我的收藏页也加」，
   于是把「更新 / 忽略·取消忽略 / 停用·启动 / 下载·安装本地包 / 卸载」这套
   构建逻辑抽到 hook，行列表、收藏卡（AppCard）共用同一份，避免各处口径漂移。
   详情页（AppDetailDialog）仍保留自己的实现——它需要在动作前关闭对话框。 */

interface Opts {
  app: AppInfo;
  operation?: AppOperation;
  isInstalled: boolean;
  canUpdate: boolean;
  controlBusy: boolean;
  controlling?: string | null;
  onUpdate?: (app: AppInfo) => void;
  onControl?: (app: AppInfo, action: 'start' | 'stop') => void;
  onUninstall?: (app: AppInfo) => void;
  onIgnoreUpdate?: (app: AppInfo) => void;
  onUnignoreUpdate?: (app: AppInfo) => void;
  /** 安装本地缓存包成功后（刷新目录/缓存计数） */
  onInstalled?: () => void;
  /** 需要向导参数时转详情页 */
  onNeedDetail?: (app: AppInfo) => void;
}

export function useAppMoreMenu(opts: Opts): { items: MoreMenuItem[]; dangerItems: MoreMenuItem[]; fpkBusy: boolean } {
  const {
    app, operation, isInstalled, canUpdate, controlBusy, controlling,
    onUpdate, onControl, onUninstall, onIgnoreUpdate, onUnignoreUpdate, onInstalled, onNeedDetail,
  } = opts;

  /* FPK 本地缓存清单（模块级 60s 缓存 + 单飞，一屏卡/行只发一次请求） */
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

  const startFpkDownload = () => {
    if (fpkBusy || !hasSource) return;
    setFpkBusy(true);
    let doneMsg = '';
    downloadFpk(app.appname, (ev) => { if (ev.step === 'done' && ev.message) doneMsg = ev.message; })
      .promise
      .then(async () => { toast.success(doneMsg || 'FPK 下载完成'); await refreshCachedFpk(); })
      .catch((e: unknown) => {
        if (e instanceof DOMException && e.name === 'AbortError') return;
        toast.error(e instanceof Error ? e.message : 'FPK 下载失败');
      })
      .finally(() => setFpkBusy(false));
  };

  const installCachedFpk = () => {
    const name = cachedFpk;
    if (!name || fpkBusy) return;
    setFpkBusy(true);
    fetchFpkDownloadWizard(name)
      .then((wz) => {
        if (wz.has_wizard && (wz.content?.length ?? 0) > 0) {
          toast.info('该应用安装需要填写向导参数，请在详情页继续');
          onNeedDetail?.(app);
          return;
        }
        return installFpkDownload(name, () => {}).promise.then(() => {
          toast.success(`已安装 ${app.display_name}`);
          onInstalled?.();
        });
      })
      .catch((e: unknown) => { toast.error(e instanceof Error ? e.message : '安装失败'); })
      .finally(() => setFpkBusy(false));
  };

  const items: MoreMenuItem[] = [];
  if (isInstalled && canUpdate && !app.update_ignored && onUpdate) {
    items.push({
      key: 'update', icon: <RefreshCw className="h-3.5 w-3.5" />, label: '更新',
      hint: availableVersionLabel(app) ? `v${availableVersionLabel(app)}` : undefined,
      onClick: () => onUpdate(app),
    });
  }
  if (canUpdate && !app.update_ignored && onIgnoreUpdate) {
    items.push({ key: 'ignore', icon: <BellOff className="h-3.5 w-3.5" />, label: '忽略更新', onClick: () => onIgnoreUpdate(app) });
  }
  if (app.update_ignored && onUnignoreUpdate) {
    items.push({ key: 'unignore', icon: <Bell className="h-3.5 w-3.5" />, label: '取消忽略更新', onClick: () => onUnignoreUpdate(app) });
  }
  const canToggle = isInstalled && (app.start_stop ?? true) && app.status !== 'nostart' && !controlBusy && !!onControl;
  if (canToggle) {
    const running = app.status === 'running';
    items.push({
      key: running ? 'stop' : 'start',
      icon: running ? <Square className="h-3 w-3.5 fill-current" /> : <Play className="h-3 w-3.5 fill-current" />,
      label: running ? '停用' : '启动',
      disabled: !!controlling,
      onClick: () => onControl?.(app, running ? 'stop' : 'start'),
    });
  }
  if (hasSource) {
    items.push({
      key: 'fpk',
      icon: fpkBusy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Download className="h-3.5 w-3.5" />,
      label: !fpkChecked ? '检查本地缓存…' : fpkBusy ? '处理中…' : cachedFpk ? (canUpdate ? '安装本地包' : '安装') : '下载',
      disabled: fpkBusy || !fpkChecked,
      onClick: () => { if (fpkBusy || !fpkChecked) return; if (cachedFpk) installCachedFpk(); else startFpkDownload(); },
    });
  }

  const dangerItems: MoreMenuItem[] = [];
  if (isInstalled && onUninstall) {
    dangerItems.push({
      key: 'uninstall', icon: <Trash2 className="h-3.5 w-3.5" />, label: '卸载', danger: true,
      disabled: !!operation || !!controlling, onClick: () => onUninstall(app),
    });
  }
  return { items, dangerItems, fpkBusy };
}
