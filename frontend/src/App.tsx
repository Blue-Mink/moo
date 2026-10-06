import React, { useState, useEffect, useCallback, useRef, useMemo, Suspense } from 'react';
import { useDebouncedValue, useKeyboardDock, useIsDesktop } from './lib/hooks';
import { LayoutGrid, CheckCircle2, RefreshCw, Settings, ChevronsLeft, ChevronsRight, Search, X, Film, ArrowDownToLine, Globe, Loader2, CircleX, CircleCheck, WifiOff, Compass, Brain, Clapperboard, Network, ChevronsUpDown, Check, ChevronDown, Gamepad2, Camera, Zap, Code2, Home, Database, Cpu, Star, BellOff, Grid2x2, Sparkles } from 'lucide-react';
import { Button } from './components/ui/button';
import { Input } from './components/ui/input';
import { Badge } from './components/ui/badge';
import AppList from './components/AppList';
import ProgressOverlay from './components/ProgressOverlay';
import BackgroundTasksIndicator from './components/BackgroundTasksIndicator';
import AppCard from './components/AppCard';
import { WebAppDetailCardSkeleton } from './components/webCardSkeleton';
import AppIcon from './components/AppIcon';
import FeaturedShowcase from './components/FeaturedShowcase';
// 重型对话框懒加载：首屏 bundle 只保留列表/导航核心，设置页(1089行)/详情
// (975行，含 SourceManager)/向导/面板安装/失败报告按需下载（LAN 内瞬时），
// 首屏 JS 解析编译时间减半（对齐参照实现的轻量首屏）。
const AppDetailDialog = React.lazy(() => import('./components/AppDetailDialog'));
const SettingsPage = React.lazy(() => import('./components/SettingsPage'));
const WizardDialog = React.lazy(() => import('./components/WizardDialog'));
import ThemeToggle from './components/ThemeToggle';
import MobileDock from './components/MobileDock';
import AppRowList from './components/AppRowList';
import { MinimalIconGridM, AuroraGridM } from './components/ViewModeGrids';
import { fetchApps, triggerCheck, installApp, updateApp, uninstallApp, fetchStatus, fetchStoreUpdate, triggerStoreUpdate, reloadApps, ignoreUpdate, unignoreUpdate, fetchRecommended, fetchWizard, controlApp, appWebUrl, isPublicAccessContext, urlReachableInPublicContext, isFnOSAppWebview, fetchPanelDetail, sourceLabel, effectiveMaintainer, fetchFavorites, toggleFavorite, fetchSettings, fetchSearchSourceKeys } from './api/client';
import { connectFnOSBridge, openAppInShell } from './lib/fnos-bridge';
import { alphaInitial } from './lib/pinyin';
import { isLinkLike, sourceKey } from './lib/sourceKey';
import { installPresenceTracking } from './lib/pagePresence';
import type { AppInfo, AppOperation, SSECallback, RecommendedApp, AppWizard, WizardParam, PanelDetailResponse, PanelInstallParams } from './api/client';
import { toast } from "sonner"
import { Toaster } from "@/components/ui/sonner"
// 重型对话框懒加载（见上方说明）：按需分包，首屏只加载列表/导航核心。
const PanelInstallDialog = React.lazy(() => import('./components/PanelInstallDialog'));
const SourceWizardDialog = React.lazy(() => import('./components/SourceWizardDialog'));
const ReportFailureDialog = React.lazy(() => import('./components/ReportFailureDialog').then(m => ({ default: m.ReportFailureDialog })));
// 0.6.284 网页版详情卡片（收藏区桌面端使用；markdown 依赖随懒包，不进主包）
const WebAppDetailCard = React.lazy(() => import('./components/WebAppDetailCard'));
import { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import { stableApps } from "@/lib/stableApps"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"

// 分类体系以飞牛官方应用中心为准（实用效率/开发工具/生活服务/影音娱乐/
// 游戏/AI/备份同步/摄影摄像/驱动/下载），fndepot 生态补充官方未覆盖的
// 媒体自动化/网络工具/浏览器，共 14 类（见后端 classify.go 注释）。
const CATEGORIES = [
  { key: 'ai', label: 'AI', icon: Brain },
  { key: 'media', label: '影音娱乐', icon: Film },
  { key: 'automation', label: '媒体自动化', icon: Clapperboard },
  { key: 'game', label: '游戏', icon: Gamepad2 },
  { key: 'photo', label: '摄影摄像', icon: Camera },
  { key: 'efficiency', label: '实用效率', icon: Zap },
  { key: 'devtools', label: '开发工具', icon: Code2 },
  { key: 'lifestyle', label: '生活服务', icon: Home },
  { key: 'backup', label: '备份同步', icon: Database },
  { key: 'download', label: '下载', icon: ArrowDownToLine },
  { key: 'network', label: '网络工具', icon: Network },
  { key: 'browser', label: '浏览器', icon: Globe },
  { key: 'driver', label: '驱动', icon: Cpu },
] as const;

type CategoryKey = typeof CATEGORIES[number]['key'];

type SortKey = 'default' | 'downloads' | 'name' | 'alpha' | 'updated';

const SORT_OPTIONS: { value: SortKey; label: string }[] = [
  { value: 'default', label: '随机' },
  { value: 'alpha', label: 'A-Z' },
  { value: 'downloads', label: '下载量' },
  { value: 'name', label: '名称' },
  { value: 'updated', label: '最近更新' },
];

// 0.6.282：解析深链 hash（0.6.281 的 #app=<key>，扩展为 #app=<key>&settings[=<tab>]）。
// 参数顺序不敏感；app 名经 encodeURIComponent 后取值内不会出现裸 & / =。
const DEEPLINK_TAB_KEYS = ['system', 'accel', 'source', 'backup', 'notify', 'log', 'about'];
const parseHashParams = (): { app: string | null; settings: string | null } => {
  const h = window.location.hash.replace(/^#/, '');
  const appM = h.match(/(?:^|&)app=([^&]+)/);
  const app = appM ? decodeURIComponent(appM[1]) : null;
  const settingsM = h.match(/(?:^|&)settings(?:=([^&]+))?/);
  let settings: string | null = null;
  if (settingsM) {
    const t = settingsM[1] ? decodeURIComponent(settingsM[1]) : 'system';
    settings = DEEPLINK_TAB_KEYS.includes(t) ? t : 'system';
  }
  return { app, settings };
};

const App: React.FC = () => {
  const [apps, setApps] = useState<AppInfo[]>([]);
  // false on fnOS builds whose update path destroys the app (see backend
  // platform.UpgradeCapability). Surfaced so the UI does not offer an update
  // button that can only ever fail.
  const [upgradeAllowed, setUpgradeAllowed] = useState(true);
  const [loadStatus, setLoadStatus] = useState<'loading' | 'loaded' | 'retrying' | 'failed'>('loading');
  const [loadMessages, setLoadMessages] = useState<{text: string; status: 'info' | 'success' | 'error'}[]>([]);
  const [checking, setChecking] = useState<boolean>(false);
  const [lastCheck, setLastCheck] = useState<string>('');
  // 0.6.228：侧边栏头部实时时钟（用户要求「下面的时间换成实时时间，格式同原来一样」）
  const [nowText, setNowText] = useState<string>(() => new Date().toLocaleString());
  useEffect(() => {
    const t = window.setInterval(() => setNowText(new Date().toLocaleString()), 1000);
    return () => window.clearInterval(t);
  }, []);
  const [reportTarget, setReportTarget] = useState<{ app: string; step: string; error: string } | null>(null);

  const [appOperations, setAppOperations] = useState<Map<string, AppOperation>>(new Map());
  const [selfUpdateActive, setSelfUpdateActive] = useState(false);
  const [selfUpdateState, setSelfUpdateState] = useState<{message: string; progress: number; speed?: number; downloaded?: number; total?: number} | null>(null);
  // selfUpdateActiveRef tracks whether the self-update OVERLAY should be shown.
  // It can be set optimistically (handleStoreUpdate sets it BEFORE the SSE opens).
  // selfUpdateRestartSeenRef tracks whether the backend has actually emitted the
  // 'self_update' SSE event - meaning the server is committed to killing itself
  // and a subsequent connection drop is EXPECTED, not a failure.
  // Catch blocks must read selfUpdateRestartSeenRef (not selfUpdateActiveRef)
  // when deciding whether to suppress the error toast, otherwise pre-SSE failures
  // (HTTP 409, network errors) get silently swallowed.
  const selfUpdateActiveRef = useRef(false);
  const selfUpdateRestartSeenRef = useRef(false);
  const pollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const reloadHandleRef = useRef<{ cancel: () => void } | null>(null);
  const appOperationsRef = useRef<Map<string, AppOperation>>(new Map());
  // Keep ref synced with state so guards in event handlers see current value
  // without forcing them to depend on appOperations (which would re-create
  // them on every progress update and trigger child re-renders).
  appOperationsRef.current = appOperations;

  const [settingsVisible, setSettingsVisible] = useState(false);
  // 0.6.282：设置页深链——关闭时重置 tab 记忆（下次打开默认「系统设置」，同旧行为），
  // hash 由下方同步 effect 随状态收敛
  const handleSettingsOpenChange = (open: boolean) => {
    setSettingsVisible(open);
    if (!open) setSettingsTab(null);
  };
  // Dock 主导航顺序（系统设置「Dock 栏排序」）：启动拉一次 + 设置页改完刷新
  const [dockOrder, setDockOrder] = useState<string[] | null>(null);
  // 0.6.284：桌面/移动真分支（收藏区两套卡片只挂载一套，见 useIsDesktop）
  const isDesktop = useIsDesktop();

  // 0.6.293 网页端三种预览模式（用户定稿）：
  //   minimal=极简（图标+名称+安装/未安装）| aurora=极光（底部极光渐变推荐卡）
  //   | standard=标准（现 WebAppDetailCard）。localStorage 持久化。
  // 0.6.301：移动端同享三模式（发现页搜索框后的入口；其他 dock 页不显示按钮）。
  const [viewMode, setViewMode] = useState<'minimal' | 'aurora' | 'standard'>(() => {
    const v = localStorage.getItem('moo.web_view_mode');
    return v === 'minimal' || v === 'aurora' ? v : 'standard';
  });
  useEffect(() => { localStorage.setItem('moo.web_view_mode', viewMode); }, [viewMode]);

  // 0.6.301：三模式切换组（桌面顶栏 / 移动端发现页共用同一份 JSX）
  const viewModeSwitchGroup = (
    <div className="flex items-center gap-0.5 rounded-full bg-muted/60 p-1" role="group" aria-label="预览模式">
      {([
        ['minimal', Grid2x2, '极简模式'],
        ['aurora', Sparkles, '极光模式'],
        ['standard', LayoutGrid, '标准模式'],
      ] as const).map(([mode, Icon, label]) => (
        <button
          key={mode}
          onClick={() => setViewMode(mode)}
          title={label}
          aria-label={label}
          aria-pressed={viewMode === mode}
          className={cn(
            "h-7 w-7 rounded-full flex items-center justify-center transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40",
            viewMode === mode ? "bg-background text-primary shadow-sm" : "text-muted-foreground hover:text-foreground"
          )}
        >
          <Icon className="h-[15px] w-[15px]" />
        </button>
      ))}
    </div>
  );

  // 0.6.290：0.6.284 的滚动条「活动才显示」(.ui-active) 已撤——隐形时抓不住拖不到顶；
  // 样式改为常驻细竖条（见 index.css），此类切换逻辑随之删除

  const [storeHasUpdate, setStoreHasUpdate] = useState(false);
  const [activeFilter, setActiveFilter] = useState<'all' | 'installed' | 'update_available' | 'recommended'>('all');
  const [recommendedApps, setRecommendedApps] = useState<RecommendedApp[]>([]);
  // 应用收藏（目录 key 数组，顺序 = 收藏先后；发现页「收藏列表」按此顺序渲染）。
  const [favoriteKeys, setFavoriteKeys] = useState<string[]>([]);
  const [searchInput, setSearchInput] = useState('');
  const [searchExpanded, setSearchExpanded] = useState(false);
  const searchInputRef = useRef<HTMLInputElement>(null);
  // 收起 = 立即（无过渡，1.14.24 用户要求恢复原来的生硬但干脆的行为）。
  // 收起触发源：Enter / 点外部(blur) / × 清除 / 收起按钮 / 键盘收起（无论有无输入，
  // 有输入时搜索词保留、列表保持过滤）。
  // 注意：**不做**输入停顿自动收起 —— 中文输入停顿思考时会被误收（1.14.23 修复）。
  const collapseSearch = () => {
    setSearchExpanded(false);
    searchInputRef.current?.blur();
  };
  const expandSearch = () => {
    setSearchExpanded(true);
  };
  // 搜索防抖：按键即时更新输入框（廉价），筛选/列表只依赖 150ms 后的
  // searchQuery —— WebView 里打字不再每个字符都重渲染数百张卡片。
  const searchQuery = useDebouncedValue(searchInput, 150);
  // 0.6.198 源链接搜索：「归一化 key → 源名集合」map（启动拉一次源列表构建）。
  // 搜索框贴源链接（仓库根 / raw 前缀 / JSON 索引 / fndepot v1v2 / 镜像）
  // → 归一化后命中已配置源 → 该词条 = 源过滤条件，搜出该源全部应用。
  const [sourceKeyMap, setSourceKeyMap] = useState<Map<string, string[]> | null>(null);
  useEffect(() => {
    // 公开端点（归一化 key → 源名，后端已处理 fnos-store/fnos-apps 别名）
    fetchSearchSourceKeys().then(entries => {
      const m = new Map<string, string[]>();
      for (const e of entries || []) {
        if (e.key && e.names?.length) m.set(e.key, e.names);
      }
      setSourceKeyMap(m);
    }).catch(() => { /* 静默：源链接搜索不可用，普通搜索不受影响 */ });
  }, []);
  // 0.6.198 未命中提示：链接词条对不上任何已配置源 → toast 一次（同词条
  // 不重复弹；词条移出搜索框后重置，可再弹）。
  const linkMissToastRef = useRef<Set<string>>(new Set());
  useEffect(() => {
    if (!sourceKeyMap) return;
    const terms = searchQuery.trim().toLowerCase().split(/\s+/).filter(Boolean);
    const linkTerms = new Set<string>();
    for (const q of terms) {
      if (!isLinkLike(q)) continue;
      linkTerms.add(q);
      const hit = (k => k && sourceKeyMap.get(k))(sourceKey(q));
      if (!hit && !linkMissToastRef.current.has(q)) {
        linkMissToastRef.current.add(q);
        toast('未找到匹配的已配置源（可先在「源管理」里添加）');
      }
    }
    for (const k of [...linkMissToastRef.current]) {
      if (!linkTerms.has(k)) linkMissToastRef.current.delete(k);
    }
  }, [searchQuery, sourceKeyMap]);
  // 底部 dock 键盘处理（钉在屏幕底边方案，对齐 iOS App Store 观感）：
  //   offsetPx = 键盘高度 → dock 用 bottom:-offset 下移，键盘弹出时 dock
  //   停在物理屏幕底边被键盘盖住（不跟键盘上移、不重挂载、无回弹位移），
  //   收起时已就位于视口底边；hidden 仅 pan 型壳（整个 WebView 被平移、
  //   视口不变）兜底用 —— 聚焦输入框期间整体隐藏。
  // 兼容：浏览器型（visualViewport 差值）、飞牛 app 等 adjustResize
  // WebView（innerHeight 收缩）、iframe 裁剪型。
  const { offsetPx: dockOffsetPx, open: kbOpen, hidden: dockHidden } = useKeyboardDock();
  // 键盘收起自动收搜索框：展开态下键盘从弹出到收起（open 开→关沿）即收起 ——
  // 飞牛 app 点 IME 完成/收起按钮不触发 blur，需键盘收起信号兜底。
  // 无论有无输入：空=直接收起；有输入=搜索词保留、列表保持过滤。
  // 0.6.210：改用 open 标志（clip 型视口下 offsetPx 恒 0，旧判据会失效）。
  const prevKbOpenRef = useRef(false);
  useEffect(() => {
    if (prevKbOpenRef.current && !kbOpen && searchExpanded) {
      collapseSearch();
    }
    prevKbOpenRef.current = kbOpen;
  }, [kbOpen, searchExpanded]);

  // Dock 主导航顺序：启动拉一次；设置页每次关闭后重拉（「Dock 栏排序」改完即生效）
  useEffect(() => {
    fetchSettings()
      .then((s) => { if (s.dock_order) setDockOrder(s.dock_order); })
      .catch(() => { /* 静默：默认顺序兜底 */ });
  }, [settingsVisible]);
  // 源 / 开发者 / 发布者 多选筛选：点徽章 = 把词条跳进搜索框（与原行为一致），
  // 多个徽章词条在搜索框内叠加（空格分隔、AND 组合）；再点同一徽章移除该词条；
  // 搜索框后的 × 一次性清空全部词条。无独立筛选 chip 行。
  const applyTextFilter = (term: string) => {
    const t = (term || '').trim();
    if (!t) return;
    if (activeFilter === 'recommended') switchFilter('all');
    setSearchInput(prev => {
      const terms = prev.split(/\s+/).filter(Boolean);
      if (terms.includes(t)) return terms.filter(x => x !== t).join(' ');
      return terms.length ? `${terms.join(' ')} ${t}` : t;
    });
  };
  // 搜索框内当前生效的徽章/搜索词条（徽章选中态 + 计数联动用）
  const activeSearchTerms = useMemo(
    () => searchInput.trim().split(/\s+/).filter(Boolean),
    [searchInput]
  );
  const [activeCategory, setActiveCategory] = useState<CategoryKey | null>(null);
  const [pendingUninstallApp, setPendingUninstallApp] = useState<AppInfo | null>(null);
  // Apps can declare an install-time form (fnos/wizard/install). When one
  // exists we ask first, then install with the answers — matching what the
  // native App Center does. Previously the store silently accepted defaults,
  // so an app needing a token or password came up misconfigured.
  const [wizardApp, setWizardApp] = useState<AppInfo | null>(null);
  const [wizardDef, setWizardDef] = useState<AppWizard | null>(null);
  const [wizardLoading, setWizardLoading] = useState(false);
  // 官方通道：用户在体积/依赖弹窗里确认的参数，向导弹窗确认后随安装一起提交
  // （FPK 通道恒为 null）。
  const [panelWizardParams, setPanelWizardParams] = useState<PanelInstallParams | null>(null);
  // 0.6.269：源声明的安装向导（moo.json wizard.fields）——先收集、后走 FPK 探测流；
  // 收集值暂存 ref，最终在 runInstall 调用点与 FPK 向导参数合并（?wizard= 契约）。
  const [sourceWizardApp, setSourceWizardApp] = useState<AppInfo | null>(null);
  const pendingSourceParamsRef = useRef<WizardParam[]>([]);
  // 官方应用中心（fnos-official）安装：详情+依赖弹窗
  const [panelApp, setPanelApp] = useState<AppInfo | null>(null);
  const [panelDetail, setPanelDetail] = useState<PanelDetailResponse | null>(null);
  const [panelLoading, setPanelLoading] = useState(false);
  const [detailApp, setDetailApp] = useState<AppInfo | null>(null);
  // 0.6.282：设置页深链——设置对话框 + 当前选中 tab 一并写入 URL（#settings[=<tab>]），
  // 「关于」tab 等页面的外链返回后也能回到设置页。tab 本体状态在 SettingsPage 内，
  // 这里只记最近 tab；关闭时重置（下次打开仍默认「系统设置」，行为同旧版）。
  const [settingsTab, setSettingsTab] = useState<string | null>(null);
  // 0.6.282：深链 hash 同步——详情页 + 设置页状态原地写入 URL（#app=<key>[&settings[=<tab>]]），
  // 不加历史条目。关键竞态：挂载首帧两个状态必为 false，若此时同步会把 URL 里
  // 待恢复的 hash 清掉（恢复 effect 等列表加载后才跑）→ 首帧跳过，只跳过这一次。
  const hashSyncReadyRef = useRef(false);
  useEffect(() => {
    if (!hashSyncReadyRef.current) { hashSyncReadyRef.current = true; return; }
    const base = window.location.pathname + window.location.search;
    const parts: string[] = [];
    const key = detailApp?.key || detailApp?.appname || '';
    if (key) {
      parts.push(`app=${encodeURIComponent(key)}`);
    } else if (pendingAppRef.current) {
      // 0.6.282：恢复尚未判定（深链待解析）时保留 app 参数——以挂载时捕获的 ref 为准，
      // 不读当前 hash（避免「自己刚要清掉的参数又被自己保留」的自锁）；
      // 判定完成后 ref 被恢复 effect 清空，本分支自然失效
      parts.push(`app=${encodeURIComponent(pendingAppRef.current)}`);
    }
    if (settingsVisible) parts.push(settingsTab ? `settings=${settingsTab}` : 'settings');
    window.history.replaceState(null, '', parts.length ? `${base}#${parts.join('&')}` : base);
  }, [detailApp, settingsVisible, settingsTab]);
  // 0.6.282：恢复门——挂载时 URL 带深链参数（外链返回/F5 刷新）就先显示纯净加载屏
  // （不渲染列表/顶栏），恢复判定完成才放行，消除「首页列表闪一下再弹详情」的跳闪。
  // 放行：无 app 参数 / 目录就绪完成解析 / 加载失败（交还正常 UI，重试后可再恢复）/ 8s 兜底。
  const [restoreGate, setRestoreGate] = useState(() => {
    const p = parseHashParams();
    return p.app !== null || p.settings !== null;
  });
  // 0.6.282：待解析的深链 app 参数（挂载时从 URL 取一次）。恢复判定完成前同步 effect
  // 以它为准保留 hash 参数；loaded 后必被下方恢复 effect 消费（开了/失效清了），不会残留
  const pendingAppRef = useRef<string | null>(null);
  // 挂载即：捕获待解析 app 参数 + 与目录拉取**并行**预载详情/设置懒加载 chunk——
  // 否则目录就绪、门放行后 chunk 才开始下载，对话框仍会晚于列表出现（列表闪一下）
  useEffect(() => {
    const p = parseHashParams();
    if (p.app) pendingAppRef.current = p.app;
    if (p.app) import('./components/AppDetailDialog').catch(() => {});
    if (p.settings) import('./components/SettingsPage').catch(() => {});
  }, []);
  // settings 参数不依赖目录：与判定同批应用（failed/兜底/正常三路都覆盖）——
  // 不单独提前开设置对话框，避免其中间态重渲染冲掉待解析的 app 参数
  const applySettingsParam = () => {
    const s = parseHashParams().settings;
    if (s && !settingsVisible) {
      setSettingsVisible(true);
      setSettingsTab(s);
    }
  };
  // 0.6.282：期望恢复的对话框数量（判定时刻写入）= 详情（命中时）+ 设置（有参数时）。
  // 门只在 DOM 对话框数达到它时才放——列表与对话框同帧出现，结构性消除跳闪
  // （懒加载 chunk 的 eval / Radix 挂载可能滞后于 detailApp 置位，实测 276ms）。
  // 初值从 hash 取：只有设置参数时挂载首帧就要开始等对话框，不能等判定 effect
  // 的状态更新（同提交里 effect 闭包还是旧值 0，会提前放门）
  const [expectedDialogs, setExpectedDialogs] = useState(() => (parseHashParams().settings ? 1 : 0));
  // app 参数是否仍待解析（目录未就绪）——为 true 期间门不放行
  const [appPending, setAppPending] = useState(() => parseHashParams().app !== null);
  // 判定：目录就绪后解析 app 参数；failed/兜底交还正常 UI（hash 不动，
  // 目录稍后就绪时下方恢复 effect 仍能补开详情）
  useEffect(() => {
    if (!restoreGate) return;
    const p = parseHashParams();
    if (loadStatus === 'failed') {
      setAppPending(false);
      setExpectedDialogs(p.settings ? 1 : 0);
      applySettingsParam();
      return;
    }
    if (p.app !== null && loadStatus !== 'loaded') return;
    let need = p.settings ? 1 : 0;
    if (p.app) {
      const found = apps.find(a => (a.key || a.appname) === p.app || a.appname === p.app);
      if (found) { setDetailApp(found); need += 1; }
    }
    setAppPending(false);
    setExpectedDialogs(need);
    applySettingsParam();
  }, [restoreGate, loadStatus, apps]);
  // 放门前确认：DOM 对话框数达到期望即放门；3s 兜底防对话框异常未挂载时恒挂加载屏
  useEffect(() => {
    if (!restoreGate || appPending) return;
    const have = () => document.querySelectorAll('[role="dialog"]').length;
    if (have() >= expectedDialogs) { setRestoreGate(false); return; }
    let raf = 0;
    const t0 = performance.now();
    const tick = () => {
      if (have() >= expectedDialogs || performance.now() - t0 > 3000) setRestoreGate(false);
      else raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [restoreGate, appPending, expectedDialogs]);
  // 8s 兜底：目录长超时卡住时不再等 app 参数（hash 不动——下方恢复 effect 在目录
  // 就绪后仍会补开详情），设置参数照常恢复
  useEffect(() => {
    if (!restoreGate) return;
    const t = window.setTimeout(() => {
      const p = parseHashParams();
      setAppPending(false);
      setExpectedDialogs(p.settings ? 1 : 0);
      applySettingsParam();
    }, 8000);
    return () => window.clearTimeout(t);
  }, [restoreGate]);
  // 0.6.281：加载/刷新后若 URL 带深链参数且未开，列表就绪后自动恢复
  // （等 loadStatus=loaded 再匹配，避免目录未拉到时误判「无此应用」；
  //  0.6.282：loaded 即目录定论——空目录也算，失效 app 参数在此清理）
  // 0.6.282：兼管「门因失败/兜底放行后、重试成功」的补恢复场景
  useEffect(() => {
    if (loadStatus !== 'loaded') return;
    const { app, settings } = parseHashParams();
    if (detailApp) {
      // 详情已开（深链所开或用户点开的）：待解析标记已消费
      pendingAppRef.current = null;
      return;
    }
    if (!app) {
      pendingAppRef.current = null;
      return;
    }
    const found = apps.find(a => (a.key || a.appname) === app || a.appname === app);
    if (found) {
      setDetailApp(found);
      pendingAppRef.current = null;
    } else {
      // 深链目标不在目录（应用已删/源未同步）：清掉失效 app 参数（保留 settings 参数）
      pendingAppRef.current = null;
      const base = window.location.pathname + window.location.search;
      window.history.replaceState(null, '', settings ? `${base}#settings=${settings}` : base);
    }
  }, [apps, loadStatus, detailApp]);
  // App Store large title：内容滚动后标题收缩、头部转毛玻璃
  const [mainScrolled, setMainScrolled] = useState(false);
  const [sortBy, setSortBy] = useState<SortKey>('default');
  // 「全部」视图随机展示：每次数据加载后重新洗牌（不再按 fnos-apps 等来源分组置顶），
  // 同一次会话内顺序稳定（切 tab 返回不重洗，记住的滚动位置仍落在同一应用上）。
  const [shuffleTick, setShuffleTick] = useState(0);
  const [sortMenuOpen, setSortMenuOpen] = useState(false);
  // 发现页「收藏列表」区：默认折叠，点「展开」显示收藏应用网格
  const [favExpanded, setFavExpanded] = useState(false);
  // 发现页「忽略更新」区（0.6.181）：收藏列表下方的折叠按钮，默认折叠，
  // 展开后列出被忽略更新的应用（行内可快速取消忽略）
  const [ignoreExpanded, setIgnoreExpanded] = useState(false);
  // 排序菜单位置：pill 行是 overflow-x-auto 滚动容器（会同时裁剪 y 轴），
  // 菜单必须 fixed 定位逃出裁剪，坐标在打开时按触发钮实测位置计算。
  const [sortMenuPos, setSortMenuPos] = useState<{ left: number; top: number } | null>(null);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() =>
    localStorage.getItem('sidebar-collapsed') === 'true'
  );

  const toggleSidebar = useCallback(() => {
    setSidebarCollapsed(prev => {
      const next = !prev;
      localStorage.setItem('sidebar-collapsed', String(next));
      return next;
    });
  }, []);

  useEffect(() => {
    loadApps();
    fetchStoreUpdate().then(info => setStoreHasUpdate(info.has_update)).catch(() => {});
    fetchRecommended().then(data => setRecommendedApps(data.apps)).catch(() => {});
    fetchFavorites().then(setFavoriteKeys).catch(() => {});
    // 预握手 fnOS Web UI 壳窗口（postmate 子端协议）：内嵌时"应用设置"
    // 才能秒开「设置→应用」面板；独立打开时静默失败无副作用。
    connectFnOSBridge().catch(() => {});
    // large title 收缩：滚动容器是 window（实测 main 的 overflow-y-auto
    // 不生效，整页随窗口滚动、sticky 头部吸顶），监听 window scroll
    const onScroll = () => setMainScrolled(window.scrollY > 24);
    window.addEventListener('scroll', onScroll, { passive: true });
    onScroll();
    return () => {
      window.removeEventListener('scroll', onScroll);
      // Cleanup on unmount: cancel pending poll timer and any in-flight reload SSE
      if (pollTimerRef.current) {
        clearTimeout(pollTimerRef.current);
        pollTimerRef.current = null;
      }
      reloadHandleRef.current?.cancel();
      reloadHandleRef.current = null;
    };
  }, []);

  // 底部 dock / 侧栏切换 tab：记住每个 tab 的滚动位置，切回时恢复
  //（实际滚动容器是 window —— 实测 main 的 overflow-y-auto 不生效，整页随窗口滚动）
  const scrollPosRef = useRef<Record<string, number>>({});
  const activeFilterRef = useRef(activeFilter);
  // 持续记录当前 tab 的滚动位置。不能在切换后的 effect 里读 window.scrollY：
  // React 提交新列表后文档高度突变，浏览器会先把 scrollY 钳制到新列表的
  // 最大值，effect 再读就已经是被钳制过的错误值（深滚动切短列表必丢位置）。
  useEffect(() => {
    const onScroll = () => { scrollPosRef.current[activeFilterRef.current] = window.scrollY; };
    window.addEventListener('scroll', onScroll, { passive: true });
    onScroll();
    return () => window.removeEventListener('scroll', onScroll);
  }, []);
  // tab 切换（dock / 侧栏 / 返回按钮 / 搜索跳转统一走这里）：
  // 点击时 DOM 还没换、浏览器也还没钳制 scrollY，此刻保存的才是旧 tab 的真实
  // 位置；并同步把 activeFilterRef 指向新 tab，使 DOM 切换期间浏览器因文档
  // 高度突变发出的 scroll 事件记到新 tab 名下，不会覆盖旧 tab 的位置。
  const switchFilter = useCallback((next: 'all' | 'installed' | 'update_available' | 'recommended') => {
    const prev = activeFilterRef.current;
    scrollPosRef.current[prev] = window.scrollY;
    activeFilterRef.current = next;
    setActiveFilter(next);
    setActiveCategory(null);
  }, []);
  // 切回某 tab 时恢复其滚动位置：等新列表提交布局；目标超出当前文档高度时
  // 逐帧重试（列表还在渲染），最多 10 帧后钳制到当前最大值兜底。
  useEffect(() => {
    const target = scrollPosRef.current[activeFilter] ?? 0;
    let tries = 0;
    let raf = 0;
    const attempt = () => {
      const max = Math.max(0, document.documentElement.scrollHeight - window.innerHeight);
      if (target <= max) { window.scrollTo(0, target); return; }
      if (tries++ < 10) { raf = requestAnimationFrame(attempt); }
      else { window.scrollTo(0, max); }
    };
    requestAnimationFrame(() => requestAnimationFrame(attempt));
    return () => cancelAnimationFrame(raf);
  }, [activeFilter]);

  // 通知栏残留修复（「退出 Moo 再进来不应看到旧通知」）：
  // 双信号在场跟踪（visibilitychange + IntersectionObserver 识别
  // iframe 被隐藏），返回时清空屏上通知、离开期间吞掉新通知。
  // 实现见 lib/pagePresence.ts。
  useEffect(() => installPresenceTracking(), []);

  const setAppOp = useCallback((appname: string, op: AppOperation | null) => {
    setAppOperations(prev => {
      const next = new Map(prev);
      if (op === null) {
        next.delete(appname);
      } else {
        next.set(appname, op);
      }
      return next;
    });
  }, []);

  const pollForRestart = useCallback(() => {
    // Dedup: createSSEHandler and handleStoreUpdate may both call this.
    if (pollTimerRef.current) return;

    let retries = 0;
    const poll = async () => {
      pollTimerRef.current = null;
      try {
        await fetchStatus();
        window.location.reload();
      } catch {
        retries++;
        if (retries > 30) {
          setSelfUpdateState({ message: '重启超时，请手动刷新页面', progress: 100 });
          return;
        }
        setSelfUpdateState({ message: '正在重启...', progress: 100 });
        pollTimerRef.current = setTimeout(poll, 2000);
      }
    };
    pollTimerRef.current = setTimeout(poll, 2000);
  }, []);

  const translateStep = (step?: string) => {
      switch(step) {
          case 'downloading': return '正在下载...';
          case 'pulling': return '正在拉取镜像...';
          case 'installing': return '正在安装...';
          case 'verifying': return '正在验证...';
          case 'starting': return '正在启动...';
          case 'uninstalling': return '正在卸载...';
          default: return '处理中...';
      }
  };

  const loadApps = async (autoReload = true) => {
    try {
      const data = await fetchApps();
      // 稳定合并：内容未变的条目复用旧对象引用（见 lib/stableApps.ts）——
      // memo 化行跳过重渲染，无变化的全量刷新主线程开销从数百 ms 降到 ~ms。
      setApps(prev => stableApps(prev, data.apps).list);
      setShuffleTick(t => t + 1); // 新数据 → 重新洗牌（随机展示）
      setUpgradeAllowed(data.upgrade_allowed !== false);
      setLastCheck(data.last_check);
      if (data.apps.length > 0) {
        setLoadStatus('loaded');
      } else if (!data.last_check && autoReload) {
        triggerReload();
      } else {
        setLoadStatus('loaded');
      }
    } catch (error) {
      console.error('Failed to load apps:', error);
      triggerReload();
    }
  };

  // 官方应用描述后台回填轮询：面板 app/list 不带 desc，后端首启后异步
  // 逐条 app/detail 回填（并发 8）。列表首屏官方卡无简介时，指数退避
  // 4s/8s/16s/32s（封顶 32s）静默重拉 /api/apps，直到描述出现（整轮
  // 封顶 20 次）。稳定合并：无变化的刷新复用旧引用、不触发 setApps，
  // 行级 memo 全跳过——此前固定 4s 拉全量 1.2MB 并整表重渲染，4G 手机
  // 上每次占主线程 200-600ms，点击落在窗口内就「卡一下」。
  const descPollsRef = useRef(0);
  useEffect(() => {
    if (loadStatus !== 'loaded') return;
    const missing = apps.some(a => a.source === 'fnos-official' && !a.description);
    if (!missing || descPollsRef.current >= 20) return;
    const delay = Math.min(4000 * 2 ** descPollsRef.current, 32000);
    const t = window.setTimeout(async () => {
      descPollsRef.current += 1;
      try {
        const data = await fetchApps();
        setApps(prev => {
          const m = stableApps(prev, data.apps);
          return m.changed ? m.list : prev;
        });
      } catch { /* 单次失败静默，下一轮再试 */ }
    }, delay);
    return () => window.clearTimeout(t);
  }, [apps, loadStatus]);

  // 启动/停用已安装应用（与 fnOS 应用中心同步）
  const [controlling, setControlling] = useState<string | null>(null);
  // loadApps 每次渲染都是新函数；handleControl 要稳定（供 memo 化列表使用），
  // 故经 ref 间接调用，避免把 loadApps 身份带进依赖数组。
  const loadAppsRef = useRef<(autoReload?: boolean) => Promise<void>>(() => Promise.resolve());
  loadAppsRef.current = loadApps;

  const handleControl = useCallback(async (app: AppInfo, action: 'start' | 'stop') => {
    setControlling(app.appname);
    try {
      await controlApp(app.appname, action);
      toast.success(action === 'start' ? `已启动「${app.display_name}」` : `已停用「${app.display_name}」`);
      await loadAppsRef.current();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : `${action === 'start' ? '启动' : '停用'}失败`);
    } finally {
      setControlling(null);
    }
  }, []);

  // 打开已安装应用的 Web UI（与 fnOS 应用中心"打开"按钮同机制）：
  // 内嵌 fnOS Web UI（microApp 桥可用）时走壳窗口 openApp(serviceName)
  // 在壳内任务标签页打开 —— 原生应用中心同款行为；
  // 独立打开 :38011（桥不可用）时降级为新浏览器标签打开应用 URL。
  const handleOpenApp = useCallback(async (app: AppInfo) => {
    // App Store 风格单一"打开"按钮：应用未运行时先自动启动，轮询到 running 再打开。
    // 注意：应用停止时 daemon 不下发 web 字段（web_port/web_service_name 均为空），
    // 启动成功后必须用重新拉取的最新记录解析打开目标。
    let target = app;
    if (app.status && app.status !== 'running' && app.status !== 'nostart' && (app.start_stop ?? true)) {
      try {
        toast.info(`「${app.display_name}」尚未运行，正在启动后打开…`);
        await controlApp(app.appname, 'start');
        const t0 = Date.now();
        let fresh: AppInfo | undefined;
        let running = false;
        while (Date.now() - t0 < 30000) {
          await new Promise(r => setTimeout(r, 2000));
          try {
            const res = await fetchApps();
            fresh = (res.apps || []).find(a => a.appname === app.appname);
            if (fresh) {
              if (fresh.status === 'running') { running = true; break; }
              if (fresh.status === 'stopped') break; // 启动后又回落到 stopped，视为失败
            }
          } catch { /* 单次拉取失败继续轮询 */ }
        }
        await loadAppsRef.current();
        if (!running) {
          toast.error(`「${app.display_name}」未能及时运行，请稍后重试`);
          return;
        }
        if (fresh) target = fresh;
      } catch (e) {
        toast.error(e instanceof Error ? e.message : '启动失败，无法打开');
        return;
      }
    }
    if (target.web_service_name) {
      const ok = await openAppInShell(target.web_service_name);
      if (ok) return;
    }
    const url = appWebUrl(target);
    if (!url) {
      toast.error(`「${target.display_name}」没有可打开的 Web 界面`);
      return;
    }
    // 公网上下文（FN Connect / 公网 IP / 自建域名，飞牛 app 公网入口即此类）：
    // 桥不可用（独立标签页访问 /app/moo/）时，端口型 URL（公网入口不转发
    // 应用端口）/ 指向局域网地址的 web_url 必然不可达 → 不开死标签页。
    // 内网（私网 IP / .local）不触发此分支。
    //
    // 两种环境的处理（实测差异，勿混）：
    // - 浏览器：有真实 Web 会话 → 新标签开飞牛主页，在主页点该应用即可
    //   （平台壳按 fnDomain 子域名打开，与主页行为一致）。
    // - 飞牛 App WebView：无有效 Web 会话（主页 SPA 会自己路由到 /login，
    //   12:33 日志实锤），且 app 为顶层直载 /app/moo/、无 iframe 桥 →
    //   任何「带用户去主页」的动作都是登录陷阱。只给明确指引：回 app
    //   主界面点该应用图标（原生路径）。不做 window.open、不做页面跳转。
    if (isPublicAccessContext() && !urlReachableInPublicContext(target, url)) {
      if (isFnOSAppWebview()) {
        toast.warning(
          `「${target.display_name}」当前访问方式下无法在 Moo 内直接打开`,
          {
            description:
              '请返回飞牛 App 主界面，点击该应用的图标打开（App 内走的是平台原生通道）。',
            duration: 8000,
          },
        );
      } else {
        toast.warning(
          `公网访问下「${target.display_name}」需从飞牛主页打开`,
          {
            description:
              '已为你打开主页，在那里点击该应用即可。若仍打不开，请在 系统设置→远程访问 中确认该应用配置了外链访问。',
            duration: 6000,
          },
        );
        window.open(`${window.location.origin}/`, '_blank', 'noopener');
      }
      return;
    }
    window.open(url, '_blank', 'noopener');
  }, []);

  const triggerReload = () => {
    // Cancel any in-flight reload SSE before starting a new one.
    reloadHandleRef.current?.cancel();

    setLoadStatus('retrying');
    setLoadMessages([]);

    const handle = reloadApps((data) => {
      if (data.step === 'trying') {
        setLoadMessages(prev => [...prev, { text: data.message || '', status: 'info' }]);
      } else if (data.step === 'failed') {
        setLoadMessages(prev => {
          const updated = [...prev];
          if (updated.length > 0) {
            updated[updated.length - 1] = { text: data.message || '', status: 'error' };
          }
          return updated;
        });
      } else if (data.step === 'success') {
        setLoadMessages(prev => {
          const updated = [...prev];
          if (updated.length > 0) {
            updated[updated.length - 1] = { text: data.message || '', status: 'success' };
          }
          return updated;
        });
      } else if (data.step === 'done') {
        setLoadMessages(prev => [...prev, { text: data.message || '', status: 'success' }]);
        loadApps(false);
      } else if (data.step === 'error') {
        setLoadMessages(prev => [...prev, { text: data.message || '', status: 'error' }]);
        setLoadStatus('failed');
      }
    });
    reloadHandleRef.current = handle;

    handle.promise.catch(() => {
      setLoadStatus('failed');
      setLoadMessages(prev => [...prev, { text: '网络连接失败', status: 'error' }]);
    }).finally(() => {
      if (reloadHandleRef.current === handle) {
        reloadHandleRef.current = null;
      }
    });
  };

  const createSSEHandler = useCallback((app: AppInfo, operation: 'install' | 'update' | 'uninstall'): SSECallback => (data) => {
    const appname = app.appname;

    if (data.step === 'self_update') {
      setSelfUpdateActive(true);
      selfUpdateActiveRef.current = true;
      selfUpdateRestartSeenRef.current = true;
      setSelfUpdateState({ message: data.message || '商店正在更新，请稍候...', progress: 100 });
      pollForRestart();
      return;
    }

    if (data.step === 'error') {
      const lastStep = appOperationsRef.current.get(appname)?.step || 'starting';
      toast.error(data.message || '发生未知错误', {
        duration: 8000, // 带「上报」操作的错误通知留足点击时间，其余通知 3 秒自动消失
        action: {
          label: '上报',
          onClick: () => setReportTarget({ app: appname, step: lastStep, error: data.message || '发生未知错误' })
        }
      });
      setAppOp(appname, null);
      loadApps();
      return;
    }

    if (data.step === 'done') {
      setAppOp(appname, null);
      loadApps();

      if (operation === 'uninstall') {
        toast.success(`${app.display_name} 已卸载`);
      }
      // 安装/更新成功不再弹大弹窗（0.6.119 用户定稿「安装成功的这个大弹窗就不要了」）：
      // 顶部通知栏统一提示「XX 安装成功」（BackgroundTasksIndicator 监听任务终态）。
      return;
    }

    setAppOp(appname, {
      step: data.step || 'processing',
      progress: data.progress || 0,
      message: data.message || translateStep(data.step),
      speed: data.speed,
      downloaded: data.downloaded,
      total: data.total,
    });
  }, [setAppOp]);

  const handleCheck = async () => {
    setChecking(true);
    try {
      await triggerCheck();
      await loadApps();
    } catch (error) {
      console.error('Check failed:', error);
      toast.error('检查更新失败');
    } finally {
      setChecking(false);
    }
  };

  const runInstall = useCallback(async (app: AppInfo, wizard?: WizardParam[], panel?: PanelInstallParams) => {
    const appname = app.appname;
    // Guard: prevent double-trigger overwriting an in-flight operation's cancel handle.
    if (appOperationsRef.current.has(appname)) return;

    const handler = createSSEHandler(app, 'install');
    const handle = installApp(appname, handler, wizard, panel);
    setAppOp(appname, {
      step: 'starting',
      progress: 0,
      message: `正在安装 ${app.display_name}...`,
      cancel: handle.cancel,
    });

    try {
      await handle.promise;
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') {
        toast.info('已取消');
        setAppOp(appname, null);
        return;
      }
      // Self-update path: backend kills itself; SSE drop is expected ONLY
      // after we have actually seen the 'self_update' event.
      // pollForRestart() (started by self_update event) handles recovery.
      if (selfUpdateRestartSeenRef.current) {
        return;
      }
      console.error(error);
      toast.error('安装请求失败', {
        action: {
          label: '上报',
          onClick: () => setReportTarget({ app: app.appname, step: 'request', error: error instanceof Error ? error.message : String(error) })
        }
      });
    } finally {
      const hadOperation = appOperationsRef.current.has(appname);
      setAppOperations(prev => {
        if (!prev.has(appname)) return prev;
        const next = new Map(prev);
        next.delete(appname);
        return next;
      });
      // Skip loadApps during self-update: the server is restarting and the
      // poll-then-reload flow will refresh the entire page anyway.
      if (hadOperation && !selfUpdateActiveRef.current) {
        loadApps();
      }
    }
  }, [createSSEHandler, setAppOp]);

  /** 0.6.269：安装主流程（源级向导收集后 / 无源级向导时直达）：
      探测 FPK 自带向导 → 有则弹 WizardDialog（其 onConfirm 合并 pending
      源参数），无则直接 runInstall（带上 pending 源参数）。 */
  const continueInstall = useCallback(async (app: AppInfo) => {
    const pending = pendingSourceParamsRef.current;
    // Probe for an install form first. A lookup failure must never block
    // installing, so anything unexpected falls through to a plain install.
    setWizardApp(app);
    setWizardLoading(true);
    setWizardDef(null);
    setPanelWizardParams(null);
    try {
      const w = await fetchWizard(app.appname);
      if (w.has_wizard && (w.content?.length ?? 0) > 0) {
        // FPK 自带向导：源向导值已暂存 ref，WizardDialog onConfirm 处合并
        setWizardDef(w);
        setWizardLoading(false);
        return;
      }
    } catch {
      // ignore — fall through and install with defaults
    }
    setWizardApp(null);
    setWizardLoading(false);
    pendingSourceParamsRef.current = [];
    void runInstall(app, pending.length ? pending : undefined);
  }, [runInstall]);

  const handleInstall = useCallback(async (app: AppInfo) => {
    if (appOperationsRef.current.has(app.appname)) return;

    // 官方应用中心源：走面板 cloud 通道。先拉详情+依赖（依赖弹窗数据），
    // 失败必须提示而不是静默回退（回退到 FPK 通道会 404/死循环，无意义）。
    if (app.source === 'fnos-official') {
      setPanelApp(app);
      setPanelLoading(true);
      setPanelDetail(null);
      try {
        const d = await fetchPanelDetail(app.appname);
        setPanelDetail(d);
      } catch (err) {
        setPanelApp(null);
        setPanelLoading(false);
        toast.error(err instanceof Error ? err.message : '获取官方应用详情失败');
        return;
      }
      setPanelLoading(false);
      return;
    }

    // 0.6.269：源声明了向导字段（moo.json wizard.fields）→ 先收集再走安装流
    if (app.wizard?.fields?.length) {
      pendingSourceParamsRef.current = [];
      setSourceWizardApp(app);
      return;
    }
    void continueInstall(app);
  }, [continueInstall]);

  const handleUpdate = useCallback(async (app: AppInfo) => {
    const appname = app.appname;
    if (appOperationsRef.current.has(appname)) return;

    const handler = createSSEHandler(app, 'update');
    const handle = updateApp(appname, handler);
    setAppOp(appname, {
      step: 'starting',
      progress: 0,
      message: `正在更新 ${app.display_name}...`,
      cancel: handle.cancel,
    });

    try {
      await handle.promise;
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') {
        toast.info('已取消');
        setAppOp(appname, null);
        return;
      }
      if (selfUpdateRestartSeenRef.current) {
        return;
      }
      console.error(error);
      toast.error('更新请求失败', {
        action: {
          label: '上报',
          onClick: () => setReportTarget({ app: app.appname, step: 'request', error: error instanceof Error ? error.message : String(error) })
        }
      });
    } finally {
      const hadOperation = appOperationsRef.current.has(appname);
      setAppOperations(prev => {
        if (!prev.has(appname)) return prev;
        const next = new Map(prev);
        next.delete(appname);
        return next;
      });
      if (hadOperation && !selfUpdateActiveRef.current) {
        loadApps();
      }
    }
  }, [createSSEHandler, setAppOp]);

  const handleUninstall = useCallback((app: AppInfo) => {
    setPendingUninstallApp(app);
  }, []);

  const confirmUninstall = useCallback(async () => {
    if (!pendingUninstallApp) return;
    const app = pendingUninstallApp;
    setPendingUninstallApp(null);

    const appname = app.appname;
    const handler = createSSEHandler(app, 'uninstall');

    setAppOp(appname, {
      step: 'uninstalling',
      progress: 0,
      message: `正在卸载 ${app.display_name}...`,
    });

    const handle = uninstallApp(appname, handler);

    try {
      await handle.promise;
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') {
        toast.info('已取消');
        setAppOp(appname, null);
        return;
      }
      console.error(error);
      toast.error('卸载请求失败', {
        action: {
          label: '上报',
          onClick: () => setReportTarget({ app: app.appname, step: 'request', error: error instanceof Error ? error.message : String(error) })
        }
      });
    } finally {
      const hadOperation = appOperationsRef.current.has(appname);
      setAppOperations(prev => {
        if (!prev.has(appname)) return prev;
        const next = new Map(prev);
        next.delete(appname);
        return next;
      });
      if (hadOperation) {
        loadApps();
      }
    }
  }, [pendingUninstallApp, createSSEHandler, setAppOp]);

  const handleCancelOp = useCallback((app: AppInfo) => {
    const op = appOperations.get(app.appname);
    if (op?.cancel) {
      op.cancel();
      toast.info('已取消');
      setAppOp(app.appname, null);
      loadApps();
    }
  }, [appOperations, setAppOp]);

  const handleIgnoreUpdate = useCallback(async (app: AppInfo) => {
    try {
      await ignoreUpdate(app.appname);
      await loadApps();
      toast.success(`${app.display_name} 已忽略更新`);
    } catch {
      toast.error('忽略更新失败');
    }
  }, []);

  const handleUnignoreUpdate = useCallback(async (app: AppInfo) => {
    try {
      await unignoreUpdate(app.appname);
      await loadApps();
      toast.success(`${app.display_name} 已取消忽略更新`);
    } catch {
      toast.error('取消忽略更新失败');
    }
  }, []);

  // 收藏：key→Set 派生（O(1) 查询，行/卡 memo 依赖稳定）
  const favoriteSet = useMemo(() => new Set(favoriteKeys), [favoriteKeys]);
  const favoriteKeysRef = useRef(favoriteKeys);
  favoriteKeysRef.current = favoriteKeys;
  // 收藏列表（发现页）：按收藏先后排序；目录中已不存在的 key 静默跳过。
  const favoriteApps = useMemo(() => {
    if (favoriteKeys.length === 0) return [] as AppInfo[];
    const byKey = new Map(apps.map(a => [a.key || a.appname, a]));
    const out: AppInfo[] = [];
    for (const k of favoriteKeys) {
      const a = byKey.get(k);
      if (a) out.push(a);
    }
    return out;
  }, [favoriteKeys, apps]);
  // 忽略更新（发现页，0.6.181）：更新信号被忽略的已装应用（后端
  // update_ignored 标记；has_update 已在目录层置假，更新列表/角标不再出现）。
  const ignoredApps = useMemo(() => apps.filter(a => a.update_ignored), [apps]);

  const handleToggleFavorite = useCallback((app: AppInfo) => {
    const key = app.key || app.appname;
    if (!key) return;
    // 乐观更新（星标即时反馈），完成后以后端权威集合校准，失败回滚。
    const prev = favoriteKeysRef.current;
    const willFavor = !prev.includes(key);
    setFavoriteKeys(willFavor ? [...prev, key] : prev.filter(k => k !== key));
    toggleFavorite(key)
      .then(res => setFavoriteKeys(res.favorites && res.favorites.length > 0 ? res.favorites : []))
      .catch(() => {
        setFavoriteKeys(prev);
        toast.error('收藏操作失败，请重试');
      });
  }, []);

  const handleStoreUpdate = useCallback(async () => {
    setSelfUpdateActive(true);
    selfUpdateActiveRef.current = true;
    setSelfUpdateState({ message: '正在更新商店...', progress: 0 });

    const handle = triggerStoreUpdate((data) => {
      if (data.step === 'self_update') {
        selfUpdateRestartSeenRef.current = true;
        setSelfUpdateState({ message: '商店正在重启...', progress: 100 });
        pollForRestart();
        return;
      }
      if (data.step === 'error') {
        toast.error(data.message || '商店更新失败');
        setSelfUpdateActive(false);
        selfUpdateActiveRef.current = false;
        setSelfUpdateState(null);
        return;
      }
      setSelfUpdateState({
        message: data.message || '正在更新商店...',
        progress: data.progress || 0,
        speed: data.speed,
        downloaded: data.downloaded,
        total: data.total,
      });
    });

    try {
      await handle.promise;
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') return;
      // Only suppress the toast if the backend actually emitted the self_update
      // event - otherwise pre-SSE failures (HTTP 409, network errors) would be
      // silently swallowed and the overlay would be stuck at 0% forever.
      if (selfUpdateRestartSeenRef.current) {
        return;
      }
      console.error(error);
      toast.error('商店更新失败');
      setSelfUpdateActive(false);
      selfUpdateActiveRef.current = false;
      setSelfUpdateState(null);
    }
  }, []);

  // 随机排序权重：Fisher-Yates 洗牌当前应用列表，key → 随机位次
  const appsRef = useRef<AppInfo[]>(apps);
  appsRef.current = apps;
  const shuffledRank = useMemo(() => {
    const arr = appsRef.current.map(a => a.key || a.appname);
    for (let i = arr.length - 1; i > 0; i--) {
      const j = Math.floor(Math.random() * (i + 1));
      [arr[i], arr[j]] = [arr[j], arr[i]];
    }
    const rank = new Map<string, number>();
    arr.forEach((n, idx) => rank.set(n, idx));
    return rank;
    // 故意只依赖 shuffleTick：洗牌时机 = 每次 loadApps 成功（见 setShuffleTick）
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shuffleTick]);

  const filteredApps = useMemo(() => apps.filter(app => {
    if (activeFilter === 'installed' && !app.installed) return false;
    // 0.6.197：已忽略但有被压住更新的应用也列入「有更新」（行上带
    // 「已忽略」徽章标记；纯已最新的忽略 app 不进列表）
    if (activeFilter === 'update_available' && !app.has_update && !app.update_ignored_pending) return false;
    if (activeCategory && app.category !== activeCategory) return false;
    if (searchQuery.trim()) {
      // 多词条 AND：搜索框里的每个空格分隔词条都必须命中（徽章词条叠加多选即走这里）
      const terms = searchQuery.toLowerCase().split(/\s+/).filter(Boolean);
      const name = (app.display_name || '').toLowerCase();
      const appname = (app.appname || '').toLowerCase();
      const desc = (app.description || '').toLowerCase();
      // 源名/开发者走显示名（官方→飞牛应用中心源、内置→fnos-store/conversun），
      // 与徽章点击填入搜索框的词条一致
      const source = sourceLabel(app).toLowerCase();
      const author = effectiveMaintainer(app).toLowerCase();
      const distributor = (app.distributor || '').toLowerCase();
      const hay = [name, appname, desc, source, author, distributor];
      for (const q of terms) {
        // 0.6.198 链接词条 → 归一化匹配已配置源：命中 = 应用 source 在
        // 该源名集合内（搜出该源全部应用）；未命中 = 词条永不可满足。
        if (isLinkLike(q)) {
          const k = sourceKey(q);
          const hit = k ? sourceKeyMap?.get(k) : undefined;
          if (!hit || !app.source || !hit.includes(app.source)) return false;
          continue;
        }
        if (!hay.some(h => h.includes(q))) return false;
      }
    }

    return true;
  }).sort((a, b) => {
    switch (sortBy) {
      case 'downloads':
        return (b.download_count ?? 0) - (a.download_count ?? 0);
      case 'name':
        return (a.display_name || '').localeCompare(b.display_name || '');
      case 'alpha': {
        // 首字母 A-Z：字母优先（1Panel→P）、纯数字名 0-9、中文按拼音首字母
        const ia = alphaInitial(a.display_name || a.appname);
        const ib = alphaInitial(b.display_name || b.appname);
        if (ia !== ib) {
          if (ia === '#') return 1;
          if (ib === '#') return -1;
          return ia.localeCompare(ib);
        }
        return (a.display_name || '').localeCompare(b.display_name || '', 'zh-Hans-CN');
      }
      case 'updated':
        return (b.updated_at || '').localeCompare(a.updated_at || '');
      default:
        // 随机展示：「全部」视图按洗牌顺序（不按来源分组）；其他 tab 保持后端顺序
        if (activeFilter === 'all') {
          const ra = shuffledRank.get(a.key || a.appname) ?? Number.MAX_SAFE_INTEGER;
          const rb = shuffledRank.get(b.key || b.appname) ?? Number.MAX_SAFE_INTEGER;
          return ra - rb;
        }
        return 0;
    }
  }), [apps, activeFilter, activeCategory, searchQuery, sortBy, shuffledRank, sourceKeyMap]);

  const counts = useMemo(() => ({
      all: apps.length,
      installed: apps.filter(a => a.installed).length,
      // 0.6.197：计数含「已忽略但有被压住更新」的应用（与列表一致）
      update_available: apps.filter(a => a.has_update || a.update_ignored_pending).length,
      recommended: recommendedApps.length
  }), [apps, recommendedApps]);

  const categoryCounts = useMemo(() => CATEGORIES.reduce((acc, cat) => {
    acc[cat.key] = apps.filter(a => a.category === cat.key).length;
    return acc;
  }, {} as Record<string, number>), [apps]);

  // 「全部」复合 pill：pill 主体 = 选择全部分类；右侧 ▾ = 排序菜单（折叠在全部里）
  const allCategoryPill = (
    // z-50：菜单打开时固定覆盖层（z-40）挡住页面其余部分，但 pill 本体要
    // 保持在覆盖层之上，用户才能再点 pill/▾ 收起菜单。
    <div className="relative z-50 shrink-0">
      <button
        onClick={() => setActiveCategory(null)}
        className={cn(
          // 0.6.284：「全部」胶囊与其他胶囊统一 Dock 毛玻璃材质
          "relative z-[60] flex items-center gap-0.5 shrink-0 h-8 pl-3.5 pr-1.5 rounded-full border border-white/10 backdrop-blur-xl text-[13px] font-medium whitespace-nowrap",
          activeCategory === null ? "bg-primary/15 text-primary border-primary/40" : "bg-card/55 text-foreground"
        )}
      >
        全部
        <span
          role="button"
          aria-label="排序"
          title="排序"
          onClick={(e) => {
            e.stopPropagation();
            if (sortMenuOpen) {
              setSortMenuOpen(false);
              return;
            }
            // 菜单左缘与「全部」pill 左缘对齐（不用 ▾ 的位置）
            const r = ((e.currentTarget as HTMLElement).parentElement as HTMLElement).getBoundingClientRect();
            const menuW = 160;
            const menuH = 190;
            const left = Math.max(8, Math.min(r.left, window.innerWidth - menuW - 8));
            const top = Math.max(8, Math.min(r.bottom + 4, window.innerHeight - menuH - 8));
            setSortMenuPos({ left, top });
            setSortMenuOpen(true);
          }}
          className={cn("flex items-center justify-center h-6 w-6 rounded-full transition-colors", sortMenuOpen && "bg-black/10")}
        >
          <ChevronsUpDown className="h-3.5 w-3.5" />
        </span>
      </button>
      {sortMenuOpen && sortMenuPos && (
        <>
          <div className="fixed inset-0 z-40" onClick={() => setSortMenuOpen(false)} />
          <div
            className="fixed z-50 w-40 rounded-xl border border-border bg-popover p-1 shadow-lg"
            style={{ left: sortMenuPos.left, top: sortMenuPos.top }}
          >
            <p className="px-2.5 py-1.5 text-[11px] font-medium text-muted-foreground">排序</p>
            {SORT_OPTIONS.map(o => (
              <button
                key={o.value}
                onClick={() => { setSortBy(o.value); setSortMenuOpen(false); }}
                className="flex w-full items-center justify-between gap-2 rounded-lg px-2.5 py-1.5 text-[13px] hover:bg-muted"
              >
                {o.label}
                {sortBy === o.value && <Check className="h-3.5 w-3.5 shrink-0 text-primary" />}
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  );

  // 0.6.282：恢复门——门开启期间主树保持挂载（display:none 离屏；恢复对话框可
  // portal 到 body 先就绪），视觉上只有纯净加载屏（无列表/顶栏/Dock）；门一开
  // 列表与对话框同帧出现，结构性消除跳闪
  const gateSplash = restoreGate ? (
    <div className="fixed inset-0 z-[100] flex items-center justify-center bg-background">
      <img src="./icon-192.png" alt="" className="h-14 w-14 rounded-2xl opacity-80" />
    </div>
  ) : null;

  return (
    <>
      {gateSplash}
      <div className={cn("min-h-dvh bg-background text-foreground flex flex-col md:flex-row", restoreGate && "hidden")}>
      {/* 0.6.306（用户定稿）：页面环境光——极淡静态径向渐变（极光语言 ~1/4 强度），
          让毛玻璃卡/顶栏/胶囊「透」到有色内容才有玻璃质感；纯静态层零滚动成本。
          色值随主题切换见 index.css .app-ambient。fixed 贴底渲染，内容自然盖其上。 */}
      <div aria-hidden className="app-ambient pointer-events-none fixed inset-0" />
      {/* 0.6.230（用户定稿）：「苹果风」PC 布局 —— 左侧栏隐去，菜单模块搬到
          底部毛玻璃 Dock（见页面末尾的 Desktop Dock），品牌与实时时钟移到顶栏左侧。
          这里保留原侧栏结构（已置为 hidden）便于随时回退 / 后续复用。 */}
      <aside className={cn(
        "hidden flex-col bg-card/70 backdrop-blur-xl border-r border-border/50 h-dvh sticky top-0 transition-all duration-300 overflow-hidden shrink-0",
        sidebarCollapsed ? "w-[68px]" : "w-64"
      )}>
        <TooltipProvider delayDuration={0}>
         <div className={cn("border-b border-border shrink-0", sidebarCollapsed ? "p-3 flex items-center justify-center" : "p-6")}>
           {sidebarCollapsed ? (
             <Button variant="ghost" size="icon" className="h-8 w-8" onClick={toggleSidebar}>
               <ChevronsRight className="h-4 w-4" />
             </Button>
           ) : (
             <div className="flex items-start justify-between gap-2">
               <div className="min-w-0">
                 {/* 0.6.228（用户定稿）：侧边栏标题 Moo → Moo is more；
                     下方由「上次检查」改为**实时时钟**（格式沿用 toLocaleString()），
                     上次检查时间移到 title 提示里，不丢信息 */}
                 <h1 className="text-xl font-semibold tracking-tight whitespace-nowrap">Moo is more</h1>
                 <p
                   className="text-sm text-muted-foreground mt-1.5 whitespace-nowrap tabular-nums"
                   title={`上次检查: ${lastCheck ? new Date(lastCheck).toLocaleString() : '从未'}`}
                 >
                   {nowText}
                 </p>
               </div>
               <Button variant="ghost" size="icon" className="h-8 w-8 shrink-0 -mr-2 -mt-1" onClick={toggleSidebar}>
                 <ChevronsLeft className="h-4 w-4" />
               </Button>
             </div>
           )}
         </div>

         <div className="flex-1 overflow-y-auto">
          <nav className={cn("space-y-1", sidebarCollapsed ? "p-2" : "p-4")}>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant={activeFilter === 'recommended' ? 'default' : 'ghost'}
                  className={cn("w-full h-10 shadow-none rounded-lg font-medium", sidebarCollapsed ? "justify-center px-0" : "justify-start px-3")}
                  onClick={() => { switchFilter('recommended'); }}
                >
                  <Compass className={cn("h-4 w-4 shrink-0", !sidebarCollapsed && "mr-3")} />
                  {!sidebarCollapsed && (
                    <>
                      <span className="flex-1 text-left whitespace-nowrap">发现</span>
                      <span className="ml-auto text-xs opacity-80 tabular-nums">{counts.recommended}</span>
                    </>
                  )}
                </Button>
              </TooltipTrigger>
              {sidebarCollapsed && <TooltipContent side="right">发现 ({counts.recommended})</TooltipContent>}
            </Tooltip>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant={activeFilter === 'all' ? 'default' : 'ghost'}
                  className={cn("w-full h-10 shadow-none rounded-lg font-medium", sidebarCollapsed ? "justify-center px-0" : "justify-start px-3")}
                  onClick={() => { switchFilter('all'); }}
                >
                  <LayoutGrid className={cn("h-4 w-4 shrink-0", !sidebarCollapsed && "mr-3")} />
                  {!sidebarCollapsed && (
                    <>
                      <span className="flex-1 text-left whitespace-nowrap">全部</span>
                      <span className={cn("ml-auto text-xs tabular-nums", activeFilter === 'all' ? "text-white/80" : "text-muted-foreground")}>{counts.all}</span>
                    </>
                  )}
                </Button>
              </TooltipTrigger>
              {sidebarCollapsed && <TooltipContent side="right">全部 ({counts.all})</TooltipContent>}
            </Tooltip>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant={activeFilter === 'installed' ? 'default' : 'ghost'}
                  className={cn("w-full h-10 shadow-none rounded-lg font-medium", sidebarCollapsed ? "justify-center px-0" : "justify-start px-3")}
                  onClick={() => { switchFilter('installed'); }}
                >
                  <CheckCircle2 className={cn("h-4 w-4 shrink-0", !sidebarCollapsed && "mr-3")} />
                  {!sidebarCollapsed && (
                    <>
                      <span className="flex-1 text-left whitespace-nowrap">已安装</span>
                      <span className={cn("ml-auto text-xs tabular-nums", activeFilter === 'installed' ? "text-white/80" : "text-muted-foreground")}>{counts.installed}</span>
                    </>
                  )}
                </Button>
              </TooltipTrigger>
              {sidebarCollapsed && <TooltipContent side="right">已安装 ({counts.installed})</TooltipContent>}
            </Tooltip>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant={activeFilter === 'update_available' ? 'default' : 'ghost'}
                  className={cn("w-full h-10 shadow-none rounded-lg font-medium", sidebarCollapsed ? "justify-center px-0" : "justify-start px-3")}
                  onClick={() => { switchFilter('update_available'); }}
                >
                  <div className="relative shrink-0">
                    <RefreshCw className={cn("h-4 w-4", !sidebarCollapsed && "mr-3")} />
                    {sidebarCollapsed && counts.update_available > 0 && (
                      <span className="absolute -top-1 -right-1 h-2 w-2 rounded-full bg-destructive" />
                    )}
                  </div>
                  {!sidebarCollapsed && (
                    <>
                      <span className="flex-1 text-left whitespace-nowrap">有更新</span>
                      {counts.update_available > 0 ? (
                        <Badge
                          variant={activeFilter === 'update_available' ? 'secondary' : 'destructive'}
                          className={cn("ml-auto shrink-0", activeFilter === 'update_available' && "bg-white/25 text-white border-0")}
                        >
                          {counts.update_available}
                        </Badge>
                      ) : (
                        <span className="ml-auto text-xs text-muted-foreground tabular-nums">0</span>
                      )}
                    </>
                  )}
                </Button>
              </TooltipTrigger>
              {sidebarCollapsed && (
                <TooltipContent side="right">有更新 ({counts.update_available})</TooltipContent>
              )}
            </Tooltip>
            {/* 0.6.228（用户定稿）：设置从「侧栏底部固定」改到「有更新」之后，同一组菜单里 */}
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  className={cn(
                    // 0.6.229（用户定稿）：与上面「发现/全部/已安装/有更新」**统一字体与配色**
                    // （原来这里是底部固定项，用的是 font-normal + text-muted-foreground，
                    //  移进同一组菜单后显得不一致）
                    "w-full h-10 shadow-none rounded-lg font-medium",
                    sidebarCollapsed ? "justify-center px-0" : "justify-start px-3"
                  )}
                  onClick={() => setSettingsVisible(true)}
                 >
                  <div className="relative shrink-0">
                    <Settings className={cn("h-4 w-4", !sidebarCollapsed && "mr-3")} />
                    {storeHasUpdate && (
                      <span className="absolute -top-1 -right-1 h-2 w-2 rounded-full bg-destructive" />
                    )}
                  </div>
                  {!sidebarCollapsed && <span className="flex-1 text-left whitespace-nowrap">设置</span>}
                </Button>
              </TooltipTrigger>
              {sidebarCollapsed && <TooltipContent side="right">设置{storeHasUpdate ? ' (有更新)' : ''}</TooltipContent>}
            </Tooltip>
          </nav>
          
         </div>
        </TooltipProvider>
       </aside>

      <div className="flex-1 flex flex-col min-h-0 md:min-h-dvh min-w-0">
        <div className={cn(
            "md:hidden bg-card/70 backdrop-blur-xl border-b border-border/50 px-4 pt-4 pb-3 sticky top-0 z-20 flex flex-col gap-3 transition-[box-shadow,border-color] duration-300",
            searchExpanded && "shadow-lg border-b-transparent"
          )}>
            {searchExpanded ? (
              <>
              {/* 展开态：与原搜索框等长的全宽搜索框，悬浮在页面上方（sticky header + 阴影）；
                 收起立即（无过渡，1.14.24 用户要求） */}
              <div className="search-expand-anim relative">
                <Search className="absolute left-3.5 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground pointer-events-none" />
                <Input
                  ref={searchInputRef}
                  autoFocus
                  type="text"
                  placeholder="搜索应用..."
                  value={searchInput}
                  onChange={(e) => {
                    setSearchInput(e.target.value);
                    /* 从"发现"页搜索时切到应用列表，保证有结果区。
                       输入不再触发任何自动收起（输入途中保持展开） */
                    if (activeFilter === 'recommended' && e.target.value) switchFilter('all');
                  }}
                  onKeyDown={(e) => {
                    /* 按回车 → 收起（搜索词保留，列表保持过滤） */
                    if (e.key === 'Enter') collapseSearch();
                  }}
                  onBlur={() => collapseSearch()}
                  className="w-full pl-9 pr-16 h-9 shadow-none rounded-full border-0 bg-muted/60 focus-visible:ring-primary/40"
                />
                {searchInput && (
                  <button
                    /* preventDefault 保住输入框焦点，让清除点击生效（否则 blur 先收起）；
                       清空 → 带动画收起 */
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => { setSearchInput(''); collapseSearch(); }}
                    className="absolute right-[52px] top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                  >
                    <X className="h-4 w-4" />
                  </button>
                )}
                <button
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => collapseSearch()}
                  className="absolute right-2.5 top-1/2 -translate-y-1/2 h-8 px-1.5 text-[13px] font-medium text-primary"
                >
                  收起
                </button>
              </div>
              {/* 0.6.301：展开态搜索框后同样给三模式入口（仅发现页） */}
              {activeFilter === 'recommended' && (
                <div className="flex items-center">{viewModeSwitchGroup}</div>
              )}
            </>
            ) : (
              /* 收起态：标题 + 紧凑搜索药丸 + 三个按钮（间距加大防误触） */
              <div className="flex items-center justify-between gap-2">
                {/* 收起态：加长药丸；有搜索词时显示内容 + × 清除 */}
                <button
                  onClick={() => expandSearch()}
                  className="flex items-center gap-1.5 h-9 w-[150px] pl-3.5 pr-2.5 rounded-full bg-muted/60 hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
                  aria-label="搜索"
                  title="搜索"
                >
                  <Search className="h-[18px] w-[18px] shrink-0" />
                  {searchInput ? (
                    <>
                      <span className="min-w-0 flex-1 truncate text-left text-[13px]">{searchInput}</span>
                      <span
                        role="button"
                        aria-label="清除搜索"
                        title="清除搜索"
                        onMouseDown={(e) => { e.preventDefault(); e.stopPropagation(); }}
                        onClick={(e) => {
                          e.stopPropagation();
                          setSearchInput('');
                        }}
                        className="flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-black/10 text-muted-foreground hover:text-foreground"
                      >
                        <X className="h-3 w-3" />
                      </span>
                    </>
                  ) : (
                    <span className="text-[13px]">搜索</span>
                  )}
                </button>
                {/* 搜索栏后常显应用数：始终=当前视图条目数
                    （tab 过滤 + 分类 + 搜索词全生效）；发现 tab 不显示（用户要求） */}
                {activeFilter !== 'recommended' && (
                  <span className="shrink-0 text-[12px] tabular-nums text-muted-foreground" title="当前视图应用数">
                    {filteredApps.length} 个应用
                  </span>
                )}
                {/* 0.6.301（用户定稿）：移动端三模式按钮 = 发现页搜索框后常显；
                    其他 dock 页（全部/已安装/有更新）不显示 */}
                {activeFilter === 'recommended' && (
                  <div className="shrink-0">{viewModeSwitchGroup}</div>
                )}
                <div className="flex items-center gap-3 shrink-0">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-9 w-9"
                    onClick={() => setSettingsVisible(true)}
                    aria-label="设置"
                    title="设置"
                  >
                    <div className="relative">
                      <Settings className="h-[18px] w-[18px]" />
                      {storeHasUpdate && (
                        <span className="absolute -top-0.5 -right-0.5 h-2 w-2 rounded-full bg-destructive" />
                      )}
                    </div>
                  </Button>
                  <ThemeToggle className="h-9 w-9" />
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-9 w-9"
                    onClick={handleCheck}
                    disabled={checking}
                    aria-label="检查更新"
                    title="检查更新"
                  >
                    <RefreshCw className={cn("h-[18px] w-[18px]", checking && "animate-spin")} />
                  </Button>
                </div>
              </div>
            )}
            {/* 分类 pill 行：与上方搜索框左缘对齐（header px-4），不再贴屏幕边 */}
            {activeFilter !== 'recommended' && (
              // 0.6.292：触屏保持隐藏滑条（移动端布局不变）；鼠标设备（含缩小窗口的
              // 网页端）溢出时显示全局同款极简横向细条，可用鼠标拖动切换类别
              <div className="flex gap-2 overflow-x-auto pill-bar">
                {allCategoryPill}
                {CATEGORIES.map(cat => (
                  <button
                    key={cat.key}
                    onClick={() => setActiveCategory(cat.key)}
                    className={cn(
                      // 0.6.284：移动端胶囊与桌面同材质（Dock 毛玻璃）；布局不变
                      "shrink-0 h-8 px-3.5 rounded-full border border-white/10 backdrop-blur-xl text-[13px] font-medium whitespace-nowrap",
                      activeCategory === cat.key ? "bg-primary/15 text-primary border-primary/40" : "bg-card/55 text-foreground"
                    )}
                  >
                    {cat.label}
                  </button>
                ))}
              </div>
            )}
        </div>

        <header className={cn(
            "hidden md:flex flex-col px-8 sticky top-0 z-10 transition-all duration-300",
            mainScrolled ? "bg-card/70 backdrop-blur-xl border-b border-border/50 py-2" : "bg-transparent border-b border-transparent py-4"
          )}>
           {/* row1：品牌+大标题 | 搜索+计数 | 三视图+主题+刷新（原顶栏内容，布局不变） */}
           <div className="flex w-full items-center justify-between">
           <div className="flex items-center gap-2 shrink-0">
           {/* 0.6.230：侧栏隐去后，品牌「Moo is more」+ 实时时钟移到顶栏左侧
               （用户要求内容与格式保持不变：日期+时间、秒级刷新；上次检查在悬停提示里） */}
           <div className="mr-4 min-w-0">
             <div className="text-lg font-semibold tracking-tight whitespace-nowrap leading-tight">Moo is more</div>
             <div
               className="text-[11px] text-muted-foreground tabular-nums whitespace-nowrap"
               title={`上次检查: ${lastCheck ? new Date(lastCheck).toLocaleString() : '从未'}`}
             >
               {nowText}
             </div>
           </div>
           <h2 className={cn("font-bold tracking-tight shrink-0 transition-all duration-300", mainScrolled ? "text-lg" : "text-[32px] leading-[1.2]")}>
              {activeFilter === 'recommended' && '发现'}
              {activeFilter === 'all' && '应用'}
              {activeFilter === 'installed' && '已安装'}
              {activeFilter === 'update_available' && '可用更新'}
              {activeFilter !== 'recommended' && activeCategory && (
                <span className={cn("text-muted-foreground font-normal transition-all duration-300", mainScrolled ? "text-sm" : "text-xl")}>{' · '}{CATEGORIES.find(c => c.key === activeCategory)?.label}</span>
              )}
           </h2>
           </div>
           {/* 0.6.284（用户定稿）：搜索框从右侧组独立出来，居中占顶栏中段并加长
               （224/256px → 320/420px），计数跟随其后；主题/刷新保持在最右。 */}
           <div className="flex flex-1 min-w-0 items-center justify-center gap-2">
               <>
                 <div className="relative shrink-0">
                   <Search className="absolute left-3.5 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground pointer-events-none" />
                     <Input
                       type="text"
                       placeholder="搜索应用..."
                       value={searchInput}
                       onChange={(e) => {
                         const v = e.target.value;
                         setSearchInput(v);
                         // 与移动端一致：发现 tab 里输入即跳「全部」搜索目录
                         if (activeFilter === 'recommended' && v) switchFilter('all');
                       }}
                       className="w-80 xl:w-[420px] pl-9 pr-8 h-9 shadow-none rounded-full border-0 bg-muted/60 focus-visible:ring-primary/40"
                     />
                     {searchInput && (
                       <button
                         onClick={() => setSearchInput('')}
                         className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                       >
                         <X className="h-4 w-4" />
                       </button>
                     )}
                   </div>
                   {/* 搜索框后常显应用数：始终=当前视图条目数
                       （tab 过滤 + 分类 + 搜索词全生效）；发现 tab 不显示（用户要求） */}
                   {activeFilter !== 'recommended' && (
                     <span className="shrink-0 text-[12px] tabular-nums text-muted-foreground" title="当前视图应用数">
                       {filteredApps.length} 个应用
                     </span>
                   )}
                 </>
           </div>
           <div className="flex items-center gap-3 shrink-0">
               {/* 0.6.293 预览模式切换（用户定位）：ThemeToggle 之前、与后面的
                   刷新按钮同组同 gap-3 间距。极简=Grid2x2 / 极光=Sparkles / 标准=LayoutGrid */}
               <div className="flex items-center gap-0.5 rounded-full bg-muted/60 p-1" role="group" aria-label="预览模式">
                 {([
                   ['minimal', Grid2x2, '极简模式'],
                   ['aurora', Sparkles, '极光模式'],
                   ['standard', LayoutGrid, '标准模式'],
                 ] as const).map(([mode, Icon, label]) => (
                   <button
                     key={mode}
                     onClick={() => setViewMode(mode)}
                     title={label}
                     aria-label={label}
                     aria-pressed={viewMode === mode}
                     className={cn(
                       "h-7 w-7 rounded-full flex items-center justify-center transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40",
                       viewMode === mode ? "bg-background text-primary shadow-sm" : "text-muted-foreground hover:text-foreground"
                     )}
                   >
                     <Icon className="h-[15px] w-[15px]" />
                   </button>
                 ))}
               </div>
               <ThemeToggle />
               <Button 
                 onClick={handleCheck} 
                 disabled={checking}
                 className="rounded-full"
               >
                 {checking ? (
                   <>
                     <RefreshCw className="mr-2 h-4 w-4 animate-spin" />
                     刷新中...
                   </>
                 ) : (
                   <>
                     <RefreshCw className="mr-2 h-4 w-4" />
                     刷新页面
                   </>
                 )}
               </Button>
           </div>
           </div>
           {/* row2：分类 pill 行（0.6.305 从 main 顶部移入顶栏冻结区，对齐移动端既有
               模式——移动端分类条本就在 sticky 头里）：滚列表时类别常顶，不用回顶换类。
               发现页（recommended）与移动端同款不显示。 */}
           {activeFilter !== 'recommended' && (
             <div className="flex items-center gap-2 overflow-x-auto pill-bar">
               {allCategoryPill}
               {CATEGORIES.map(cat => (
                 <button
                   key={cat.key}
                   onClick={() => setActiveCategory(cat.key)}
                   className={cn(
                     // 0.6.284：胶囊统一 Dock 毛玻璃材质（bg-card/55 + backdrop-blur + white/10 描边）
                     "shrink-0 h-8 px-3.5 rounded-full border border-white/10 backdrop-blur-xl text-[13px] font-medium whitespace-nowrap transition-colors",
                     activeCategory === cat.key ? "bg-primary/15 text-primary border-primary/40" : "bg-card/55 text-foreground hover:bg-card/80"
                   )}
                 >
                   {cat.label}
                   <span className="ml-1 text-xs opacity-60 tabular-nums">{categoryCounts[cat.key] ?? 0}</span>
                 </button>
               ))}
             </div>
           )}
        </header>

        {/* 0.6.273：移动端 Dock 改悬浮胶囊（距底 10px+safe），内容底部留白 96→112px */}
        <main className="flex-grow p-4 pb-28 md:p-8 md:pb-36 overflow-y-auto">
          {activeFilter === 'recommended' ? (
            <div className="space-y-10">
              {apps.length > 0 && (
                <FeaturedShowcase apps={apps} onDetail={setDetailApp} />
              )}
              {/* 收藏列表（原「探索推荐」位）：收藏的应用按收藏先后呈现；
                  保留折叠按钮（默认折叠）；空态给收藏入口提示。 */}
              <section>
                <div className="flex items-center gap-2 mb-3">
                  <h2 className="text-lg font-bold tracking-tight flex items-center gap-1.5">
                    <Star className="h-4 w-4 text-muted-foreground" />
                    收藏列表
                  </h2>
                  {favoriteApps.length > 0 && (
                    <span className="text-sm text-muted-foreground tabular-nums">{favoriteApps.length}</span>
                  )}
                  <button
                    type="button"
                    onClick={() => setFavExpanded(v => !v)}
                    aria-expanded={favExpanded}
                    className="inline-flex items-center gap-1 h-7 px-3 rounded-full bg-muted/60 hover:bg-muted text-xs font-medium text-foreground transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                  >
                    {favExpanded ? '收起' : '展开'}
                    <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", favExpanded && "rotate-180")} />
                  </button>
                </div>
                {favExpanded && (
                  favoriteApps.length > 0 ? (
                    /* 0.6.295（用户定稿）：收藏区随三种预览模式切换（0.6.301 移动端同享） */
                    viewMode === 'minimal' ? (
                      <MinimalIconGridM apps={favoriteApps} onDetail={setDetailApp} />
                    ) : viewMode === 'aurora' ? (
                      <AuroraGridM apps={favoriteApps} onDetail={setDetailApp} />
                    ) : isDesktop ? (
                      /* 0.6.285：与主列表统一 —— 固定等高大卡网格（h-180）；
                         卡面动作只剩安装/打开；2xl 5 列 */
                      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5 gap-4 items-start">
                        {favoriteApps.map(app => (
                          <Suspense key={app.key || app.appname} fallback={<WebAppDetailCardSkeleton />}>
                            <WebAppDetailCard
                              app={app}
                              operation={appOperations.get(app.appname)}
                              onInstall={handleInstall}
                              onUpdate={handleUpdate}
                              upgradeAllowed={upgradeAllowed}
                              onDetail={setDetailApp}
                              onCancelOp={handleCancelOp}
                              onOpenApp={handleOpenApp}
                              isFavorite={favoriteSet.has(app.key || app.appname)}
                              onToggleFavorite={handleToggleFavorite}
                            />
                          </Suspense>
                        ))}
                      </div>
                    ) : (
                      /* 移动端布局不变：保留原瀑布流卡（单列宽度） */
                      <div className="columns-1 gap-4">
                        {favoriteApps.map(app => (
                          <div key={app.key || app.appname} className="mb-4 break-inside-avoid">
                          <AppCard
                            app={app}
                            operation={appOperations.get(app.appname)}
                            onInstall={handleInstall}
                            onUpdate={handleUpdate}
                            onUninstall={handleUninstall}
                            onDetail={setDetailApp}
                            onCancelOp={handleCancelOp}
                            upgradeAllowed={upgradeAllowed}
                            onOpenApp={handleOpenApp}
                            isFavorite={favoriteSet.has(app.key || app.appname)}
                            onToggleFavorite={handleToggleFavorite}
                          />
                          </div>
                        ))}
                      </div>
                    )
                  ) : (
                    <div className="flex flex-col items-center justify-center h-64 text-muted-foreground">
                      <Star className="h-12 w-12 mb-4 opacity-40" />
                      <p className="text-sm font-medium">还没有收藏应用</p>
                      <p className="text-sm mt-1">点应用卡片或详情页的 ☆ 收藏，收藏的应用会出现在这里</p>
                    </div>
                  )
                )}
              </section>
              {/* 忽略更新（0.6.181）：被忽略更新的应用；收藏列表下方的折叠
                  按钮（默认折叠）；行内展示 旧→新 版本 + 快速「取消忽略」。 */}
              <section>
                <div className="flex items-center gap-2 mb-3">
                  <h2 className="text-lg font-bold tracking-tight flex items-center gap-1.5">
                    <BellOff className="h-4 w-4 text-muted-foreground" />
                    忽略更新
                  </h2>
                  {ignoredApps.length > 0 && (
                    <span className="text-sm text-muted-foreground tabular-nums">{ignoredApps.length}</span>
                  )}
                  <button
                    type="button"
                    onClick={() => setIgnoreExpanded(v => !v)}
                    aria-expanded={ignoreExpanded}
                    className="inline-flex items-center gap-1 h-7 px-3 rounded-full bg-muted/60 hover:bg-muted text-xs font-medium text-foreground transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                  >
                    {ignoreExpanded ? '收起' : '展开'}
                    <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", ignoreExpanded && "rotate-180")} />
                  </button>
                </div>
                {ignoreExpanded && (
                  ignoredApps.length > 0 ? (
                    /* 0.6.295（用户定稿）：忽略更新区随三种预览模式切换（0.6.301 移动端同享）；
                       极简/极光形态下「取消忽略」经详情对话框完成 */
                    viewMode === 'minimal' ? (
                      <MinimalIconGridM apps={ignoredApps} onDetail={setDetailApp} />
                    ) : viewMode === 'aurora' ? (
                      <AuroraGridM apps={ignoredApps} onDetail={setDetailApp} />
                    ) : (
                    <div className="bg-card rounded-[18px] overflow-hidden border border-border/20 shadow-appstore">
                      {ignoredApps.map((app, i) => (
                        <div
                          key={app.key || app.appname}
                          className={cn(
                            "flex items-center gap-3.5 p-4 cursor-pointer transition-colors hover:bg-muted/30 active:bg-muted/50",
                            i > 0 && "border-t border-border/40"
                          )}
                          onClick={() => setDetailApp(app)}
                        >
                          <AppIcon app={app} className="w-11 h-11 shrink-0" />
                          <div className="flex-1 min-w-0">
                            <div className="flex items-center gap-1.5 min-w-0">
                              <span className="font-semibold text-[15px] leading-tight truncate" title={app.display_name}>
                                {app.display_name}
                              </span>
                              {app.available_version ? (
                                <span className="text-[12px] text-muted-foreground truncate tabular-nums">
                                  {app.installed_version} → {app.available_version}
                                </span>
                              ) : (
                                <span className="text-[12px] text-muted-foreground/70 shrink-0">已是最新</span>
                              )}
                            </div>
                            <span className="text-[12px] text-muted-foreground/80 truncate block" title={app.appname}>
                              {app.appname}
                            </span>
                          </div>
                          <button
                            type="button"
                            onClick={(e) => { e.stopPropagation(); handleUnignoreUpdate(app); }}
                            className="shrink-0 h-7 px-3 rounded-full bg-muted/60 hover:bg-muted text-xs font-medium text-foreground transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                          >
                            取消忽略
                          </button>
                        </div>
                      ))}
                    </div>
                    )
                  ) : (
                    <div className="flex flex-col items-center justify-center h-40 text-muted-foreground">
                      <BellOff className="h-10 w-10 mb-3 opacity-40" />
                      <p className="text-sm font-medium">还没有忽略更新的应用</p>
                      <p className="text-sm mt-1">在应用详情页点「忽略更新」，该应用会出现在这里</p>
                    </div>
                  )
                )}
              </section>
            </div>
          ) : loadStatus === 'loaded' ? (
            <>
              {/* 0.6.301（用户定稿）：三种预览模式引入移动端 —— 极简/极光桌面移动
                  共用同一网格（auto-fill/响应式列数已适配窄屏）；标准模式保持
                  各自形态：桌面=卡网格 / 移动=行列表。交互：触屏单击开详情、
                  鼠标双击（见各卡组件的 coarse 分支）。 */}
              {viewMode === 'minimal' ? (
                <MinimalIconGridM apps={filteredApps} onDetail={setDetailApp} />
              ) : viewMode === 'aurora' ? (
                <AuroraGridM apps={filteredApps} onDetail={setDetailApp} />
              ) : isDesktop ? (
                <AppList
                   apps={filteredApps}
                   loading={false}
                   onInstall={handleInstall}
                   onUpdate={handleUpdate}
                   upgradeAllowed={upgradeAllowed}
                   onDetail={setDetailApp}
                   onCancelOp={handleCancelOp}
                   filterType={activeFilter}
                   appOperations={appOperations}
                   searchQuery={searchQuery}
                   onSourceFilter={applyTextFilter}
                   onAuthorFilter={applyTextFilter}
                   onDistributorFilter={applyTextFilter}
                   activeTerms={activeSearchTerms}
                   onOpenApp={handleOpenApp}
                   favoriteSet={favoriteSet}
                   onToggleFavorite={handleToggleFavorite}
                />
              ) : (
                <AppRowList
                  apps={filteredApps}
                  onInstall={handleInstall}
                  onUpdate={handleUpdate}
                  onDetail={setDetailApp}
                  onCancelOp={handleCancelOp}
                  appOperations={appOperations}
                  searchQuery={searchQuery}
                  filterType={activeFilter}
                  upgradeAllowed={upgradeAllowed}
                  onSourceFilter={applyTextFilter}
                  onAuthorFilter={applyTextFilter}
                  onDistributorFilter={applyTextFilter}
                  activeTerms={activeSearchTerms}
                  onOpenApp={handleOpenApp}
                  favoriteSet={favoriteSet}
                  onToggleFavorite={handleToggleFavorite}
                />
              )}
            </>
          ) : loadStatus === 'loading' ? (
            <div className="flex flex-col items-center justify-center h-64">
              <Loader2 className="h-8 w-8 animate-spin text-muted-foreground mb-4" />
              <p className="text-sm text-muted-foreground">正在加载应用列表...</p>
            </div>
          ) : (
            <div className="flex flex-col items-center justify-center h-64 max-w-sm mx-auto">
              {loadStatus === 'retrying' && (
                <Loader2 className="h-8 w-8 animate-spin text-primary mb-6" />
              )}
              {loadStatus === 'failed' && (
                <WifiOff className="h-8 w-8 text-muted-foreground mb-6" />
              )}
              <div className="w-full space-y-2 mb-6">
                {loadMessages.map((msg, i) => (
                  <div key={i} className="flex items-center gap-2 text-sm">
                    {msg.status === 'info' && i === loadMessages.length - 1 ? (
                      <Loader2 className="h-3.5 w-3.5 animate-spin text-primary shrink-0" />
                    ) : msg.status === 'success' ? (
                      <CircleCheck className="h-3.5 w-3.5 text-emerald-500 shrink-0" />
                    ) : msg.status === 'error' ? (
                      <CircleX className="h-3.5 w-3.5 text-destructive shrink-0" />
                    ) : (
                      <div className="h-3.5 w-3.5 shrink-0" />
                    )}
                    <span className={cn(
                      msg.status === 'error' ? 'text-destructive' :
                      msg.status === 'success' ? 'text-emerald-500' :
                      'text-muted-foreground'
                    )}>{msg.text}</span>
                  </div>
                ))}
              </div>
              {loadStatus === 'failed' && (
                <div className="flex gap-2">
                  <Button size="sm" onClick={triggerReload}>
                    <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
                    重试
                  </Button>
                  <Button size="sm" variant="outline" onClick={() => setSettingsVisible(true)}>
                    <Settings className="mr-1.5 h-3.5 w-3.5" />
                    更换加速节点
                  </Button>
                </div>
              )}
            </div>
          )}
        </main>
      </div>

      {/* 移动端底部 dock（iOS App Store 标签栏）：
          始终挂载，键盘弹出时靠 bottomOffset 下移钉在物理屏幕底边
          （被键盘盖住、不跟键盘上移、收起无回弹）；仅 pan 型壳兜底
          时整体隐藏。 */}
      {!dockHidden && (
        <MobileDock
          active={activeFilter}
          onSelect={(key) => { switchFilter(key); }}
          updateCount={counts.update_available}
          order={dockOrder ?? undefined}
          bottomOffset={dockOffsetPx}
        />
      )}

      {/* 0.6.230（用户定稿）：「苹果风」底部毛玻璃 Dock（桌面端）——
          原左侧菜单模块（发现 / 全部 / 已安装 / 有更新 / 设置）搬到这里；
          悬浮居中、圆角、毛玻璃（backdrop-blur），有更新时右上角小红点，
          计数放 title 提示里（保持 Dock 干净，贴近 macOS 观感）。
          0.6.284（用户报「网页版排序不起作用+太小太短」）：
          ① 应用系统设置「Dock 栏排序」（此前桌面端固定顺序，设置只对移动端生效）；
          ② 整体加大加宽：按钮 70→88px、图标 24→28px、字号 10→11px、留白全面加大。 */}
      <nav className="hidden md:flex fixed bottom-6 left-1/2 -translate-x-1/2 z-30 items-end gap-3 rounded-[26px] border border-white/10 bg-card/55 px-4 py-3 shadow-2xl shadow-black/40 backdrop-blur-2xl">
        {(() => {
          type DockKey = 'recommended' | 'all' | 'installed' | 'update_available';
          const items: { key: DockKey; label: string; icon: React.ElementType; count: number }[] = [
            { key: 'recommended', label: '发现', icon: Compass, count: counts.recommended },
            { key: 'all', label: '全部', icon: LayoutGrid, count: counts.all },
            { key: 'installed', label: '已安装', icon: CheckCircle2, count: counts.installed },
            { key: 'update_available', label: '有更新', icon: RefreshCw, count: counts.update_available },
          ];
          // 与 MobileDock.orderTabs 同逻辑：按已存顺序排，缺 key 追加末尾
          if (!dockOrder || dockOrder.length === 0) return items;
          const byKey = new Map(items.map((t) => [t.key as string, t]));
          const out: typeof items = [];
          for (const k of dockOrder) {
            const t = byKey.get(k);
            if (t) { out.push(t); byKey.delete(k); }
          }
          for (const t of byKey.values()) out.push(t);
          return out;
        })().map(({ key, label, icon: Icon, count }) => {
          const active = activeFilter === key;
          return (
            <button
              key={key}
              type="button"
              onClick={() => switchFilter(key)}
              title={`${label}（${count}）`}
              className={cn(
                'relative flex w-[88px] flex-col items-center gap-1.5 rounded-2xl px-2 py-2 transition-[background-color,color,transform] duration-100 active:scale-90',
                active ? 'bg-primary/15 text-primary' : 'text-muted-foreground hover:bg-white/5 hover:text-foreground'
              )}
            >
              <Icon className="h-7 w-7" strokeWidth={active ? 2.2 : 1.8} />
              <span className={cn('text-[11px] leading-none', active && 'font-semibold')}>{label}</span>
              {key === 'update_available' && count > 0 && (
                <span className="absolute right-3 top-1 h-2 w-2 rounded-full bg-destructive" />
              )}
            </button>
          );
        })}
        <span className="mx-1 h-9 w-px self-center bg-border/60" />
        <button
          type="button"
          onClick={() => setSettingsVisible(true)}
          title="设置"
          className="relative flex w-[88px] flex-col items-center gap-1.5 rounded-2xl px-2 py-2 text-muted-foreground transition-[background-color,color,transform] duration-100 hover:bg-white/5 hover:text-foreground active:scale-90"
        >
          <div className="relative">
            <Settings className="h-7 w-7" strokeWidth={1.8} />
            {storeHasUpdate && (
              <span className="absolute -right-1 -top-0.5 h-2 w-2 rounded-full bg-destructive" />
            )}
          </div>
          <span className="text-[11px] leading-none">设置</span>
        </button>
      </nav>

      {selfUpdateActive && selfUpdateState && (
        <ProgressOverlay
          visible={true}
          message={selfUpdateState.message}
          progress={selfUpdateState.progress}
          speed={selfUpdateState.speed}
          downloaded={selfUpdateState.downloaded}
          total={selfUpdateState.total}
        />
      )}

      {/* 后台任务进度（退出应用后继续跑的安装/更新，轮询展示） */}
      <BackgroundTasksIndicator />

      <Suspense fallback={null}>
        <SettingsPage
          open={settingsVisible}
          onOpenChange={handleSettingsOpenChange}
          initialTab={settingsTab ?? undefined}
          onTabChange={setSettingsTab}
          onStoreUpdate={handleStoreUpdate}
          aurora={viewMode === 'aurora'}
          onCatalogChanged={() => setTimeout(() => loadApps(), 2500)}
        />
      </Suspense>

      {wizardApp && (
        <Suspense fallback={null}>
        <WizardDialog
          appDisplayName={wizardApp.display_name}
          wizard={wizardDef}
          loading={wizardLoading}
          onCancel={() => {
            setWizardApp(null);
            setWizardDef(null);
            setWizardLoading(false);
            setPanelWizardParams(null);
          }}
          onConfirm={(params) => {
            const app = wizardApp;
            const panelParams = panelWizardParams;
            // 0.6.269：源向导参数（pending）+ FPK 向导参数 合并下发
            const pending = pendingSourceParamsRef.current;
            pendingSourceParamsRef.current = [];
            setWizardApp(null);
            setWizardDef(null);
            setWizardLoading(false);
            setPanelWizardParams(null);
            const merged = [...pending, ...params];
            void runInstall(app, merged.length ? merged : undefined, panelParams ?? undefined);
          }}
        />
        </Suspense>
      )}

      {/* 0.6.269：源声明的安装向导（moo.json wizard.fields，键名由应用定义） */}
      {sourceWizardApp && (
        <Suspense fallback={null}>
        <SourceWizardDialog
          appDisplayName={sourceWizardApp.display_name}
          fields={sourceWizardApp.wizard?.fields ?? []}
          onCancel={() => {
            pendingSourceParamsRef.current = [];
            setSourceWizardApp(null);
          }}
          onConfirm={(params) => {
            const app = sourceWizardApp;
            pendingSourceParamsRef.current = params;
            setSourceWizardApp(null);
            void continueInstall(app);
          }}
        />
        </Suspense>
      )}

      {panelApp && panelDetail && (
        <Suspense fallback={null}>
        <PanelInstallDialog
          detail={panelDetail}
          loading={panelLoading}
          onCancel={() => {
            setPanelApp(null);
            setPanelDetail(null);
            setPanelLoading(false);
          }}
          onConfirm={(params: PanelInstallParams) => {
            const app = panelApp;
            setPanelApp(null);
            setPanelDetail(null);
            setPanelLoading(false);
            // 官方通道：体积/依赖确认后先静默预取向导（后端会先下载包再取
            // install/info，安装时直接复用）。带向导则弹向导，否则直接装。
            // 预取失败不阻塞安装（与 FPK 通道同一契约）。
            setPanelWizardParams(params);
            setWizardApp(app);
            setWizardLoading(true);
            setWizardDef(null);
            fetchWizard(app.appname)
              .then((w) => {
                if (w.has_wizard && (w.content?.length ?? 0) > 0) {
                  setWizardDef(w);
                  setWizardLoading(false);
                  return;
                }
                setWizardApp(null);
                setWizardLoading(false);
                setPanelWizardParams(null);
                void runInstall(app, undefined, params);
              })
              .catch(() => {
                setWizardApp(null);
                setWizardLoading(false);
                setPanelWizardParams(null);
                void runInstall(app, undefined, params);
              });
          }}
        />
        </Suspense>
      )}

      <AlertDialog open={!!pendingUninstallApp} onOpenChange={(open) => !open && setPendingUninstallApp(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认卸载</AlertDialogTitle>
            <AlertDialogDescription>
              确定要卸载 {pendingUninstallApp?.display_name} 吗？此操作无法撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction onClick={confirmUninstall}>
              确认卸载
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <Suspense fallback={null}>
        <AppDetailDialog
          app={detailApp}
          open={!!detailApp}
          onOpenChange={(open) => !open && setDetailApp(null)}
          onInstall={handleInstall}
          onUpdate={handleUpdate}
          onIgnoreUpdate={handleIgnoreUpdate}
          onUnignoreUpdate={handleUnignoreUpdate}
          onUninstall={handleUninstall}
          operation={detailApp ? appOperations.get(detailApp.appname) : undefined}
          onSourceFilter={applyTextFilter}
          onAuthorFilter={applyTextFilter}
          onDistributorFilter={applyTextFilter}
          activeTerms={activeSearchTerms}
          onOpenApp={handleOpenApp}
          onControl={handleControl}
          controlling={controlling}
          isFavorite={detailApp ? favoriteSet.has(detailApp.key || detailApp.appname) : false}
          onToggleFavorite={handleToggleFavorite}
          aurora={viewMode === 'aurora'}
        />
      </Suspense>

      {/* 安装/更新成功大弹窗已移除（0.6.119）：顶部通知栏统一提示「XX 安装成功」 */}

      <Suspense fallback={null}>
        <ReportFailureDialog
          open={!!reportTarget}
          onClose={() => setReportTarget(null)}
          app={reportTarget?.app || ''}
          step={reportTarget?.step || ''}
          errorMessage={reportTarget?.error || ''}
        />
      </Suspense>
      {/* 通知栏（对齐 fn-knock 模式）：顶部居中、3 秒自动消失；
          visibleToasts=1 = 同一时刻只显示最新 1 条，其余排队折叠隐藏
          （前一条消失后自动顶上来），绝不出现多条通知栏上下排列 */}
      <Toaster position="top-center" duration={3000} visibleToasts={1} gap={8} />
      </div>
    </>
  );
};

export default App;
