import React, { useState, useEffect, useRef, useCallback } from 'react';
import { fetchSettings, updateSettings, fetchStoreUpdate, checkMirrors, fetchMirrorHealth, fetchDockerMirrorHealth, fetchFpkDownloads, deleteFpkDownload, installFpkDownload, installApp, fetchTasks, clearDownloadTask, pauseDownload, resumeDownload, browseDownloadDirs, fetchBackups, runBackupNow, deleteBackup, cleanAppCache, restoreBackup, downloadBackup, fetchAbout, testProxy, type MirrorOption, type MirrorCheckResult, type VolumeOption, type UpdateProgress, type MirrorHealth, type FpkDownloadFile, type BackgroundTask, type BackupEntry, type AppCacheStats, type AboutInfo } from '../api/client';
import type { StoreUpdateInfo } from '../api/client';
import { useKeyboardDock } from '../lib/hooks';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Separator } from "@/components/ui/separator"
import { Badge } from "@/components/ui/badge"
import { Switch } from "@/components/ui/switch"
import { Progress } from "@/components/ui/progress"
import { ArrowLeft, Archive, Bell, ChevronDown, ChevronRight, Database, Download, FileText, Folder, FolderDown, HardDrive, Info, Loader2, Pause, Play, RefreshCw, RotateCcw, SlidersHorizontal, Trash2, XCircle, Zap } from 'lucide-react'
import { toast } from 'sonner'
import { cn } from "@/lib/utils"
import SourceManager from './SourceManager'
import NotifySettingsTab from './NotifySettingsTab'
import LogSettingsTab from './LogSettingsTab'
import ReorderList from './ReorderList'
import GearTimePicker from './GearTimePicker'

type SettingsTab = 'system' | 'accel' | 'source' | 'backup' | 'notify' | 'log' | 'about';

const TABS: { key: SettingsTab; label: string; icon: React.ElementType }[] = [
  { key: 'system', label: '系统设置', icon: SlidersHorizontal },
  { key: 'accel', label: '加速源设置', icon: Zap },
  { key: 'source', label: '应用源设置', icon: Database },
  { key: 'backup', label: '备份设置', icon: Archive },
  { key: 'notify', label: '通知设置', icon: Bell },
  { key: 'log', label: '日志', icon: FileText },
  { key: 'about', label: '关于', icon: Info },
];

// 可排序清单（0.6.122 系统设置）：与后端 DockTabKeys/SettingsTabKeys 对齐
const DOCK_TAB_ITEMS: { key: string; label: string }[] = [
  { key: 'recommended', label: '发现' },
  { key: 'all', label: '全部' },
  { key: 'installed', label: '已安装' },
  { key: 'update_available', label: '有更新' },
];
const DOCK_DEFAULT_ORDER = DOCK_TAB_ITEMS.map((t) => t.key);
const SETTINGS_TAB_DEFAULT_ORDER = TABS.map((t) => t.key);

interface SettingsPageProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onStoreUpdate?: () => void;
  /** 外部应用源变化后刷新应用目录 */
  onCatalogChanged?: () => void;
}

function formatBytes(bytes: number): string {
  if (bytes <= 0) return '';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  const val = bytes / Math.pow(1024, i);
  return `${val >= 100 ? Math.round(val) : val.toFixed(1)} ${units[i]}`;
}

function latencyColor(result: MirrorCheckResult): string {
  if (result.status !== 'ok') return 'text-muted-foreground';
  if (result.latency_ms <= 300) return 'text-green-600';
  if (result.latency_ms <= 800) return 'text-yellow-600';
  return 'text-red-500';
}

function latencyText(result: MirrorCheckResult): string {
  if (result.status === 'timeout') return '超时';
  if (result.status === 'error') return '失败';
  return `${result.latency_ms}ms`;
}

/**
 * 加速源健康面板（GitHub / Docker 加速共用）：
 * 每源一行（状态点 + 标签 + 「当前」徽标 + 延迟/失败次数），
 * 顶部「立即测速」按钮 + 智能模式提示 + 最近一次自动切换横幅。
 * 列表 = 全部真实镜像（去掉 direct/auto；自定义仅在已配置时显示）。
 * 监测源列表可折叠（状态持久化），折叠时显示一行健康摘要。
 */
