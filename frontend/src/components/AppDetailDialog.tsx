import React, { useState, useEffect, useRef, useCallback } from 'react';
import { createPortal } from 'react-dom';
import { categoryLabel } from '@/lib/categories';
import { installTypeRow } from '@/lib/appMeta';
import type { AppInfo, AppOperation, PanelDetailResponse, SSEHandle } from '../api/client';
import { apiFetch, availableVersionLabel, installedVersionLabel, assetUrl, appWebUrl, fetchPanelDetail, fetchPanelDetailCached, fetchInstalledDetail, fetchAppDetail, downloadFpk, fetchTasks, pauseDownload, resumeDownload, sourceLabel, effectiveMaintainer, descriptionPlainText, rewriteReadmeImgSrc, fetchFpkDownloads, installFpkDownload, fetchFpkDownloadWizard, addSource, toggleSource, type AppWizard, type WizardParam } from '../api/client';
import { toast } from 'sonner';
import { cn } from '@/lib/utils';
import { useIsDesktop, useMobileDesktopSim } from '@/lib/hooks';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import AppIcon from "./AppIcon";
import WizardDialog from "./WizardDialog";
import { auroraFor } from "./ViewModeGrids";
import {
  Package,
  Globe,
  Clock,
  Tag,
  Network,
  ExternalLink,
  Circle,
  Download,
  RefreshCw,
  BellOff,
  Bell,
  Loader2,
  Play,
  Pause,
  Square,
  Trash2,
  User,
  Images,
  Hash,
  HardDrive,
  FileText,
  X,
  ChevronLeft,
  ChevronRight,
  Check,
  Star,
  ShieldCheck,
  CalendarClock,
  Cpu,
  GitFork,
  // 0.6.326g：溯源第二行三钮恒显后的图标（与上方源/开发者/发布者胶囊同款 12px）
  Plus,
  Ban,
  EyeOff,
} from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeRaw from 'rehype-raw';
import rehypeSanitize from 'rehype-sanitize';
import DOMPurify from 'dompurify';

// 0.6.325 C9：与后端 normAppName 同口径的归一化（lower + 只保留 a-z0-9）。
// 消解包内 manifest 脏值（字面引号 / 大小写不符 / 未展开模板变量）导致的
// 「已下载」永不命中；仅用于比较，显示一律用原始值。
const normFpk = (s?: string) => (s || '').toLowerCase().replace(/[^a-z0-9]/g, '');

/**
 * 判断 README 内容是否为 HTML 文档/片段（而非 Markdown）。
 * 第三方 FnDepot 源里相当一部分 README 直接给 HTML（<p>/<div>…），
 * 走 ReactMarkdown 会把原始标签当纯文本显示出来——那种必须走
 * innerHTML（先经 DOMPurify 消毒）渲染。
 */
// 详情页与应用列表共用的"源/开发者/发布者"徽章样式（字号两端统一）
// 0.6.319b（用户定稿）：与顶部应用分类胶囊同款语言——默认中性胶囊
// （bg-muted/50 浅灰底+border/40 细描边，浅深主题下白卡上均可见），
// 筛选选中才变蓝（bg-primary/15+primary/40 描边，见各处 pillCls(active)
// 覆盖）。不挂 backdrop-blur：行内徽章随列表滚动，模糊是每帧 GPU 成本
// （319 跟手优化同源教训）。
// 0.6.319d（用户定稿）：徽章退居次要——字号 12px、字重 regular、灰色
// （原 foreground 深色模式太白/浅色太黑、medium 太粗，抢了简介的阅读焦点）；
// line-height 17px 不变，行高零变化。选中态仍变蓝（功能指示）。
export const META_PILL = "inline-flex items-start gap-1 rounded-full bg-muted/50 border border-border/40 px-2 py-[2px] max-w-full text-xs leading-[17px] font-normal text-muted-foreground hover:bg-muted transition-colors focus:outline-none focus-visible:outline-none";

