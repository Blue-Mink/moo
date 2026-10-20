import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Loader2, RefreshCw, Trash2, Check, ChevronDown } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from './ui/button';
import WizardDialog from './WizardDialog';
import { cn } from '../lib/utils';
import {
  fetchFpkDownloads,
  deleteFpkDownload,
  installFpkDownload,
  fetchFpkDownloadWizard,
  type FpkDownloadFile,
  type AppWizard,
  type WizardParam,
} from '../api/client';

/** 0.6.332 F4：列表副行取值——display_name → appname → 「解析中…」。 */
function fpkSubLine(f: FpkDownloadFile): { text: string; pending?: boolean } | null {
  const stem = f.name.replace(/\.fpk$/i, '').toLowerCase();
  // 与文件名（去 .fpk）相同、空串、未展开的 ${...} 占位串都视为「无可用名字」
  const usable = (v?: string): string => {
    const t = (v || '').trim();
    if (!t || t.startsWith('${') || t.toLowerCase() === stem) return '';
    return t;
  };
  const dn = usable(f.display_name);
  if (dn) return { text: dn };
  const an = usable(f.appname);
  if (an) return { text: an };
  // manifest 后台解析中：先给占位，解析完自动换成真实名字（避免首开整列空白）
  if (f.parsing) return { text: '解析中…', pending: true };
  return null;
}

/** 与 SettingsPage 原本地实现逐字节同款（设置页行为零变化；0 返回空串）。 */
function formatBytes(bytes: number): string {
  if (bytes <= 0) return '';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  const val = bytes / Math.pow(1024, i);
  return `${val >= 100 ? Math.round(val) : val.toFixed(1)} ${units[i]}`;
}

export interface FpkDownloadsProps {
  /** 设置页专属：上传按钮槽（渲染在列表段头部右侧，刷新钮之前）。 */
  uploadSlot?: React.ReactNode;
  /** 设置页专属：后台下载任务块（渲染在头部与列表之间，与原布局同位）。 */
  tasksBlock?: React.ReactNode;
  /** 自增即刷新列表（设置页上传完成/保存设置/切换下载目录后）。 */
  refreshSignal?: number;
  /** 列表变化（加载/刷新/删除/安装后重载）回调当前条数——供 Dock 计数。 */
  onCountChange?: (n: number) => void;
  /** 0.6.325 C6：无持久化记录时的初值——「下载」弹窗场景传 true（展开）；
      设置页保持默认折叠（行为零变化）。有持久化值时一律以持久化为准。 */
  defaultExpanded?: boolean;
  /** 0.6.325 C13：「下载」弹窗场景传 true——列表区为**定高**（h-60，内部滚动）：
      加载中/空列表/满列表三种状态占用同一高度，消除弹窗打开瞬间
      「只有头部(89px) → 满列表(334px)」的一次性高度突跳（实测 +240px）。
      设置页不传 = 原 max-h-60 自适应行为，逐字节零变化。 */
  stableHeight?: boolean;
  /** 0.6.325 C13：stableHeight 定高区内的空态内容（列表确实为空且加载完成时
      渲染；加载中显示 spinner）。宿主弹窗传入，设置页不传。 */
  emptyHint?: React.ReactNode;
  /** 安装成功回调（触发主列表目录重算）。 */
  onCatalogChanged?: () => void;
}

/**
 * 已下载 FPK 列表段（0.6.325 C4 从 SettingsPage 整段抽出）。
 *
 * 行=文件名（+display_name 双行）/size·mtime/已安装徽章/安装钮
 * （SSE 进度 + 必填字段无默认值时弹 WizardDialog）/删除钮（busy 态）。
 * 头部=「已下载的 FPK（n）」+（上传槽）+ 刷新 + 折叠（有条目才显示）。
 *
 * 两处宿主：设置页「FPK 下载目录」卡内（uploadSlot/tasksBlock 保持原布局）
 * 与 Dock「下载」页（页自有大标题，组件头部即刷新/折叠行）。
 * 折叠态本地持久化（localStorage fpk-list-collapsed，沿用原 key）。
 */
