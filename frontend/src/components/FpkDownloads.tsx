import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Loader2, RefreshCw, Trash2, Check, ChevronDown, Upload } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from './ui/button';
import WizardDialog from './WizardDialog';
import { cn } from '../lib/utils';
import {
  fetchFpkDownloads,
  deleteFpkDownload,
  batchDeleteFpkDownloads,
  installFpkDownload,
  uploadFpkDownload,
  fetchFpkDownloadWizard,
  type FpkDownloadFile,
  type AppWizard,
  type WizardParam,
} from '../api/client';

/** 0.6.391：列表主行=应用名（display_name → appname → 文件名），
    副行=安装包文件名。此前主行固定文件名、副行才显示 display_name，且
    display_name 与文件名主干相同（如 Moo / moo.fpk）时被当成「重复」隐藏，
    用户看到的就是「只剩安装包名」。 */
function fpkTitleLine(f: FpkDownloadFile): { text: string; pending?: boolean } | null {
  const usable = (v?: string): string => {
    const t = (v || '').trim();
    // 空串、未展开的 ${...} 占位、与完整文件名相同 → 视为无可用名字
    if (!t || t.startsWith('${') || t.toLowerCase() === f.name.toLowerCase()) return '';
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

/** 0.6.397（用户复测第 3 项）：安装向导标题也用应用名（display_name → appname），
    与列表主行同口径；此前取 fpkWizardDef.appname，缺失时回退成安装包文件名
    （如「安装 moo_0.6.396_x86」），而列表主行显示的是「Moo」。 */
function fpkAppLabel(f: FpkDownloadFile): string {
  const t = fpkTitleLine(f);
  if (t && !t.pending) return t.text;
  return f.name;
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
  /** 0.6.531：是否在列表段头部内置「上传 FPK」按钮（设置页与首页下载弹窗共用）。 */
  enableUpload?: boolean;
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
 * 行=应用名（display_name/appname；缺失时回退文件名）+包文件名/size·mtime/已安装徽章/安装钮
 * （SSE 进度 + 必填字段无默认值时弹 WizardDialog）/删除钮（busy 态）。
 * 头部=「已下载的 FPK（n）」+（上传）+ 刷新 + 批量删除 + 折叠；整行可点开合。
 * 两处宿主：设置页「FPK 下载目录」卡内（enableUpload/tasksBlock）
 * 与首页顶栏「下载」弹窗（enableUpload）共用同一套头部。
 * 折叠态本地持久化（localStorage fpk-list-collapsed，沿用原 key）。
 */
const FpkDownloads: React.FC<FpkDownloadsProps> = ({
  enableUpload,
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
  // 0.6.393（用户令）：批量删除模式（对齐 moo-mate：行前圆圈选中 + 全选/取消全选 +
  // 已选计数 + 取消 + 删除）。入口钮在折叠钮之后；设置页与首页「下载」弹窗
  // 共用本组件，两处自动同时具备。
  const [batchMode, setBatchMode] = useState(false);
  const [batchSel, setBatchSel] = useState<Set<string>>(new Set());
  const [batchBusy, setBatchBusy] = useState(false);
  // 批量删除两段确认（与本组件行内删除同口径：第一下只进确认态，3s 内再点才真删）
  const [batchConfirm, setBatchConfirm] = useState(false);
  const batchConfirmTimer = useRef<number | null>(null);
  // 直接安装某个已下载 FPK（SSE 进度）
  const [fpkInstalling, setFpkInstalling] = useState<string | null>(null);
  const [fpkInstallMsg, setFpkInstallMsg] = useState<string>('');
  // 已下载 FPK 的安装向导（必填字段无默认值时弹 WizardDialog 收集参数）
  const [fpkWizardName, setFpkWizardName] = useState<string | null>(null);
  const [fpkWizardDef, setFpkWizardDef] = useState<AppWizard | null>(null);
  // 0.6.397：向导标题用的应用名（打开时从列表条目取，避免回退成包文件名）
  const [fpkWizardTitle, setFpkWizardTitle] = useState<string | null>(null);
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
  // ── 0.6.393 批量删除 ───────────────────────────────────────────────────
  const clearBatchConfirm = useCallback(() => {
    if (batchConfirmTimer.current !== null) {
      window.clearTimeout(batchConfirmTimer.current);
      batchConfirmTimer.current = null;
    }
    setBatchConfirm(false);
  }, []);
  const exitBatchMode = useCallback(() => {
    setBatchMode(false);
    setBatchSel(new Set());
    clearBatchConfirm();
  }, [clearBatchConfirm]);
  const toggleBatchMode = () => {
    if (batchMode) { exitBatchMode(); return; }
    setBatchMode(true);
    setBatchSel(new Set());
    clearBatchConfirm();
    // 折叠态看不到任何行 → 自动展开并持久化（下次打开保持展开）
    setFpkListCollapsed((v) => {
      if (!v) return v;
      try { localStorage.setItem('fpk-list-collapsed', '0'); } catch { /* ignore */ }
      return false;
    });
  };
  const toggleBatchSelect = (name: string) => {
    if (batchBusy) return;
    setBatchSel((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name); else next.add(name);
      return next;
    });
  };
  const batchAllSelected = fpkFiles.length > 0 && fpkFiles.every((f) => batchSel.has(f.name));
  const toggleBatchSelectAll = () => {
    if (batchBusy) return;
    clearBatchConfirm();
    setBatchSel(batchAllSelected ? new Set() : new Set(fpkFiles.map((f) => f.name)));
  };
  // 列表为空（全删完 / 目录被清空）→ 自动退出批量模式，避免留下空操作条
  useEffect(() => {
    if (batchMode && fpkLoadedOnce && fpkFiles.length === 0) exitBatchMode();
  }, [batchMode, fpkFiles.length, fpkLoadedOnce, exitBatchMode]);
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
    if (batchConfirmTimer.current !== null) window.clearTimeout(batchConfirmTimer.current);
  }, []);
  const loadFpkFiles = useCallback(async () => {
    setFpkLoading(true);
    try {
      const res = await fetchFpkDownloads();
      const files = res.files || [];
      setFpkFiles(files);
      // 列表重载（刷新/安装后重载/宿主信号）→ 清行内删除确认态，避免残留红钮
      clearConfirmDel();
      // 0.6.393：批量选中项按最新列表裁剪（文件被别处删掉/改名后不留下幽灵选中）
      setBatchSel((prev) => {
        if (prev.size === 0) return prev;
        const alive = new Set(files.map((f) => f.name));
        const next = new Set([...prev].filter((n) => alive.has(n)));
        return next.size === prev.size ? prev : next;
      });
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

  // 0.6.531（用户令）：上传入口内置到组件——设置页与首页「下载」弹窗共用，
  // 上传成功即刷新列表（不再依赖宿主各自实现一套 input + handler）。
  const [fpkUploading, setFpkUploading] = useState(false);
  const fpkFileRef = useRef<HTMLInputElement>(null);
  const handleFpkUpload = async (file: File) => {
    if (fpkUploading) return;
    setFpkUploading(true);
    try {
      const r = await uploadFpkDownload(file);
      toast.success(r.replaced ? `已覆盖 ${r.name}` : `已上传 ${r.name}（可在列表安装）`);
      await loadFpkFiles();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '上传失败');
    } finally {
      setFpkUploading(false);
      if (fpkFileRef.current) fpkFileRef.current.value = ''; // 允许重选同名文件
    }
  };

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
  /**
   * 0.6.393（用户令）：批量删除选中的已下载 FPK。
   * 一次请求（POST /api/fpk-downloads/batch-delete）逐项处理；两段确认与行内删除
   * 同口径（第一下只进确认态，3s 内再点才真删）。部分失败时保留失败项选中态，
   * 停在批量模式里方便直接重试；全部成功则退出批量模式。
   */
  const handleBatchDelete = async () => {
    const names = [...batchSel];
    if (names.length === 0 || batchBusy) return;
    if (!batchConfirm) {
      if (batchConfirmTimer.current !== null) window.clearTimeout(batchConfirmTimer.current);
      setBatchConfirm(true);
      batchConfirmTimer.current = window.setTimeout(() => {
        batchConfirmTimer.current = null;
        setBatchConfirm(false);
      }, 3000);
      return;
    }
    clearBatchConfirm();
    setBatchBusy(true);
    try {
      const res = await batchDeleteFpkDownloads(names);
      const failed = res.failed || [];
      const failedNames = new Set(failed.map((f) => f.name));
      if (failed.length === 0) {
        toast.success(`已删除 ${res.deleted} 个 FPK`);
      } else {
        toast.error(`已删除 ${res.deleted} 个，${failed.length} 个失败：${failed.map((f) => f.name).join('、')}`);
      }
      // 以服务端列表为准刷新（同时裁剪选中态：失败项保留、已删项移除）
      await loadFpkFiles();
      setBatchSel((prev) => new Set([...prev].filter((n) => failedNames.has(n))));
      if (failed.length === 0) exitBatchMode();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '批量删除失败');
    } finally {
      setBatchBusy(false);
    }
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

  const handleFpkInstall = (file: FpkDownloadFile) => {
    // 先探测安装向导（与商店同一模式）：必填无默认值的字段必须弹向导
    // 收集用户输入，否则 daemon 侧会拒装。
    const name = file.name;
    if (fpkInstalling) return;
    setFpkInstalling(name);
    setFpkInstallMsg('加载安装向导...');
    fetchFpkDownloadWizard(name)
      .then((wz) => {
        if (wz.has_wizard && (wz.content?.length ?? 0) > 0) {
          setFpkWizardDef(wz);
          setFpkWizardName(name);
          setFpkWizardTitle(fpkAppLabel(file));
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
      {/* 已下载的 FPK 小节。
          0.6.530（用户令）：
          ① 标题行整行可点 = 展开/折叠（对齐「通知规则」的分组折叠：点那一行就开合）；
          ② 按钮顺序固定为 上传 → 刷新 → 批量删除 → 展开/折叠（折叠放最后）；
          ③ 批量删除由纯图标改为「垃圾桶 + 文字」，语义直白（进入后文字变「取消」）；
          ④ 折叠时不再重复渲染「已下载 N 个 FPK · 点击展开」条（标题行已含条数）。
          0.6.531（用户令）：整行（含按钮之间的空白）都可点开合；后段按钮间距
          8px→12px、点击区 24px→28px，避免误触；上传按钮内置（设置页/首页弹窗共用）。 */}
      <div
        className={cn(
          'flex items-center gap-2 pt-1',
          fpkFiles.length > 0 && 'cursor-pointer',
        )}
        onClick={fpkFiles.length > 0 ? toggleFpkList : undefined}
        title={fpkFiles.length > 0 ? (fpkListCollapsed ? '点击展开已下载列表' : '点击折叠已下载列表') : undefined}
      >
        <button
          type="button"
          onClick={(e) => { e.stopPropagation(); if (fpkFiles.length > 0) toggleFpkList(); }}
          className="flex shrink-0 items-center text-left"
          aria-expanded={fpkFiles.length > 0 ? !fpkListCollapsed : undefined}
        >
          <span className="truncate text-xs font-medium text-muted-foreground">
            已下载的 FPK{fpkFiles.length > 0 ? `（${fpkFiles.length}）` : ''}
          </span>
        </button>
        <div className="flex shrink-0 items-center gap-3 sm:gap-4">
          {enableUpload && (
            <>
              <input
                ref={fpkFileRef}
                type="file"
                accept=".fpk"
                className="hidden"
                onChange={(e) => { const f = e.target.files?.[0]; if (f) void handleFpkUpload(f); }}
              />
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7"
                onClick={(e) => { e.stopPropagation(); fpkFileRef.current?.click(); }}
                disabled={fpkUploading}
                title="上传 FPK 安装包（手机/电脑本地文件）"
                aria-label="上传 FPK 安装包"
              >
                {fpkUploading
                  ? <Loader2 className="h-3.5 w-3.5 animate-spin text-primary" />
                  : <Upload className="h-3.5 w-3.5" />}
              </Button>
            </>
          )}
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7"
            onClick={(e) => { e.stopPropagation(); void handleFpkRefresh(); }}
            disabled={fpkLoading}
            title="刷新已下载 FPK 列表"
            aria-label="刷新已下载列表"
          >
            <RefreshCw className={`h-3.5 w-3.5 ${fpkLoading ? 'animate-spin text-primary' : ''}`} />
          </Button>
          {/* 0.6.530：批量删除入口——垃圾桶 + 文字（桌面显示文字，<sm 只留图标防挤）；
              进入后行前出现选择圆圈，操作条提供全选/取消全选/取消/删除。 */}
          {fpkFiles.length > 0 && (
            <Button
              variant="ghost"
              size="sm"
              className={cn(
                'h-7 gap-1 rounded-md px-1.5 text-[11px] font-medium text-muted-foreground hover:text-foreground sm:px-2',
                batchMode && 'bg-red-500/10 text-red-600 hover:bg-red-500/15 hover:text-red-600',
              )}
              onClick={(e) => { e.stopPropagation(); toggleBatchMode(); }}
              disabled={batchBusy}
              aria-pressed={batchMode}
              title={batchMode ? '退出批量删除' : '批量删除已下载的 FPK'}
              aria-label={batchMode ? '退出批量删除' : '批量删除已下载的 FPK'}
            >
              {batchBusy
                ? <Loader2 className="h-3.5 w-3.5 animate-spin" />
                : <Trash2 className="h-3.5 w-3.5" />}
              <span className="hidden sm:inline">{batchMode ? '取消' : '批量删除'}</span>
            </Button>
          )}
          {fpkFiles.length > 0 && (
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7"
              onClick={(e) => { e.stopPropagation(); if (fpkFiles.length > 0) toggleFpkList(); }}
              title={fpkListCollapsed ? '展开已下载列表' : '折叠已下载列表'}
              aria-label={fpkListCollapsed ? '展开已下载列表' : '折叠已下载列表'}
            >
              <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", fpkListCollapsed && "-rotate-90")} />
            </Button>
          )}
        </div>
        {/* 0.6.530：按钮仍紧贴标题（0.6.325 C8 定稿），行尾空白也是点击区；
            0.6.531 起整行父容器承接点击，此占位只负责把按钮顶到标题内侧。 */}
        <div className="h-6 min-w-0 flex-1" />
      </div>
      {tasksBlock}
      {/* 0.6.393（用户令）：批量操作条（对齐 moo-mate：全选 / 已选 N 个 / 取消 / 删除） */}
      {batchMode && fpkFiles.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 rounded-lg border border-dashed border-primary/40 bg-primary/5 px-3 py-2">
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2 text-xs"
            onClick={toggleBatchSelectAll}
            disabled={batchBusy}
          >
            {batchAllSelected ? '取消全选' : '全选'}
          </Button>
          <span className="text-[11px] text-muted-foreground">已选 {batchSel.size} 个</span>
          <span className="flex-1" />
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2 text-xs"
            onClick={exitBatchMode}
            disabled={batchBusy}
          >
            取消
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className={cn(
              'h-6 px-2 text-xs text-red-600 hover:bg-red-500/10 hover:text-red-600',
              batchConfirm && 'bg-red-500/10',
            )}
            onClick={handleBatchDelete}
            disabled={batchSel.size === 0 || batchBusy}
            title={batchConfirm ? '再点一次确认删除' : `删除选中的 ${batchSel.size} 个 FPK 缓存`}
          >
            {batchBusy
              ? <Loader2 className="h-3.5 w-3.5 animate-spin" />
              : batchConfirm ? `确认删除（${batchSel.size}）` : '删除'}
          </Button>
        </div>
      )}
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
      {/* 0.6.530（用户令）：折叠态不再渲染「已下载 N 个 FPK · 点击展开」重复条
          （标题行已显示条数，且整行可点展开）；列表只在展开时存在。 */}
      {fpkFiles.length > 0 && !fpkListCollapsed && (
        <div className={cn(
          'overflow-y-auto rounded-lg border border-border/40 divide-y divide-border/40',
          // C13：弹窗（stableHeight）用定高 h-60 与加载占位区同高；设置页保持 max-h-60
          stableHeight ? 'h-60' : 'max-h-60',
        )}>
          {fpkFiles.map((f) => {
            // 0.6.391：主行=应用名，副行=包文件名（详见 fpkTitleLine 注释）。
            const title = fpkTitleLine(f);
            const primary = title && !title.pending ? title.text : f.name;
            const showFile = primary.toLowerCase() !== f.name.toLowerCase();
            // 0.6.393：版本号移到应用名后；第三行 = 安装包大小 · 时间。
            const ver = (f.version || '').trim();
            const verLabel = ver ? (/^v/i.test(ver) ? ver : 'v' + ver) : '';
            return (
            <div
              key={f.name}
              // 0.6.393：批量模式下整行=选择开关（行前圆圈为可视指示）
              role={batchMode ? 'checkbox' : undefined}
              aria-checked={batchMode ? batchSel.has(f.name) : undefined}
              tabIndex={batchMode ? 0 : undefined}
              onClick={batchMode ? () => toggleBatchSelect(f.name) : undefined}
              onKeyDown={batchMode ? (e) => {
                if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); toggleBatchSelect(f.name); }
              } : undefined}
              className={cn(
                'flex items-center gap-2.5 px-3 py-2.5',
                batchMode && 'cursor-pointer select-none hover:bg-muted/40 focus-visible:bg-muted/40 focus-visible:outline-none',
                batchMode && batchSel.has(f.name) && 'bg-primary/5',
              )}
            >
              {batchMode && (
                <span
                  aria-hidden
                  className={cn(
                    'flex h-5 w-5 shrink-0 items-center justify-center rounded-full border-[1.5px] transition-colors',
                    batchSel.has(f.name) ? 'border-primary bg-primary text-primary-foreground' : 'border-border',
                  )}
                >
                  {batchSel.has(f.name) && <Check className="h-3 w-3" />}
                </span>
              )}
              <div className="min-w-0 flex-1">
                {/* 0.6.393（用户令）：版本号跟到应用名后面（原来在第三行大小前，长名字会折行） */}
                <div className="flex min-w-0 items-baseline gap-1.5">
                  <span className="truncate text-xs font-medium" title={primary}>
                    {primary}
                  </span>
                  {verLabel && (
                    <span className="shrink-0 text-[11px] text-muted-foreground/80">{verLabel}</span>
                  )}
                </div>
                {title?.pending ? (
                  <div className="truncate text-xs text-muted-foreground italic">解析中…</div>
                ) : showFile ? (
                  <div className="truncate text-[11px] text-muted-foreground" title={f.name}>
                    {f.name}
                  </div>
                ) : null}
                <div className="text-[11px] text-muted-foreground">
                  {fpkInstalling === f.name && fpkInstallMsg
                    ? <span className="text-primary">{fpkInstallMsg}</span>
                    : [formatBytes(f.size), f.mod_at ? new Date(f.mod_at).toLocaleString() : ''].filter(Boolean).join(' · ')
                  }
                </div>
              </div>
              {!batchMode && (<>
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
                  onClick={() => handleFpkInstall(f)}
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
              </>)}
            </div>
            );
          })}
        </div>
      )}
      {/* 已下载 FPK 的安装向导（必填字段无默认值时弹出；
          确认后带参数安装（与应用源 WizardDialog 同一组件） */}
      {fpkWizardName && fpkWizardDef && (
        <WizardDialog
          appDisplayName={fpkWizardTitle || fpkWizardDef.appname || fpkWizardName}
          wizard={fpkWizardDef}
          loading={false}
          onCancel={() => {
            setFpkWizardName(null);
            setFpkWizardDef(null);
            setFpkWizardTitle(null);
          }}
          onConfirm={(params) => {
            const name = fpkWizardName;
            setFpkWizardName(null);
            setFpkWizardDef(null);
            setFpkWizardTitle(null);
            runFpkInstall(name, params);
          }}
        />
      )}
    </>
  );
};

export default FpkDownloads;