/** 字节数 → 人类可读（下载按钮「总量未知」时显示已下载大小） */
function formatBytes(n: number): string {
  if (!n || n <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

/** 描述富文本渲染样式（官方 desc / HTML 第三方 desc 共用；链接=主色+下划线）
    0.6.319e（用户定稿）：基线=14px 常规 + foreground/70（与纯文本简介同款） */
export const DESC_RICH_CLS = "text-sm leading-relaxed text-foreground/70 [&_h1]:text-base [&_h1]:font-semibold [&_h2]:text-sm [&_h2]:font-semibold [&_h3]:text-sm [&_h3]:font-semibold [&_h4]:text-[13px] font-medium [&_p]:my-1.5 [&_b]:font-semibold [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:list-decimal [&_ol]:pl-5 [&_li]:my-0.5 [&_img]:max-w-full [&_img]:rounded-lg [&_a]:text-primary [&_a]:underline";

export const readmeLooksLikeHtml = (t: string): boolean => {
  const s = (t || '').trimStart();
  if (!s.startsWith('<')) return false;
  const head = s.slice(0, 500);
  const hasBlockTag = /<\/?(?:p|div|br|hr|span|ul|ol|li|h[1-6]|table|thead|tbody|tr|td|th|pre|code|section|article|blockquote|img|a)\b/i.test(head);
  if (!hasBlockTag) return false;
  // 同时存在明显的 Markdown 结构（# 标题 / 加粗 / 列表 / 表格）时按 Markdown 处理
  const hasMarkdown = /^#{1,6}\s+\S|\*\*[^*\n]+\*\*|^[-*]\s+\S|^\d+\.\s+\S|^\|.+\|/m.test(s);
  return !hasMarkdown;
};

/** README 图片加载失败处理：破图图标+长 alt 会撑满单元格（GenOffice
    实锤），压成紧凑的「图片不可用」占位。img 的 error 事件不冒泡，
    用容器 onErrorCapture（capture 阶段）统一拦截。 */
export const handleReadmeImgError = (e: React.SyntheticEvent) => {
  const t = e.target as HTMLImageElement;
  if (t.tagName !== 'IMG' || t.dataset.imgFailed) return;
  t.dataset.imgFailed = '1';
  t.alt = '图片不可用';
  t.style.maxWidth = '120px';
  t.style.maxHeight = '36px';
  t.style.opacity = '0.55';
  t.style.border = '1px dashed currentColor';
  t.style.borderRadius = '4px';
  t.style.padding = '2px 4px';
};

// 移动端悬浮返回钮：磨玻璃圆钮贴左缘半露出（磁吸），细线 ‹ 箭头右移完全可见。

/* ── 0.6.284 共享件（对话框 ⇄ 网页版详情卡片同源）───────────────────
   SmallBox：README/更新日志同款「固定上限独立滚动小框」（0.6.279/280 定稿
   材质：圆角+细边框+浅底 px-4 py-3 + overscroll-contain）；maxH 由调用方
   给（对话框移动 24rem/桌面 28rem；详情卡片更矮以适配卡高）。
   ReadmeRender：README 正文渲染（HTML/Markdown 双分支 + 图片代理 + 表格
   横滚容器），对话框与卡片共用 → 两处布局逐字节一致。 */
export const SmallBox: React.FC<{ children: React.ReactNode; maxH?: string }> = ({ children, maxH = "max-h-[24rem] sm:max-h-[28rem]" }) => (
  <div className={cn("my-2 overflow-y-auto overscroll-contain no-scrollbar rounded-xl border border-border/50 bg-muted/20 px-4 py-3", maxH)}>
    {children}
  </div>
);

export const ReadmeRender: React.FC<{ readme: string; appKey: string; maxH?: string }> = ({ readme, appKey, maxH }) => (
  <SmallBox maxH={maxH}>
    <div onErrorCapture={handleReadmeImgError} className="markdown-body text-sm leading-relaxed text-foreground/90 prose prose-sm dark:prose-invert max-w-none
      [&_img]:max-w-full [&_img]:rounded-lg [&_h1]:text-lg [&_h2]:text-base [&_h3]:text-sm [&_h1]:mt-4 [&_h1]:mb-2 [&_h2]:mt-3 [&_h2]:mb-1.5 [&_h3]:mt-2 [&_h3]:mb-1
      [&_pre]:bg-muted [&_pre]:rounded-lg [&_pre]:p-3 [&_pre]:overflow-x-auto [&_code]:text-xs
      [&_table]:w-full [&_table]:text-xs [&_th]:border [&_th]:border-border [&_th]:p-1.5 [&_td]:border [&_td]:border-border [&_td]:p-1.5
      [&_a]:text-primary [&_a]:underline [&_a]:break-all [&_a]:hover:opacity-80 [&_img]:h-auto [&_th]:bg-muted/60 [&_th]:text-left [&_table]:border-collapse [&_table]:block [&_table]:overflow-x-auto [&_code]:bg-muted [&_code]:px-1 [&_code]:py-0.5 [&_code]:rounded [&_code]:break-all [&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_pre_code]:rounded-none [&_pre_code]:break-normal
      [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:list-decimal [&_ol]:pl-5 [&_li]:my-0.5
      [&_p]:my-2 [&_blockquote]:border-l-4 [&_blockquote]:border-border [&_blockquote]:pl-3 [&_blockquote]:text-muted-foreground [&_hr]:my-4 [&_video]:max-w-full [&_video]:rounded-lg">
      {readmeLooksLikeHtml(readme) ? (
        <div dangerouslySetInnerHTML={{ __html: DOMPurify.sanitize(
          readme.replace(/src="(https?:\/\/[^"]+)"/g, (_m, u: string) => `src="${rewriteReadmeImgSrc(u, appKey)}"`),
          { ADD_ATTR: ['target'] },
        ) }} />
      ) : (
        <ReactMarkdown
          remarkPlugins={[remarkGfm]}
          rehypePlugins={[rehypeRaw, rehypeSanitize]}
          components={{
            img: (props: any) => (
              <img {...props} src={rewriteReadmeImgSrc(props.src, appKey)} loading="lazy" alt={props.alt ?? ''} />
            ),
            table: ({ node, ...props }: any) => (
              <div className="my-2 overflow-x-auto rounded-lg border border-border/40">
                <table {...props} />
              </div>
            ),
          }}
        >
          {readme || ''}
        </ReactMarkdown>
      )}
    </div>
  </SmallBox>
);

interface AppDetailDialogProps {
  app: AppInfo | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 0.6.312 B2：候选表行点击 → 打开该源卡的详情（显式引用逃生门）。 */
  onInstall: (app: AppInfo) => void;
  /** 0.6.325 C5：详情内从本地缓存直接安装成功 → 宿主刷新目录/下载数。 */
  onLocalInstallDone?: () => void;
  onUpdate: (app: AppInfo) => void;
  onIgnoreUpdate?: (app: AppInfo) => void;
  onUnignoreUpdate?: (app: AppInfo) => void;
  /** 0.6.326 应用溯源：禁用/恢复该应用（列表隐藏，已安装卡保留可恢复）。 */
  onToggleHide?: (app: AppInfo) => void;
  /** 0.6.326f：溯源行「禁用fork源 / 恢复fork源」成功后通知宿主刷新目录。 */
  onSourceToggled?: () => void;
  onUninstall?: (app: AppInfo) => void;
  operation?: AppOperation;
  /** 点击源名 → 只看该源的应用 */
  onSourceFilter?: (source: string) => void;
  /** 点击作者 → 只看该作者的应用 */
  onAuthorFilter?: (author: string) => void;
  /** 点击发布者 → 只看该发布者发布的应用 */
  onDistributorFilter?: (distributor: string) => void;
  /** 搜索框内当前词条（徽章词条叠加多选），命中者渲染选中态。 */
  activeTerms?: string[];
  /** 打开应用 Web UI（与 fnOS 应用中心"打开"按钮同机制） */
  onOpenApp?: (app: AppInfo) => void;
  /** 已安装应用启动/停用（与 fnOS 应用中心同步） */
  onControl?: (app: AppInfo, action: 'start' | 'stop') => void;
  /** 正在执行启停操作的应用名（显示转圈） */
  controlling?: string | null;
  /** 收藏状态（头部星标实心/空心） */
  isFavorite?: boolean;
  /** 切换收藏（头部星标，与列表同一端点） */
  onToggleFavorite?: (app: AppInfo) => void;
  /** 极光预览模式（0.6.297，仅网页端）：面板底=该应用极光卡同款渐变 */
  aurora?: boolean;
}

export const DetailRow: React.FC<{ icon: React.ElementType; label: string; children: React.ReactNode }> = ({ icon: Icon, label, children }) => (
  <div className="flex items-start gap-3 py-2">
    <Icon className="h-4 w-4 mt-0.5 text-muted-foreground shrink-0" />
    <div className="flex-1 min-w-0">
      <p className="text-xs text-muted-foreground mb-0.5">{label}</p>
      {/* 0.6.319e（用户定稿）：回 14px 常规 + foreground/70（与简介同款，提一点点亮色） */}
      <div className="text-sm text-foreground/70 break-words">{children}</div>
    </div>
  </div>
);

/** 更新内容列表（对标 FnDepot 自更新屏：版本化条目逐条列出，最新在前）。
    0.6.280：去掉展开/收起——整表常驻 README 同款「固定上限独立滚动小框」，
    始终渲染全量条目，溢出由框体滚动承接（短文自然缩到内容高度）。 */
export const ChangelogList: React.FC<{
  entries: { version?: string; text: string }[];
  /** 有可更新版本时高亮首条（即将装上的新版说明）。 */
  highlightLatest: boolean;
}> = ({ entries, highlightLatest }) => {
  return (
    <div className="space-y-1.5">
      {entries.map((e, i) => (
        <div
          key={`${e.version ?? 'x'}-${i}`}
          className={cn(
            'flex items-start gap-2 text-sm leading-relaxed',
            highlightLatest && i === 0 && 'rounded-md border-l-2 border-primary bg-primary/5 py-1 pl-2 pr-1'
          )}
        >
          {e.version ? (
            <span className="mt-0.5 shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
              {e.version}
            </span>
          ) : (
            <span className="w-1 shrink-0" />
          )}
          <span className="min-w-0 whitespace-pre-wrap break-words text-foreground/90">{e.text}</span>
        </div>
      ))}
    </div>
  );
};

export const formatSize = (bytes?: number): string => {
  if (!bytes || bytes <= 0) return '-';
  if (bytes >= 1024 ** 3) return (bytes / 1024 ** 3).toFixed(2) + ' GB';
  if (bytes >= 1024 ** 2) return (bytes / 1024 ** 2).toFixed(1) + ' MB';
  return (bytes / 1024).toFixed(0) + ' KB';
};

export const formatDownloads = (n?: number): string => {
  if (!n || n <= 0) return '-';
  if (n >= 10000) return (n / 10000).toFixed(1) + ' 万';
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
  return String(n);
};

// 0.6.207-panel D3：描述 HTML 消毒统一走 DOMPurify（与 README 渲染同一防线），
// 弃用自定义 DOMParser 白名单（漏 data:/base 等向量，维护面大）。
// DOMPurify 默认策略即剥离 script/style/iframe/object/embed/on* 与
// javascript: 数据 URL，只保留展示型标签。

const AppDetailDialog: React.FC<AppDetailDialogProps> = ({ app: propApp, open, onOpenChange, onInstall, onUpdate, onIgnoreUpdate, onUnignoreUpdate, onToggleHide, onSourceToggled, onUninstall, onLocalInstallDone, operation, onSourceFilter, onAuthorFilter, onDistributorFilter, activeTerms, onOpenApp, onControl, controlling, isFavorite, onToggleFavorite, aurora }) => {
  // 0.6.301：打开时刻记录 —— 配合 DialogContent 的 400ms 外点免疫窗
  //（触屏双击卡片：第一下开详情，第二下落在遮罩上会秒关 = 用户观感仍「没反应」）
  const openedAtRef = useRef(0);
  useEffect(() => { if (open) openedAtRef.current = Date.now(); }, [open]);
  // 列表载荷瘦身：changelog/homepage/release_url/sha256 与外部源 icon_url
  // 不在列表里，打开详情后由 /api/apps/{key} 补齐（LAN 内几 KB 瞬时）。
  // 接口未回前先以列表条目兜底渲染，回包后无缝升级为完整字段。
  const [fullApp, setFullApp] = useState<AppInfo | null>(null);
  // 官方应用（fnos-official）：列表条目不带描述/截图/发布者，需要面板详情
  // 补全（描述 HTML、预览截图、发布者、安装体积等）；已装但无源元数据的应用
  // （自装 FPK/系统自带）走 installed-detail 补全。
  const [panelInfo, setPanelInfo] = useState<PanelDetailResponse | null>(null);
  // 内容区同步加载：详情完整字段 + 面板详情并行拉取，都回来后才一次性渲染
  // 内容区（此前两者各自定时到达 → 描述/版本/预览区逐波「弹出」，观感不同步）。
  // 头部（图标/名称/操作按钮）始终用列表条目稳定渲染，内容区先出骨架屏。
  const [bodyReady, setBodyReady] = useState(false);
  // 0.6.326 应用溯源：禁用态的本地即时回显。对话框内的 fullApp 快照不随宿主
  // 列表重拉而更新，点「禁用」后药丸必须立刻变「已禁用」（否则再点一次只是
  // 重复禁用、无法恢复）。打开/换应用时复位为落库值（effect 内 reset）。
  const [hiddenOverride, setHiddenOverride] = useState<boolean | null>(null);
  // 0.6.326d（H3/H9）：溯源行「添加为源」的本地态。idle=可点 / busy=请求中 /
  // done=已添加（不可重复点）。对话框内不重拉目录——上游 source/version 由
  // 后端下一次目录刷新补上，但**不允许重复提交**（加源会立刻抓取+解析索引）。
  const [srcAddState, setSrcAddState] = useState<'idle' | 'busy' | 'done'>('idle');
  // 0.6.326f：「禁用fork源」的本地即时回显 + 请求中的忙碌态。理由同
  // hiddenOverride——对话框内的 fullApp 快照不随宿主列表重拉更新，点完
  // 「禁用fork源」后按钮必须立刻变「恢复fork源」（否则第二次点击无按钮可用；
  // 且禁用后该源卡片从列表消失，对话框是唯一还能恢复的入口）。
  const [srcForkDisabledOverride, setSrcForkDisabledOverride] = useState<boolean | null>(null);
  const [srcForkBusy, setSrcForkBusy] = useState(false);
  useEffect(() => {
    setFullApp(null);
    setPanelInfo(null);
    setBodyReady(false);
    setHiddenOverride(null);
    setSrcAddState('idle');
    setSrcForkDisabledOverride(null);
    setSrcForkBusy(false);
    const key = propApp?.key;
    if (!key || !open) return;
    let alive = true;
    const isOff = propApp?.source === 'fnos-official';
    const wantPanel = isOff || (!propApp?.source && !!propApp.installed);
    const name = propApp?.appname || '';
    const pDetail = fetchAppDetail(key).catch(() => null as AppInfo | null);
    const pPanel: Promise<PanelDetailResponse | null> = !wantPanel
      ? Promise.resolve(null)
      // 打开时走前端会话缓存：同一应用二次打开不发面板详情请求，
      // bodyReady 只剩 fetchAppDetail 一个快请求（不再「卡一下」）。
      : (isOff ? fetchPanelDetailCached(name) : fetchInstalledDetail(name)).catch(() => null);
    Promise.all([pDetail, pPanel]).then(([d, pi]) => {
      if (!alive) return;
      if (d) setFullApp(d);
      if (pi) setPanelInfo(pi);
      setBodyReady(true);
    });
    return () => { alive = false; };
  }, [propApp?.key, open]);
  const app = fullApp ?? propApp;
  // 0.6.326 溯源：禁用态显示值 = 本地即时回显优先，未点击过则取落库值。
  const hiddenNow = hiddenOverride ?? !!app?.user_hidden;
  // 0.6.326d（H3/H9 闭环）：溯源行取值。
  // - upstreamLabel：Moo 已收录该上游源时显示源名，否则回落 owner/repo。
  // - upstreamHref：优先用后端给的 upstream_url，缺失时按 upstream_repo 拼
  //   GitHub 地址（两条分支后端都恒填 repo，直链兜底保证一定可点）。
  const upstreamLabel = app?.upstream_source || app?.upstream_repo || '';
  const upstreamHref = app?.upstream_url || (app?.upstream_repo ? `https://github.com/${app.upstream_repo}` : '');
  // 「添加为源」：把上游仓库一键加进 Moo 应用源（POST /api/sources，源管理页
  // 同款助手）。这是 32 张「有 upstream_repo 无 upstream_source」卡的唯一闭环
  // 入口 —— 加源后详情页才显示上游版本，且 applyUpstreamCorrection 首行
  // （UpstreamSource == "" 即跳过）不再拦，「强制上游」策略随之生效。
  // 0.6.326e：「添加为源」实际要加的地址。后端探测过就听它的（本仓可加=上游仓
  // 地址；本仓只是作者打包仓、owner 有索引仓=那个索引仓地址——举一反三：真正
  // 加得上的是它）；未探测（空）则维持旧行为，指向上游仓。
  const upstreamAddURL = app?.upstream_add_url || upstreamHref;
  // 非空 = 探测确认加不上（本仓与 owner 索引仓都没有索引）→ 不出按钮，只给原因。
  const upstreamAddNote = app?.upstream_add_note || '';
  const handleAddUpstreamSource = async () => {
    if (!upstreamAddURL || srcAddState !== 'idle') return;
    setSrcAddState('busy');
    try {
      const entry = await addSource(upstreamAddURL);
      setSrcAddState('done');
      toast.success(`已添加应用源：${entry?.name || upstreamLabel}`, {
        description: '稍后目录刷新即可看到该上游的来源与版本；把更新策略设为「强制上游」即会跟随上游更新。',
      });
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      // 同地址已存在（后端 ErrExists「源已存在」）= 用户目标已达成，按成功处理。
      if (/已存在|同地址/.test(msg)) {
        setSrcAddState('done');
        toast.success(`该上游源已在列表中：${upstreamLabel}`);
        return;
      }
      setSrcAddState('idle');
      toast.error(`添加应用源失败：${msg}`);
    }
  };
  // 0.6.326f：本源 fork 源的禁用态显示值 = 本地即时回显优先，否则取落库值。
  const forkSourceDisabled = srcForkDisabledOverride ?? !!app?.source_disabled;
  // 「禁用fork源 / 恢复fork源」：把**本源仓库**（该卡 Source 对应的那一条源）
  // 整体禁用/恢复——禁用后该源全部卡片从目录列表消失（聚合跳过禁用源），
  // 恢复即回来。只在后端判定「本源是 GitHub fork」时渲染（source_is_fork）。
  const handleToggleForkSource = async () => {
    const src = app?.source;
    if (!src || srcForkBusy) return;
    // 端点的参数语义是 **enabled**（启用与否），与本地「禁用态」互为反义。
    // 0.6.326f 首版直接把「目标禁用态」当 enabled 传，结果点「禁用fork源」
    // 反而又启用了一次（E2E B1/B2 实锤：点完 enabled 仍为 true）——这里
    // 必须先算目标禁用态，再取反传 enabled。
    const willDisable = !forkSourceDisabled;
    setSrcForkBusy(true);
    try {
      await toggleSource(src, !willDisable);
      setSrcForkDisabledOverride(willDisable);
      toast.success(
        willDisable ? `已禁用源「${src}」，其应用已从列表隐藏` : `已恢复源「${src}」`,
      );
      onSourceToggled?.();
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      toast.error(`操作应用源失败：${msg}`);
    } finally {
      setSrcForkBusy(false);
    }
  };
  // 0.6.326g（用户定稿）：溯源第二行三钮恒显 + 置灰 + 与上方元信息胶囊同语言。
  // tracePill(active, disabled)：
  //   - META_PILL 打底（中性灰 bg-muted/50 + border/40 + text-muted-foreground）；
  //   - active（生效/选中）→ bg-primary/15 + text-primary + border-primary/40 变蓝；
  //   - disabled → opacity-50 + cursor-not-allowed，并用 `disabled:hover:*` 抵消
  //     META_PILL 的 hover:bg-muted（disabled 元素在 Chromium 下仍会命中 :hover，
  //     必须显式复原底色才叫「不响应 hover 变色」）。
  const tracePill = (active: boolean, disabled: boolean) =>
    cn(
      META_PILL,
      active && "bg-primary/15 text-primary border-primary/40",
      disabled && "opacity-50 cursor-not-allowed",
      disabled && (active ? "disabled:hover:bg-primary/15" : "disabled:hover:bg-muted/50"),
    );
  // ① 添加上游源：上游源已在列表 / 后台探测判定加不上 → 恒显但置灰。
  const addUnavailable = !!app?.upstream_source || !!upstreamAddNote;
  const addDisabled = addUnavailable || srcAddState !== 'idle';
  // 0.6.326i（P1）：上游源已经在源列表里时，胶囊必须**直接说「已添加」**。
  // 旧实现只把这件事塞进 disabled + title（tooltip）——用户不悬停根本看不见
  //（现网 1120 张卡受影响，用户实测反馈「并不提示已添加这个上游源」）。
  // addDone = 本地刚加成功（srcAddState==='done'）或后端已解析出上游源名。
  const addDone = srcAddState === 'done' || !!app?.upstream_source;
  const addTitle = app?.upstream_source
    ? `上游源「${app.upstream_source}」已在应用源列表中`
    : (upstreamAddNote || `把 ${upstreamAddURL || app?.upstream_repo || upstreamLabel} 添加为 Moo 应用源`);
  // ② 禁用fork源：本源仓库不是 GitHub fork 搬运源时置灰。
  const forkDisabled = !app?.source_is_fork || srcForkBusy;
  const forkTitle = !app?.source_is_fork
    ? '该源不是搬运 fork 源，无需禁用'
    : (forkSourceDisabled ? `恢复应用源「${app.source}」` : `禁用应用源「${app.source}」（其应用从列表隐藏）`);
  // ③ 禁用应用：宿主没给 onToggleHide（例如只读场景）时置灰。
  const hideDisabled = !onToggleHide;
  const hideTitle = hiddenNow
    ? '恢复显示该应用'
    : (onToggleHide ? '隐藏该应用（已安装的仍可在「已安装」中找回）' : '当前场景不支持禁用应用');
  // 极光模式（0.6.297，用户定稿「只是底板用这种渐变，页面其他元素不用」）：
  // 面板底 = 该应用极光卡同款渐变（同应用恒同色）；内容元素一律不改样式，
  // 靠一层 bg-background/65+blur 玻璃遮罩把渐变压成可读底色染色。
  const aBg = aurora && app ? auroraFor(app.appname || app.display_name) : '';
  // 0.6.303（用户实抓）：移动端极光渐变此前铺满全屏对话框，内卡四周露出
  // 渐变「溢出一圈」——与网页端（渐变=对话框底板、四周是页面背景）观感
  // 不一致。移动端改法：渐变只给 12px 内缩的玻璃层面板（圆角收边），
  // 全屏对话框回普通底色；内卡毛玻璃罩在渐变上 = 与网页端同款柔和染色。
  const isDesktop = useIsDesktop();
  const mobileAuroraPanel = !isDesktop && aBg !== '';
  // 0.6.316 ①：手机桌面模拟（飞牛 app 桌面模式）→ 对话框挂 .mds-sim 去玻璃
  const mdsSim = useMobileDesktopSim();
  // 安装/更新/卸载完成（operation 从有值变无值）后重拉一次详情，同步已装状态、
  // 版本与主操作行——detailApp 是打开时的静态快照，不重拉的话头部 GET 位与
  // 底部操作区会停留在装前状态（与「装完即变」不符）。
  const prevOperationRef = useRef<AppOperation | undefined>(undefined);
  useEffect(() => {
    const wasBusy = !!prevOperationRef.current;
    prevOperationRef.current = operation;
    if (!wasBusy || operation || !open) return;
    const key = propApp?.key;
    if (!key) return;
    let alive = true;
    const isOff = propApp?.source === 'fnos-official';
    const wantPanel = isOff || (!propApp?.source && !!propApp.installed);
    const name = propApp?.appname || '';
    const pDetail = fetchAppDetail(key).catch(() => null as AppInfo | null);
    const pPanel: Promise<PanelDetailResponse | null> = !wantPanel
      ? Promise.resolve(null)
      : (isOff ? fetchPanelDetail(name) : fetchInstalledDetail(name)).catch(() => null);
    Promise.all([pDetail, pPanel]).then(([d, pi]) => {
      if (!alive) return;
      if (d) setFullApp(d);
      if (pi) setPanelInfo(pi);
    });
    return () => { alive = false; };
  }, [operation, open, propApp?.key, propApp?.source, propApp?.installed, propApp?.appname]);
  const [readme, setReadme] = useState<string | null>(null);
  // 0.6.280（用户定稿）：README 与更新日志均为「固定上限独立滚动小框」，无展开/收起状态
  const [readmeError, setReadmeError] = useState('');
  // 0.6.326 低风险项 #5：源声明了 has_readme 但 asset 端点确定性 404
  // （上游 README 已删/地址失效，如 boombird）→ 整块隐藏，不留空框。
  const [readmeMissing, setReadmeMissing] = useState(false);
  const isOfficial = app?.source === 'fnos-official';
  const [lightbox, setLightbox] = useState<number | null>(null);
  const [lightboxLoading, setLightboxLoading] = useState(false);
  const [lightboxError, setLightboxError] = useState(false);
  const [lightboxRetry, setLightboxRetry] = useState(0);
  // 「下载 fpk」：后台任务 + SSE 实时视图。下载解耦到服务端后台，关闭详情页后
  // 继续跑；可暂停/继续（.part 断点续传）。进度显示在按钮内。
  const [dlBusy, setDlBusy] = useState(false);
  const [dlPct, setDlPct] = useState<number | null>(null);
  // 镜像无 Content-Length 时后端给不出总量：改显示已下载大小（不确定进度条）
  const [dlBytes, setDlBytes] = useState(0);
  const [dlPaused, setDlPaused] = useState(false);
  const sseActiveRef = useRef(false);
  // 下载状态「归属」哪个应用：对话框实例是复用的（切应用不换组件），
  // 状态/SSE 必须按 appname 重归属，否则 A 的下载进度会串到 B 的按钮上
  // （表现：没下载的应用，按钮却显示「暂停/下载中」）。
  const dlOwnerRef = useRef<string>('');
  // 0.6.319g（用户定稿）：底部悬浮动作区实测高度——内容滚动框据此设
  // paddingBottom，滚到底时最后一行文字正好停在胶囊上方、不被遮挡；
  // ResizeObserver 跟踪（忽略更新药丸/主操作行出现、下载条显隐都会变高）
  const [dlAreaH, setDlAreaH] = useState(0);
  const dlAreaRoRef = useRef<ResizeObserver | null>(null);
  // callback ref（稳定身份）：Radix portal 内容挂载晚于本组件 effect，
  // 用 ref 回调在节点真正挂上时建观察器，避免漏测（首帧 height=0）
  const onDlAreaRef = useCallback((el: HTMLDivElement | null) => {
    dlAreaRoRef.current?.disconnect();
    dlAreaRoRef.current = null;
    if (!el) { setDlAreaH(0); return; }
    const measure = () => setDlAreaH(el.offsetHeight);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    dlAreaRoRef.current = ro;
  }, []);
  const dlSSERef = useRef<SSEHandle | null>(null);
  const handleDownloadFpk = () => {
    if (dlBusy || dlPaused || !app) return;
    const owner = app.appname;
    dlOwnerRef.current = owner;
    sseActiveRef.current = true;
    setDlBusy(true);
    setDlPct(null);
    setDlBytes(0);
    let doneMsg = '';
    const h = downloadFpk(owner, (ev) => {
      if (dlOwnerRef.current !== owner) return; // 已切到别的应用：不再更新状态
      if (ev.step === 'done' && ev.message) doneMsg = ev.message;
      if (ev.step === 'downloading' && typeof ev.downloaded === 'number') {
        if (ev.total && ev.total > 0) {
          setDlBytes(0);
          setDlPct(Math.min(99, Math.round((ev.downloaded / ev.total) * 100)));
        } else if (ev.downloaded > 0) {
          // 总量未知（镜像无 Content-Length）：显示已下载大小
          setDlPct(null);
          setDlBytes(ev.downloaded);
        }
      }
    });
    dlSSERef.current = h;
    h.promise
      // 页面不可见（用户已离开/关闭窗口）时到达的完成/错误事件不弹通知——
      // 重新打开时由按钮轮询恢复状态，避免「再进去还挂着通知栏」。
      .then(() => {
        if (dlOwnerRef.current !== owner) return;
        if (!document.hidden) toast.success(doneMsg || 'FPK 下载完成');
        // 0.6.325 C5：下载完成 → 重派生「已下载」态（按钮从进度回落到「已下载」）
        refreshLocalFpk(owner, app?.display_name);
      })
      .catch((e: unknown) => {
        // 切应用时主动 cancel 的 SSE 会以 AbortError 落到这里：不是下载失败
        if (e instanceof DOMException && e.name === 'AbortError') return;
        if (dlOwnerRef.current === owner && !document.hidden) toast.error(e instanceof Error ? e.message : 'FPK 下载失败');
      })
      .finally(() => {
        // 状态已归属别的应用（已切走）：旧应用的复位动作不能带到新应用上
        if (dlOwnerRef.current !== owner) return;
        sseActiveRef.current = false;
        setDlBusy(false);
        setDlPct(null);
        setDlBytes(0);
      });
  };
  const handleDlPause = async () => {
    if (!app) return;
    try {
      await pauseDownload(app.appname);
      toast.success(`已暂停 ${app.appname} 下载`);
      sseActiveRef.current = false;
      setDlBusy(false);
      setDlPaused(true);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '暂停失败');
    }
  };
  const handleDlResume = async () => {
    if (!app) return;
    try {
      await resumeDownload(app.appname);
      toast.success(`继续下载 ${app.appname}`);
      setDlPaused(false);
      setDlBusy(true);
      setDlPct(null);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '继续失败');
    }
  };
  // 打开详情页时恢复后台下载任务状态（从别处/上次启动的下载），并跟踪暂停/进行
  useEffect(() => {
    if (!app) return;
    const appname = app.appname;
    const prevOwner = dlOwnerRef.current;
    dlOwnerRef.current = appname;
    if (prevOwner !== appname) {
      // 切到另一个应用：结束上一个应用的 SSE（服务端后台下载不受影响，
      // 其状态由下方轮询接管），并复位按钮状态——不复位的话，上一个应用
      // 「下载中/暂停」的状态会串到当前应用按钮上（未下载却显示「暂停」）。
      if (dlSSERef.current) { dlSSERef.current.cancel(); dlSSERef.current = null; }
      sseActiveRef.current = false;
      setDlBusy(false);
      setDlPaused(false);
      setDlPct(null);
      setDlBytes(0);
    }
    let cancelled = false;
    const poll = async () => {
      try {
        const list = await fetchTasks();
        if (cancelled) return;
        const t = list.find((x) => x.appname === appname && x.op === 'download');
        if (t && (t.status === 'running' || t.status === 'queued')) {
          setDlPaused(false);
          setDlBusy(true);
          if (t.total && t.total > 0 && typeof t.downloaded === 'number') {
            setDlBytes(0);
            setDlPct(Math.min(99, Math.round((t.downloaded / t.total) * 100)));
          } else if (typeof t.downloaded === 'number' && t.downloaded > 0) {
            setDlPct(null);
            setDlBytes(t.downloaded);
          }
        } else if (t && t.status === 'paused') {
          setDlBusy(false);
          setDlPaused(true);
        } else if (!sseActiveRef.current) {
          // 无进行中/暂停任务，且本页面未在流式下载 → 复位
          setDlBusy(false);
          setDlPaused(false);
          setDlPct(null);
          setDlBytes(0);
        }
      } catch { /* 忽略轮询错误 */ }
    };
    poll();
    const timer = window.setInterval(poll, 3000);
    return () => { cancelled = true; window.clearInterval(timer); };
  }, [app]);

  // ── 0.6.325 C5：「已下载」态 = 本应用 FPK 已在本地缓存（从缓存列表实时派生，
  //    不靠内存）：按钮变「已下载」，点击=直接安装该缓存文件（不重新下载）。
  //    派生时机=打开/切应用（下方 effect）+ 本对话框内下载完成（handleDownloadFpk
  //    .then 调 refreshLocalFpk）。在「下载」页删除该文件后重开详情=自动回「下载 fpk」。
  const [localFpkName, setLocalFpkName] = useState<string | null>(null);
  // 0.6.325 C9：首次「已下载」派生是否完成——未完成前按钮显示「检查中…」并禁用，
  // 消除冷启动 0.41s 的误闪「下载 fpk」与误点（见 VERIFY_325C5 §8.2）。
  const [localFpkChecked, setLocalFpkChecked] = useState(false);
  const [localInstalling, setLocalInstalling] = useState(false);
  const [localInstallMsg, setLocalInstallMsg] = useState('');
  const [localWizardDef, setLocalWizardDef] = useState<AppWizard | null>(null);
  const refreshLocalFpk = useCallback((appname: string, displayName?: string) => {
    let cancelled = false;
    setLocalFpkChecked(false);
    fetchFpkDownloads()
      .then((res) => {
        if (cancelled) return;
        // 匹配口径（0.6.325 C9 归一化）：
        // 主口径 = 缓存包 appname 归一后等于当前应用 appname 归一（两者都非空）；
        // 次口径 = 缓存包 appname 归一后为空（解析失败）时，回落 display_name 归一相等。
        const na = normFpk(appname);
        const nd = normFpk(displayName);
        const hit = (res.files || []).find((f) => {
          const fa = normFpk(f.appname);
          if (fa && na && fa === na) return true;
          if (!fa) return !!(nd && normFpk(f.display_name) === nd);
          return false;
        });
        setLocalFpkName(hit ? hit.name : null);
      })
      .catch(() => { if (!cancelled) setLocalFpkName(null); })
      .finally(() => { if (!cancelled) setLocalFpkChecked(true); });
    return () => { cancelled = true; };
  }, []);
  useEffect(() => {
    if (!app) { setLocalFpkName(null); return; }
    return refreshLocalFpk(app.appname, app.display_name);
  }, [app, refreshLocalFpk]);
  // 点击「已下载」→ 直接安装缓存 FPK（SSE 进度；必填无默认值的字段弹向导，
  // 与 FpkDownloads 组件同一探测/回落模式）
  const runLocalInstall = (name: string, wizard?: WizardParam[]) => {
    setLocalInstalling(true);
    setLocalInstallMsg('准备安装...');
    installFpkDownload(name, (ev) => {
      if (ev.step === 'error' || ev.error) return;
      if (ev.message) setLocalInstallMsg(ev.message);
    }, wizard).promise
      .then(() => {
        toast.success(`已安装 ${name}`);
        // 文件保留在缓存 → localFpkName 不变（仍是「已下载」）；宿主刷新目录/计数
        onLocalInstallDone?.();
      })
      .catch((e: unknown) => {
        toast.error(e instanceof Error ? e.message : '安装失败');
      })
      .finally(() => {
        setLocalInstalling(false);
        setLocalInstallMsg('');
      });
  };
  const handleLocalInstall = () => {
    if (!localFpkName || localInstalling) return;
    const name = localFpkName;
    setLocalInstalling(true);
    setLocalInstallMsg('加载安装向导...');
    fetchFpkDownloadWizard(name)
      .then((wz) => {
        if (wz.has_wizard && (wz.content?.length ?? 0) > 0) {
          setLocalWizardDef(wz);
          setLocalInstalling(false);
          setLocalInstallMsg('');
          return;
        }
        runLocalInstall(name);
      })
      .catch(() => {
        // 探测失败不阻塞安装 → 直接装，由服务端 AutoFill 报明确错误
        runLocalInstall(name);
      });
  };
  // 灯箱换图动画方向：open=首次打开(缩放进入) / next / prev(左右滑入，消除生硬跳切)
  const [lightboxAnim, setLightboxAnim] = useState<'open' | 'next' | 'prev'>('open');
  // 预览轮播：当前可见图索引（按滚动位置更新，驱动圆点/计数/箭头）
  const [previewIndex, setPreviewIndex] = useState(0);
  const carouselRef = useRef<HTMLDivElement>(null);
  // 灯箱滑动切换：pointer 拖拽（触屏/鼠标通用），水平位移足够才翻页
  const lightboxDrag = useRef<{ x: number; y: number; swiping: boolean } | null>(null);
  // ── 0.6.227：预览图双指缩放（pinch-zoom）+ 缩放后单指拖动平移 ──────────────
  // 交互口径：
  //  · 双指捏合 = 以两指中点为锚点缩放（1×–5×），手松开停在当前倍数；
  //  · 缩放态下单指拖动 = 平移图片（带边界，防拖出屏幕）；
  //  · 缩回 1× 自动归位；缩放态下**禁用**点按关闭与左右滑动翻页（避免误触）；
  //  · 桌面端滚轮也可缩放（调试与桌面体验）。
  // 实现要点：用 pointer events 同时跟踪多个触点（pointerId → 坐标），
  // 两指时算距离比值得到倍数；锚点数学保证「手指下那一点不动」。
  const ZOOM_MAX = 5;
  const [zoom, setZoom] = useState<{ s: number; x: number; y: number }>({ s: 1, x: 0, y: 0 });
  const pointers = useRef<Map<number, { x: number; y: number }>>(new Map());
  const pinchRef = useRef<{ dist: number; cx: number; cy: number; s0: number; x0: number; y0: number; rectCx: number; rectCy: number; rectW: number; rectH: number } | null>(null);
  const zoomPanRef = useRef<{ x: number; y: number; tx: number; ty: number; rectW: number; rectH: number } | null>(null);
  const zoomClamp = (s: number) => Math.min(ZOOM_MAX, Math.max(1, s));
  const zoomPanClamp = (x: number, y: number, s: number, w: number, h: number) => {
    const mx = Math.max(0, ((s - 1) * w) / 2);
    const my = Math.max(0, ((s - 1) * h) / 2);
    return { x: Math.min(mx, Math.max(-mx, x)), y: Math.min(my, Math.max(-my, y)) };
  };
  // 切图 / 关闭灯箱时缩放归位（否则下一张会带着上一张的倍数与位移）
  useEffect(() => {
    setZoom({ s: 1, x: 0, y: 0 });
    pointers.current.clear();
    pinchRef.current = null;
    zoomPanRef.current = null;
  }, [lightbox]);
  // 悬浮返回钮（移动端）：磨玻璃圆钮磁吸贴左缘（半露出）—— x 恒锁定左边缘，
  // 只能沿左缘纵向拖动（y 持久化）；静置 = 比背景浅一档的半透白磨玻璃（不影响阅读），
  // 拖动 = 加深为页面背景色 + 微放大；细线 ‹ 箭头右移、露出区内完全可见；轻点 = 返回。
  const BACK_X = -32; // 56px 圆钮露出 24px，紧贴左边缘（加大点击区）
  // 0.6.322：iOS 刘海屏安全区补偿——viewport-fit=cover 下 window.innerHeight 是
  // 整屏物理高度，原几何中心 (H-56)/2 落在「感知可见区」(扣除顶部刘海/底部 home 条)
  // 中心之上，返回钮显得偏上（用户 iOS 实抓）；Android 无安全区(=0)行为不变。
  // 改为对可见安全区 [sTop, H-sBottom] 居中：默认 y = sTop + (H-sTop-sBottom-56)/2。
  const [backY, setBackY] = useState<number>(() => {
    // 322R4：不再读 localStorage 旧值——旧版（322/322R3）把「整屏几何中心」落盘，
    // 读回会覆盖安全区补偿 → 仍偏上（用户 iOS 实抓）。返回钮本就不可定位（拖动=
    // 滚动页面、松手弹回原位，见 onPointerUp 的 moveBackY(d.oy)），持久化无意义，
    // 恒用「对可见安全区居中」的补偿默认值。
    let sTop = 0, sBottom = 0;
    try {
      const probe = document.createElement('div');
      probe.style.cssText = 'position:fixed;inset:0;visibility:hidden;pointer-events:none;padding:env(safe-area-inset-top,0px) 0 env(safe-area-inset-bottom,0px) 0;';
      document.body.appendChild(probe);
      const cs = getComputedStyle(probe);
      sTop = parseFloat(cs.paddingTop) || 0;
      sBottom = parseFloat(cs.paddingBottom) || 0;
      probe.remove();
    } catch { /* ignore */ }
    const H = window.innerHeight;
    return Math.max(sTop + 8, Math.round(sTop + (H - sTop - sBottom - 56) / 2)); // 对可见安全区居中
  });
  const [backDragging, setBackDragging] = useState(false);
  const backDragRef = useRef<{ sx: number; sy: number; oy: number; st0: number; moved: boolean } | null>(null);
  // 0.6.321r5b 方案B：返回钮纵向拖动=滚动主内容框（跟手式，相对增量）
  const backScrollRef = useRef<HTMLDivElement | null>(null);
  const moveBackY = (ny: number) => {
    setBackY(ny); // 322R4：不落盘——返回钮不可定位（拖动=滚动页面、松手弹回），持久化只会留旧偏上值
  };
  // 0.6.321r5b 方案B：手指位移 dy → scrollTop = 起始 − dy（内容跟手；
  // 直接写 DOM，不 setState、零重渲染；钳在 [0, maxScroll] 无橡皮筋）
  const backScrollByDelta = (dy: number, st0: number) => {
    const sc = backScrollRef.current;
    if (!sc) return;
    const maxScroll = sc.scrollHeight - sc.clientHeight;
    if (maxScroll <= 0) return; // 内容不可滚：无效果
    sc.scrollTop = Math.max(0, Math.min(maxScroll, st0 - dy));
  };

  // 灯箱切换图片：重置加载/错误态 + 预加载下一张（弱网下少一次白等）
  // 注意：previewCount/previewSrc 在下方 early-return 之后才声明，这里自包含计算。
  useEffect(() => {
    if (lightbox == null || !app) return;
    setLightboxLoading(true);
    setLightboxError(false);
    const official = app.source === 'fnos-official';
    const poster = official ? (panelInfo?.app.appDetail?.poster ?? []) : [];
    const usePoster = official && poster.length > 0;
    const pc = usePoster ? poster.length : (app.preview_count || 0);
    if (pc > 1) {
      const next = new Image();
      next.src = usePoster
        ? (poster[(lightbox + 1) % pc] ?? '')
        : assetUrl(app.key || app.appname, 'preview', (lightbox + 1) % pc);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lightbox, app?.key, open, panelInfo]);

  // 切换应用时预览轮播回第一张
  useEffect(() => {
    setPreviewIndex(0);
    carouselRef.current?.scrollTo({ left: 0 });
  }, [app?.key]);

  // 轮播滚动 → 以容器中线最近的一张为当前页
  const onCarouselScroll = () => {
    const el = carouselRef.current;
    if (!el) return;
    const mid = el.scrollLeft + el.clientWidth / 2;
    let best = 0;
    let bestDist = Infinity;
    Array.from(el.children).forEach((child, i) => {
      const c = child as HTMLElement;
      const d = Math.abs(c.offsetLeft + c.offsetWidth / 2 - mid);
      if (d < bestDist) { bestDist = d; best = i; }
    });
    setPreviewIndex(best);
  };
  const scrollToPreview = (i: number) => {
    const el = carouselRef.current;
    if (!el) return;
    const target = el.children[Math.max(0, Math.min((el.children.length || 1) - 1, i))] as HTMLElement | undefined;
    target?.scrollIntoView({ behavior: 'smooth', inline: 'center', block: 'nearest' });
  };

  // 已装但无源元数据的应用（自装 FPK/系统自带）：渲染层判断是否用面板详情字段。
  // 拉取本身并入上方 bodyReady 统一 effect（与详情并行、一次提交）。
  const noSource = !!app && !app.source;
  // 「下载 fpk」条显示条件：有可下载 FPK 安装包的应用——
  // 社区源（镜像链下载）+ 官方应用（daemon cloud 通道免登录下载：FPK 型
  // 直接落盘，TPK 型目录重打包为标准 FPK——0.6.261 起官方全类型可用）。
  const downloadFpkVisible = !!app && !noSource;
  // 0.6.325 C9：首次本地缓存派生未完成 → 按钮显示「检查中…」并禁用（不响应点击）。
  // 下载中/暂停/安装中优先级更高，不落此态。
  const fpkChecking = downloadFpkVisible && !localFpkChecked && !dlBusy && !dlPaused && !localInstalling;

  // 切换应用时重新拉 README
  useEffect(() => {
    // 0.6.326：未声明 README 的条目也要试（后端会做 <appname>/README.md
    // 惰性自动发现）；失败时前端静默隐藏，不显示错误块。
    if (!app || !open) {
      setReadme(null);
      setReadmeError('');
      setReadmeMissing(false);
      return;
    }
    let cancelled = false;
    setReadme(null);
    setReadmeError('');
    setReadmeMissing(false);
    apiFetch(assetUrl(app.key || app.appname, 'readme'))
      .then(async (r) => {
        if (!r.ok) {
          let msg = `README 加载失败（HTTP ${r.status}）`;
          try {
            const j = await r.json();
            if (j && j.error) msg = j.error;
          } catch {}
          const err: any = new Error(msg);
          err.status = r.status;
          throw err;
        }
        return r.text();
      })
      .then((text) => { if (!cancelled) setReadme(text); })
      .catch((e) => {
        if (cancelled) return;
        if (e?.status === 404) {
          setReadmeMissing(true);
          return;
        }
        setReadmeError(e?.message || 'README 加载失败（源服务器不可达）');
      });
    return () => { cancelled = true; };
  }, [app?.key, open, app?.has_readme]);

  if (!app) return null;

  const isInstalled = app.installed;
  const canUpdate = isInstalled && app.has_update;
  // 面板详情可用范围：官方应用 + 无源已装应用（installed-detail 补全）
  const hasPanelInfo = isOfficial || noSource;
  // 预览图源：官方卡优先面板 poster（CDN 直链；平台只给 25 个自研
  // 应用提供），没有则回退 asset 通道——官方合并卡的 preview_urls 可能
  // 是从同名社区源借来的（后端 catalog 里已挂好），社区卡本来就走它。
  const posterUrls: string[] = hasPanelInfo ? (panelInfo?.app.appDetail?.poster ?? []) : [];
  const usePoster = isOfficial && posterUrls.length > 0;
  const previewCount = usePoster ? posterUrls.length : (app.preview_count || 0);
  const previewSrc = (i: number): string =>
    usePoster ? posterUrls[i] : assetUrl(app.key || app.appname, 'preview', i);
  // 打开目标（daemon appServiceInfo）；仅运行中的应用提供"打开"
  const openUrl = isInstalled ? appWebUrl(app) : null;
  const canControl = isInstalled && !!onControl && (app.start_stop ?? true) && app.status !== 'nostart';
  const controlBusy = app.status === 'starting' || app.status === 'stopping';

  // 主操作行：放在内容区最底部（「下载 fpk」全宽条上方），图标行不再放动作按钮。
  //   未安装 = 安装（主色，占满）
  //   有更新 = 更新（中性）+ 卸载（中性）（各占一半）
  //   运行中 = 打开（有 Web 入口时，头部 GET 位）+ 停用（中性）+ 卸载（中性）
  //   已停止 = 启动（中性）+ 卸载（中性）（各占一半）
  //   0.6.320r2（用户定稿）：底部主操作行全中性胶囊不带色；颜色只留给头部 GET 位（安装/打开）
  const actionBtnCls = "h-10 rounded-xl text-[13px] font-semibold flex items-center justify-center gap-1.5 flex-1 min-w-0";
  // 头部 GET 位胶囊（未安装=安装 / 安装中=转圈；已装应用的主操作在底部主操作行）
  const headerPillCls = "h-9 px-4 rounded-full text-[13px] font-semibold flex items-center justify-center gap-1.5 shrink-0";
  const uninstallBtn = onUninstall ? (
    // 0.6.326h：回到旧形态——点「卸载」直接关详情页并交给宿主 App 的
    // 「确认卸载」AlertDialog 二次确认（确认才发请求；取消则零请求）。
    <button
      type="button"
      onClick={() => { onOpenChange(false); onUninstall(app); }}
      disabled={!!operation || controlling !== null}
      aria-label={`卸载 ${app.display_name}`}
      // 0.6.319b（用户定稿）：卸载=中性胶囊（原红色调移除，颜色只留给主操作）
      className={cn(actionBtnCls, "bg-muted/60 text-foreground border border-border/50 hover:bg-muted disabled:opacity-50")}
    >
      <Trash2 className="h-3.5 w-3.5" />
      卸载
    </button>
  ) : null;
  // 未安装应用的主操作 = 头部「安装」胶囊（图标旁），底部不重复渲染
  const actionRow = !isInstalled ? null : operation ? (
    <button disabled className={cn(actionBtnCls, "bg-primary/15 text-primary border border-primary/40 opacity-70")}>
      <Loader2 className="h-3.5 w-3.5 animate-spin" />
      处理中
    </button>
  ) : canUpdate ? (
    <>
      {/* 0.6.320r2（用户定稿）：更新=中性胶囊，与「卸载」同款不带任何颜色 */}
      <button
        onClick={() => { onOpenChange(false); onUpdate(app); }}
        aria-label={`更新 ${app.display_name}`}
        className={cn(actionBtnCls, "bg-muted/60 text-foreground border border-border/50 hover:bg-muted disabled:opacity-50")}
      >
        <RefreshCw className="h-3.5 w-3.5" />
        更新
      </button>
      {uninstallBtn}
    </>
  ) : isInstalled && controlBusy ? (
    <button disabled className={cn(actionBtnCls, "border border-border/60 text-muted-foreground")}>
      <Loader2 className="h-3.5 w-3.5 animate-spin" />
      {app.status === 'starting' ? '启动中' : '停用中'}
    </button>
  ) : isInstalled && app.status === 'running' ? (
    <>
      {/* 「打开」在头部 GET 位（与安装同一位置）；此处 = 停用 + 卸载 各占一半 */}
      <button
        onClick={() => onControl?.(app, 'stop')}
        disabled={controlling !== null}
        aria-label={`停用 ${app.display_name}`}
        // 0.6.319b（用户定稿）：停用=中性胶囊（原琥珀色移除）
        className={cn(actionBtnCls, "bg-muted/60 text-foreground border border-border/50 hover:bg-muted disabled:opacity-50")}
      >
        <Square className="h-3 w-3 fill-current" />
        停用
      </button>
      {uninstallBtn}
    </>
  ) : isInstalled && canControl ? (
    <>
      {/* 0.6.320r（用户定稿）：启动=中性胶囊，与「卸载」完全同款（原绿色、主色蓝都不要，不带任何颜色） */}
      <button
        onClick={() => onControl?.(app, 'start')}
        disabled={controlling !== null}
        aria-label={`启动 ${app.display_name}`}
        className={cn(actionBtnCls, "bg-muted/60 text-foreground border border-border/50 hover:bg-muted disabled:opacity-50")}
      >
        <Play className="h-3 w-3 fill-current" />
        启动
      </button>
      {uninstallBtn}
    </>
  ) : isInstalled ? uninstallBtn : null;

  const getStatusColor = (status: string) => {
    switch (status) {
      case 'running': return 'text-emerald-500 fill-emerald-500';
      case 'stopped': return 'text-amber-500 fill-amber-500';
      default: return 'text-muted-foreground/40';
    }
  };

  const getStatusText = (status: string) => {
    switch (status) {
      case 'running': return '运行中';
      case 'stopped': return '已停止';
      default: return status || '未安装';
    }
  };

  const formatDate = (dateStr?: string) => {
    if (!dateStr) return '-';
    try {
      return new Date(dateStr).toLocaleString('zh-CN', {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
      });
    } catch {
      return dateStr;
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* 移动端 = 整页展示（左上角返回按钮退回应用列表，App Store 同构）；
          桌面端保持居中对话框 */}
      <DialogContent
        onInteractOutside={(e) => {
          // 0.6.301：打开后 400ms 免疫窗——忽略外点/遮罩点击（双击第二下防秒关）
          if (Date.now() - openedAtRef.current < 400) e.preventDefault();
        }}
        className={cn("inset-0 w-full h-full max-w-none rounded-none sm:rounded-[18px] translate-x-0 translate-y-0 flex flex-col !p-0 gap-0 overflow-visible sm:overflow-hidden sm:inset-auto sm:left-[50%] sm:top-[50%] sm:h-[min(90vh,920px)] sm:max-w-2xl sm:translate-x-[-50%] sm:translate-y-[-50%] [&>button.absolute]:hidden", // 0.6.306（用户定稿）：标准主题玻璃化——bg-background/70 + blur-2xl，
// 背后 45% 黑遮罩下的列表隐约可见并被真实模糊（iOS 景深）；
// 极光路径（aBg 渐变）不变。背景在对话框打开时静止（body 滚动锁定），
// backdrop-filter 无逐帧重算成本。
// 0.6.313 A2（与设置页同构缺陷域，WEBVIEW_SETTINGS_BUG.md §6 点名同批）：
// 移动端（!isDesktop）玻璃层移到下方内层 absolute 子层——backdrop-filter
// 不再作用于含内容的整屏 fixed 层本体（WebView GPU 合成异常经典绕法）；
// 桌面分支（isDesktop）本体玻璃/极光渐变原样保留，零改动。
isDesktop ? (aBg || "bg-background/70 backdrop-blur-2xl") : "",
// 0.6.316 ①：手机桌面模拟命中 → 去 backdrop-filter + 玻璃底提 /95（index.css .mds-sim）
mdsSim && "mds-sim")}>
        {/* 0.6.313 A2：移动端玻璃层移入内层 absolute 子层（JS 分支与本体
            玻璃同用 isDesktop，640–768px 带=居中卡形态，玻璃自带 sm:rounded
            防四角方角外溢——0.6.298 教训：带 backdrop-filter 的子元素提升
            合成层会逃逸父级 rounded+overflow-hidden 裁剪）。层级：本层 z-0
            → 列布局 div relative z-10 压其上；Radix 关闭钮（button.absolute，
            dialog.tsx 在 children 之后渲染）本页恒隐藏（[&>button.absolute]:hidden，
            移动端用返回箭头、桌面用内容内关闭钮），不受玻璃层影响。 */}
        {!isDesktop && (
          // 0.6.315（iOS 真机修复，与设置页同构缺陷域）：移动端玻璃层去
          // backdrop-filter、底色 /95 近实心——WKWebView GPU 合成异常下
          // 「玻璃出得来、内容不出来」，归零合成面止血。
          <div aria-hidden className="pointer-events-none absolute inset-0 z-0 bg-background/95 sm:rounded-[18px]" />
        )}
        {/* 列布局：头部行冻结在顶部（不随内容滚动），下方内容区独立滚动。
            移动端整体包一张圆角内边框卡（与列表同款）；桌面端卡片透明化。 */}
        {/* 0.6.298：玻璃层自带圆角——带 backdrop-filter 的子元素会提升合成层
            逃逸父级 rounded+overflow-hidden 裁剪，四角方角底色外溢（用户实抓） */}
        <div className={cn(
          "flex-1 min-h-0 flex flex-col sm:px-0 sm:pt-0",
          // 0.6.313 A2：移动端（标准+极光）抬到玻璃层（z-0）之上；桌面零改动
          !isDesktop && "relative z-10",
          // 移动端无极光：原 12px 内缩内容区
          !mobileAuroraPanel && "px-3 pt-3",
          // 移动端极光：玻璃层 = 12px 内缩的圆角极光渐变面板（overflow-hidden
          // 裁掉子元素四角外溢），四周露出页面普通底色 = 与网页端一致
          mobileAuroraPanel && cn("m-3 mb-0 rounded-[18px] overflow-hidden", aBg),
          // 桌面端极光：原方案——整卡渐变 + 70% 玻璃罩
          isDesktop && aBg && "bg-background/70 backdrop-blur-2xl sm:rounded-[18px]",
        )}>
        {/* 0.6.315（iOS 真机修复）：移动端内容卡去 backdrop-filter、底色 /95
            近实心；桌面（sm+）保持透明卡+磨砂玻璃原样（sm:backdrop-blur-xl）。 */}
        {/* 0.6.319g：relative——底部动作区改 absolute 悬浮 overlay，以此为定位基准 */}
        <div className="relative flex-1 min-h-0 flex flex-col bg-card/95 rounded-[18px] border border-white/10 shadow-appstore overflow-hidden sm:bg-transparent sm:backdrop-blur-xl sm:rounded-none sm:border-0 sm:shadow-none">
        {/* 头部行：冻结（应用信息 + 动作胶囊组）。
            0.6.217：底色改透明（原 bg-background 在暗色主题 = 纯黑 #000，
            与卡片底 bg-card 形成黑框；滚动区是独立盒、内容不会滑过头部，
            透明安全，桌面/移动统一）。 */}
        <div className="flex-none border-b border-border/60 px-3 py-3">
          <DialogHeader className="space-y-0">
          {/* 0.6.293（用户定稿）：桌面端「安装/打开」胶囊与关闭叉位置互换——
              GET 位胶囊绝对居中于图标行（与图标同一水平线），「磨玻璃圆圈+叉」
              移到行尾右侧；手机端布局不动（GET 位仍在行尾，叉隐藏）。 */}
          <div className="relative flex items-center gap-3">
            <AppIcon app={app} className="w-12 h-12 rounded-[12px] shrink-0" />
            {/* 收藏星标：图标旁（与列表卡片同一位置语言），实心琥珀 = 已收藏 */}
            {onToggleFavorite && (
              <button
                onClick={() => onToggleFavorite(app)}
                className={cn(
                  /* 0.6.146 用户定稿：星标不要任何圆圈（含焦点环）——触屏点按后
                     :focus-visible 会留下常驻圆环；只保留颜色变化 */
                  "shrink-0 p-1.5 rounded-full transition-colors focus:outline-none focus-visible:outline-none",
                  isFavorite ? "text-amber-500" : "text-muted-foreground/50 hover:text-amber-500"
                )}
                title={isFavorite ? '取消收藏' : '收藏'}
                aria-label={isFavorite ? `取消收藏 ${app.display_name}` : `收藏 ${app.display_name}`}
                aria-pressed={isFavorite}
              >
                <Star className={cn("h-5 w-5", isFavorite && "fill-amber-400 text-amber-500")} />
              </button>
            )}
            <div className="flex-1 min-w-0">
              <DialogTitle className="text-base truncate">{app.display_name}</DialogTitle>
              {/* appname 统一显示在应用名下面（与列表同款） */}
              {app.appname && (
                <div className="text-[13px] text-muted-foreground/80 truncate" title={app.appname}>
                  {app.appname}
                </div>
              )}
            </div>
            {/* 头部 GET 位：未安装 = 安装胶囊；安装中 = 转圈。已装应用的
                主操作在底部「主操作行」。0.6.293：桌面端绝对居中于图标行
                （wrapper sm 以下 = contents 完全透明，手机行为与旧版一致）。 */}
            <div className="contents sm:block sm:absolute sm:left-1/2 sm:top-1/2 sm:-translate-x-1/2 sm:-translate-y-1/2">
              {operation ? (
              /* 0.6.321r6 方案2：「处理中」升级进度胶囊——消息+百分比在对话框内可见
                 （移动端详情页全屏，列表行的进度条在对话框后面看不到） */
              <div className={cn(headerPillCls, "min-w-[84px] max-w-[240px] bg-primary/15 text-primary border border-primary/40 cursor-default")}
                title={operation.message}>
                <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin" />
                <span className="truncate">{operation.message || '处理中'}</span>
                <span className="shrink-0 tabular-nums">{Math.round(operation.progress)}%</span>
              </div>
            ) : !isInstalled ? (
              // 0.6.319F（用户定稿）：安装/打开/下载 FPK 三胶囊去箭头图标，只留文字（网页端同款）
              <Button onClick={() => { onOpenChange(false); onInstall(app); }} className={cn(headerPillCls, "px-5 shadow-sm hover:opacity-90")}>
                安装
              </Button>
            ) : isInstalled && app.status === 'running' && onOpenApp && openUrl ? (
              /* 已装且运行中且有 Web 入口：安装完成后的主操作 = 打开（与安装同一 GET 位） */
              <Button onClick={() => onOpenApp(app)} className={cn(headerPillCls, "px-5 shadow-sm hover:opacity-90")}>
                打开
              </Button>
            ) : null}
            </div>
            {/* 0.6.293：关闭叉 = 行尾「磨玻璃圆圈+叉」（与图标/安装同一水平线；
                触屏隐藏，手机端保持左上角返回） */}
            <button
              onClick={() => onOpenChange(false)}
              aria-label="关闭详情"
              title="关闭"
              className="hidden sm:inline-flex shrink-0 h-8 w-8 items-center justify-center rounded-full bg-card/55 backdrop-blur-xl border border-white/10 text-muted-foreground hover:text-foreground hover:bg-card/80 transition-colors focus:outline-none focus-visible:outline-none"
            >
              <X className="h-4 w-4" />
            </button>
          </div>
          {/* 来源 + 开发者/发布者：图标行下方横排，左缘对齐标题/"未安装"列
              （pl = 图标 48px + gap 12px）；三项与列表同一款蓝框徽章（字号统一）、
              小地球源图标、点击过滤；官方→飞牛应用中心源、内置→fnos-store/conversun */}
          <div className="mt-2 flex flex-wrap items-center">
            {/* 状态（未安装/运行中）：放在图标正下方、与徽章同一行、居中于图标
                （槽宽 = 图标宽 48px，中心与图标中心重合） */}
            <div className="w-12 shrink-0 flex items-center justify-center">
              {isInstalled ? (
                <div className="flex items-center gap-1 whitespace-nowrap text-xs text-muted-foreground">
                  <Circle className={`h-2 w-2 shrink-0 ${getStatusColor(app.status)}`} />
                  <span>{getStatusText(app.status)}</span>
                </div>
              ) : (
                <span className="text-xs text-muted-foreground/70">未安装</span>
              )}
            </div>
            {/* 源/作者/更新状态徽章：左缘与应用名对齐（图标 48 + gap 12 = 60px） */}
            <div className="flex-1 min-w-0 flex flex-wrap items-center gap-2 ml-3">
            {(() => {
              const src = sourceLabel(app);
              const author = effectiveMaintainer(app);
              // 徽章词条已在搜索框（多选叠加）→ 命中徽章渲染选中态（实心 + ✓）
              const aSrc = !!activeTerms && !!src && activeTerms.includes(src);
              const aAuth = !!activeTerms && !!author && activeTerms.includes(author);
              // 0.6.308：选中态=移动端行徽章同款实心蓝（网页端对齐）
              // 0.6.319b：选中=分类胶囊同款蓝（原实心蓝过深）
              const pillCls = (active: boolean) => cn(META_PILL, active && "bg-primary/15 text-primary border-primary/40");
              return (<>
                {src && onSourceFilter && (
                  <button
                    onClick={() => { onOpenChange(false); onSourceFilter(src); }}
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
                    onClick={() => { onOpenChange(false); onAuthorFilter(author); }}
                    className={pillCls(aAuth)}
                    title={aAuth ? `正在筛选「${author}」· 点击清除` : `只看「${author}」开发的应用`}
                  >
                    <User className="h-3 w-3 mt-px shrink-0" />
                    <span className="min-w-0 break-words">{author}</span>
                    {aAuth && <Check className="h-2.5 w-2.5 mt-px shrink-0" />}
                  </button>
                )}
                {/* 官方/无源已装应用的开发者同步自面板详情（后台批量回填前，惰性详情兜底） */}
                {!author && hasPanelInfo && panelInfo?.app.appDetail?.maintainer && (
                  // 0.6.319d（用户定稿）：与其余徽章同款中性浅灰（12px regular，不抢眼）
                  <span className="inline-flex items-start gap-1 rounded-full bg-muted/50 border border-border/40 px-2 py-[3px] max-w-full text-xs leading-[17px] font-normal text-muted-foreground">
                    <User className="h-3 w-3 mt-px shrink-0" />
                    <span className="min-w-0 break-words">{panelInfo.app.appDetail.maintainer}</span>
                  </span>
                )}
              </>);
            })()}
            {app.distributor && app.distributor !== effectiveMaintainer(app) && (() => {
              const aDist = !!activeTerms && activeTerms.includes(app.distributor);
              return (
              <button
                onClick={() => { if (onDistributorFilter) { onOpenChange(false); onDistributorFilter(app.distributor!); } }}
                className={cn(META_PILL, aDist && "bg-primary/15 text-primary border-primary/40")}
                title={onDistributorFilter ? (aDist ? `正在筛选「${app.distributor}」· 点击清除` : `只看「${app.distributor}」发布的应用`) : `发布：${app.distributor}`}
              >
                <Package className="h-3 w-3 mt-px shrink-0" />
                <span className="min-w-0 break-words">发布：{app.distributor}</span>
                {aDist && <Check className="h-2.5 w-2.5 mt-px shrink-0" />}
                {app.distributor_url && (
                  <a href={app.distributor_url} target="_blank" rel="noreferrer" className="inline-flex mt-px hover:text-primary" onClick={(e) => e.stopPropagation()}>
                    <ExternalLink className="h-2.5 w-2.5" />
                  </a>
                )}
              </button>
              );
            })()}
              {/* 0.6.235：应用分类标签（源提供 labels/categories/tags 时映射成分类；
                  没有分类的旧源不显示，保持原观感） */}
              {app.category && (
                <span className={cn(META_PILL, "pointer-events-none")} title={`分类：${categoryLabel(app.category)}`}>
                  <Tag className="h-3 w-3 mt-px shrink-0" />
                  <span className="min-w-0 break-words">{categoryLabel(app.category)}</span>
                </span>
              )}

              {canUpdate && (
                <Badge variant="secondary" className="bg-primary/10 text-primary border-0 font-medium px-1.5 h-5 text-[11px] rounded-full">
                  有更新
                </Badge>
              )}
              {app.update_ignored && (
                <Badge variant="secondary" className="bg-muted text-muted-foreground border-0 font-medium px-1.5 h-5 text-[11px] rounded-full gap-0.5">
                  <BellOff className="h-2.5 w-2.5" />
                  已忽略更新
                </Badge>
              )}
            </div>
          </div>
          </DialogHeader>
        </div>
        {/* 内容区：独立滚动区（描述 / 预览 / 信息 / 更新说明 / README / 操作）。
            0.6.319g：滚动框延伸到对话框最底（底部动作区不再占 flex 段），
            paddingBottom=动作区实测高度——文字可滚到胶囊下方穿过（胶囊浅蓝
            玻璃半透可见），静止时最后内容正好停在胶囊上方、不被遮。 */}
        {/* 0.6.321r6 方案2：进行中操作的细进度条（冻结头部与内容区之间，全宽）。
            下载阶段=「正在下载 xxx 12.3MB/45.6MB」的实时进度，安装阶段无缝续接。 */}
        {operation && (
          <div className="flex-none px-3 pb-2">
            <Progress value={operation.progress} className="h-1" />
          </div>
        )}
        <div ref={backScrollRef}
          className="flex-1 min-h-0 overflow-y-auto overscroll-contain px-4 py-3 sm:px-5"
          style={dlAreaH > 0 ? { paddingBottom: dlAreaH } : undefined}>
        {!bodyReady ? (
          /* 同步加载骨架：详情完整字段 + 面板详情并行拉取，齐了再一次性渲染
             内容区（描述 3 行 / 预览 1 幅 / 信息 3 行，布局与正式内容大体对应，
             数据到位后无逐波弹出、无高度跳变）。 */
          <div className="space-y-3 pt-1" aria-busy>
            <div className="space-y-2">
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-4 w-11/12" />
              <Skeleton className="h-4 w-2/5" />
            </div>
            <Skeleton className="h-44 w-full rounded-xl" />
            <div className="space-y-2 pt-2">
              <Skeleton className="h-7 w-44" />
              <Skeleton className="h-7 w-56" />
              <Skeleton className="h-7 w-36" />
            </div>
          </div>
        ) : (
        <>
        {(() => {
          const officialDesc = hasPanelInfo ? panelInfo?.app.appDetail?.desc : undefined;
          if (officialDesc) {
            // 官方描述是 HTML 片段：白名单清洗后按富文本渲染
            return (
              <>
                <DialogDescription
                  className={DESC_RICH_CLS}
                  dangerouslySetInnerHTML={{ __html: DOMPurify.sanitize(officialDesc) }}
                />
                <Separator />
              </>
            );
          }
          if (app.desc_html) {
            // moo.json 扩展（0.6.269）：源显式声明的富文本简介，优先于 desc
            // 猜测式识别；DOMPurify 白名单消毒后渲染（与官方 desc 同策略）
            return (
              <>
                <DialogDescription
                  className={DESC_RICH_CLS}
                  dangerouslySetInnerHTML={{ __html: DOMPurify.sanitize(app.desc_html) }}
                />
                <Separator />
              </>
            );
          }
          if (app.description) {
            const d = app.description;
            // 第三方 desc 同样允许 HTML（与官方 desc 同源写法）：像 HTML 则清洗后按富文本
            // 渲染（<a> 超链接可点，如 QQ 群链接），否则纯文本
            if (readmeLooksLikeHtml(d)) {
              return (
                <>
                  <DialogDescription
                    className={DESC_RICH_CLS}
                    dangerouslySetInnerHTML={{ __html: DOMPurify.sanitize(d) }}
                  />
                  <Separator />
                </>
              );
            }
            return (
              <>
                {/* 0.6.319e（用户定稿）：回 14px 常规 + foreground/70（提一点点亮色，不到应用名那么深） */}
                <DialogDescription className="text-sm leading-relaxed text-foreground/70">
                  {descriptionPlainText(d)}
                </DialogDescription>
                <Separator />
              </>
            );
          }
          return null;
        })()}

        {/* 预览图画廊：App Store 风格大图轮播 —— 手指横滑（touch snap 滚动）/
            桌面端圆点+箭头，点图放大进灯箱（灯箱同样支持左右滑动翻页） */}
        {previewCount > 0 && (
          <>
            <div className="flex items-center justify-between text-xs text-muted-foreground">
              <span className="flex items-center gap-1.5">
                <Images className="h-3.5 w-3.5" />
                预览
              </span>
              <span className="tabular-nums">{previewIndex + 1}/{previewCount}</span>
            </div>
            <div className="relative">
              <div
                ref={carouselRef}
                onScroll={onCarouselScroll}
                className="flex gap-3 overflow-x-auto snap-x snap-mandatory scroll-px-4 px-4 -mx-4 sm:px-0 sm:mx-0 py-1.5 no-scrollbar"
              >
                {Array.from({ length: previewCount }, (_, i) => (
                  <button
                    key={i}
                    onClick={() => { setLightboxAnim('open'); setLightbox(i); }}
                    className="snap-center shrink-0 w-[86%] max-w-[340px] sm:w-[76%] sm:max-w-none rounded-2xl overflow-hidden border border-border/40 hover:opacity-90 active:opacity-90 transition-opacity"
                    title="点击放大"
                  >
                    <img
                      src={previewSrc(i)}
                      alt={`${app.display_name} 预览 ${i + 1}`}
                      loading="lazy"
                      draggable={false}
                      className="w-full aspect-[16/10] object-cover bg-muted/40"
                    />
                  </button>
                ))}
              </div>
              {previewCount > 1 && (
                <>
                  <button
                    onClick={() => scrollToPreview(previewIndex - 1)}
                    disabled={previewIndex === 0}
                    className="hidden sm:flex absolute left-0 top-1/2 -translate-y-1/2 h-8 w-8 items-center justify-center rounded-full bg-background/90 border border-border/50 shadow-sm text-foreground hover:bg-background disabled:opacity-0"
                    aria-label="上一张预览"
                  >
                    <ChevronLeft className="h-4 w-4" />
                  </button>
                  <button
                    onClick={() => scrollToPreview(previewIndex + 1)}
                    disabled={previewIndex === previewCount - 1}
                    className="hidden sm:flex absolute right-0 top-1/2 -translate-y-1/2 h-8 w-8 items-center justify-center rounded-full bg-background/90 border border-border/50 shadow-sm text-foreground hover:bg-background disabled:opacity-0"
                    aria-label="下一张预览"
                  >
                    <ChevronRight className="h-4 w-4" />
                  </button>
                </>
              )}
            </div>
            {previewCount > 1 && (
              <div className="flex justify-center gap-1 mt-1" aria-hidden>
                {Array.from({ length: previewCount }, (_, i) => (
                  <span key={i} className={cn("h-1.5 rounded-full transition-all", i === previewIndex ? "w-4 bg-foreground/60" : "w-1.5 bg-foreground/20")} />
                ))}
              </div>
            )}
            <Separator />
          </>
        )}

        <div className="space-y-0">
          {/* 0.6.326 应用溯源（用户定稿 + 0.6.326-ui 微调）：后端按内容血缘判定
              「本源的包来自其它 owner 仓库」时才下发 upstream_* —— 值优先显示 Moo
              已收录的上游源名，未收录回落 owner/repo。
              0.6.326-ui：本行移到「版本」之上；上游文字与「禁用」二字加粗高亮，
              「禁用」药丸改用主色胶囊（与安装/更新等主操作钮同款浅蓝玻璃语言）。
              0.6.326d（H3/H9）：上游文字变外链 + 未收录上游源时给「添加为源」
              （补上 32 张卡的闭环入口）。
              0.6.326f（用户定稿）：拆两行 —— 第一行「上游 xxx」（外链）；
              第二行依次「添加上游源 / 禁用fork源 / 禁用应用」（括号说明不上屏）。 */}
          {(app.upstream_source || app.upstream_repo) && (
            <DetailRow icon={GitFork} label="溯源">
              <div className="flex flex-col gap-1.5">
                {/* 0.6.326f（用户定稿）：改两行 —— 第一行「上游 xxx」（外链）；
                    第二行依次「添加上游源 / 禁用fork源 / 禁用应用」（顺序固定）。 */}
                {/* 0.6.326d（H3/H9 闭环）：上游文字改成外链 —— 32 张「有
                    upstream_repo 无 upstream_source」的卡原本只有一段死文本，
                    用户既去不了上游仓库，也没有入口把上游加为源（于是溯源只到
                    文本、自动纠正永远不触发）。 */}
                {upstreamHref ? (
                  <a
                    href={upstreamHref}
                    target="_blank"
                    rel="noreferrer"
                    title={`在浏览器打开上游仓库 ${upstreamLabel}`}
                    className="min-w-0 break-words font-semibold text-primary underline decoration-primary/40 underline-offset-2 hover:decoration-primary"
                  >
                    上游 {upstreamLabel}
                  </a>
                ) : (
                  <span className="min-w-0 break-words font-semibold text-primary">
                    上游 {upstreamLabel}
                  </span>
                )}
                {/* 0.6.326g（用户定稿）：三钮**恒显**，不可用时 disabled + 置灰
                    （opacity-50 + cursor-not-allowed，且 hover 不换底色）；
                    样式与上方「源/开发者/发布者」胶囊同语言（META_PILL：默认中性
                    灰，生效/选中态才变蓝 bg-primary/15 text-primary
                    border-primary/40）；间距 gap-2 = 上方元信息行同款 8px。
                    顺序固定：添加上游源 → 禁用fork源 → 禁用应用。 */}
                <div className="flex flex-wrap items-center gap-2">
                  {/* ① 添加上游源（= 原「添加为源」仅改标签）。不可用两种：
                      上游源已在列表（upstream_source 非空），或后台探测判定
                      本仓/owner 索引仓都没有索引（upstream_add_note 非空）。
                      0.6.326i（P1）：第一种情况文案变「已添加 ✓」并转生效态蓝色，
                      不再让用户靠悬停 tooltip 才知道已经加过。 */}
                  <button
                    type="button"
                    disabled={addDisabled}
                    onClick={handleAddUpstreamSource}
                    title={addTitle}
                    aria-label="添加上游源"
                    className={tracePill(addDone, addDisabled)}
                  >
                    {/* 0.6.326i（P1）：已添加 → 图标由「+」换成对勾（与生效态变蓝同语言）。 */}
                    {addDone ? (
                      <Check className="h-3 w-3 mt-px shrink-0" />
                    ) : (
                      <Plus className="h-3 w-3 mt-px shrink-0" />
                    )}
                    <span className="min-w-0 break-words">
                      {srcAddState === 'busy' ? '添加中…' : (addDone ? '已添加' : '添加上游源')}
                    </span>
                  </button>
                  {/* ② 禁用fork源：仅当本源仓库是 GitHub fork 时可用（整条源禁用
                      → 该源全部卡片从列表消失）；否则置灰说明原因。 */}
                  <button
                    type="button"
                    disabled={forkDisabled}
                    onClick={handleToggleForkSource}
                    title={forkTitle}
                    aria-label={forkSourceDisabled ? '恢复fork源' : '禁用fork源'}
                    className={tracePill(forkSourceDisabled, forkDisabled)}
                  >
                    <Ban className="h-3 w-3 mt-px shrink-0" />
                    <span className="min-w-0 break-words">
                      {forkSourceDisabled ? '恢复fork源' : '禁用fork源'}
                    </span>
                    {forkSourceDisabled && <Check className="h-2.5 w-2.5 mt-px shrink-0" />}
                  </button>
                  {/* ③ 禁用应用（= 原「禁用」逻辑原样搬移，含目标态语义）。 */}
                  <button
                    type="button"
                    disabled={hideDisabled}
                    onClick={() => {
                      // 目标态由本次点击决定（宿主按 app.user_hidden 选 PUT/DELETE）：
                      // 连点也能正确来回切换，不受对话框内陈旧快照影响。
                      const next = !hiddenNow;
                      setHiddenOverride(next);
                      onToggleHide!({ ...app, user_hidden: next });
                    }}
                    title={hideTitle}
                    aria-label={hiddenNow ? '恢复显示该应用' : '禁用应用'}
                    className={tracePill(hiddenNow, hideDisabled)}
                  >
                    <EyeOff className="h-3 w-3 mt-px shrink-0" />
                    <span className="min-w-0 break-words">{hiddenNow ? '已禁用' : '禁用应用'}</span>
                    {hiddenNow && <Check className="h-2.5 w-2.5 mt-px shrink-0" />}
                  </button>
                </div>
                {/* 探测判定「本仓与 owner 索引仓都没发布索引」时，按钮保持恒显
                    （置灰）并在下方给一行原因。 */}
                {!!upstreamAddNote && (
                  <p className="text-[11px] leading-snug text-muted-foreground">{upstreamAddNote}</p>
                )}
              </div>
            </DetailRow>
          )}

          <DetailRow icon={Tag} label="版本">
            <div className="flex items-center gap-2 flex-wrap">
              {/* 同步源里的最新版本作为主值：未更新/未安装直接显示最新版本；
                  已装旧版时已装版降为次要值，箭头指向（并高亮）最新版本 */}
              {isInstalled && canUpdate ? (
                <>
                  <span className="text-muted-foreground">v{installedVersionLabel(app) || '-'}</span>
                  <span className="text-muted-foreground">→</span>
                  <span className="text-primary font-medium">v{availableVersionLabel(app)} (最新)</span>
                </>
              ) : (
                <span>{app.latest_version ? `v${app.latest_version}` : (isInstalled ? `v${installedVersionLabel(app) || '-'}` : '-')}</span>
              )}
              {!isInstalled && (
                <span className="text-muted-foreground text-xs">(最新)</span>
              )}
            </div>
          </DetailRow>

          {/* 0.6.303（arm 适配准备）：平台架构——源提供的全部架构（x86 / arm，
              展示序 x86 在前）；单架构源只显示一个。安装/下载由后端按本机
              架构自动选包（arm 设备选中 arm 包，x86→all 回退），此行=依据。
              源未提供架构信息（平铺单链接）时不显示该行。 */}
          {(app.archs?.length || app.arch) && (
            <DetailRow icon={Cpu} label="架构">
              {app.archs?.length
                ? app.archs.map(v => (v === 'all' ? '通用' : v)).join(' / ')
                : (app.arch === 'all' ? '通用' : app.arch)}
            </DetailRow>
          )}

          {/* 0.6.312：同名候选表按用户要求整体移除（后端 same_name 字段保留，
              冲突处理仍走列表页冲突对话框；显式引用逃生门不再需要）。 */}
          {app.service_port ? (
            <DetailRow icon={Network} label="服务端口">
              {app.service_port}
            </DetailRow>
          ) : null}

          {(app.size_bytes || (hasPanelInfo && panelInfo?.app.appDetail?.installSize)) ? (
            <DetailRow icon={HardDrive} label="安装包大小">
              {formatSize(app.size_bytes || panelInfo?.app.appDetail?.installSize)}
            </DetailRow>
          ) : null}

          {app.download_count ? (
            <DetailRow icon={Download} label="下载次数">
              {formatDownloads(app.download_count)}
            </DetailRow>
          ) : app.local_installs ? (
            // 第三方源应用无全局下载量（官方规范不统计外部源）→ 本机安装次数回退
            <DetailRow icon={Download} label="下载次数">
              本机 {app.local_installs} 次
            </DetailRow>
          ) : null}

          {/* moo.json 扩展（0.6.235）：运行方式（root / 用户空间等）与最早发布时间。
              源未提供时不显示该行，保持与旧源一致的观感。 */}
          {installTypeRow(app.install_type) && (
            <DetailRow icon={ShieldCheck} label={installTypeRow(app.install_type)!.label}>
              {installTypeRow(app.install_type)!.value}
            </DetailRow>
          )}

          {/* 官方目录不提供更新时间（面板契约无此字段），空值行不显示 */}
          {!isOfficial && app.updated_at && (
            <DetailRow icon={Clock} label="最近更新">
              {formatDate(app.updated_at)}
            </DetailRow>
          )}

          {app.first_release_at && (
            <DetailRow icon={CalendarClock} label="最早发布">
              {formatDate(app.first_release_at)}
            </DetailRow>
          )}

          {/* moo.json 扩展（0.6.269）：许可协议 / 最低 fnOS 版本（源未提供不显示） */}
          {app.license && (
            <DetailRow icon={FileText} label="许可协议">
              {app.license}
            </DetailRow>
          )}

          {app.min_fnos && (
            <DetailRow icon={Network} label="系统最低版本">
              fnOS {app.min_fnos}
            </DetailRow>
          )}

          {hasPanelInfo && panelInfo?.app.appDetail?.osMinVersion && (
            <DetailRow icon={Network} label="系统最低版本">
              fnOS {panelInfo.app.appDetail.osMinVersion}
            </DetailRow>
          )}

          {app.sha256 && (
            <DetailRow icon={Hash} label="SHA256">
              <code className="text-xs font-mono break-all text-muted-foreground">{app.sha256}</code>
            </DetailRow>
          )}

          {app.homepage && (
            <DetailRow icon={Globe} label="官网">
              <a
                href={app.homepage}
                target="_blank"
                rel="noopener noreferrer"
                className="text-primary hover:underline inline-flex items-center gap-1 break-all"
              >
                {app.homepage.replace(/^https?:\/\//, '').replace(/\/$/, '')}
                <ExternalLink className="h-3 w-3 shrink-0" />
              </a>
            </DetailRow>
          )}

          {app.release_url && (
            <DetailRow icon={Tag} label="发布页">
              <a
                href={app.release_url}
                target="_blank"
                rel="noopener noreferrer"
                className="text-primary hover:underline inline-flex items-center gap-1"
              >
                GitHub Release
                <ExternalLink className="h-3 w-3 shrink-0" />
              </a>
            </DetailRow>
          )}
        </div>

        {/* README */}
        {((app.has_readme && !readmeMissing) || !!readme) && (
          <>
            <Separator />
            {/* 0.6.279（用户定稿）：去掉展开/收起——README 改为固定上限的独立滚动小框：
                内容只在框内滚、不再随大卡片一起滚；短文自然缩到内容高度，
                长文到 max-h 上限后框内独立滚动（overscroll-contain 防滚动链外泄） */}
            <div className="text-xs text-muted-foreground flex items-center gap-1.5 min-w-0">
              <FileText className="h-3.5 w-3.5 shrink-0" />
              <span className="truncate">README</span>
            </div>
            {readme === null && !readmeError ? (
              <div className="flex items-center gap-2 py-4 text-xs text-muted-foreground">
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                正在加载 README…
              </div>
            ) : readmeError ? (
              <p className="py-2 text-xs text-muted-foreground">{readmeError}</p>
            ) : (
              /* 阅读友好框：上限约 17~20 行（移动端 24rem / 桌面 28rem），
                 圆角+细边框+浅底区分卡片层，四周留白 px-4 py-3 */
              <div className="my-2 max-h-[24rem] sm:max-h-[28rem] overflow-y-auto overscroll-contain no-scrollbar rounded-xl border border-border/50 bg-muted/20 px-4 py-3">
              <div onErrorCapture={handleReadmeImgError} className="markdown-body text-sm leading-relaxed text-foreground/90 prose prose-sm dark:prose-invert max-w-none
                [&_img]:max-w-full [&_img]:rounded-lg [&_h1]:text-lg [&_h2]:text-base [&_h3]:text-sm [&_h1]:mt-4 [&_h1]:mb-2 [&_h2]:mt-3 [&_h2]:mb-1.5 [&_h3]:mt-2 [&_h3]:mb-1
                [&_pre]:bg-muted [&_pre]:rounded-lg [&_pre]:p-3 [&_pre]:overflow-x-auto [&_code]:text-xs
                [&_table]:w-full [&_table]:text-xs [&_th]:border [&_th]:border-border [&_th]:p-1.5 [&_td]:border [&_td]:border-border [&_td]:p-1.5
                [&_a]:text-primary [&_a]:underline [&_a]:break-all [&_a]:hover:opacity-80 [&_img]:h-auto [&_th]:bg-muted/60 [&_th]:text-left [&_table]:border-collapse [&_table]:block [&_table]:overflow-x-auto [&_code]:bg-muted [&_code]:px-1 [&_code]:py-0.5 [&_code]:rounded [&_code]:break-all [&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_pre_code]:rounded-none [&_pre_code]:break-normal
                [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:list-decimal [&_ol]:pl-5 [&_li]:my-0.5
                [&_p]:my-2 [&_blockquote]:border-l-4 [&_blockquote]:border-border [&_blockquote]:pl-3 [&_blockquote]:text-muted-foreground [&_hr]:my-4 [&_video]:max-w-full [&_video]:rounded-lg">
                {readme && readmeLooksLikeHtml(readme) ? (
                  /* HTML README：第三方源直接给 HTML，Markdown 渲染会裸露标签，
                     改走 DOMPurify 消毒后的 innerHTML；github 系图片 src 先改写
                     走后端代理（国内直连 github 慢/挂） */
                  <div dangerouslySetInnerHTML={{ __html: DOMPurify.sanitize(
                    readme.replace(/src="(https?:\/\/[^"]+)"/g, (_m, u: string) => `src="${rewriteReadmeImgSrc(u, app.key || app.appname)}"`),
                    { ADD_ATTR: ['target'] },
                  ) }} />
                ) : (
                  /* Markdown README；rehypeRaw+sanitize 让内嵌 HTML 片段也安全渲染；
                     img 组件覆盖：github 系图片改走后端代理 */
                  <ReactMarkdown
                    remarkPlugins={[remarkGfm]}
                    rehypePlugins={[rehypeRaw, rehypeSanitize]}
                    components={{
                      img: (props: any) => (
                        <img {...props} src={rewriteReadmeImgSrc(props.src, app.key || app.appname)} loading="lazy" alt={props.alt ?? ''} />
                      ),
                      // 宽表（SHA256 长哈希/长文件名等不可断 token）包进横向
                      // 滚动容器，不再把对话框内容撑出横向溢出
                      table: ({ node, ...props }: any) => (
                        <div className="my-2 overflow-x-auto rounded-lg border border-border/40">
                          <table {...props} />
                        </div>
                      ),
                    }}
                  >
                    {readme || ''}
                  </ReactMarkdown>
                )}
              </div>
              </div>
            )}
          </>
        )}
        </>
        )}
        {/* 更新内容（版本化列表，对标 FnDepot 自更新屏） */}
        {(() => {
          const entries = app.changelog_entries?.length
            ? app.changelog_entries
            : app.changelog
              ? [{ version: app.latest_version, text: app.changelog }]
              : [];
          if (entries.length === 0) return null;
          return (
            <>
              <Separator />
              <div className="text-xs text-muted-foreground flex items-center gap-1.5 min-w-0">
                <FileText className="h-3.5 w-3.5 shrink-0" />
                <span className="truncate">更新日志</span>
                {app.has_update && (
                  <Badge variant="secondary" className="h-5 px-1.5 text-[10px] font-normal shrink-0">
                    可更新至 v{app.latest_version}
                    {/* 0.6.272：跨源同宗更新时标明来源源（供应链透明） */}
                    {app.update_from_source ? ` · 来自 ${app.update_from_source} 源` : ''}
                  </Badge>
                )}
              </div>
              {/* 0.6.280（用户定稿）：去掉「展开全部 N 条 / 收起」按钮——更新日志装入 README 同款小框：
                  圆角+细边框+浅底、固定上限（移动 24rem / 桌面 28rem）；短列表自然缩到内容高度，
                  长列表框内独立滚动（overscroll-contain 防滚动链外泄），两框外观与行为完全一致 */}
              <div className="my-2 max-h-[24rem] sm:max-h-[28rem] overflow-y-auto overscroll-contain no-scrollbar rounded-xl border border-border/50 bg-muted/20 px-4 py-3">
                <ChangelogList entries={entries} highlightLatest={app.has_update} />
              </div>
            </>
          );
        })()}

        </div>

        {/* 底部动作区：0.6.319g（用户定稿）改 absolute 悬浮 overlay。
            旧版「冻结底部」flex 段在胶囊上方留 20px 死区——滚动框底边裁在
            胶囊上方 20px 处，滚动时文字在那条隐形线「消失」；且死区挡住
            滚轮/触摸，鼠标悬停底部上下滑动时内容纹丝不动（用户实抓：
            「底边框遮住文字内容」）。
            改后：内容滚动框贯穿对话框最底，文字从胶囊下方滚过（胶囊浅蓝玻璃
            半透、文字隐约可见）；胶囊本体之外全 pointer-events-none=
            滚轮/触摸穿透，底部也能滑；胶囊位置与旧版逐像素一致
            （319e dl-bottom 平台分支保留：Android=完整安全区 /
            iOS=安全区-16px / 桌面=12px）。
            ① 主操作行——两个按钮各占一半（停用/卸载、启动/卸载、更新/卸载），单按钮占满；
            ② 「下载 fpk」= 最长一条全宽悬浮条（与设置页「保存」同款磨砂条 + 主色按钮）。 */}
        <div
          ref={onDlAreaRef}
          className={cn(
            "absolute inset-x-0 bottom-0 pointer-events-none",
            (actionRow || downloadFpkVisible) && "dl-bottom px-4 py-3 sm:px-5",
          )}
        >
          {(actionRow || downloadFpkVisible) && (
          <div className="pointer-events-auto">
            {/* 次要操作（忽略/取消忽略更新）：居中 ghost 小药丸 */}
            {((app.update_ignored && onUnignoreUpdate) || (canUpdate && !app.update_ignored && onIgnoreUpdate)) && (
              <div className="flex flex-wrap justify-center gap-2 mb-2">
                {app.update_ignored && onUnignoreUpdate && (
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => onUnignoreUpdate(app)}
                    className="rounded-full px-4 text-muted-foreground"
                  >
                    <Bell className="mr-1.5 h-3.5 w-3.5" />
                    取消忽略
                  </Button>
                )}
                {canUpdate && !app.update_ignored && onIgnoreUpdate && (
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => onIgnoreUpdate(app)}
                    className="rounded-full px-4 text-muted-foreground"
                  >
                    <BellOff className="mr-1.5 h-3.5 w-3.5" />
                    忽略更新
                  </Button>
                )}
              </div>
            )}

            {/* 主操作行 */}
            {actionRow && <div className="flex gap-2">{actionRow}</div>}

            {/* 下载 fpk：全宽悬浮条（与设置页「保存」同款）。
                社区源 + 官方 FPK 型应用显示（官方走面板 cloud 通道，
                下载后 .fpk 复制进 FPK 下载目录）；无源已装/官方
                docker/native(TPK) 型不显示。 */}
            {/* 不挂 backdrop-blur：磨砂模糊要在滚动每帧重算，移动端
                WebView GPU 吃紧时会把整个对话框的滚动/触摸反馈拖慢
                （New Store 同款动作区就是实心底）。用近实色底+阴影
                保留悬浮感，视觉差异极小。 */}
            {/* 0.6.303（用户实抓）：0.6.301 残留的 rounded-b-none/border-b-0/pb-safe
                未在 0.6.302 回退干净——移动端 pill 底部无圆角+无下边框+多 12px
                padding = 阴影拖长、不成胶囊。恢复 0.6.283 定稿形态。 */}
            {downloadFpkVisible && (
              // 0.6.308（用户定稿）：外框白条去掉——浅色主题下 card/55=白色半透圈
              // （浏览器实锤 computed bg=white/55），保留 p-2 间距即可，按钮直落玻璃底。
              // 0.6.319b：移动端去掉底部 8px 内衬（胶囊贴 home 线）；桌面保持
              // 0.6.319g：wrapper 本身 pointer-events-none（间距区滚轮/触摸穿透
              // 到底层滚动框），只有胶囊本体 pointer-events-auto（它才是按钮）
              <div className={cn("pointer-events-none pl-2 pr-2 pt-2 pb-0 sm:p-2", actionRow && "mt-2")}>
                <button
                  onClick={() => (fpkChecking ? undefined : dlPaused ? handleDlResume() : dlBusy ? handleDlPause() : localFpkName ? handleLocalInstall() : handleDownloadFpk())}
                  disabled={localInstalling || fpkChecking}
                  title={fpkChecking ? '正在检查本地缓存…' : dlBusy ? '暂停下载' : dlPaused ? '继续下载（断点续传）' : localFpkName ? '直接安装已下载的 FPK（不重新下载）' : '下载 FPK 到本地缓存'}
                  // 0.6.319d（用户定稿）：实心蓝与整体不搭，回退浅蓝玻璃（318 定稿款）
                  className="pointer-events-auto relative w-full h-10 overflow-hidden rounded-xl bg-primary/15 text-primary border border-primary/40 text-[14px] font-semibold flex items-center justify-center gap-1.5 hover:bg-primary/25 active:opacity-80 transition-colors"
                >
                  {dlBusy && dlPct != null && (
                    <span
                      className="absolute inset-y-0 left-0 bg-primary-foreground/20 transition-[width] duration-300"
                      style={{ width: `${dlPct}%` }}
                    />
                  )}
                  {dlBusy && dlPct == null && dlBytes > 0 && (
                    // 总量未知：不确定进度条（全宽呼吸）+ 已下载大小
                    <span className="absolute inset-0 animate-pulse bg-primary-foreground/15" />
                  )}
                  <span className="relative inline-flex items-center">
                    {fpkChecking ? (
                      // 0.6.325 C9：首次派生未完成——占位「检查中…」，不误闪「下载 fpk」
                      <><Loader2 className="h-3.5 w-3.5 animate-spin" />检查中…</>
                    ) : dlBusy ? (
                      <><Pause className="h-3.5 w-3.5" />{dlPct != null ? `暂停 ${dlPct}%` : dlBytes > 0 ? `下载中 ${formatBytes(dlBytes)}` : '暂停'}</>
                    ) : dlPaused ? (
                      <><Play className="h-3.5 w-3.5" />继续</>
                    ) : localInstalling ? (
                      // 0.6.325 C5：点击「已下载」直接安装中（SSE 消息行）
                      <><Loader2 className="h-3.5 w-3.5 animate-spin" />{localInstallMsg || '安装中'}</>
                    ) : localFpkName ? (
                      // 0.6.325 C5（用户定稿）：FPK 已在本机缓存 →「已下载」，
                      // 点击=直接安装该缓存文件（不重新下载）
                      <><Check className="h-3.5 w-3.5" />已下载</>
                    ) : (
                      // 0.6.319F（用户定稿）：idle 态去 Download 箭头只留文字（暂停/继续态图标非箭头，保留）
                      <span>下载 fpk</span>
                    )}
                  </span>
                </button>
              </div>
            )}
          </div>
          )}
        </div>
        </div>
        </div>
        {/* 悬浮返回钮（移动端）：磨玻璃圆钮磁吸贴左缘半露出。
            静置 = 毛玻璃 + 比背景浅一档的半透白（不影响阅读）；拖动 = 加深为背景色 + 微放大。
            x 磁吸贴左缘，只能沿左缘纵向拖动（y 持久化）；轻点 = 返回列表。
            0.6.321r5b 方案B：纵向拖动=滚动主内容框（跟手式相对增量，
            scrollTop = 起始 − dy，内容跟手；钮视觉跟手、松手弹回原位，
            拖动定位让位；轻点返回不变）。
            细线 ‹ 箭头右移，在露出区内完全可见。
            必须放在 DialogContent 内部：Radix Dialog 会把对话框外的 DOM 置为
            inert（无法交互）；在内容同层堆叠上下文内 z-40 浮于内容之上。 */}
        <button
          // 0.6.315（iOS 真机修复）：悬浮返回钮去 backdrop-filter（移动端
          // 专属元素，WKWebView 合成异常下小玻璃层同样不可信；底色半透白保留）
          className={`back-wing sm:hidden fixed z-40 h-14 w-14 rounded-full border shadow-md flex items-center justify-center select-none touch-none transition-[background-color,border-color,box-shadow,transform] duration-200 ease-out ${
            backDragging
              ? 'bg-background border-black/10 dark:border-white/20 shadow-lg text-foreground scale-105'
              : 'bg-white/75 dark:bg-white/10 border-black/5 dark:border-white/10 text-muted-foreground dark:text-white/70'
          }`}
          style={{ left: BACK_X, top: backY }}
          onPointerDown={(e) => {
            if (backDragRef.current) return; // 0.6.321r5：拖动进行中忽略第二指
            (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
            backDragRef.current = { sx: e.clientX, sy: e.clientY, oy: backY, st0: backScrollRef.current?.scrollTop ?? 0, moved: false };
            setBackDragging(true);
          }}
          onPointerMove={(e) => {
            const d = backDragRef.current;
            if (!d) return;
            const dx = e.clientX - d.sx;
            const dy = e.clientY - d.sy;
            if (!d.moved) {
              if (Math.abs(dy) <= 6) return;
              // 0.6.321r5：横向手势不认领（iOS 左缘系统滑返保险）
              if (Math.abs(dy) < Math.abs(dx)) return;
              d.moved = true;
            }
            // 0.6.321r5b 方案B（跟手式）：钮视觉跟手（松手弹回原位=拖动定位让位）；
            // 页面内容跟手相对增量：scrollTop = 起始 − dy（手指下移→内容下移）
            moveBackY(Math.max(8, Math.min(window.innerHeight - 64, d.oy + dy)));
            backScrollByDelta(dy, d.st0);
          }}
          onPointerUp={() => {
            const d = backDragRef.current;
            backDragRef.current = null;
            setBackDragging(false);
            if (!d) return;
            if (d.moved) {
              // 0.6.321r5b 方案B：拖动后钮弹回原位（拖动定位让位，y 不落盘终值）
              moveBackY(d.oy);
              return;
            }
            // 轻点 = 返回。关掉弹窗后按钮随之卸载，pointerup 之后的 click 事件
            // 可能穿透落到下方的列表行上把详情重新打开 —— 捕获阶段一次性吞掉它。
            // 兜底：若该 click 因目标节点已卸载而未派发，300ms 后移除监听，避免
            // 残留监听误吞用户的下一次点击。注意移除必须带 { capture: true } ——
            // 两参 removeEventListener 的 capture 默认 false，移除不掉 capture
            // 注册的监听（曾导致吞掉用户下一次点击）。
            const suppressClick = (e: Event) => { e.stopPropagation(); e.preventDefault(); };
            document.addEventListener('click', suppressClick, { capture: true, once: true });
            setTimeout(() => document.removeEventListener('click', suppressClick, { capture: true }), 300);
            onOpenChange(false);
          }}
          onPointerCancel={() => { backDragRef.current = null; setBackDragging(false); }}
          aria-label="返回应用列表（轻点返回，上下拖动滚动页面）"
        >
          {/* 细线 ‹ 箭头（用户指定样式）：右移到露出区(0~24px)偏右、完全可见
              （56px 钮 + ml-9 → 笔画中心落在屏幕 x≈14，露出区中点 12 的右侧） */}
          <ChevronLeft className="ml-9 h-5 w-5" strokeWidth={2.5} />
        </button>
        {/* 0.6.325 C5：「已下载」直接安装的向导（必填字段无默认值时弹出，
            与 FpkDownloads 组件同一 WizardDialog；必须在 DialogContent 内——
            Radix 会把对话框外 DOM 置 inert） */}
        {localWizardDef && localFpkName && (
          <WizardDialog
            appDisplayName={localWizardDef.appname || localFpkName}
            wizard={localWizardDef}
            loading={false}
            onCancel={() => setLocalWizardDef(null)}
            onConfirm={(params) => {
              const name = localFpkName;
              setLocalWizardDef(null);
              runLocalInstall(name, params);
            }}
          />
        )}
      </DialogContent>

      {/* 预览图灯箱：createPortal 挂到 document.body + z-[100]，
          确保压在 Radix DialogOverlay(z-50) 之上（修复点击被 overlay 拦截的问题） */}
      {lightbox != null && createPortal(
        <div
          className="fixed inset-0 z-[100] bg-black/90 flex items-center justify-center p-4 select-none"
          style={{ zIndex: 100, pointerEvents: 'auto' }}
          // 灯箱 portal 在 Radix Dialog 的 DOM 之外：必须在这里拦掉 pointerdown，
          // 否则灯箱内任何点击都会触发 Radix 的「外部点击关闭对话框」。
          onPointerDown={(e) => e.stopPropagation()}
          onClick={() => setLightbox(null)}
        >
          {/* X 仅桌面端保留（鼠标够得到右上角）；移动端单手用 点按/上下滑 关闭 */}
          <button className="absolute top-4 right-4 z-10 hidden sm:inline-flex p-2 text-white/80 hover:text-white pointer-events-auto" onClick={() => setLightbox(null)} aria-label="关闭">
            <X className="h-6 w-6" />
          </button>
          <button
            className="absolute left-2 sm:left-4 top-1/2 -translate-y-1/2 z-10 hidden sm:inline-flex p-2 text-white/60 hover:text-white disabled:opacity-0 pointer-events-auto"
            disabled={lightbox === 0}
            onClick={(e) => { e.stopPropagation(); setLightboxAnim('prev'); setLightbox((lightbox - 1 + previewCount) % previewCount); }}
            aria-label="上一张"
          >
            <ChevronLeft className="h-8 w-8" />
          </button>
          {/* 灯箱内容区：横向滑动翻页；点按（无位移）= 关闭；纵向滑动
              （|dy|>60 且为主方向）= 关闭 —— 单手无需够右上角 X。
              顶部浅色提示行说明手势，不干扰看图 */}
          <div
            className="relative flex items-center justify-center w-full h-full"
            // touchAction:none —— 让浏览器别把双指手势当成「缩放整个网页」
            style={{ pointerEvents: 'auto', touchAction: 'none' }}
            onClick={(e) => e.stopPropagation()}
            onPointerDown={(e) => {
              pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
              // 双指落下 → 进入缩放（取消点按/滑动判定）
              if (pointers.current.size === 2) {
                const [a, b] = [...pointers.current.values()];
                const rect = e.currentTarget.getBoundingClientRect();
                pinchRef.current = {
                  dist: Math.max(1, Math.hypot(a.x - b.x, a.y - b.y)),
                  cx: (a.x + b.x) / 2,
                  cy: (a.y + b.y) / 2,
                  s0: zoom.s,
                  x0: zoom.x,
                  y0: zoom.y,
                  rectCx: rect.left + rect.width / 2,
                  rectCy: rect.top + rect.height / 2,
                  rectW: rect.width,
                  rectH: rect.height,
                };
                lightboxDrag.current = null;
                zoomPanRef.current = null;
                return;
              }
              if (zoom.s > 1.01) {
                // 缩放态：单指 = 平移
                const rect = e.currentTarget.getBoundingClientRect();
                zoomPanRef.current = { x: e.clientX, y: e.clientY, tx: zoom.x, ty: zoom.y, rectW: rect.width, rectH: rect.height };
                lightboxDrag.current = null;
              } else {
                lightboxDrag.current = { x: e.clientX, y: e.clientY, swiping: false };
              }
            }}
            onPointerMove={(e) => {
              if (!pointers.current.has(e.pointerId)) return;
              pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
              // ① 双指缩放：以两指中点为锚点，保证手指下那一点不动
              if (pinchRef.current && pointers.current.size >= 2) {
                const [a, b] = [...pointers.current.values()];
                const p = pinchRef.current;
                const dist = Math.max(1, Math.hypot(a.x - b.x, a.y - b.y));
                const s = zoomClamp(p.s0 * (dist / p.dist));
                const cx = (a.x + b.x) / 2;
                const cy = (a.y + b.y) / 2;
                const px = (p.cx - p.rectCx - p.x0) / p.s0;
                const py = (p.cy - p.rectCy - p.y0) / p.s0;
                const cl = zoomPanClamp(cx - p.rectCx - s * px, cy - p.rectCy - s * py, s, p.rectW, p.rectH);
                setZoom({ s, x: cl.x, y: cl.y });
                return;
              }
              // ② 缩放态单指平移
              if (zoomPanRef.current && zoom.s > 1.01) {
                const p = zoomPanRef.current;
                const cl = zoomPanClamp(p.tx + (e.clientX - p.x), p.ty + (e.clientY - p.y), zoom.s, p.rectW, p.rectH);
                setZoom((z) => ({ ...z, x: cl.x, y: cl.y }));
                return;
              }
              // ③ 原有：判定是否横向滑动（用于翻页）
              const d = lightboxDrag.current;
              if (!d) return;
              const dx = e.clientX - d.x;
              const dy = e.clientY - d.y;
              if (Math.abs(dx) > 10 && Math.abs(dx) > Math.abs(dy)) d.swiping = true;
            }}
            onPointerUp={(e) => {
              pointers.current.delete(e.pointerId);
              if (pointers.current.size < 2) pinchRef.current = null;
              zoomPanRef.current = null;
              // 缩回 1× 自动归位
              if (zoom.s <= 1.02) setZoom({ s: 1, x: 0, y: 0 });
              const d = lightboxDrag.current;
              lightboxDrag.current = null;
              // 缩放态：不响应点按关闭与翻页（退出缩放后手势恢复）
              if (zoom.s > 1.01) return;
              if (!d || lightbox == null) return;
              // 点按在按钮上（箭头/X/重试）不触发"点按关闭"
              if (e.target instanceof Element && e.target.closest('button')) return;
              const dx = e.clientX - d.x;
              const dy = e.clientY - d.y;
              // 点按（无位移）= 关闭
              if (Math.abs(dx) < 10 && Math.abs(dy) < 10) { setLightbox(null); return; }
              // 纵向滑动 = 关闭（手指上滑/下滑）
              if (Math.abs(dy) > 60 && Math.abs(dy) > Math.abs(dx)) { setLightbox(null); return; }
              // 横向滑动 = 翻页（带方向滑入动画）
              if (previewCount > 1 && d.swiping && Math.abs(dx) > 50 && Math.abs(dx) > Math.abs(dy) * 1.2) {
                setLightboxAnim(dx < 0 ? 'next' : 'prev');
                setLightbox((dx < 0 ? lightbox + 1 : lightbox - 1 + previewCount) % previewCount);
              }
            }}
            onPointerCancel={(e) => {
              pointers.current.delete(e.pointerId);
              if (pointers.current.size < 2) pinchRef.current = null;
              zoomPanRef.current = null;
              lightboxDrag.current = null;
              if (zoom.s <= 1.02) setZoom({ s: 1, x: 0, y: 0 });
            }}
            onPointerLeave={() => { lightboxDrag.current = null; }}
            // 桌面端滚轮缩放（以指针为锚点）
            onWheel={(e) => {
              if (lightboxError) return;
              e.preventDefault();
              const rect = e.currentTarget.getBoundingClientRect();
              const s = zoomClamp(zoom.s * (e.deltaY < 0 ? 1.12 : 0.89));
              const cx = rect.left + rect.width / 2;
              const cy = rect.top + rect.height / 2;
              const mx = e.clientX - cx;
              const my = e.clientY - cy;
              const px = (mx - zoom.x) / zoom.s;
              const py = (my - zoom.y) / zoom.s;
              const cl = zoomPanClamp(mx - s * px, my - s * py, s, rect.width, rect.height);
              setZoom({ s, x: cl.x, y: cl.y });
            }}
          >
            {/* 提示行文案保持原样（用户要求：不要在屏上出现「双指缩放」字样；
                缩放手势本身默默可用即可） */}
            <div className="absolute top-3 left-1/2 -translate-x-1/2 text-[13px] tracking-wide text-white/45 pointer-events-none select-none whitespace-nowrap">
              上下滑动可退出 · 左右滑动切换
            </div>
            {lightboxLoading && !lightboxError && (
              <div className="absolute flex flex-col items-center gap-2 text-white/70">
                <Loader2 className="h-8 w-8 animate-spin" />
                <span className="text-xs">预览图加载中…</span>
              </div>
            )}
            {lightboxError ? (
              <div className="flex flex-col items-center gap-3 text-white/80">
                <p className="text-sm">预览图加载失败</p>
                <Button variant="outline" size="sm" className="border-white/40 text-white hover:bg-white/10 hover:text-white"
                  onClick={() => { setLightboxAnim('open'); setLightboxError(false); setLightboxLoading(true); setLightboxRetry((n) => n + 1); }}>
                  重试
                </Button>
              </div>
            ) : (
              /* 0.6.227：缩放/平移包一层 —— transform 只作用在这层，
                 img 自身的换页动画（lightbox-anim-*）不受影响 */
              <div
                className="flex items-center justify-center w-full h-full"
                style={{
                  transform: `translate3d(${zoom.x}px, ${zoom.y}px, 0) scale(${zoom.s})`,
                  transformOrigin: 'center center',
                  transition: pinchRef.current || zoomPanRef.current ? 'none' : 'transform 160ms ease-out',
                }}
              >
                <img
                  key={`${lightbox}-${lightboxRetry}`}
                  src={previewSrc(lightbox) + (lightboxRetry ? `&r=${lightboxRetry}` : '')}
                  alt={`${app.display_name} 预览 ${lightbox + 1}`}
                  className={`max-w-full max-h-full object-contain rounded-lg transition-opacity duration-150 pointer-events-auto touch-none select-none ${
                    lightboxLoading ? 'opacity-0' : 'opacity-100'
                  } ${
                    lightboxAnim === 'next' ? 'lightbox-anim-next' : lightboxAnim === 'prev' ? 'lightbox-anim-prev' : 'lightbox-anim-open'
                  }`}
                  onLoad={() => setLightboxLoading(false)}
                  onError={() => { setLightboxLoading(false); setLightboxError(true); }}
                  draggable={false}
                />
              </div>
            )}
          </div>
          <button
            className="absolute right-2 sm:right-4 top-1/2 -translate-y-1/2 z-10 hidden sm:inline-flex p-2 text-white/60 hover:text-white disabled:opacity-0 pointer-events-auto"
            disabled={lightbox === previewCount - 1}
            onClick={(e) => { e.stopPropagation(); setLightboxAnim('next'); setLightbox((lightbox + 1) % previewCount); }}
            aria-label="下一张"
          >
            <ChevronRight className="h-8 w-8" />
          </button>
          {previewCount > 1 && (
            <div className="absolute bottom-5 left-1/2 -translate-x-1/2 text-white/70 text-xs tabular-nums pointer-events-none">
              {lightbox + 1} / {previewCount}
            </div>
          )}
        </div>,
        document.body
      )}
    </Dialog>
  );
};

export default AppDetailDialog;