const FpkDownloads: React.FC<FpkDownloadsProps> = ({
  uploadSlot,
  tasksBlock,
  refreshSignal,
  onCountChange,
  onCatalogChanged,
  defaultExpanded = false,
  stableHeight = false,
  emptyHint,
}) => {
  const [fpkFiles, setFpkFiles] = useState<FpkDownloadFile[]>([]);
  const [fpkLoading, setFpkLoading] = useState(false);
  // 0.6.325 C13：是否已完成过至少一次取数——定高占位区据此区分
  // 「还没加载完（spinner）」与「确实 0 条（emptyHint）」，避免开弹窗瞬间
  // 先闪现「暂无已下载的 FPK」再变列表的二次闪。
  const [fpkLoadedOnce, setFpkLoadedOnce] = useState(false);
  const [fpkRemoving, setFpkRemoving] = useState<string | null>(null);
  // 0.6.325 C10：行内删除两段式确认（第一下进红色确认态，3s 内再点才真删）。
  // 同一时刻只允许一行处于确认态；交互对齐设置页「强制清空」按钮。
  const [confirmDel, setConfirmDel] = useState<string | null>(null);
  const confirmDelTimer = useRef<number | null>(null);
  // 直接安装某个已下载 FPK（SSE 进度）
  const [fpkInstalling, setFpkInstalling] = useState<string | null>(null);
  const [fpkInstallMsg, setFpkInstallMsg] = useState<string>('');
  // 已下载 FPK 的安装向导（必填字段无默认值时弹 WizardDialog 收集参数）
  const [fpkWizardName, setFpkWizardName] = useState<string | null>(null);
  const [fpkWizardDef, setFpkWizardDef] = useState<AppWizard | null>(null);
  // 已下载列表折叠（本地持久化）
  const [fpkListCollapsed, setFpkListCollapsed] = useState<boolean>(() => {
    try {
      // 无持久化记录时取 defaultExpanded 的补数（弹窗默认展开/设置页默认折叠）；
      // 用户手动展开/折叠后按保存值
      const v = localStorage.getItem('fpk-list-collapsed');
      return v === null ? !defaultExpanded : v === '1';
    } catch { return !defaultExpanded; }
  });
  const toggleFpkList = () => {
    setFpkListCollapsed((v) => {
      try { localStorage.setItem('fpk-list-collapsed', v ? '0' : '1'); } catch { /* ignore */ }
      return !v;
    });
  };
  // 宿主回调经 ref 透传（宿主内联箭头每次渲染都是新身份——直接进
  // useCallback deps 会让 loadFpkFiles 重建 → 挂载 effect 反复拉取）。
  const onCountChangeRef = useRef(onCountChange);
  const onCatalogChangedRef = useRef(onCatalogChanged);
  useEffect(() => { onCountChangeRef.current = onCountChange; onCatalogChangedRef.current = onCatalogChanged; });
  /** 清确认态 + 定时器（删除成功 / 列表重载 / 组件卸载 / 超时 / 切行时调用）。 */
  const clearConfirmDel = useCallback(() => {
    if (confirmDelTimer.current !== null) {
      window.clearTimeout(confirmDelTimer.current);
      confirmDelTimer.current = null;
    }
    setConfirmDel(null);
  }, []);
  // 组件卸载清定时器（避免卸载后 setState 残留）
  useEffect(() => () => {
    if (confirmDelTimer.current !== null) window.clearTimeout(confirmDelTimer.current);
  }, []);
  const loadFpkFiles = useCallback(async () => {
    setFpkLoading(true);
    try {
      const res = await fetchFpkDownloads();
      const files = res.files || [];
      setFpkFiles(files);
      // 列表重载（刷新/安装后重载/宿主信号）→ 清行内删除确认态，避免残留红钮
      clearConfirmDel();
      onCountChangeRef.current?.(files.length);
    } catch { /* 目录不存在等场景：显示空列表 */ }
    finally { setFpkLoading(false); setFpkLoadedOnce(true); }
  }, [clearConfirmDel]);

  useEffect(() => {
    loadFpkFiles();
  }, [loadFpkFiles]);
  // 宿主侧信号自增（上传完成/保存设置/切换目录）→ 静默刷新
  const firstSignalRef = useRef(refreshSignal);
  useEffect(() => {
    if (refreshSignal === undefined || refreshSignal === firstSignalRef.current) return;
    firstSignalRef.current = refreshSignal;
    loadFpkFiles();
  }, [refreshSignal, loadFpkFiles]);

  const handleFpkRemove = async (name: string) => {
    if (fpkRemoving) return;
    clearConfirmDel();
    setFpkRemoving(name);
    try {
      await deleteFpkDownload(name);
      setFpkFiles((prev) => {
        const next = prev.filter((f) => f.name !== name);
        onCountChange?.(next.length);
        return next;
      });
      toast.success(`已删除 ${name}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '删除失败');
    } finally {
      setFpkRemoving(null);
    }
  };
  /**
   * 0.6.325 C10（用户定稿）：行内删除两段式确认。
   * 第一下 = 只进入红色确认态并起 3s 定时器（**不执行删除**）；
   * 3s 内再点同一行 = 清确认态后真删；点别的行 = 确认态转移（全局只一行）；
   * 3s 超时自动解除。
   */
  const handleFpkRemoveClick = (name: string) => {
    if (confirmDel !== name) {
      if (confirmDelTimer.current !== null) window.clearTimeout(confirmDelTimer.current);
      setConfirmDel(name);
      confirmDelTimer.current = window.setTimeout(() => {
        confirmDelTimer.current = null;
        setConfirmDel(null);
      }, 3000);
      return;
    }
    clearConfirmDel();
    void handleFpkRemove(name);
  };
  // 手动刷新已下载列表（带 toast 反馈，避免"点了没反应"的观感）
  const handleFpkRefresh = async () => {
    if (fpkLoading) return;
    setFpkLoading(true);
    try {
      const res = await fetchFpkDownloads();
      const files = res.files || [];
      setFpkFiles(files);
      clearConfirmDel();
      onCountChange?.(files.length);
      toast.success(`已刷新：${files.length} 个 FPK`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '刷新失败');
    } finally {
      setFpkLoading(false);
    }
  };
  // 直接安装已下载的 FPK（走 SSE 进度；文件保留在缓存中）
  const runFpkInstall = (name: string, wizard?: WizardParam[]) => {
    if (fpkInstalling) return;
    setFpkInstalling(name);
    setFpkInstallMsg('准备安装...');
    installFpkDownload(name, (ev) => {
      if (ev.step === 'error' || ev.error) return;
      if (ev.message) setFpkInstallMsg(ev.message);
    }, wizard).promise
      .then(() => {
        toast.success(`已安装 ${name}`);
        onCatalogChangedRef.current?.();
        loadFpkFiles();
      })
      .catch((e: unknown) => {
        toast.error(e instanceof Error ? e.message : '安装失败');
      })
      .finally(() => {
        setFpkInstalling(null);
        setFpkInstallMsg('');
      });
  };

  const handleFpkInstall = (name: string) => {
    // 先探测安装向导（与商店同一模式）：必填无默认值的字段必须弹向导
    // 收集用户输入，否则 daemon 侧会拒装。
    if (fpkInstalling) return;
    setFpkInstalling(name);
    setFpkInstallMsg('加载安装向导...');
    fetchFpkDownloadWizard(name)
      .then((wz) => {
        if (wz.has_wizard && (wz.content?.length ?? 0) > 0) {
          setFpkWizardDef(wz);
          setFpkWizardName(name);
          setFpkInstalling(null);
          setFpkInstallMsg('');
          return;
        }
        runFpkInstall(name);
      })
      .catch(() => {
        // 探测失败不阻塞安装 → 直接装，由服务端 AutoFill 报明确错误
        runFpkInstall(name);
      });
  };

  return (
    <>
      {/* 已下载的 FPK 小节（刷新/展开按钮在此，不再占卡片标题行）。
          0.6.325 C8（用户令）：刷新/展开（+设置页的上传）不再被 justify-between
          推到卡片最外侧右缘，改为紧贴「已下载的 FPK」标题右侧（内侧），
          三钮内距仍 12px 防误触。 */}
      <div className="flex items-center gap-3 pt-1">
        <span className="shrink-0 text-xs font-medium text-muted-foreground">
          已下载的 FPK{fpkFiles.length > 0 ? `（${fpkFiles.length}）` : ''}
        </span>
        {/* 322：上传/刷新/折叠三钮间距再调宽（321R8 gap-1.5=6px 用户仍反馈易误触）→ gap-3=12px */}
        <div className="flex items-center gap-3">
          {uploadSlot}
          <Button
            variant="ghost"
            size="icon"
            className="h-6 w-6"
            onClick={handleFpkRefresh}
            disabled={fpkLoading}
            title="刷新已下载 FPK 列表"
            aria-label="刷新已下载列表"
          >
            <RefreshCw className={`h-3.5 w-3.5 ${fpkLoading ? 'animate-spin text-primary' : ''}`} />
          </Button>
          {fpkFiles.length > 0 && (
            <Button
              variant="ghost"
              size="icon"
              className="h-6 w-6"
              onClick={toggleFpkList}
              title={fpkListCollapsed ? '展开已下载列表' : '折叠已下载列表'}
              aria-label={fpkListCollapsed ? '展开已下载列表' : '折叠已下载列表'}
            >
              <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", fpkListCollapsed && "-rotate-90")} />
            </Button>
          )}
        </div>
      </div>
      {tasksBlock}
      {/* 0.6.325 C13：定高占位区（仅宿主弹窗 stableHeight）——列表尚未就绪时
          先占用与满列表完全相同的高度（h-60），使弹窗尺寸在数据到达前后恒定。
          折叠态持久化为折叠时按折叠条同高占位（防折叠用户开弹窗时反向跳变）。 */}
      {stableHeight && fpkFiles.length === 0 && (
        fpkListCollapsed ? (
          // 折叠态持久化为折叠：占位与真实折叠条同形同高（且可点=真按钮），
          // 保证「加载中 → 加载完成」全程高度不变（含 0 条时展开看空态）
          <button
            type="button"
            onClick={toggleFpkList}
            className="w-full rounded-lg border border-border/40 px-3 py-2 text-left text-[11px] text-muted-foreground hover:text-foreground"
          >
            已下载 {fpkLoadedOnce ? 0 : '…'} 个 FPK · 点击展开
          </button>
        ) : (
          <div className="h-60 rounded-lg border border-border/40 flex items-center justify-center px-4 text-center">
            {fpkLoading || !fpkLoadedOnce
              ? <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
              : (emptyHint ?? <span className="text-[11px] text-muted-foreground">暂无已下载的 FPK</span>)}
          </div>
        )
      )}
      {fpkFiles.length > 0 && (fpkListCollapsed ? (
        <button
          type="button"
          onClick={toggleFpkList}
          className="w-full rounded-lg border border-border/40 px-3 py-2 text-left text-[11px] text-muted-foreground hover:text-foreground"
        >
          已下载 {fpkFiles.length} 个 FPK · 点击展开
        </button>
      ) : (
        <div className={cn(
          'overflow-y-auto rounded-lg border border-border/40 divide-y divide-border/40',
          // C13：弹窗（stableHeight）用定高 h-60 与加载占位区同高；设置页保持 max-h-60
          stableHeight ? 'h-60' : 'max-h-60',
        )}>
          {fpkFiles.map((f) => {
            // 0.6.332 F4：副行=display_name → appname → 「解析中…」（都不适用则不渲染）；
            // 解析完成（含「包损坏/无 manifest」的负结果）后后端 parsing 转 false。
            const sub = fpkSubLine(f);
            return (
            <div key={f.name} className="flex items-center gap-2.5 px-3 py-2.5">
              <div className="min-w-0 flex-1">
                <div className="truncate text-xs font-medium" title={f.name}>
                  {f.name}
                </div>
                {sub && (
                  <div
                    className={cn(
                      'truncate text-xs',
                      sub.pending ? 'text-muted-foreground italic' : 'text-foreground/80',
                    )}
                    title={sub.text}
                  >
                    {sub.text}
                  </div>
                )}
                <div className="text-[11px] text-muted-foreground">
                  {fpkInstalling === f.name && fpkInstallMsg
                    ? <span className="text-primary">{fpkInstallMsg}</span>
                    : `${formatBytes(f.size)} · ${f.mod_at ? new Date(f.mod_at).toLocaleString() : ''}`
                  }
                </div>
              </div>
              {fpkInstalling === f.name ? (
                <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-primary" />
              ) : f.installed ? (
                <span
                  className="shrink-0 rounded-full bg-muted/80 border border-white/10 px-2 h-6 inline-flex items-center text-[11px] font-medium text-muted-foreground"
                  title="该应用当前已安装"
                >
                  已安装
                </span>
              ) : (
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-6 shrink-0 px-2 text-xs text-primary hover:bg-primary/10"
                  onClick={() => handleFpkInstall(f.name)}
                  disabled={!!fpkInstalling}
                  title="直接安装该 FPK（不重新下载）"
                >
                  安装
                </Button>
              )}
              <Button
                variant="ghost"
                size="sm"
                // 尺寸恒为 h-6 w-6（C8 定稿：行内钮不得挤出卡片）；
                // 确认态只换配色 + 图标（border 走 border-box，外框尺寸不变）
                className={cn(
                  'h-6 w-6 shrink-0 p-0',
                  confirmDel === f.name
                    ? 'border border-red-500/40 bg-red-500/10 text-red-600 hover:bg-red-500/15 hover:text-red-600'
                    : 'text-muted-foreground hover:text-red-500',
                )}
                onClick={() => handleFpkRemoveClick(f.name)}
                disabled={fpkRemoving === f.name || !!fpkInstalling}
                title={confirmDel === f.name ? '再点一次确认删除' : '删除该 FPK 缓存'}
                aria-label={`删除 ${f.name}`}
              >
                {fpkRemoving === f.name
                  ? <Loader2 className="h-3 w-3 animate-spin" />
                  : confirmDel === f.name
                    ? <Check className="h-3 w-3" />
                    : <Trash2 className="h-3 w-3" />}
              </Button>
            </div>
            );
          })}
        </div>
      ))}
      {/* 已下载 FPK 的安装向导（必填字段无默认值时弹出；
          确认后带参数安装（与应用源 WizardDialog 同一组件） */}
      {fpkWizardName && fpkWizardDef && (
        <WizardDialog
          appDisplayName={fpkWizardDef.appname || fpkWizardName}
          wizard={fpkWizardDef}
          loading={false}
          onCancel={() => {
            setFpkWizardName(null);
            setFpkWizardDef(null);
          }}
          onConfirm={(params) => {
            const name = fpkWizardName;
            setFpkWizardName(null);
            setFpkWizardDef(null);
            runFpkInstall(name, params);
          }}
        />
      )}
    </>
  );
};

export default FpkDownloads;