const MirrorHealthPanel: React.FC<{
  title: string;
  health: MirrorHealth | null;
  options: MirrorOption[];
  customConfigured: boolean;
  labelOf: (key: string) => string;
  refreshing: boolean;
  onRefresh: () => void;
  /** 显示吞吐测速列（MB/s，GitHub / Docker 加速源均按吞吐优选） */
  showSpeed?: boolean;
  /** showSpeed 时的说明文案（缺省为 GitHub 文案） */
  speedNote?: string;
  /**
   * 参考模式（Docker 组专用）：优选结果只驱动本面板展示，**不接入实际镜像
   * 拉取**（拉取由 fnOS 系统级 registry-mirrors 决定，Moo 不写该配置）。
   * 徽章改「最快」、隐藏「自动切换」横幅、说明文案改为如实描述——
   * 2026-09-25 用户质疑「优选是否真的生效」后审计定案。
   */
  referenceOnly?: boolean;
  /** 自动测速间隔（0.6.148 齿轮选择框）：时/分，随设置保存生效 */
  intervalH: number;
  intervalM: number;
  onIntervalChange: (h: number, m: number) => void;
}> = ({ title, health, options, customConfigured, labelOf, refreshing, onRefresh, showSpeed, speedNote, referenceOnly, intervalH, intervalM, onIntervalChange }) => {
  const [collapsed, setCollapsed] = React.useState<boolean>(() => {
    try {
      // 默认折叠（无持久化记录时）；用户手动展开/折叠后按保存值
      const v = localStorage.getItem(`health-panel-collapsed:${title}`);
      return v === null ? true : v === 'true';
    } catch { return true; }
  });
  const toggleCollapsed = () => {
    setCollapsed(prev => {
      const next = !prev;
      try { localStorage.setItem(`health-panel-collapsed:${title}`, String(next)); } catch { /* ignore */ }
      return next;
    });
  };

  const rows = React.useMemo(() => {
    const statsByKey = new Map((health?.mirrors || []).map((s) => [s.key, s] as const));
    const base = options.filter((o) =>
      o.key !== 'direct' && o.key !== 'auto' && (o.key !== 'custom' || customConfigured)
    );
    return base.map((o) => {
      const st = statsByKey.get(o.key);
      return {
        key: o.key,
        label: o.label,
        status: st?.status || '',
        latency_ms: st?.latency_ms || 0,
        speed_bps: st?.speed_bps || 0,
        consec_fails: st?.consec_fails || 0,
      };
    });
  }, [options, health, customConfigured]);

  const okCount = rows.filter((r) => r.status === 'ok').length;
  const failCount = rows.filter((r) => r.status === 'fail').length;

  return (
    <div className="relative rounded-xl bg-card border border-border/20 px-3 py-3">
      {/* 0.6.150（用户定稿）：标题上移至「卡片最顶部 ↔ 齿轮最顶部」的正中——
          卡片外顶→齿轮顶 = 1(border) + 12(py-3) + 14(齿轮列顶部留白) = 27px，
          标题中心 = 距卡片顶 13.5px（绝对定位 -translate-y-1/2；
          ⚠ 若 border/py-3/齿轮顶部留白任一改动，此值需同步）。
          标题原位置（滚轮中心线）改「自动监测间隔」字样（0.6.152 定稿：与标题
          同大小 13px，左区水平居中；0.6.150 的 11px 左对齐已废弃）。
          左列 14+72+14=100px 与 GearTimePicker 列同构：中 72px 带=滚轮同高
          （小字中心=滚轮中心线，与刷新/折叠按钮平齐，0.6.149 对齐保持） */}
      <span className="absolute left-[13px] top-[13.5px] -translate-y-1/2 max-w-[180px] truncate text-[13px] font-medium">
        {title}
      </span>
      <div className="flex items-center justify-between mb-1 gap-2">
        <div className="flex min-w-0 flex-1 flex-col items-start">
          <div className="h-3.5 shrink-0" aria-hidden />
          {/* 0.6.152（用户定稿）：小字与标题同大小（13px），并在齿轮左侧空余区域
              水平居中（原 11px 左对齐废弃）。
              0.6.153（用户定稿）：「0h0m=5m」放在「自动监测间隔」下方、水平居中，
              并下移与右侧「时/分」单位行平齐（实测单位行比 +14px 位低 26px →
              +40px，两卡均验）；齿轮选择框本身不动（恢复 0.6.152 原样），
              标签中心线位置保持 0.6.152 不变（与滚轮中心线平齐） */}
          <div className="relative flex h-[72px] w-full min-w-0 shrink-0 items-center justify-center">
            <span className="text-[13px] font-medium text-muted-foreground">自动监测间隔</span>
            <span className="absolute left-1/2 top-[calc(50%+40px)] -translate-x-1/2 whitespace-nowrap text-[10px] leading-none text-muted-foreground/80">0h0m=5m</span>
          </div>
          <div className="h-3.5 shrink-0" aria-hidden />
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          {/* 0.6.148：自动测速间隔（苹果时钟齿轮式：时 0-23 / 分 0-59），
              替换原「每 5 分钟自动测速」静态文案；保存后下一起效周期即按新间隔 */}
          <GearTimePicker
            hours={intervalH}
            minutes={intervalM}
            onChange={onIntervalChange}
            ariaLabel={`${title} 自动测速间隔`}
          />
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7"
            onClick={onRefresh}
            disabled={refreshing}
            title="立即测速"
            aria-label="立即测速"
          >
            <RefreshCw className={cn("h-3.5 w-3.5", refreshing && "animate-spin")} />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7"
            onClick={toggleCollapsed}
            title={collapsed ? '展开监测的加速源列表' : '折叠监测的加速源列表'}
            aria-label={collapsed ? '展开监测的加速源列表' : '折叠监测的加速源列表'}
          >
            <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", collapsed && "-rotate-90")} />
          </Button>
        </div>
      </div>
      {collapsed ? (
        <p className="text-[11px] text-muted-foreground">
          {okCount}/{rows.length} 正常{failCount > 0 ? `（${failCount} 失败）` : ''}
          {health?.active && health.active !== 'direct' && ` · ${referenceOnly ? '最快' : '当前'} ${labelOf(health.active)}`}
        </p>
      ) : (
        <>
          <p className="text-[11px] text-muted-foreground mb-2.5">
            {referenceOnly
              ? (speedNote ?? '测速结果仅供参考：实际镜像拉取由系统 Docker 守护进程按系统级镜像源配置执行。')
              : (showSpeed
                ? (speedNote ?? '对 FPK 下载、应用列表图标、README/预览图生效：手选源优先，手选源失效后自动切换智能优选（实测各源速度差可达百倍）。后台按右侧间隔自动测速。')
                : '按健康状态自动优选加速源，手选源失效后自动切换。')}
            {health?.selected === 'auto' && !referenceOnly && '当前为智能模式（自动选最快稳定源）。'}
          </p>
          {!referenceOnly && health?.last_switch && (
            <div className="mb-2.5 rounded-lg border border-primary/30 bg-primary/5 px-3 py-2 text-[12px] leading-relaxed text-primary">
              {health.last_switch.reason}
              <span className="ml-1 whitespace-nowrap">
                （{labelOf(health.last_switch.from)} → {labelOf(health.last_switch.to)}）
              </span>
            </div>
          )}
          {/* 列名表头：与数据行的延迟/速度列对齐 */}
          <div className="flex items-center gap-2 px-1 pb-1 text-[11px] text-muted-foreground/80">
            <span className="w-2 shrink-0" />
            <span className="flex-1">加速源</span>
            <span className="w-[64px] text-right">延迟</span>
            {showSpeed && <span className="w-[72px] text-right">速度</span>}
          </div>
          <div className="space-y-1">
            {rows.map((row) => (
              <div key={row.key} className="flex items-center gap-2 text-[13px]">
                <span
                  className={cn(
                    "h-2 w-2 rounded-full shrink-0",
                    row.status === 'ok' && "bg-emerald-500",
                    row.status === 'fail' && "bg-red-500",
                    !row.status && "bg-muted-foreground/30"
                  )}
                />
                <span className="flex-1 min-w-0 flex items-center gap-1.5">
                  <span className="truncate">{row.label}</span>
                  {row.key === health?.active && (
                    <span className="shrink-0 rounded-full bg-primary/10 px-1.5 h-4 flex items-center text-[10px] font-medium text-primary">
                      {referenceOnly ? '最快' : '当前'}
                    </span>
                  )}
                </span>
                <span className="w-[64px] shrink-0 text-right text-[11px] tabular-nums text-muted-foreground">
                  {row.status === 'ok'
                    ? `${row.latency_ms}ms`
                    : row.status === 'fail'
                      ? row.consec_fails > 1 ? `失败×${row.consec_fails}` : '失败'
                      : '未测速'}
                </span>
                {showSpeed && (
                  <span className="w-[72px] shrink-0 text-right text-[11px] tabular-nums text-muted-foreground">
                    {row.status === 'ok' && row.speed_bps > 0
                      ? row.speed_bps >= 1048576
                        ? `${(row.speed_bps / 1048576).toFixed(1)}MB/s`
                        : `${Math.round(row.speed_bps / 1024)}KB/s`
                      : '—'}
                  </span>
                )}
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
};

// ── 关于 tab（0.6.120：版本号 + 应用信息；后续按需扩充）──

function formatUptime(s: number): string {
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d} 天 ${h} 小时`;
  if (h > 0) return `${h} 小时 ${m} 分钟`;
  return `${Math.max(m, 1)} 分钟`;
}

const AboutRow: React.FC<{ k: string; v: React.ReactNode }> = ({ k, v }) => (
  <div className="flex items-center justify-between gap-3 text-[13px]">
    <span className="text-muted-foreground shrink-0">{k}</span>
    <span className="text-right min-w-0 truncate">{v}</span>
  </div>
);

const AboutTab: React.FC = () => {
  const [info, setInfo] = React.useState<AboutInfo | null>(null);

  React.useEffect(() => {
    fetchAbout().then(setInfo).catch(() => setInfo(null));
  }, []);

  return (
    <div className="px-3 py-4 sm:px-6 sm:py-5 space-y-4">
      {/* 应用信息 */}
      <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-4 space-y-3.5">
        <div className="flex items-center gap-3.5">
          <img src="./icon-192.png" alt="Moo" className="h-14 w-14 rounded-2xl border border-border/20 shrink-0" />
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <span className="text-base font-semibold">{info?.app.display_name || 'Moo'}</span>
              {info && <Badge variant="outline" className="text-xs tabular-nums">v{info.version}</Badge>}
            </div>
            <p className="mt-1 text-xs text-muted-foreground leading-relaxed">
              {info?.app.desc || '加载中…'}
            </p>
          </div>
        </div>
        <Separator />
        <div className="space-y-2.5">
          <AboutRow k="开发者" v={
            <a href={info?.app.author_url || '#'} target="_blank" rel="noreferrer" className="hover:underline text-primary">
              {info?.app.author || 'Blue-Mink'}
            </a>
          } />
          <AboutRow k="项目主页" v={
            <a href={info?.app.homepage || '#'} target="_blank" rel="noreferrer" className="hover:underline text-primary">
              GitHub 仓库
            </a>
          } />
          <AboutRow k="运行平台" v="飞牛 fnOS" />
        </div>
      </div>

      {/* 版本信息 */}
      <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-4 space-y-3">
        <div className="text-sm font-medium">版本信息</div>
        <div className="space-y-2.5">
          <AboutRow k="当前版本" v={info ? `v${info.version}` : '—'} />
          <AboutRow k="服务端口" v={info?.port || '—'} />
          <AboutRow k="运行架构" v={info ? `${info.platform} · ${info.arch}` : '—'} />
          {info?.go_version && <AboutRow k="运行时" v={info.go_version} />}
          {info && <AboutRow k="启动时间" v={new Date(info.started_at * 1000).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })} />}
          {info && <AboutRow k="运行时长" v={formatUptime(info.uptime_s)} />}
        </div>
      </div>

      {/* 致谢（0.6.131 用户定稿；0.6.137 标题互换；0.6.138 整体优化用户定稿）：
          血缘收敛去功能宣传 /「反编译」柔化为「参考实现」/ fn-knock 参考范围补全
          （通知事件中心形式 + 设置备份模型）/ 补 GitHub 加速镜像维护者 /
          补人机协作开发一句（不带版本号）/ 末尾补许可声明 */}
      <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-4 space-y-3">
        <div className="text-base font-semibold text-center">Moo is more</div>
        <div className="space-y-2 text-xs leading-relaxed text-muted-foreground">
          <p className="py-0.5 text-center text-[13px] font-medium tracking-wide text-foreground/80">致谢</p>
          <p>
            Moo 由开源项目{' '}
            <a href="https://github.com/conversun/fnos-apps" target="_blank" rel="noreferrer" className="text-primary hover:underline">conversun/fnos-apps</a>
            {' '}发展而来：初版 Blue-Mink/New-Store 在其上重构了前端 UI，Moo 在此基础上持续演进。
          </p>
          <p>
            感谢{' '}
            <a href="https://github.com/EWEDLCM/FnDepot" target="_blank" rel="noreferrer" className="text-primary hover:underline">EWEDLCM/FnDepot</a>
            {' '}应用仓库，部分内容优化参考了 FnDepot 应用的实现；同时感谢 FnDepot V1 / V2 源的开发者们。
          </p>
          <p>
            感谢 kci-lnk 的{' '}
            <a href="https://github.com/kci-lnk/fn-knock-turborepo" target="_blank" rel="noreferrer" className="text-primary hover:underline">fn-knock</a>
            {' '}项目，通知事件中心与设置备份的形式参考了它。
          </p>
          <p>
            感谢{' '}
            <a href="https://github.com/kspeeder/docker_kspeeder" target="_blank" rel="noreferrer" className="text-primary hover:underline">KSpeeder</a>
            {' '}项目，Moo「Docker 镜像加速」中的本地镜像缓存加速由它的独立应用提供。
          </p>
          <p>
            感谢飞牛团队构建的 fnOS 系统与开放的应用生态，以及 GitHub 加速源与 Docker 镜像加速源的维护者们
            （Moo 的分发、源同步与镜像拉取都离不开它们）。
          </p>
          <p>Moo 由人类与 AI 协作开发。</p>
          <p>
            感谢所有投身飞牛应用生态的开发者、爱好者与项目测试者——Moo 的每个版本迭代都来自你们的反馈，
            感谢你们为爱发电。
          </p>
          <p className="pt-1 text-center text-[11px] text-muted-foreground/70">开源许可：MIT · © 2026 Blue-Mink</p>
        </div>
      </div>
    </div>
  );
};

/**
 * 设置页（整页展示，与单应用详情同构）：
 * - 移动端 = 整页（inset-0），桌面端 = 居中宽面板
 * - 页内 tab：系统设置 / 加速源设置 / 应用源设置（分段控件，内容区全宽）
 *   默认移动端收起、桌面端展开）
 * - 顶栏 ← 返回按钮回到应用列表
 */
const SettingsPage: React.FC<SettingsPageProps> = ({
  open,
  onOpenChange,
  onStoreUpdate,
  onCatalogChanged,
}) => {
  const [tab, setTab] = useState<SettingsTab>('system');

  // ── 排序（0.6.122）：dock 主导航 / 设置 tab ────────────────────────
  const [dockOrderArr, setDockOrderArr] = useState<string[]>([]);
  const [settingsTabOrderArr, setSettingsTabOrderArr] = useState<string[]>([]);
  const [orderSaving, setOrderSaving] = useState(false);
  // 排序卡片折叠（本地持久化，默认折叠）：与「应用源列表」折叠同款交互
  const [dockOrderCollapsed, setDockOrderCollapsed] = useState<boolean>(() => {
    try {
      const v = localStorage.getItem('new-store.dock-order.collapsed');
      return v === null ? true : v === '1';
    } catch { return true; }
  });
  const [tabOrderCollapsed, setTabOrderCollapsed] = useState<boolean>(() => {
    try {
      const v = localStorage.getItem('new-store.tab-order.collapsed');
      return v === null ? true : v === '1';
    } catch { return true; }
  });
  const toggleOrderCollapsed = (which: 'dock' | 'tab') => {
    if (which === 'dock') {
      setDockOrderCollapsed((v) => {
        try { localStorage.setItem('new-store.dock-order.collapsed', v ? '0' : '1'); } catch { /* ignore */ }
        return !v;
      });
    } else {
      setTabOrderCollapsed((v) => {
        try { localStorage.setItem('new-store.tab-order.collapsed', v ? '0' : '1'); } catch { /* ignore */ }
        return !v;
      });
    }
  };

  // 设置页 tab 按自定义顺序排列（缺 key 追加末尾，防旧配置丢项）
  const orderedTabs = React.useMemo(() => {
    if (settingsTabOrderArr.length === 0) return TABS;
    const byKey = new Map(TABS.map((t) => [t.key as string, t]));
    const out: typeof TABS = [];
    for (const k of settingsTabOrderArr) {
      const t = byKey.get(k);
      if (t) { out.push(t); byKey.delete(k); }
    }
    for (const t of byKey.values()) out.push(t);
    return out;
  }, [settingsTabOrderArr]);

  // 排序卡片的行项目：按已保存顺序排列（缺 key 追加末尾）
  const orderedDockItems = React.useMemo(() => {
    if (dockOrderArr.length === 0) return DOCK_TAB_ITEMS;
    const byKey = new Map(DOCK_TAB_ITEMS.map((t) => [t.key, t]));
    const out: typeof DOCK_TAB_ITEMS = [];
    for (const k of dockOrderArr) {
      const t = byKey.get(k);
      if (t) { out.push(t); byKey.delete(k); }
    }
    for (const t of byKey.values()) out.push(t);
    return out;
  }, [dockOrderArr]);

  const orderedSettingsTabItems = React.useMemo(() => {
    const items = TABS.map((t) => ({ key: t.key as string, label: t.label, icon: t.icon }));
    if (settingsTabOrderArr.length === 0) return items;
    const byKey = new Map(items.map((t) => [t.key, t]));
    const out: typeof items = [];
    for (const k of settingsTabOrderArr) {
      const t = byKey.get(k);
      if (t) { out.push(t); byKey.delete(k); }
    }
    for (const t of byKey.values()) out.push(t);
    return out;
  }, [settingsTabOrderArr]);

  const saveDockOrder = async (keys: string[]) => {
    setDockOrderArr(keys);
    setOrderSaving(true);
    try {
      await updateSettings({ dock_order: keys });
      toast.success('Dock 栏顺序已保存');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '保存失败');
      setDockOrderArr(DOCK_DEFAULT_ORDER);
    } finally {
      setOrderSaving(false);
    }
  };

  const saveSettingsTabOrder = async (keys: string[]) => {
    setSettingsTabOrderArr(keys);
    setOrderSaving(true);
    try {
      await updateSettings({ settings_tab_order: keys });
      toast.success('设置 tab 顺序已保存');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '保存失败');
      setSettingsTabOrderArr(SETTINGS_TAB_DEFAULT_ORDER);
    } finally {
      setOrderSaving(false);
    }
  };

  // ── 系统设置字段 ───────────────────────────────────────────────────
  const [interval, setInterval] = useState<number>(24);
  const [mirror, setMirror] = useState<string>('gh-proxy');
  const [mirrorOptions, setMirrorOptions] = useState<MirrorOption[]>([]);
  const [dockerMirror, setDockerMirror] = useState<string>('daocloud');
  const [dockerMirrorOptions, setDockerMirrorOptions] = useState<MirrorOption[]>([]);
  const [customGithubMirror, setCustomGithubMirror] = useState<string>('');
  const [customDockerMirror, setCustomDockerMirror] = useState<string>('');
  // 科学加速（0.6.206，加速源设置末卡片）：本机代理，仅 GitHub 域名改道
  const [proxyEnabled, setProxyEnabled] = useState<boolean>(false);
  const [proxyUrl, setProxyUrl] = useState<string>('');
  const [proxyTesting, setProxyTesting] = useState<boolean>(false);
  const [installVolume, setInstallVolume] = useState<number>(0);
  const [volumeOptions, setVolumeOptions] = useState<VolumeOption[]>([]);
  // 自动更新应用（周期检查发现更新时后台自动安装，无需打开应用）
  const [autoUpdate, setAutoUpdate] = useState<boolean>(false);
  // FPK 下载目录 + 已下载列表（设置页展示，可同步刷新）
  // 目录选择对话框：可下钻浏览（卷根 → 共享目录 → 任意子层），切换中状态
  const [dirApplying, setDirApplying] = useState(false);
  const [dirDialogOpen, setDirDialogOpen] = useState(false);
  const [browsePath, setBrowsePath] = useState<string>('/');
  const [browseParent, setBrowseParent] = useState<string>('');
  const [browseAllowed, setBrowseAllowed] = useState(false);
  const [browseEntries, setBrowseEntries] = useState<{ name: string; path: string }[]>([]);
  const [browseLoading, setBrowseLoading] = useState(false);
  const [fpkFiles, setFpkFiles] = useState<FpkDownloadFile[]>([]);
  const [fpkDir, setFpkDir] = useState<string>('');
  const [fpkLoading, setFpkLoading] = useState(false);
  const [fpkRemoving, setFpkRemoving] = useState<string | null>(null);
  // 已下载列表折叠（本地持久化）
  const [fpkListCollapsed, setFpkListCollapsed] = useState<boolean>(() => {
    try {
      // 默认折叠（无持久化记录时）；用户手动展开/折叠后按保存值
      const v = localStorage.getItem('fpk-list-collapsed');
      return v === null ? true : v === '1';
    } catch { return true; }
  });
  const toggleFpkList = () => {
    setFpkListCollapsed((v) => {
      try { localStorage.setItem('fpk-list-collapsed', v ? '0' : '1'); } catch { /* ignore */ }
      return !v;
    });
  };
  // 直接安装某个已下载 FPK（SSE 进度）
  const [fpkInstalling, setFpkInstalling] = useState<string | null>(null);
  const [fpkInstallMsg, setFpkInstallMsg] = useState<string>('');
  const [storeInfo, setStoreInfo] = useState<StoreUpdateInfo | null>(null);
  // 自更新确认弹窗（0.6.130 用户定稿：版本号有更新时点击先弹窗确认再更新）
  const [storeUpdateConfirm, setStoreUpdateConfirm] = useState(false);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  // 保存动作计数：传给 SourceManager，点过保存后让其退出抖动排序模式
  const [saveCounter, setSaveCounter] = useState(0);

  // ── 备份设置 tab ──────────────────────────────────────────────────
  // 备份目录（空 = 本机应用数据目录；/vol 下 = 外部存储）；
  // 自动备份开关 + 周期（1–30 天）；应用缓存自动清理（开关 + 保留 1–30 天）。
  const [backupDir, setBackupDir] = useState('');
  const [backupAuto, setBackupAuto] = useState(false);
  // 天数类设置用手动输入框（字符串态，保存时解析钳制 1–30）
  const [backupIntervalDays, setBackupIntervalDays] = useState('7');
  const [cacheCleanOn, setCacheCleanOn] = useState(false);
  const [cacheCleanDays, setCacheCleanDays] = useState('7');
  const [cacheCleanEveryDays, setCacheCleanEveryDays] = useState('1');
  // 加速源自动测速间隔（0.6.148 齿轮选择框）：GitHub / Docker 各自时+分钟
  const [ghProbeH, setGhProbeH] = useState(0);
  const [ghProbeM, setGhProbeM] = useState(5);
  const [dkProbeH, setDkProbeH] = useState(0);
  const [dkProbeM, setDkProbeM] = useState(5);
  // 解析天数输入：非法/越界回默认，钳制 1–30
  const parseDays = useCallback((s: string, def: number): number => {
    const n = parseInt(s, 10);
    if (!Number.isFinite(n) || n < 1) return def;
    return Math.min(n, 30);
  }, []);
  // 恢复确认对话框目标备份（null = 未打开）
  const [restoringName, setRestoringName] = useState<string | null>(null);
  const [restoring, setRestoring] = useState(false);
  // 目录选择对话框目标：fpk=FPK 下载目录 / backup=备份目录（同一浏览器复用）
  const [dirDialogTarget, setDirDialogTarget] = useState<'fpk' | 'backup'>('fpk');
  // 备份列表 + 缓存统计（打开备份 tab 时刷新）
  const [backups, setBackups] = useState<BackupEntry[]>([]);
  const [lastBackupAt, setLastBackupAt] = useState('');
  const [appCache, setAppCache] = useState<AppCacheStats | null>(null);
  const [backupsLoading, setBackupsLoading] = useState(false);
  const [backupRunning, setBackupRunning] = useState(false);
  const [backupRemoving, setBackupRemoving] = useState<string | null>(null);
  const [cleaning, setCleaning] = useState(false);
  const [forceClearing, setForceClearing] = useState(false);
  const [forceArmed, setForceArmed] = useState(false);
  // 备份列表折叠（0.6.130 用户定稿：设置里所有折叠默认都是折叠；本地持久化，与「应用源列表」同款）
  const [backupsCollapsed, setBackupsCollapsed] = useState<boolean>(() => {
    try {
      const v = localStorage.getItem('backups-list-collapsed');
      return v ? v === '1' : true;
    } catch { return true; }
  });

  // 0.6.255：官方应用中心面板账号已彻底移除（纯 OAuth，授权入口在
  // 「应用源 → 飞牛应用中心 → 🔑」）。以下 panel* 状态一并删除。
  // 0.6.209/210：底部保存 dock 键盘处理——与首页 MobileDock 同一套
  // 信号/状态机（resize/clip/pan 三型分流）：
  //   resize（飞牛 app 布局压扁）→ dock translateY(+offsetPx) 钉物理底边；
  //   clip（iOS/微信 可视裁剪）  → 不位移（0.6.209 位移导致大黑框），
  //   改由对话框高度 open 期间切 100dvh 跟随可视视口、底边停在键盘上方；
  //   pan（壳平移）              → 聚焦期间整体隐藏。
  const { open: kbOpen, hidden: kbDockHidden, vvTop: kbVvTop, vvHeight: kbVvHeight, baseHeight: kbBaseH } = useKeyboardDock();
  // 0.6.225（用户 2026-10-01 定稿）：「保存按钮**不管键盘有没有弹出都一直在底部**，
  // 不要贴键盘上沿」。做法＝键盘弹出时**不改对话框高度**，保持键盘弹出前的整屏高度
  // （baseHeight）→ 对话框底边 = **物理屏幕底边**，按钮待在原地不动；键盘只是盖住它
  // （收起键盘即见可点）。这样布局零重排，按钮位置与键盘无关。
  // 若拿不到基线（异常）→ 不套用内联样式（退回自然布局，不劣化）。
  const narrowViewport = typeof window !== 'undefined' && window.innerWidth < 640;
  // 0.6.226：不再等键盘弹出才写内联几何——只要拿到基线就常驻写入。
  // 这样键盘弹出那一刻 **DOM 不发生变化**（基线未变），减少一次重绘/重排，
  // 进一步降低"开关滑钮丢一帧"（真机录屏实锤 0.63s 那一帧）的概率。
  const dialogKeyboardStyle: React.CSSProperties | undefined =
    narrowViewport && kbBaseH > 240
      ? { top: 0, height: kbBaseH, bottom: 'auto' }
      : undefined;

  // 0.6.222：键盘几何调试浮层（**长按顶部版本号 chip** 切换显示）。
  // 用于真机排查「保存按钮位置」类问题：一张截图即可拿到 innerHeight /
  // visualViewport（含 offsetTop）/ 保存按钮与对话框的实际范围。
  const [kbDebug, setKbDebug] = useState(false);
  const [kbDebugText, setKbDebugText] = useState('');
  const kbDebugPressTimer = useRef<number | undefined>(undefined);
  const startKbDebugPress = () => {
    window.clearTimeout(kbDebugPressTimer.current);
    kbDebugPressTimer.current = window.setTimeout(() => setKbDebug((v) => !v), 650);
  };
  const cancelKbDebugPress = () => window.clearTimeout(kbDebugPressTimer.current);
  useEffect(() => {
    if (!kbDebug) return;
    const tick = () => {
      const vv = window.visualViewport;
      const btn = [...document.querySelectorAll('button')].find((b) => (b.textContent || '').trim() === '保存');
      const r = btn ? btn.getBoundingClientRect() : null;
      const d = document.querySelector('[role=dialog]')?.getBoundingClientRect();
      const vk = (navigator as unknown as { virtualKeyboard?: { boundingRect?: DOMRect } }).virtualKeyboard;
      const vkTop = vk?.boundingRect && vk.boundingRect.height > 0 ? Math.round(vk.boundingRect.y) : null;
      setKbDebugText(
        `innerH=${window.innerHeight} vvH=${vv ? Math.round(vv.height) : '-'} vvTop=${vv ? Math.round(vv.offsetTop) : '-'}` +
          ` | kb=${kbOpen ? 1 : 0} vvPx=${kbVvHeight} styleTop=${kbVvTop}` +
          ` | dock=${r ? `${Math.round(r.top)}~${Math.round(r.bottom)}` : '-'}` +
          ` | dlg=${d ? `${Math.round(d.top)}~${Math.round(d.bottom)}` : '-'}` +
          ` | vkTop=${vkTop ?? 'n/a'}`
      );
    };
    tick();
    const id = window.setInterval(tick, 300);
    return () => window.clearInterval(id);
  }, [kbDebug, kbOpen, kbVvHeight, kbVvTop]);

  // 0.6.225：键盘弹出后，若聚焦的输入框恰好落在键盘覆盖区（看不到），
  // 只做**最小滚动**把它带进可见区；布局与保存按钮位置不受影响。
  useEffect(() => {
    if (!kbOpen) return;
    const t = window.setTimeout(() => {
      const el = document.activeElement as HTMLElement | null;
      if (!el || (el.tagName !== 'INPUT' && el.tagName !== 'TEXTAREA')) return;
      const r = el.getBoundingClientRect();
      const visibleBottom = window.innerHeight; // 0.6.225：布局视口 = 键盘顶边（resize 壳）
      if (r.bottom > visibleBottom || r.top < 0) {
        el.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
      }
    }, 140);
    return () => window.clearTimeout(t);
  }, [kbOpen]);

  // FPK 下载列表（打开设置/保存目录后同步刷新）
  const loadFpkFiles = useCallback(async () => {
    setFpkLoading(true);
    try {
      const res = await fetchFpkDownloads();
      setFpkFiles(res.files || []);
      setFpkDir(res.dir || '');
    } catch { /* 目录不存在等场景：显示空列表 */ }
    finally { setFpkLoading(false); }
  }, []);
  const handleFpkRemove = async (name: string) => {
    if (fpkRemoving) return;
    setFpkRemoving(name);
    try {
      await deleteFpkDownload(name);
      setFpkFiles((prev) => prev.filter((f) => f.name !== name));
      toast.success(`已删除 ${name}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '删除失败');
    } finally {
      setFpkRemoving(null);
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
      setFpkDir(res.dir || '');
      toast.success(`已刷新：${files.length} 个 FPK`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '刷新失败');
    } finally {
      setFpkLoading(false);
    }
  };
  // 目录浏览器：读取单级列表（path='/' 为卷根虚拟层）
  const browseTo = useCallback(async (path: string) => {
    setBrowseLoading(true);
    try {
      const res = await browseDownloadDirs(path);
      setBrowsePath(res.path);
      setBrowseParent(res.parent || '');
      setBrowseAllowed(!!res.is_allowed);
      setBrowseEntries(res.entries || []);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '读取目录失败');
    } finally {
      setBrowseLoading(false);
    }
  }, []);
  // 打开目录选择对话框：从当前生效目录的父层进入（立即能看到当前目录本身）；
  // 默认目录（@appdata 下）从虚拟根（卷列表）进入
  const openDirDialog = () => {
    let start = '/';
    const m = fpkDir.match(/^\/vol\d+(\/.*)?$/);
    if (m && m[1]) {
      start = fpkDir.slice(0, fpkDir.lastIndexOf('/')) || '/';
    }
    setDirDialogTarget('fpk');
    setDirDialogOpen(true);
    browseTo(start);
  };
  // 切换下载目录（后端迁移旧目录缓存 + 即时生效，无需重启应用）
  const handleDirSelect = async (path: string) => {
    if (!path || path === fpkDir || dirApplying) return;
    setDirApplying(true);
    try {
      await updateSettings({ download_dir: path });
      toast.success('已切换 FPK 下载目录（已有缓存已迁移）');
      loadFpkFiles();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '切换下载目录失败');
    } finally {
      setDirApplying(false);
    }
  };
  // 对话框内确认「使用此目录」（按目标分派：FPK 下载目录 / 备份目录）
  const confirmUseDir = () => {
    setDirDialogOpen(false);
    if (dirDialogTarget === 'backup') {
      handleBackupDirSelect(browsePath);
    } else {
      handleDirSelect(browsePath);
    }
  };
  // 切换备份目录（后端即时生效；已有备份不迁移，新备份写新目录）
  const handleBackupDirSelect = async (path: string) => {
    if (path === backupDir || dirApplying) return;
    setDirApplying(true);
    try {
      await updateSettings({ backup_dir: path });
      setBackupDir(path);
      toast.success('已切换备份目录（新备份写入此目录）');
      loadBackups();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '切换备份目录失败');
    } finally {
      setDirApplying(false);
    }
  };
  // 打开备份目录选择对话框（从卷列表进入；备份目录允许 /vol 下任意层）
  const openBackupDirDialog = () => {
    setDirDialogTarget('backup');
    setDirDialogOpen(true);
    browseTo('/');
  };
  // 刷新备份列表 + 缓存统计（打开备份 tab / 写备份后）
  const loadBackups = useCallback(async () => {
    setBackupsLoading(true);
    try {
      const res = await fetchBackups();
      setBackups(res.files || []);
      setLastBackupAt(res.last_backup_at || '');
      setAppCache(res.app_cache || null);
      setCacheCleanOn(res.cache_clean_days > 0);
      if (res.cache_clean_days > 0) setCacheCleanDays(String(res.cache_clean_days));
      if (res.cache_clean_every_days > 0) setCacheCleanEveryDays(String(res.cache_clean_every_days));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '读取备份列表失败');
    } finally {
      setBackupsLoading(false);
    }
  }, []);
  // 立即写一份备份快照
  const handleBackupNow = async () => {
    if (backupRunning) return;
    setBackupRunning(true);
    try {
      const r = await runBackupNow();
      toast.success(`已备份：${r.name}`);
      loadBackups();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '备份失败');
    } finally {
      setBackupRunning(false);
    }
  };
  // 删除一个备份文件
  const handleDeleteBackup = async (name: string) => {
    if (backupRemoving) return;
    setBackupRemoving(name);
    try {
      await deleteBackup(name);
      toast.success('已删除备份');
      loadBackups();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '删除备份失败');
    } finally {
      setBackupRemoving(null);
    }
  };
  // 手动清理应用缓存中超过保留天数的文件（不碰已下载 FPK；独立于自动清理开关）
  const handleCleanCache = async () => {
    if (cleaning) return;
    const days = parseDays(cacheCleanDays, 7);
    setCleaning(true);
    try {
      const r = await cleanAppCache(days);
      const mb = (r.freed_bytes / 1048576).toFixed(1);
      toast.success(`已清理 ${r.removed} 个缓存文件，释放 ${mb} MB`);
      loadBackups();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '清理失败');
    } finally {
      setCleaning(false);
    }
  };
  // 强制清空全部应用缓存（不设年龄阈值；仍不碰已下载 FPK）。两次点击确认防误触。
  const handleForceCleanCache = async () => {
    if (forceClearing) return;
    if (!forceArmed) {
      setForceArmed(true);
      setTimeout(() => setForceArmed(false), 3000);
      return;
    }
    setForceArmed(false);
    setForceClearing(true);
    try {
      const r = await cleanAppCache(0, true);
      const mb = (r.freed_bytes / 1048576).toFixed(1);
      toast.success(`已强制清空 ${r.removed} 个缓存文件，释放 ${mb} MB`);
      loadBackups();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '强制清空失败');
    } finally {
      setForceClearing(false);
    }
  };
  // 下载备份文件到手机/电脑（外部存储设备通道）
  const [downloading, setDownloading] = useState<string | null>(null);
  const handleDownloadBackup = async (name: string) => {
    if (downloading) return;
    setDownloading(name);
    try {
      const blob = await downloadBackup(name);
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 5000);
      toast.success('已开始下载，请保存到手机或电脑');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '下载备份失败');
    } finally {
      setDownloading(null);
    }
  };
  // 用指定备份覆盖当前配置（应用随后自动重启）
  const handleRestore = async () => {
    if (!restoringName || restoring) return;
    setRestoring(true);
    try {
      await restoreBackup(restoringName);
      setRestoringName(null);
      toast.success('已恢复，应用即将重启…');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '恢复失败');
      setRestoringName(null);
    } finally {
      setRestoring(false);
    }
  };
  // 直接安装已下载的 FPK（走 SSE 进度；文件保留在缓存中）
  const handleFpkInstall = (name: string) => {
    if (fpkInstalling) return;
    setFpkInstalling(name);
    setFpkInstallMsg('准备安装...');
    installFpkDownload(name, (ev) => {
      if (ev.step === 'error' || ev.error) return;
      if (ev.message) setFpkInstallMsg(ev.message);
    }).promise
      .then(() => {
        toast.success(`已安装 ${name}`);
        onCatalogChanged?.();
        loadFpkFiles();
      })
      .catch((e: unknown) => {
        const msg = e instanceof Error ? e.message : '安装失败';
        toast.error(msg);
      })
      .finally(() => {
        setFpkInstalling(null);
        setFpkInstallMsg('');
      });
  };

  // 后台下载列表（进行中的「下载 fpk」任务：可暂停/继续，参考官方下载中心）
  const [dlTasks, setDlTasks] = useState<BackgroundTask[]>([]);
  const [dlBusyApp, setDlBusyApp] = useState<string | null>(null);
  const loadDlTasks = useCallback(async () => {
    try {
      const list = await fetchTasks();
      // 下载类未完成任务：running/paused 可暂停/继续；error/failed 显示失败行（可清除）。
      // （0.6.146 修复：后端失败态是 'error'，此前只过滤 'failed' → 失败任务
      //  被渲染成「正在下载 0%」且无法清除）
      setDlTasks(list.filter((t) => t.op === 'download' && t.status !== 'done'));
    } catch { /* 后端短暂不可用：保留上次值 */ }
  }, []);
  useEffect(() => {
    loadDlTasks();
    // window.setInterval：组件内 state setter 名为 setInterval，会遮蔽全局函数
    const timer = window.setInterval(loadDlTasks, 3000);
    return () => window.clearInterval(timer);
  }, [loadDlTasks]);
  const handleDlPause = async (appname: string) => {
    if (dlBusyApp) return;
    setDlBusyApp(appname);
    try {
      await pauseDownload(appname);
      toast.success(`已暂停 ${appname} 下载`);
      loadDlTasks();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '暂停失败');
    } finally { setDlBusyApp(null); }
  };
  const handleDlResume = async (appname: string) => {
    if (dlBusyApp) return;
    setDlBusyApp(appname);
    try {
      await resumeDownload(appname);
      toast.success(`继续下载 ${appname}`);
      loadDlTasks();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '继续失败');
    } finally { setDlBusyApp(null); }
  };
  // 删除下载任务（0.6.146 失败态可清除；0.6.147 全部任务可删除——
  // 运行中后端先停止再删，断点文件随任务清除，成品 FPK 保留）
  const handleDlClear = async (t: BackgroundTask) => {
    if (dlBusyApp) return;
    setDlBusyApp(t.appname);
    try {
      if (!t.id) throw new Error('任务无 ID（重启 Moo 可清除列表）');
      await clearDownloadTask(t.id);
      toast.success(`已删除 ${t.appname} 的下载任务`);
      loadDlTasks();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '删除失败');
    } finally { setDlBusyApp(null); }
  };

  // 加速源健康监测（智能监测 + 自动切换提示）
  const [mirrorHealth, setMirrorHealth] = useState<MirrorHealth | null>(null);
  const [healthRefreshing, setHealthRefreshing] = useState(false);
  // 手动测速：触发后立即轮询 last_probe，等探测真正完成再给汇总 toast
  // （避免"点了没反应"的观感——旧实现 4s 后就停，探测往往还没跑完）。
  const runManualSpeedTest = useCallback(async (
    label: string,
    fetchFn: (refresh?: boolean) => Promise<MirrorHealth>,
    apply: (h: MirrorHealth) => void,
  ) => {
    const before = await fetchFn();
    apply(before);
    const probeAge = before.last_probe ? Date.now() - new Date(before.last_probe).getTime() : Number.POSITIVE_INFINITY;
    const summarize = (h: MirrorHealth) => {
      const ok = (h.mirrors || []).filter((m) => m.status === 'ok').length;
      const fail = (h.mirrors || []).filter((m) => m.status === 'fail').length;
      return `${ok} 个正常${fail > 0 ? `，${fail} 个失败` : ''}`;
    };
    if (Number.isFinite(probeAge) && probeAge < 60000) {
      toast.success(`${label}：${summarize(before)}（${Math.max(1, Math.round(probeAge / 1000))} 秒前刚测过）`);
      return;
    }
    await fetchFn(true); // ?refresh=1 触发后台立即探测
    const deadline = Date.now() + 45000;
    let latest = before;
    while (Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 2000));
      try {
        latest = await fetchFn();
        apply(latest);
      } catch { break; }
      if (latest.last_probe && latest.last_probe !== before.last_probe) break;
    }
    toast.success(`${label}完成：${summarize(latest)}`);
  }, []);
  const refreshHealth = useCallback(async () => {
    setHealthRefreshing(true);
    try {
      await runManualSpeedTest('GitHub 测速', fetchMirrorHealth, setMirrorHealth);
    } catch {
      toast.error('GitHub 测速失败，请稍后再试');
    } finally {
      setHealthRefreshing(false);
    }
  }, [runManualSpeedTest]);
  useEffect(() => {
    if (!open || tab !== 'accel') return;
    let cancelled = false;
    const load = async () => {
      try {
        const h = await fetchMirrorHealth();
        if (!cancelled) setMirrorHealth(h);
      } catch { /* ignore */ }
    };
    load();
    // window.setInterval：组件内 state setter 名为 setInterval，会遮蔽全局函数
    const t = window.setInterval(load, 5000);
    return () => { cancelled = true; window.clearInterval(t); };
  }, [open, tab]);

  // Docker 镜像加速健康监测（智能监测 + 自动切换提示）
  const [dockerMirrorHealth, setDockerMirrorHealth] = useState<MirrorHealth | null>(null);
  const [dkHealthRefreshing, setDkHealthRefreshing] = useState(false);
  const refreshDkHealth = useCallback(async () => {
    setDkHealthRefreshing(true);
    try {
      // ?refresh=1 触发后台立即探测（GitHub/Docker 一起探，last_probe 同款轮询）
      await runManualSpeedTest('Docker 测速', fetchDockerMirrorHealth, setDockerMirrorHealth);
    } catch {
      toast.error('Docker 测速失败，请稍后再试');
    } finally {
      setDkHealthRefreshing(false);
    }
  }, [runManualSpeedTest]);
  // KSpeeder 独立应用状态：列表行探测 127.0.0.1:5443（ok=运行中）
  const kspeederRunning = (dockerMirrorHealth?.mirrors || []).find((m) => m.key === 'kspeeder')?.status === 'ok';
  const [ksInstalling, setKsInstalling] = useState(false);
  const handleInstallKSpeeder = useCallback(() => {
    if (ksInstalling) return;
    setKsInstalling(true);
    const handler = (data: UpdateProgress) => {
      if (data.step === 'error') {
        toast.error('安装 KSpeeder 失败：' + (data.message || data.error || '未知错误'));
        setKsInstalling(false);
        return;
      }
      if (data.step === 'done') {
        toast.success('KSpeeder 已安装，本地镜像缓存生效中');
        setKsInstalling(false);
        refreshDkHealth();
      }
    };
    installApp('kspeeder', handler).promise
      .catch((e) => {
        toast.error('安装 KSpeeder 失败：' + (e instanceof Error ? e.message : String(e)));
      })
      .finally(() => setKsInstalling(false));
  }, [ksInstalling, refreshDkHealth]);

  useEffect(() => {
    if (!open || tab !== 'accel') return;
    let cancelled = false;
    const load = async () => {
      try {
        const h = await fetchDockerMirrorHealth();
        if (!cancelled) setDockerMirrorHealth(h);
      } catch { /* ignore */ }
    };
    load();
    const t = window.setInterval(load, 5000);
    return () => { cancelled = true; window.clearInterval(t); };
  }, [open, tab]);

  // 测速状态 —— GitHub / Docker 各自独立
  const [ghChecking, setGhChecking] = useState(false);
  const [dkChecking, setDkChecking] = useState(false);
  const [ghLatency, setGhLatency] = useState<Map<string, MirrorCheckResult>>(new Map());
  const [dkLatency, setDkLatency] = useState<Map<string, MirrorCheckResult>>(new Map());

  // 记住最后一次非 direct 选择，切回 ON 时恢复
  const prevMirrorRef = useRef<string>('gh-proxy');
  const prevDockerMirrorRef = useRef<string>('daocloud');

  const githubEnabled = mirror !== 'direct';
  const dockerEnabled = dockerMirror !== 'direct';

  // 每次打开重置状态并拉取数据
  useEffect(() => {
    if (!open) return;
    setTab('system');
    setLoading(true);
    setGhLatency(new Map());
    setDkLatency(new Map());
    let cancelled = false;
    const loadData = async () => {
      try {
        const [settings, store] = await Promise.all([
          fetchSettings(),
          fetchStoreUpdate()
        ]);
        if (cancelled) return;
        setInterval(settings.check_interval_hours);
        const m = settings.mirror || 'gh-proxy';
        const dm = settings.docker_mirror || 'daocloud';
        setMirror(m);
        setMirrorOptions(settings.mirror_options || []);
        setDockerMirror(dm);
        setDockerMirrorOptions(settings.docker_mirror_options || []);
        setCustomGithubMirror(settings.custom_github_mirror || '');
        setCustomDockerMirror(settings.custom_docker_mirror || '');
        setProxyEnabled(!!settings.proxy_enabled);
        setProxyUrl(settings.proxy_url || '');
        setInstallVolume(settings.install_volume || 0);
        setVolumeOptions(settings.volume_options || []);
        setAutoUpdate(!!settings.auto_update);
        // 0.6.255：面板账号加载行已移除（设置不再下发 panel_* 字段）
        setBackupDir(settings.backup_dir || '');
        setBackupAuto(!!settings.backup_auto);
        setBackupIntervalDays(String(settings.backup_interval_days || 7));
        setCacheCleanOn((settings.cache_clean_days || 0) > 0);
        if (settings.cache_clean_days && settings.cache_clean_days > 0) setCacheCleanDays(String(settings.cache_clean_days));
        if (settings.cache_clean_every_days && settings.cache_clean_every_days > 0) setCacheCleanEveryDays(String(settings.cache_clean_every_days));
        // 加速源测速间隔（0.6.148）：未设置（0h0m）按默认 0h5m 显示
        setGhProbeH(settings.gh_probe_hours || 0);
        setGhProbeM(settings.gh_probe_minutes || 5);
        setDkProbeH(settings.dk_probe_hours || 0);
        setDkProbeM(settings.dk_probe_minutes || 5);
        if (settings.dock_order && settings.dock_order.length > 0) setDockOrderArr(settings.dock_order);
        if (settings.settings_tab_order && settings.settings_tab_order.length > 0) setSettingsTabOrderArr(settings.settings_tab_order);
        if (m !== 'direct') prevMirrorRef.current = m;
        if (dm !== 'direct') prevDockerMirrorRef.current = dm;
        setStoreInfo(store);
      } catch (error) {
        if (cancelled) return;
        console.error('Failed to load settings:', error);
        toast.error('加载设置失败');
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    loadData();
    loadFpkFiles();
    loadBackups();
    return () => { cancelled = true; };
  }, [open, loadFpkFiles, loadBackups]);

  const handleGhSpeedTest = async () => {
    setGhChecking(true);
    setGhLatency(new Map());
    try {
      const result = await checkMirrors('github');
      const gh = new Map<string, MirrorCheckResult>();
      for (const r of result.github_mirrors) gh.set(r.key, r);
      setGhLatency(gh);
    } catch (error) {
      console.error('GitHub speed test failed:', error);
      toast.error('GitHub 测速失败');
    } finally {
      setGhChecking(false);
    }
  };

  const handleDkSpeedTest = async () => {
    setDkChecking(true);
    setDkLatency(new Map());
    try {
      const result = await checkMirrors('docker');
      const dk = new Map<string, MirrorCheckResult>();
      for (const r of result.docker_mirrors) dk.set(r.key, r);
      setDkLatency(dk);
    } catch (error) {
      console.error('Docker speed test failed:', error);
      toast.error('Docker 测速失败');
    } finally {
      setDkChecking(false);
    }
  };

  const handleGithubToggle = (checked: boolean) => {
    if (checked) {
      setMirror(prevMirrorRef.current);
    } else {
      prevMirrorRef.current = mirror;
      setMirror('direct');
    }
  };

  const handleDockerToggle = (checked: boolean) => {
    if (checked) {
      setDockerMirror(prevDockerMirrorRef.current);
    } else {
      prevDockerMirrorRef.current = dockerMirror;
      setDockerMirror('direct');
    }
  };

  const handleSave = async () => {
    // 科学加速：开启时地址必填（后端同样整单拒绝，前端先拦给提示）
    if (proxyEnabled && !proxyUrl.trim()) {
      toast.error('开启科学加速须先填写代理地址');
      return;
    }
    setSaving(true);
    try {
      await updateSettings({
        check_interval_hours: interval,
        mirror,
        docker_mirror: dockerMirror,
        // 后端指针语义：空串 = 显式清除（此前 `|| undefined` 会把「清空」
        // 变成「不提交」，用户清掉自定义镜像/地址后保存无效）。
        custom_github_mirror: customGithubMirror,
        custom_docker_mirror: customDockerMirror,
        install_volume: installVolume,
        auto_update: autoUpdate,
        // 0.6.255：面板账号字段已彻底移除（官方源 = 纯 OAuth）
        // 备份设置（备份目录空串 = 回本机默认数据目录，需显式提交）
        backup_dir: backupDir,
        backup_auto: backupAuto,
        backup_interval_days: parseDays(backupIntervalDays, 7),
        cache_clean_days: cacheCleanOn ? parseDays(cacheCleanDays, 7) : 0,
        cache_clean_every_days: parseDays(cacheCleanEveryDays, 1),
        // 加速源自动测速间隔（0.6.148 齿轮选择框）：成对提交
        gh_probe_hours: ghProbeH,
        gh_probe_minutes: ghProbeM,
        dk_probe_hours: dkProbeH,
        dk_probe_minutes: dkProbeM,
        // 科学加速（0.6.206）：开关+地址成对提交（空地址 = 后端自动关）
        proxy_enabled: proxyEnabled,
        proxy_url: proxyUrl.trim(),
      });
      toast.success('设置已保存');
      loadFpkFiles();
      // 保存后停留原地（2026-09-24 用户反馈：点保存不应自动退出设置界面）
    } catch (error) {
      console.error('Failed to save settings:', error);
      toast.error('保存设置失败');
    } finally {
      setSaving(false);
      // 点过保存（无论成败）→ 通知应用源 tab 退出抖动排序模式
      // （排序本身在每次拖拽落位时已单独持久化，这里只收敛界面状态）
      setSaveCounter((c) => c + 1);
    }
  };

  // 科学加速（0.6.206）：单独校验给定代理地址的连通性（不保存、不影响当前生效配置）
  const handleTestProxy = async () => {
    const u = proxyUrl.trim();
    if (!u) {
      toast.error('请先填写代理地址');
      return;
    }
    setProxyTesting(true);
    try {
      const r = await testProxy(u);
      if (r.ok) {
        toast.success(`代理可用，耗时 ${r.latency_ms ?? '?'}ms`);
      } else {
        toast.error(r.error || '代理不可用');
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '代理测试失败');
    } finally {
      setProxyTesting(false);
    }
  };

  const githubSelectOptions = mirrorOptions.filter((opt) => opt.key !== 'direct');
  const dockerSelectOptions = dockerMirrorOptions.filter((opt) => opt.key !== 'direct');

  const mirrorLabel = (key: string) =>
    mirrorOptions.find((o) => o.key === key)?.label || (key === 'custom' ? '自定义' : key);
  const dockerMirrorLabel = (key: string) =>
    dockerMirrorOptions.find((o) => o.key === key)?.label || (key === 'custom' ? '自定义' : key);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* 移动端 = 整页（与单应用详情同构）；桌面端 = 居中宽面板。
          右上角 X 仅桌面端保留（移动端用顶栏 ← 返回）。 */}
      {/* 0.6.210：键盘处理与首页 dock 同一套（useKeyboardDock 多信号+状态机），
          按壳形态分流（0.6.217 更新保存 dock 行为）：
          - resize（飞牛 app adjustResize）：对话框 open 期间切 100dvh 跟随
            压扁后的布局视口；保存 dock 保持自然占位钉在对话框底边 = 可见
            底边（完整压扁壳下 = 键盘顶边），部分压扁壳再用 liftPx 上抬
            键盘残留覆盖量——按钮键盘弹出时固定可见、可点；
          - clip（iOS/微信/浏览器可视裁剪）：对话框切 100dvh 跟随可视视口，
            底边自然停在键盘上方、按钮就位于键盘之上（0.6.209 位移方案已废）；
          - pan（壳平移）：聚焦期间整体隐藏兜底。 */}
      <DialogContent
        style={dialogKeyboardStyle}
        className="inset-0 w-full h-full max-w-none rounded-none sm:rounded-[18px] translate-x-0 translate-y-0 flex flex-col !p-0 gap-0 overflow-visible sm:overflow-hidden bg-background sm:inset-auto sm:left-[50%] sm:top-[50%] sm:h-[88vh] sm:max-w-3xl sm:translate-x-[-50%] sm:translate-y-[-50%] [&>button.absolute]:hidden sm:[&>button.absolute]:inline-flex">
        {/* 键盘几何调试浮层（**长按版本 chip** 才显示；真机排查用，默认不打扰） */}
        {kbDebug && (
          <div className="pointer-events-none fixed left-1 top-1 z-[9999] max-w-[97vw] rounded bg-black/85 px-1.5 py-1 font-mono text-[9px] leading-tight text-lime-300">
            {kbDebugText}
          </div>
        )}
        {/* 顶栏：← 返回 + 标题 */}
        <div className="flex items-center gap-1 border-b border-border/60 px-2 py-2 shrink-0">
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            onClick={() => onOpenChange(false)}
            aria-label="返回"
            title="返回"
          >
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <DialogTitle className="text-[15px] font-semibold tracking-tight">设置</DialogTitle>
          {/* 商店版本号：页头最右侧（原「商店版本」卡片 2026-09-26 起下线；
              有可用更新时旁边显示「更新到 vX」药丸，原卡片更新入口功能保留） */}
          <div className="ml-auto flex shrink-0 items-center gap-1.5 pr-1 sm:pr-12">
            {/* 商店版本 chip（0.6.130 用户定稿）：
                有更新=红底「有更新 vX」→ 点击弹窗确认后应用内自更新；
                无更新=灰「vX」→ 点击顶部通知「已是最新版本 vX」 */}
            {storeInfo?.has_update && onStoreUpdate ? (
              <button
                onClick={() => setStoreUpdateConfirm(true)}
                className="rounded-full bg-destructive px-2 py-0.5 text-[11px] font-medium tabular-nums text-white transition-colors hover:bg-destructive/80"
                title={`有更新 v${storeInfo.available_version}，点击确认更新`}
              >
                有更新 v{storeInfo.available_version}
              </button>
            ) : (
              <button
                onClick={() => {
                  if (storeInfo?.current_version) {
                    toast.info(`已是最新版本 v${storeInfo.current_version}`);
                  } else {
                    toast.info('版本信息加载中，请稍后再试');
                  }
                }}
                onPointerDown={startKbDebugPress}
                onPointerUp={cancelKbDebugPress}
                onPointerLeave={cancelKbDebugPress}
                onPointerCancel={cancelKbDebugPress}
                className="rounded-full bg-muted/60 px-2 py-0.5 text-[11px] font-medium tabular-nums text-muted-foreground transition-colors hover:bg-muted"
                title={`Moo 版本 v${storeInfo?.current_version || '…'}，点击查看更新（长按显示键盘调试信息）`}
              >
                v{storeInfo?.current_version || '…'}
              </button>
            )}
          </div>
        </div>

        <div className="flex-1 min-h-0 flex flex-col">
          {/* 顶部 tab：分段控件，内容区全宽。容器左右滑动（tab 文字大小不变、
              不换行 shrink-0）——tab 增多时横向滚动而非挤压换行。 */}
          <div className="shrink-0 px-4 sm:px-6 pt-3 overflow-x-auto [-ms-overflow-style:none] [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
            <div className="inline-flex rounded-xl bg-muted/60 p-1" role="tablist">
              {orderedTabs.map(({ key, label, icon: Icon }) => (
                <button
                  key={key}
                  role="tab"
                  aria-selected={tab === key}
                  onClick={() => setTab(key)}
                  className={cn(
                    "h-8 rounded-lg px-4 flex items-center gap-1.5 text-[13px] font-medium transition-colors focus:outline-none shrink-0 whitespace-nowrap",
                    tab === key
                      ? "bg-card text-foreground shadow-sm"
                      : "text-muted-foreground hover:text-foreground"
                  )}
                >
                  <Icon className="h-3.5 w-3.5 shrink-0" />
                  {label}
                </button>
              ))}
            </div>
          </div>

          {/* 内容区 */}
          <div className="flex-1 min-h-0 min-w-0 overflow-y-auto overscroll-contain pt-3">
            {tab === 'backup' ? (
              <div className="px-3 py-4 sm:px-6 sm:py-5 space-y-4">
                {/* 设置备份 */}
                <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-4 space-y-3">
                  <div className="flex items-center gap-2">
                    <Archive className="h-4 w-4 text-muted-foreground" />
                    <span className="text-sm font-medium">设置备份</span>
                  </div>
                  <p className="text-xs text-muted-foreground leading-relaxed">
                    备份文件 = 设置全量副本（应用源 / 加速 / 面板账号 / 下载目录）。可备份到本机 NAS，或用列表里的下载按钮存到手机、电脑等外部设备。
                  </p>
                  <Separator />
                  {/* 备份目录（与 FPK 下载目录同款：下钻浏览选目录，不手输、无「更换」） */}
                  <div className="space-y-1.5">
                    <div className="text-[13px] font-medium">备份目录</div>
                    <Button
                      variant="outline"
                      className="h-9 w-full justify-start gap-2 text-xs font-mono"
                      onClick={openBackupDirDialog}
                      disabled={dirApplying}
                    >
                      {dirApplying ? (
                        <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-primary" />
                      ) : (
                        <FolderDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      )}
                      <span className="truncate" title={backupDir || undefined}>
                        {dirApplying ? '切换中…' : backupDir || '默认（应用数据目录）'}
                      </span>
                    </Button>
                    <p className="text-[11px] text-muted-foreground">
                      未配置时备份存在应用数据目录（仅本机可见）；选 /vol 下的共享目录后可被外部设备同步。
                    </p>
                  </div>
                  {/* 备份周期（手动输入天数，1–30） */}
                  <div className="flex items-center justify-between gap-3">
                    <div className="min-w-0">
                      <div className="text-[13px] font-medium">备份周期</div>
                      <p className="text-[11px] text-muted-foreground">每 N 天自动写一份快照</p>
                    </div>
                    <div className="flex shrink-0 items-center gap-1.5">
                      <Input
                        type="number"
                        inputMode="numeric"
                        min={1}
                        max={30}
                        value={backupIntervalDays}
                        onChange={(e) => setBackupIntervalDays(e.target.value)}
                        className="h-8 w-16 px-2 text-center text-[13px] tabular-nums"
                        aria-label="备份周期天数"
                      />
                      <span className="text-xs text-muted-foreground">天</span>
                    </div>
                  </div>
                  {/* 自动备份开关 */}
                  <div className="flex items-center justify-between">
                    <div>
                      <div className="text-[13px] font-medium">自动备份</div>
                      <p className="text-[11px] text-muted-foreground">同目录保留最近 20 份，更早自动淘汰</p>
                    </div>
                    <Switch checked={backupAuto} onCheckedChange={setBackupAuto} />
                  </div>
                  {/* 最近备份 + 立即备份 */}
                  <div className="flex items-center justify-between">
                    <span className="text-[11px] text-muted-foreground">
                      {lastBackupAt ? `最近备份 ${lastBackupAt.replace('T', ' ').slice(0, 16)}` : '尚未备份'}
                    </span>
                    <Button size="sm" variant="secondary" className="h-8 gap-1.5" onClick={handleBackupNow} disabled={backupRunning}>
                      {backupRunning ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Archive className="h-3.5 w-3.5" />}
                      {backupRunning ? '备份中…' : '立即备份'}
                    </Button>
                  </div>
                  {/* 备份列表 */}
                  {backupsLoading && backups.length === 0 && (
                    <div className="flex items-center gap-2 px-1 py-1 text-[11px] text-muted-foreground">
                      <Loader2 className="h-3.5 w-3.5 animate-spin" />
                      正在读取备份列表…
                    </div>
                  )}
                  {backups.length > 0 && (
                    <>
                      <button
                        type="button"
                        onClick={() => setBackupsCollapsed((v) => {
                          const next = !v;
                          try { localStorage.setItem('backups-list-collapsed', next ? '1' : '0'); } catch { /* ignore */ }
                          return next;
                        })}
                        className="flex w-full items-center justify-between rounded-lg bg-muted/30 px-3 py-2 text-[13px] font-medium"
                      >
                        <span className="flex items-center gap-1.5">
                          备份列表
                          <span className="text-[11px] font-normal text-muted-foreground">{backups.length} 份</span>
                        </span>
                        <ChevronDown className={cn('h-4 w-4 text-muted-foreground transition-transform', backupsCollapsed && '-rotate-90')} />
                      </button>
                      {!backupsCollapsed && (
                        <div className="max-h-56 space-y-1 overflow-y-auto pr-0.5">
                          {backups.map((b) => (
                            <div key={b.name} className="flex items-center gap-2 rounded-lg border border-border/40 px-3 py-1.5">
                              <span className="min-w-0 flex-1 truncate font-mono text-[11px]" title={b.name}>
                                {b.name.replace('moo-backup-', '').replace('.json', '')}
                              </span>
                              <span className="shrink-0 text-[11px] text-muted-foreground">{formatBytes(b.size)}</span>
                              <Button
                                variant="ghost"
                                size="icon"
                                className="h-6 w-6 shrink-0"
                                onClick={() => handleDownloadBackup(b.name)}
                                disabled={downloading === b.name}
                                title="下载到手机/电脑"
                                aria-label={`下载备份 ${b.name}`}
                              >
                                {downloading === b.name ? <Loader2 className="h-3 w-3 animate-spin" /> : <Download className="h-3 w-3" />}
                              </Button>
                              <Button
                                variant="ghost"
                                size="icon"
                                className="h-6 w-6 shrink-0"
                                onClick={() => setRestoringName(b.name)}
                                disabled={restoring}
                                title="用此备份恢复设置（应用将重启）"
                                aria-label={`恢复备份 ${b.name}`}
                              >
                                <RotateCcw className="h-3 w-3" />
                              </Button>
                              <Button
                                variant="ghost"
                                size="icon"
                                className="h-6 w-6 shrink-0 hover:bg-destructive/10 hover:text-destructive"
                                onClick={() => handleDeleteBackup(b.name)}
                                disabled={backupRemoving === b.name}
                                title="删除此备份"
                                aria-label={`删除备份 ${b.name}`}
                              >
                                {backupRemoving === b.name ? <Loader2 className="h-3 w-3 animate-spin" /> : <Trash2 className="h-3 w-3" />}
                              </Button>
                            </div>
                          ))}
                        </div>
                      )}
                    </>
                  )}
                </div>

                {/* Moo 应用缓存清理（只动 app 自身缓存，不碰已下载 FPK） */}
                <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-4 space-y-3">
                  <div className="flex items-center gap-2">
                    <HardDrive className="h-4 w-4 text-muted-foreground" />
                    <span className="text-sm font-medium">Moo 应用缓存</span>
                  </div>
                  {appCache && (
                    <p className="text-xs text-muted-foreground leading-relaxed">
                      当前 {appCache.file_count} 个文件 · {formatBytes(appCache.total_bytes)}
                      {cacheCleanOn && appCache.old_count > 0 && (
                        <span className="text-amber-500">
                          {' '}（{parseDays(cacheCleanDays, 7)} 天前：{appCache.old_count} 个 · {formatBytes(appCache.old_bytes)}）
                        </span>
                      )}
                    </p>
                  )}
                  <div className="flex items-center justify-between">
                    <div>
                      <div className="text-[13px] font-medium">自动清理</div>
                      <p className="text-[11px] text-muted-foreground">按周期删除超过保留天数的缓存</p>
                    </div>
                    <Switch checked={cacheCleanOn} onCheckedChange={setCacheCleanOn} />
                  </div>
                  {cacheCleanOn && (
                    <div className="flex items-center justify-between gap-3">
                      <div className="min-w-0">
                        <div className="text-[13px] font-medium">清理周期</div>
                        <p className="text-[11px] text-muted-foreground">每 N 天自动清理一次</p>
                      </div>
                      <div className="flex shrink-0 items-center gap-1.5">
                        <Input
                          type="number"
                          inputMode="numeric"
                          min={1}
                          max={30}
                          value={cacheCleanEveryDays}
                          onChange={(e) => setCacheCleanEveryDays(e.target.value)}
                          className="h-8 w-16 px-2 text-center text-[13px] tabular-nums"
                          aria-label="清理周期天数"
                        />
                        <span className="text-xs text-muted-foreground">天</span>
                      </div>
                    </div>
                  )}
                  <div className="flex items-center justify-between gap-3">
                    <div className="min-w-0">
                      <div className="text-[13px] font-medium">保留天数</div>
                      <p className="text-[11px] text-muted-foreground">更早的缓存文件将被清理</p>
                    </div>
                    <div className="flex shrink-0 items-center gap-1.5">
                      <Input
                        type="number"
                        inputMode="numeric"
                        min={1}
                        max={30}
                        value={cacheCleanDays}
                        onChange={(e) => setCacheCleanDays(e.target.value)}
                        className="h-8 w-16 px-2 text-center text-[13px] tabular-nums"
                        aria-label="保留天数"
                      />
                      <span className="text-xs text-muted-foreground">天</span>
                    </div>
                  </div>
                  <div className="flex items-center justify-between">
                    <span className="text-[11px] text-muted-foreground">
                      删除 {parseDays(cacheCleanDays, 7)} 天前的缓存文件
                    </span>
                    <div className="flex shrink-0 items-center gap-1.5">
                      <Button size="sm" variant="secondary" className="h-8 gap-1.5" onClick={handleCleanCache} disabled={cleaning || forceClearing} title="手动清理一次，无需开启自动清理">
                        {cleaning ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
                        {cleaning ? '清理中…' : '手动清理'}
                      </Button>
                      <Button
                        size="sm"
                        variant="secondary"
                        className={forceArmed ? 'h-8 gap-1.5 border-red-500/40 bg-red-500/10 text-red-600 hover:bg-red-500/15 hover:text-red-600' : 'h-8 gap-1.5'}
                        onClick={handleForceCleanCache}
                        disabled={forceClearing || cleaning}
                        title="删除全部缓存文件（不设天数阈值）；首次打开时图标 / README 会重新拉取"
                      >
                        {forceClearing ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
                        {forceClearing ? '清空中…' : forceArmed ? '确认清空？' : '强制清空'}
                      </Button>
                    </div>
                  </div>
                  <p className="text-[11px] text-muted-foreground">
                    只清 Moo 自身缓存（图标 / README 等）：手动清理只删超过保留天数的，强制清空则全部删除。已下载的 FPK 与已安装的 FPK 应用绝不会被清理。
                  </p>
                </div>
              </div>
            ) : tab === 'source' ? (
              <div className="px-3 py-4 sm:px-6 sm:py-5">
                <SourceManager onCatalogChanged={onCatalogChanged} saveCounter={saveCounter} />
              </div>
            ) : tab === 'accel' ? (
              loading ? (
                <div className="flex justify-center py-12">
                  <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
                </div>
              ) : (
                <div className="px-3 py-4 sm:px-6 sm:py-5 space-y-4">
                {/* 下载加速 */}
                <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-4 space-y-5">
                  <div className="space-y-3">
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-medium leading-none whitespace-nowrap">
                          GitHub 下载加速
                        </span>
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-6 px-2 text-xs text-muted-foreground hover:text-foreground"
                          onClick={handleGhSpeedTest}
                          disabled={ghChecking}
                          title="测速"
                          aria-label="GitHub 测速"
                        >
                          {ghChecking ? (
                            <Loader2 className="h-3 w-3 animate-spin sm:mr-1" />
                          ) : (
                            <Zap className="h-3 w-3 sm:mr-1" />
                          )}
                          <span className="hidden sm:inline">测速</span>
                        </Button>
                      </div>
                      <Switch checked={githubEnabled} onCheckedChange={handleGithubToggle} />
                    </div>
                    {githubEnabled && (
                      <>
                        <Select value={mirror} onValueChange={(value) => setMirror(value)}>
                          <SelectTrigger>
                            <SelectValue placeholder="选择镜像" />
                          </SelectTrigger>
                          <SelectContent>
                            {githubSelectOptions.map((opt) => {
                              const result = ghLatency.get(opt.key);
                              return (
                                <SelectItem key={opt.key} value={opt.key}>
                                  <span className="flex items-center justify-between w-full gap-2">
                                    <span>{opt.label}</span>
                                    {result && (
                                      <span className={`text-[11px] tabular-nums ${latencyColor(result)}`}>
                                        {latencyText(result)}
                                      </span>
                                    )}
                                  </span>
                                </SelectItem>
                              );
                            })}
                          </SelectContent>
                        </Select>
                        {mirror === 'custom' && (
                          <Input
                            placeholder="https://your-proxy.example.com/"
                            value={customGithubMirror}
                            onChange={(e) => setCustomGithubMirror(e.target.value)}
                          />
                        )}
                      </>
                    )}
                    <p className="text-xs text-muted-foreground">
                      {githubEnabled ? '使用镜像加速从 GitHub 下载应用安装包' : '直接从 GitHub 下载，不使用加速'}
                    </p>
                    <MirrorHealthPanel
                      title="GitHub 加速源优选"
                      health={mirrorHealth}
                      options={mirrorOptions}
                      customConfigured={customGithubMirror !== ''}
                      labelOf={mirrorLabel}
                      intervalH={ghProbeH}
                      intervalM={ghProbeM}
                      onIntervalChange={(h, m) => { setGhProbeH(h); setGhProbeM(m); }}
                      refreshing={healthRefreshing}
                      onRefresh={refreshHealth}
                      showSpeed
                    />
                  </div>

                  <Separator />

                  <div className="space-y-3">
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-medium leading-none whitespace-nowrap">
                          Docker 镜像加速
                        </span>
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-6 px-2 text-xs text-muted-foreground hover:text-foreground"
                          onClick={handleDkSpeedTest}
                          disabled={dkChecking}
                          title="测速"
                          aria-label="Docker 测速"
                        >
                          {dkChecking ? (
                            <Loader2 className="h-3 w-3 animate-spin sm:mr-1" />
                          ) : (
                            <Zap className="h-3 w-3 sm:mr-1" />
                          )}
                          <span className="hidden sm:inline">测速</span>
                        </Button>
                      </div>
                      <Switch checked={dockerEnabled} onCheckedChange={handleDockerToggle} />
                    </div>
                    {dockerEnabled && (
                      <>
                        <Select value={dockerMirror} onValueChange={(value) => setDockerMirror(value)}>
                          <SelectTrigger>
                            <SelectValue placeholder="选择镜像" />
                          </SelectTrigger>
                          <SelectContent>
                            {dockerSelectOptions.map((opt) => {
                              const result = dkLatency.get(opt.key);
                              return (
                                <SelectItem key={opt.key} value={opt.key}>
                                  <span className="flex items-center justify-between w-full gap-2">
                                    <span>{opt.label}</span>
                                    {result && (
                                      <span className={`text-[11px] tabular-nums ${latencyColor(result)}`}>
                                        {latencyText(result)}
                                      </span>
                                    )}
                                  </span>
                                </SelectItem>
                              );
                            })}
                          </SelectContent>
                        </Select>
                        {dockerMirror === 'custom' && (
                          <Input
                            placeholder="your-mirror.example.com/"
                            value={customDockerMirror}
                            onChange={(e) => setCustomDockerMirror(e.target.value)}
                          />
                        )}
                      </>
                    )}
                    <p className="text-xs text-muted-foreground">
                      {dockerEnabled
                        ? '仅供参考：实际拉取走系统级 Docker 镜像源，可按下方测速结果配置'
                        : '加速已关闭：实际拉取走系统级 Docker 镜像源'}
                    </p>
                    {/* KSpeeder 镜像加速：独立应用依赖（同 New Store 依赖外部 iStoreEnhance 的做法），
                        未运行时提供安装入口，走标准应用安装管线（Blue-Mink 源 appname=kspeeder） */}
                    <div className="flex items-center justify-between gap-3 rounded-xl bg-muted/30 border border-border/20 px-3 py-2.5">
                      <div className="min-w-0">
                        <div className="text-[13px] font-medium">KSpeeder 镜像加速</div>
                        <p className="text-[11px] text-muted-foreground leading-relaxed">
                          独立应用（127.0.0.1:5443）；实际生效需系统镜像源指向 127.0.0.1:5443
                        </p>
                      </div>
                      {kspeederRunning ? (
                        <Badge className="shrink-0 gap-1.5 text-[11px]">
                          <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
                          运行中
                        </Badge>
                      ) : (
                        <Button
                          size="sm"
                          variant="secondary"
                          className="shrink-0 h-8"
                          onClick={handleInstallKSpeeder}
                          disabled={ksInstalling}
                        >
                          {ksInstalling ? (
                            <Loader2 className="h-3.5 w-3.5 animate-spin" />
                          ) : (
                            <Download className="h-3.5 w-3.5" />
                          )}
                          {ksInstalling ? '安装中…' : '安装 KSpeeder'}
                        </Button>
                      )}
                    </div>
                    <MirrorHealthPanel
                      title="Docker 加速源测速"
                      health={dockerMirrorHealth}
                      options={dockerMirrorOptions}
                      customConfigured={customDockerMirror !== ''}
                                            intervalH={dkProbeH}
                      intervalM={dkProbeM}
                      onIntervalChange={(h, m) => { setDkProbeH(h); setDkProbeM(m); }}
                      labelOf={dockerMirrorLabel}
                      refreshing={dkHealthRefreshing}
                      onRefresh={refreshDkHealth}
                      showSpeed
                      referenceOnly
                      speedNote="测速结果按拉 nginx 镜像首层实测。仅供参考：实际拉取走系统级镜像源，照下表配置；KSpeeder (本地) 为独立应用本地缓存。后台按右侧间隔自动测速。"
                    />
                  </div>
                </div>
                {/* 科学加速（0.6.206）：本机代理，仅 GitHub 域名改道 */}
                <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-4 space-y-3">
                  <div className="flex items-center justify-between gap-2">
                    <div className="min-w-0">
                      <div className="text-sm font-medium">科学加速</div>
                      <p className="mt-0.5 text-xs text-muted-foreground leading-relaxed">
                        GitHub 上游流量（源同步 / 图标 / FPK 下载 / 更新检测）走本机代理；镜像与非 GitHub 流量保持直连，代理失效自动回退加速源。
                      </p>
                    </div>
                    <Switch checked={proxyEnabled} onCheckedChange={setProxyEnabled} aria-label="启用科学加速" />
                  </div>
                  {proxyEnabled && (
                    <div className="space-y-2">
                      <div className="flex items-center gap-2">
                        <Input
                          value={proxyUrl}
                          onChange={(e) => setProxyUrl(e.target.value)}
                          placeholder="socks5://127.0.0.1:1080"
                          spellCheck={false}
                          className="h-9 flex-1 font-mono text-[13px]"
                          aria-label="代理地址"
                        />
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={handleTestProxy}
                          disabled={proxyTesting || !proxyUrl.trim()}
                          className="h-9 shrink-0 gap-1.5"
                        >
                          {proxyTesting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Zap className="h-3.5 w-3.5" />}
                          {proxyTesting ? '测试中…' : '测试代理'}
                        </Button>
                      </div>
                      <p className="text-[11px] text-muted-foreground leading-relaxed">
                        支持 HTTP / HTTPS / SOCKS4 / SOCKS5（含 socks5h 远端 DNS，如 http://127.0.0.1:7890 或 socks5://127.0.0.1:1080）。地址仅在本机使用，不会外发。
                      </p>
                    </div>
                  )}
                </div>
              </div>
              )
            ) : loading ? (
              <div className="flex justify-center py-12">
                <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
              </div>
            ) : tab === 'notify' ? (
              <NotifySettingsTab />
            ) : tab === 'log' ? (
              <LogSettingsTab />
            ) : tab === 'about' ? (
              <AboutTab />
            ) : (
              <div className="px-3 py-4 sm:px-6 sm:py-5 space-y-4">
                {/* Dock 栏排序（0.6.122）：移动端底部主导航，长按进入拖动；
                    头部「恢复默认 + 折叠」与「应用源列表」同款（默认折叠，持久化） */}
                <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-4">
                  <div className="flex items-center justify-between gap-2">
                    <div className="text-sm font-medium leading-none">Dock 栏排序</div>
                    <div className="flex shrink-0 items-center gap-1">
                      <Button
                        variant="ghost"
                        className="h-7 px-2.5 text-xs text-muted-foreground hover:text-foreground"
                        onClick={() => { void saveDockOrder(DOCK_DEFAULT_ORDER); }}
                        title="恢复默认顺序"
                      >
                        恢复默认
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7"
                        onClick={() => toggleOrderCollapsed('dock')}
                        title={dockOrderCollapsed ? '展开 Dock 栏排序' : '折叠 Dock 栏排序'}
                        aria-label={dockOrderCollapsed ? '展开 Dock 栏排序' : '折叠 Dock 栏排序'}
                      >
                        <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", dockOrderCollapsed && "-rotate-90")} />
                      </Button>
                    </div>
                  </div>
                  {!dockOrderCollapsed && (
                    <div className="mt-3">
                      <ReorderList items={orderedDockItems} onReorder={saveDockOrder} busy={orderSaving} />
                    </div>
                  )}
                </div>

                {/* 设置 tab 排序（0.6.122）：本页顶部 6 个 tab；头部按钮同上 */}
                <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-4">
                  <div className="flex items-center justify-between gap-2">
                    <div className="text-sm font-medium leading-none">设置 tab 排序</div>
                    <div className="flex shrink-0 items-center gap-1">
                      <Button
                        variant="ghost"
                        className="h-7 px-2.5 text-xs text-muted-foreground hover:text-foreground"
                        onClick={() => { void saveSettingsTabOrder(SETTINGS_TAB_DEFAULT_ORDER); }}
                        title="恢复默认顺序"
                      >
                        恢复默认
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7"
                        onClick={() => toggleOrderCollapsed('tab')}
                        title={tabOrderCollapsed ? '展开设置 tab 排序' : '折叠设置 tab 排序'}
                        aria-label={tabOrderCollapsed ? '展开设置 tab 排序' : '折叠设置 tab 排序'}
                      >
                        <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", tabOrderCollapsed && "-rotate-90")} />
                      </Button>
                    </div>
                  </div>
                  {!tabOrderCollapsed && (
                    <div className="mt-3">
                      <ReorderList items={orderedSettingsTabItems} onReorder={saveSettingsTabOrder} busy={orderSaving} />
                    </div>
                  )}
                </div>

                {/* 常规 */}
                <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-4 space-y-4">
                  <div className="space-y-2">
                    <label className="text-sm font-medium leading-none">
                      自动检查更新间隔
                    </label>
                    <Select value={interval.toString()} onValueChange={(value) => setInterval(Number(value))}>
                      <SelectTrigger>
                        <SelectValue placeholder="选择间隔" />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="1">1 小时</SelectItem>
                        <SelectItem value="3">3 小时</SelectItem>
                        <SelectItem value="6">6 小时</SelectItem>
                        <SelectItem value="12">12 小时</SelectItem>
                        <SelectItem value="24">24 小时</SelectItem>
                      </SelectContent>
                    </Select>
                    <p className="text-xs text-muted-foreground">
                      后台周期：刷新源目录、检测更新、源自动监测（连续 5 个周期空源自动停用）、自动更新应用均按此间隔进行。6 小时更新发现较快，24 小时更省资源。
                    </p>
                  </div>

                  {volumeOptions.length > 0 && (
                    <>
                      <Separator />
                      <div className="space-y-2">
                        <label className="text-sm font-medium leading-none">
                          应用安装位置
                        </label>
                        <Select
                          value={installVolume.toString()}
                          onValueChange={(value) => setInstallVolume(Number(value))}
                        >
                          <SelectTrigger>
                            <SelectValue placeholder="选择存储空间" />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="0">系统默认</SelectItem>
                            {volumeOptions.map((vol) => (
                              <SelectItem key={vol.index} value={vol.index.toString()}>
                                <span className="flex items-center gap-2">
                                  <span>存储空间 {vol.index}</span>
                                  {vol.total_bytes > 0 && (
                                    <span className="text-xs text-muted-foreground">
                                      {formatBytes(vol.free_bytes)} 可用 / {formatBytes(vol.total_bytes)}
                                    </span>
                                  )}
                                </span>
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                        <p className="text-xs text-muted-foreground">
                          选择应用安装到哪个存储空间，默认使用系统指定的存储空间
                        </p>
                      </div>
                    </>
                  )}

                </div>

                {/* 自动更新应用（安装位置下方小卡片：周期检查时后台自动检测+安装，
                    无需打开应用；排除已忽略应用与商店自身） */}
                <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-3.5">
                  <div className="flex items-center justify-between gap-3">
                    <div className="space-y-0.5">
                      <label className="text-sm font-medium leading-none flex items-center gap-1.5">
                        <Zap className="h-3.5 w-3.5 text-muted-foreground" />
                        自动更新应用
                      </label>
                      <p className="text-xs text-muted-foreground">
                        周期检查时自动在后台检测并安装更新，无需打开应用
                      </p>
                    </div>
                    <Switch checked={autoUpdate} onCheckedChange={setAutoUpdate} />
                  </div>
                </div>

                {/* FPK 下载目录 + 已下载列表（独立卡片，不与常规设置混在一起） */}
                <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-4">
                  <div className="space-y-2">
                    <label className="text-sm font-medium leading-none flex items-center gap-1.5">
                      <FolderDown className="h-3.5 w-3.5 text-muted-foreground" />
                      FPK 下载目录
                    </label>
                    {/* 目录选择按钮：点开下钻浏览（卷根 → 共享目录 → 任意子层），
                        不再手输路径；切换即时生效并自动迁移已有缓存 */}
                    <Button
                      variant="outline"
                      className="h-9 w-full justify-start gap-2 text-xs font-mono"
                      onClick={openDirDialog}
                      disabled={dirApplying}
                    >
                      {dirApplying ? (
                        <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-primary" />
                      ) : (
                        <FolderDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      )}
                      <span className="truncate" title={fpkDir || undefined}>
                        {dirApplying ? '切换中…' : fpkDir || '选择下载目录…'}
                      </span>
                    </Button>
                    {fpkDir && (
                      <p className="truncate font-mono text-[11px] text-muted-foreground" title={fpkDir}>
                        当前生效：{fpkDir}
                      </p>
                    )}
                    {/* 已下载的 FPK 小节（刷新/展开按钮在此，不再占卡片标题行） */}
                    <div className="flex items-center justify-between gap-2 pt-1">
                      <span className="text-xs font-medium text-muted-foreground">
                        已下载的 FPK{fpkFiles.length > 0 ? `（${fpkFiles.length}）` : ''}
                      </span>
                      <div className="flex items-center gap-0.5">
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
                    {/* 后台下载（进行中/已暂停）：下载可暂停/继续（按钮与安装同款，下载中显示「暂停」） */}
                    {dlTasks.length > 0 && (
                      <div className="rounded-lg border border-blue-500/30 bg-blue-500/5 divide-y divide-border/40">
                        {dlTasks.map((t) => {
                          const paused = t.status === 'paused';
                          const failed = t.status === 'error' || t.status === 'failed';
                          // 后台下载管理器任务带 downloaded/total 字段；官方 cloud 下载走
                          // 长操作视图（无此二字段）——暂停/继续/删除仅对前者生效（0.6.147）
                          const isMgr = t.downloaded != null || t.total != null;
                          const pct = t.total && t.total > 0 && t.downloaded != null
                            ? Math.min(100, Math.round((t.downloaded / t.total) * 100))
                            : 0;
                          return (
                            <div key={t.id || t.appname} className="px-3 py-2">
                              <div className="flex items-center gap-2">
                                <div className="min-w-0 flex-1">
                                  <div className="flex items-center gap-1.5">
                                    {failed ? (
                                      <XCircle className="h-3 w-3 text-red-500 shrink-0" />
                                    ) : paused ? (
                                      <Pause className="h-3 w-3 text-amber-500 shrink-0" />
                                    ) : (
                                      <Download className="h-3 w-3 text-blue-500 animate-pulse shrink-0" />
                                    )}
                                    <span className="truncate text-xs font-medium" title={t.appname}>
                                      {t.appname}
                                    </span>
                                  </div>
                                  <div
                                    className={cn("truncate text-[11px]", failed ? "text-red-500/80" : "text-muted-foreground")}
                                    title={failed ? t.message : undefined}
                                  >
                                    {failed
                                      ? (t.message ? `下载失败 · ${t.message}` : '下载失败')
                                      : paused ? '已暂停 · 可从断点继续' : `正在下载 ${pct}%`}
                                  </div>
                                </div>
                                {isMgr && !failed && (
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    className="h-6 shrink-0 px-2 text-xs text-primary hover:bg-primary/10"
                                    onClick={() => (paused ? handleDlResume(t.appname) : handleDlPause(t.appname))}
                                    disabled={dlBusyApp !== null}
                                    title={paused ? '继续下载（断点续传）' : '暂停下载'}
                                  >
                                    {paused ? (
                                      <><Play className="mr-0.5 h-3 w-3" />继续</>
                                    ) : (
                                      <><Pause className="mr-0.5 h-3 w-3" />暂停</>
                                    )}
                                  </Button>
                                )}
                                {/* 0.6.147：全部任务可删除（运行中先停止；断点文件随任务清除，成品 FPK 保留） */}
                                {isMgr && (
                                  <Button
                                    variant="ghost"
                                    size="icon"
                                    className="h-6 w-6 shrink-0 text-muted-foreground hover:text-red-500 hover:bg-red-500/10"
                                    onClick={() => handleDlClear(t)}
                                    disabled={dlBusyApp !== null}
                                    title="删除该下载任务（运行中会先停止；断点文件一并清除，成品 FPK 保留）"
                                    aria-label={`删除 ${t.appname} 下载任务`}
                                  >
                                    <Trash2 className="h-3.5 w-3.5" />
                                  </Button>
                                )}
                              </div>
                              {!paused && !failed && <Progress value={pct} className="mt-1.5 h-1.5 w-full" />}
                            </div>
                          );
                        })}
                      </div>
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
                      <div className="max-h-60 overflow-y-auto rounded-lg border border-border/40 divide-y divide-border/40">
                        {fpkFiles.map((f) => {
                          const dn = (f.display_name || '').trim();
                          const showName = !!dn && dn.toLowerCase() !== f.name.replace(/\.fpk$/i, '').toLowerCase();
                          return (
                          <div key={f.name} className="flex items-center gap-2.5 px-3 py-2.5">
                            <div className="min-w-0 flex-1">
                              <div className="truncate text-xs font-medium" title={f.name}>
                                {f.name}
                              </div>
                              {showName && (
                                <div className="truncate text-xs text-foreground/80" title={dn}>
                                  {dn}
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
                                className="shrink-0 rounded-full bg-muted/80 px-2 h-6 inline-flex items-center text-[11px] font-medium text-muted-foreground"
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
                              className="h-6 w-6 shrink-0 p-0 text-muted-foreground hover:text-red-500"
                              onClick={() => handleFpkRemove(f.name)}
                              disabled={fpkRemoving === f.name || !!fpkInstalling}
                              title="删除该 FPK 缓存"
                              aria-label={`删除 ${f.name}`}
                            >
                              {fpkRemoving === f.name
                                ? <Loader2 className="h-3 w-3 animate-spin" />
                                : <Trash2 className="h-3 w-3" />}
                            </Button>
                          </div>
                          );
                        })}
                      </div>
                    ))}
                  </div>
                </div>


                {/* 商店版本卡片已移除：版本号并入顶部 tab 行最右侧（2026-09-26 用户定稿） */}

              </div>
            )}

            {/* FPK 下载目录选择：下钻浏览（卷根 → 共享目录 → 任意子层），
            点行进入子目录，面包屑跳级，到底后「使用此目录」确认 */}
            <Dialog open={dirDialogOpen} onOpenChange={setDirDialogOpen}>
            <DialogContent className="rounded-[18px] max-w-[calc(100vw-1.5rem)] sm:max-w-md max-h-[calc(100dvh-2rem)] flex flex-col border-border/20 shadow-appstore bg-card">
            <DialogHeader>
            <DialogTitle>
            {dirDialogTarget === 'backup' ? '选择备份目录' : '选择 FPK 下载目录'}
            </DialogTitle>
            <DialogDescription>
            {dirDialogTarget === 'backup'
            ? '点击目录进入下一层；到目标层级后点「使用此目录」（新备份写入此目录，可被手机/电脑同步）'
            : '点击目录进入下一层；到目标层级后点「使用此目录」（已有缓存自动迁移）'}
            </DialogDescription>
            </DialogHeader>
            {/* 面包屑（点任意层级直接跳转） */}
            <div className="flex flex-wrap items-center gap-0.5 text-xs">
            <button
            type="button"
            onClick={() => browseTo('/')}
            className={cn(
            "rounded-md px-2 py-1",
            browsePath === '/' ? "bg-primary/10 text-primary" : "text-muted-foreground hover:bg-muted"
            )}
            >
            存储
            </button>
            {browsePath !== '/' &&
            browsePath.split('/').filter(Boolean).map((seg, i, arr) => {
            const prefix = '/' + arr.slice(0, i + 1).join('/');
            return (
            <span key={prefix} className="flex items-center gap-0.5">
            <span className="text-muted-foreground/50">/</span>
            <button
            type="button"
            onClick={() => browseTo(prefix)}
            className={cn(
            "rounded-md px-2 py-1 font-mono",
            prefix === browsePath ? "bg-primary/10 text-primary" : "text-muted-foreground hover:bg-muted"
            )}
            >
            {seg}
            </button>
            </span>
            );
            })}
            </div>
            <div className="max-h-64 overflow-y-auto rounded-lg border border-border/60 divide-y divide-border/40">
            {browsePath !== '/' && (
            <button
            type="button"
            onClick={() => browseTo(browseParent || '/')}
            className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-muted-foreground hover:bg-muted/40"
            >
            <ArrowLeft className="h-3.5 w-3.5 shrink-0" />
            返回上级目录
            </button>
            )}
            {/* 备份目录：根层置顶「本机·应用数据目录」（= 清空选择，回本机默认） */}
            {dirDialogTarget === 'backup' && browsePath === '/' && (
            <button
            type="button"
            onClick={() => {
              setDirDialogOpen(false);
              handleBackupDirSelect('');
            }}
            className="flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-muted/40"
            >
            <HardDrive className="h-4 w-4 shrink-0 text-muted-foreground" />
            <span className="text-xs">本机 · 应用数据目录</span>
            {backupDir === '' && (
            <span className="shrink-0 rounded-full bg-primary/10 px-1.5 text-[10px] font-medium text-primary">
            当前
            </span>
            )}
            </button>
            )}
            {browseLoading ? (
            <div className="flex items-center justify-center gap-2 px-3 py-6 text-xs text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
            加载中…
            </div>
            ) : browseEntries.length === 0 ? (
            <div className="px-3 py-6 text-center text-xs text-muted-foreground">
            该目录没有子目录（可直接「使用此目录」）
            </div>
            ) : (
            browseEntries.map((e) => (
            <button
            key={e.path}
            type="button"
            onClick={() => browseTo(e.path)}
            className="flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-muted/40"
            >
            <Folder className="h-4 w-4 shrink-0 text-muted-foreground" />
            <span className="truncate font-mono text-xs" title={e.path}>
            {e.path}
            </span>
            {e.path === (dirDialogTarget === 'backup' ? backupDir : fpkDir) && (
            <span className="shrink-0 rounded-full bg-primary/10 px-1.5 text-[10px] font-medium text-primary">
            当前
            </span>
            )}
            <ChevronRight className="ml-auto h-4 w-4 shrink-0 text-muted-foreground/40" />
            </button>
            ))
            )}
            </div>
            <DialogFooter className="gap-2 sm:gap-2">
            <Button variant="ghost" onClick={() => setDirDialogOpen(false)}>
            取消
            </Button>
            <Button
            disabled={!browseAllowed || browseLoading || dirApplying}
            onClick={confirmUseDir}
            >
            使用此目录
            </Button>
            </DialogFooter>
            </DialogContent>
            </Dialog>

            {/* 恢复确认（恢复 = 覆盖当前设置 + 应用重启） */}
            <Dialog open={!!restoringName} onOpenChange={(o) => !o && !restoring && setRestoringName(null)}>
              <DialogContent className="rounded-[18px] max-w-[calc(100vw-1.5rem)] sm:max-w-sm border-border/20 shadow-appstore bg-card">
                <DialogHeader>
                  <DialogTitle className="flex items-center gap-2">
                    <RotateCcw className="h-4 w-4 text-primary" />
                    恢复设置
                  </DialogTitle>
                  <DialogDescription className="space-y-1.5">
                    <p>
                      将用备份{' '}
                      <span className="font-mono text-foreground">
                        {restoringName?.replace('moo-backup-', '').replace('.json', '')}
                      </span>{' '}
                      覆盖当前全部设置（应用源 / 加速 / 面板账号 / 下载目录）。
                    </p>
                    <p className="text-amber-500">恢复后应用会自动重启，当前未保存的设置会丢失。</p>
                  </DialogDescription>
                </DialogHeader>
                <DialogFooter className="gap-2 sm:gap-2">
                  <Button variant="ghost" onClick={() => setRestoringName(null)} disabled={restoring}>
                    取消
                  </Button>
                  <Button onClick={handleRestore} disabled={restoring}>
                    {restoring && <Loader2 className="h-4 w-4 animate-spin" />}
                    {restoring ? '恢复中…' : '确认恢复'}
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

            {/* 自更新确认弹窗（0.6.130 用户定稿）：头部版本号有更新时，
                点击先弹窗提示、确认后才开始应用内自更新 */}
            <Dialog open={storeUpdateConfirm} onOpenChange={setStoreUpdateConfirm}>
              <DialogContent className="sm:max-w-sm">
                <DialogHeader>
                  <DialogTitle className="text-[15px] font-semibold">
                    发现更新 v{storeInfo?.available_version}
                  </DialogTitle>
                  <DialogDescription>
                    当前版本 v{storeInfo?.current_version}。点击「立即更新」将下载并安装新版本，完成后 Moo 会自动重启。
                  </DialogDescription>
                </DialogHeader>
                <DialogFooter>
                  <Button variant="ghost" onClick={() => setStoreUpdateConfirm(false)}>
                    取消
                  </Button>
                  <Button
                    onClick={() => {
                      setStoreUpdateConfirm(false);
                      onOpenChange(false);
                      onStoreUpdate?.();
                    }}
                  >
                    立即更新
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>

          </div>

          {/* 保存：底部固定 dock（0.6.127 用户定稿）：移出滚动区、作为列末子元素 →
              真正钉在设置对话框底边，不再随内容长度浮动
              （应用源列表折叠时内容短，旧 sticky 方案会停在屏幕中部）。
              0.6.129 用户定稿：关于 tab 无设置内容 → 不显示保存按钮；其余 tab 都显示。
              0.6.217：
              ① 去掉按钮外的磨砂描边卡（暗色主题下 = 蓝色按钮四周的黑框，
                 用户实锤「保存按钮有黑框，都不要黑框」）→ 按钮本身即全宽
                 悬浮条（rounded-2xl + 阴影保留悬浮感，视觉同详情页下载条）；
              ② 键盘弹出时固定在可见底边（键盘顶边）：不再压槽溢出到窗口
                 底边之下（0.6.211 方案 = 按钮藏在键盘后面、点不到，用户
                 实锤）；resize 型完整压扁壳下按钮自然落在键盘顶边，
                 部分压扁壳（飞牛 app）用 liftPx 上抬残留覆盖量；
                 收起键盘随视口扩张连续滑回、无跳变。
              外框与内容卡片同宽（px-3 / sm:px-6）。 */}
          {tab !== 'about' && !kbDockHidden && (
            <div className="shrink-0 px-3 pb-[calc(env(safe-area-inset-bottom)+0.5rem)] pt-2 sm:px-6">
              {/* 0.6.221：不再做 translateY 上抬——对话框底边已等于可见底边
                  （见上方 dialogKeyboardStyle），再抬就会过冲：按钮浮在键盘上方、
                  不像"固定在底部"（用户实报）。这里保持流式钉在对话框底边即可。 */}
              <div className="mx-auto w-full sm:w-72">
                <Button
                  className="w-full h-11 rounded-2xl shadow-lg shadow-black/10"
                  onClick={handleSave}
                  disabled={saving || loading}
                >
                  {saving && <Loader2 className="-ml-1 mr-2 h-4 w-4 animate-spin" />}
                  保存
                </Button>
              </div>
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
};

export default SettingsPage;
