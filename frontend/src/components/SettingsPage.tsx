import React, { useState, useEffect, useRef, useCallback } from 'react';
import { fetchSettings, updateSettings, fetchStoreUpdate, fetchMirrorHealth, fetchDockerMirrorHealth, fetchKspeederLatest, fetchFpkDownloads, controlApp, fetchTasks, clearDownloadTask, pauseDownload, resumeDownload, browseDownloadDirs, fetchBackups, runBackupNow, deleteBackup, cleanAppCache, restoreBackup, downloadBackup, fetchAbout, fetchAboutMem, fetchApps, fetchAppDetail, testProxy, fetchDockerMirrorStatus, applyDockerMirror, syncMirrorLists, restoreMirrorLists, setMirrorHidden, installKspeeder, type MirrorOption, type VolumeOption, type UpdateProgress, type MirrorHealth, type BackgroundTask, type BackupEntry, type AppCacheStats, type AboutInfo, type DockerMirrorStatus, type AboutDocItem, type AboutMem } from '../api/client';
import type { StoreUpdateInfo } from '../api/client';
// 0.6.308：关于页「最新更新日志」卡复用详情页 README 渲染（markdown+滚动盒）
import { ChangelogPager } from './ChangelogPager';
import { copyTextToClipboard } from '../lib/clipboard';
import { isNoticeLayerInteract } from '../lib/noticeOutside';
import { useKeyboardDock, useMobileDesktopSim } from '../lib/hooks';
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
import { AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogCancel } from "@/components/ui/alert-dialog"
import { Separator } from "@/components/ui/separator"
import { Badge } from "@/components/ui/badge"
import { Switch } from "@/components/ui/switch"
import { Progress } from "@/components/ui/progress"
import { Activity, AlertTriangle, ArrowLeft, Archive, Bell, BookOpen, ChevronDown, ChevronRight, Clock, Code2, Database, Download, DownloadCloud, FileText, Folder, FolderDown, HardDrive, Info, Loader2, Pause, Play, Plus, Power, RefreshCw, RotateCcw, SlidersHorizontal, Trash2, XCircle, Zap } from 'lucide-react'
import { toast } from 'sonner'
import { cn } from "@/lib/utils"
import { depWindowIssue, depWindowSpanMinutes, formatSpanMinutes } from "@/lib/depWindow"
import SourceManager from './SourceManager'
import NotifySettingsTab from './NotifySettingsTab'
import LogSettingsTab from './LogSettingsTab'
import ReorderList from './ReorderList'
import GearTimePicker from './GearTimePicker'
// 0.6.548：自动更新「允许自动停用的应用」白名单勾选弹窗
import DepAllowPickerDialog from './DepAllowPickerDialog'
// 0.6.325 C4：已下载 FPK 列表段（含安装向导）抽入独立组件
import FpkDownloads from './FpkDownloads'
// 0.6.325 C1：卷显示名优先级（友好名 · /volN → 存储空间 N · /volN）
import { volumeOptionLabel } from '../lib/volumeLabel'

// 已装应用更新策略（0.6.272）：横排切换
// 0.6.284（用户定稿）：档位命名 严格/同源/同宗 → 严格/友好/宽松（语义不变，仅更口语）
// 0.6.326x（用户令）：原独立开关「自动纠正应用更新」并入本策略做第四档「强制上游」
const UPDATE_POLICY_OPTIONS: { value: string; label: string }[] = [
  { value: 'strict', label: '严格' },
  { value: 'origin', label: '友好' },
  { value: 'lineage', label: '宽松' },
  { value: 'upstream', label: '强制上游' },
];
const UPDATE_POLICY_DESCS: Record<string, string> = {
  strict: '只提示当初安装来源的新版本，最保守（默认）',
  origin: '同一发布仓库的新版本也提示，其他源的同名包不算更新',
  lineage: '同作者或同发布仓库的新版本都提示（覆盖最全）',
  upstream: '已装的搬运应用改用上游原作者的版本；其余应用按「严格」处理（不跨源）',
};

// 0.6.548：自动更新遇到「依赖方正在运行」时的三档处理策略
const DEP_POLICY_OPTIONS: { value: 'skip' | 'notify' | 'stop'; label: string }[] = [
  { value: 'skip', label: '跳过' },
  { value: 'notify', label: '只提醒' },
  { value: 'stop', label: '自动停用后升级' },
];
const DEP_POLICY_DESCS: Record<'skip' | 'notify' | 'stop', string> = {
  skip: '保持现状：不碰任何应用，留给你手动处理',
  notify: '跳过，并在每轮自动更新时提示你手动处理',
  stop: '停用依赖方 → 升级 → 自动启用回来',
};

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
  { key: 'settings', label: '设置' },
];
const DOCK_DEFAULT_ORDER = DOCK_TAB_ITEMS.map((t) => t.key);
const SETTINGS_TAB_DEFAULT_ORDER = TABS.map((t) => t.key);

interface SettingsPageProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 0.6.282：深链恢复的初始 tab（无效值回落「系统设置」） */
  initialTab?: string;
  /** 0.6.282：当前选中 tab 上报，供写入深链 hash */
  onTabChange?: (tab: string) => void;
  onStoreUpdate?: () => void;
  /** 外部应用源变化后刷新应用目录 */
  onCatalogChanged?: () => void;
  /** 0.6.535：源列表行内「列表」按钮 → 关闭设置并跳到主页只看该源的应用 */
  onOpenSourceApps?: (sourceID: string, sourceName: string) => void;
  /** 极光预览模式（0.6.297，仅网页端）：面板底=推荐卡一号渐变，元素样式不动 */
  aurora?: boolean;
}

function formatBytes(bytes: number): string {
  if (bytes <= 0) return '';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  const val = bytes / Math.pow(1024, i);
  return `${val >= 100 ? Math.round(val) : val.toFixed(1)} ${units[i]}`;
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
  /** 0.6.321 F7：「智能选源包含直连」开关（仅 GitHub 组传入；0.6.525 起常驻占位，非智能模式禁用）。 */
  includeDirect?: { value: boolean; onChange: (v: boolean) => void };
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
  /** 0.6.512 清单目录化：从 lists/ 远端清单同步 / 恢复到默认 / 删掉某个默认源 */
  onSync?: () => void;
  onRestore?: () => void;
  onDelete?: (key: string) => void;
  listBusy?: boolean;
  /** 0.6.515：并排两栏时去掉自带卡片底（避免「卡片内嵌卡片」） */
  plain?: boolean;
  /** 0.6.523（用户令）：同步 / 恢复 / 添加 统一放在加速源列表正上方 */
  onAdd?: () => void;
  /**
   * 0.6.522（用户令）：取消镜像下拉框——直接在下方测速列表里点选加速源。
   * selectedKey = 表单当前选择（mirror / dockerMirror，含 auto / direct）；
   * onSelect = 点选回调（点行 = 手动选用该源；点「智能」行 = 回到智能选源）。
   */
  selectedKey?: string;
  onSelect?: (key: string) => void;
  /**
   * 0.6.532（用户令）：GitHub / Docker 的标题行（含开关那一行）也要能整行点开合——
   * 由父组件受控传入折叠状态与切换回调，标题行与「测速与监控」行共用同一状态，
   * 不传则保持组件内部自管（默认折叠，localStorage 持久化）。
   */
  collapsed?: boolean;
  onToggleCollapsed?: () => void;
}> = ({ title, health, options, customConfigured, labelOf, refreshing, onRefresh, showSpeed, speedNote, referenceOnly, includeDirect, intervalH, intervalM, onIntervalChange, onSync, onRestore, onAdd, onDelete, listBusy, plain, selectedKey, onSelect, collapsed: collapsedProp, onToggleCollapsed }) => {
  // 0.6.524（用户令）：行内删除两段式确认——第一下变红进入确认态，
  // 3s 内再点才真删（对齐设置页「强制清空」/ 下载弹窗行内删除的既有交互）。
  const [confirmDel, setConfirmDel] = React.useState<string | null>(null);
  const confirmDelTimer = React.useRef<number | null>(null);
  const clearConfirmDel = () => {
    if (confirmDelTimer.current !== null) {
      window.clearTimeout(confirmDelTimer.current);
      confirmDelTimer.current = null;
    }
    setConfirmDel(null);
  };
  const handleDeleteClick = (e: React.MouseEvent, key: string) => {
    e.stopPropagation();
    if (confirmDel === key) {
      clearConfirmDel();
      onDelete?.(key);
      return;
    }
    setConfirmDel(key);
    if (confirmDelTimer.current !== null) window.clearTimeout(confirmDelTimer.current);
    confirmDelTimer.current = window.setTimeout(() => {
      confirmDelTimer.current = null;
      setConfirmDel(null);
    }, 3000);
  };
  const [collapsedLocal, setCollapsedLocal] = React.useState<boolean>(() => {
    try {
      // 默认折叠（无持久化记录时）；用户手动展开/折叠后按保存值
      const v = localStorage.getItem(`health-panel-collapsed:${title}`);
      return v === null ? true : v === 'true';
    } catch { return true; }
  });
  // 0.6.532：受控优先（父级标题行与「测速与监控」行共用同一状态）
  const collapsed = collapsedProp !== undefined ? collapsedProp : collapsedLocal;
  const toggleCollapsed = () => {
    if (onToggleCollapsed) { onToggleCollapsed(); return; }
    setCollapsedLocal(prev => {
      const next = !prev;
      try { localStorage.setItem(`health-panel-collapsed:${title}`, String(next)); } catch { /* ignore */ }
      return next;
    });
  };

  const rows = React.useMemo(() => {
    const statsByKey = new Map((health?.mirrors || []).map((s) => [s.key, s] as const));
    // 0.6.321r：「智能选源包含直连」开启时显示 direct 行（直连作为候选参与测速
    // 排名后可见化：状态/延迟/速度/失败原因/「当前」徽章全走既有行渲染）。
    // 仅 GitHub 组传 includeDirect；Docker 组不传 → 恒隐藏 direct（原行为）。
    const showDirect = !!includeDirect?.value;
    const base = options.filter((o) =>
      o.key !== 'auto' &&
      (o.key !== 'direct' || showDirect) &&
      (o.key !== 'custom' || customConfigured)
    );
    const list = base.map((o) => {
      const st = statsByKey.get(o.key);
      return {
        key: o.key,
        label: o.label,
        status: st?.status || '',
        latency_ms: st?.latency_ms || 0,
        speed_bps: st?.speed_bps || 0,
        consec_fails: st?.consec_fails || 0,
        // 0.6.321 F6：失败原因短语透传（下方第二行展示）
        error: st?.error || '',
        // 2026-11-04：续传深测结论（undefined = 未深测，不显示徽标）
        resume_ok: st?.resume_ok,
      };
    });
    // 0.6.321r4：按测速自动排序（用户指令，GitHub/Docker 两组共用本面板）——
    // ok 在前（吞吐降序；无吞吐数据按首字节延迟升序，与后端 topSpeedKeys
    // 优选序一致=「列表头≈当前优选源」）→ 0.6.333 F4 新增 limited（429 限流，
    // 按延迟升序；限流源没有吞吐数据，只按首字节延迟排）→ fail（连败少者先）
    // → 未测速殿后。
    // 首屏无探测数据时全=未测速，stable sort 保持原选项序，无跳动。
    const rank = (r: (typeof list)[number]) =>
      r.status === 'ok' ? 0 : r.status === 'limited' ? 1 : r.status === 'fail' ? 2 : 3;
    return list.sort((a, b) => {
      const d = rank(a) - rank(b);
      if (d !== 0) return d;
      if (a.status === 'ok') return (b.speed_bps - a.speed_bps) || (a.latency_ms - b.latency_ms);
      if (a.status === 'limited') return a.latency_ms - b.latency_ms;
      if (a.status === 'fail') return a.consec_fails - b.consec_fails;
      return 0;
    });
  }, [options, health, customConfigured, includeDirect?.value]);

  const okCount = rows.filter((r) => r.status === 'ok').length;
  // 0.6.333 F4：429 限流单列计数（不计入失败，也不计入正常）
  const limitedCount = rows.filter((r) => r.status === 'limited').length;
  const failCount = rows.filter((r) => r.status === 'fail').length;
  const tallyText = [
    limitedCount > 0 ? `${limitedCount} 限流` : '',
    failCount > 0 ? `${failCount} 失败` : '',
  ].filter(Boolean).join('，');

  return (
    <div className={cn(
      'relative',
      plain
        ? 'px-0 py-0'
        : 'rounded-xl bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl border border-white/10 px-3 py-3',
    )}>
      {/* 0.6.150（用户定稿）：标题上移至「卡片最顶部 ↔ 齿轮最顶部」的正中——
          卡片外顶→齿轮顶 = 1(border) + 12(py-3) + 14(齿轮列顶部留白) = 27px，
          标题中心 = 距卡片顶 13.5px（绝对定位 -translate-y-1/2；
          ⚠ 若 border/py-3/齿轮顶部留白任一改动，此值需同步）。
          标题原位置（滚轮中心线）改「自动监测间隔」字样（0.6.152 定稿：与标题
          同大小 13px，左区水平居中；0.6.150 的 11px 左对齐已废弃）。
          左列 14+24+14=52px 与 GearTimePicker 列同构：中 24px 格=滚轮同高
          （小字中心=齿轮中心线，与刷新/折叠按钮平齐，0.6.149 对齐保持；
          0.6.358 齿轮由 72px 三行收到 24px 单格，故此处 72→24、卡片头随之矮 48px） */}
      {/* 0.6.512b（用户反馈：加速源页桌面/移动端排版崩坏）→ 标题改回正常流：
          小屏标题独占一行，控件第二行（同步/恢复 + 间隔齿轮 + 测速/折叠）；
          大屏同一行排布，控件永不挤压标题（旧实现标题绝对定位 + 左列 flex-1，
          窄屏时「自动监测间隔」被压成竖排并与标题重叠）。 */}
      <div
        className="mb-2 flex flex-wrap items-center gap-x-2 gap-y-1.5"
        onClick={toggleCollapsed}
        title={collapsed ? '点击展开监测的加速源列表' : '点击折叠监测的加速源列表'}
      >
        <span className="min-w-0 cursor-pointer truncate text-[13px] font-medium">{title}</span>
        <span
          className="flex w-full shrink-0 items-center gap-1.5 sm:ms-auto sm:w-auto"
          onClick={(e) => e.stopPropagation()}
        >
          <span className="whitespace-nowrap text-[11px] text-muted-foreground">自动监测间隔</span>
          {/* 0.6.148：自动测速间隔（苹果时钟齿轮式：时 0-23 / 分 0-59），
              替换原「每 5 分钟自动测速」静态文案；保存后下一起效周期即按新间隔 */}
          <GearTimePicker
            hours={intervalH}
            minutes={intervalM}
            onChange={onIntervalChange}
            ariaLabel={`${title} 自动测速间隔`}
          />
          {/* 0.6.527（用户令）：移动端也显示「0h0m=5m」换算提示（原 lg 以上才显示）。 */}
          <span className="whitespace-nowrap text-[10px] leading-none text-muted-foreground/80">0h0m=5m</span>
          <Button
            variant="ghost"
            size="icon"
            className="ms-auto h-7 w-7 sm:ms-0"
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
        </span>
      </div>
      {/* 0.6.523（用户令）：同步 / 恢复 / 添加 移到加速源列表正上方。
          0.6.527（用户令）：折叠从这三个按钮开始——收起时整块隐藏，
          只保留标题行（间隔/刷新/折叠）与一行健康摘要。 */}
      {!collapsed && (onSync || onRestore || onAdd) && (
        <div className="mb-2 grid grid-cols-3 gap-2">
          {onSync && (
            <Button
              variant="outline"
              size="sm"
              className="h-7 w-full px-2 text-[11px]"
              onClick={onSync}
              disabled={listBusy}
              title="从远端清单同步默认加速源（不影响自定义源）"
            >
              <DownloadCloud className={cn('h-3.5 w-3.5 mr-1', listBusy && 'animate-pulse')} />
              同步
            </Button>
          )}
          {onRestore && (
            <Button
              variant="outline"
              size="sm"
              className="h-7 w-full px-2 text-[11px]"
              onClick={onRestore}
              disabled={listBusy}
              title="恢复被删除的默认加速源（不影响自定义源）"
            >
              <RotateCcw className="h-3.5 w-3.5 mr-1" />
              恢复
            </Button>
          )}
          {onAdd && (
            <Button
              variant="outline"
              size="sm"
              className="h-7 w-full px-2 text-[11px]"
              onClick={onAdd}
              disabled={listBusy}
              title="添加自定义加速源（每行一个地址）"
            >
              <Plus className="h-3.5 w-3.5 mr-1" />
              添加
            </Button>
          )}
        </div>
      )}
      {/* 0.6.525（用户令）：卡片高度恒定——「包含直连」行常驻占位；
          0.6.527：折叠时随同步/恢复/添加一起隐藏（折叠起点在按钮组）。 */}
      {!collapsed && includeDirect && (
        <label className={cn(
          'mb-2 flex select-none items-start gap-2 px-1 text-[12px] text-muted-foreground sm:items-center',
          selectedKey === 'auto' ? 'cursor-pointer' : 'cursor-default opacity-50',
        )}>
          <input
            type="checkbox"
            className="mt-0.5 h-3.5 w-3.5 shrink-0 accent-primary sm:mt-0"
            checked={includeDirect.value}
            disabled={selectedKey !== 'auto'}
            onChange={(e) => includeDirect.onChange(e.target.checked)}
          />
          <span>包含直连（作为候选参与测速）</span>
        </label>
      )}
      {collapsed ? (
        <p className="text-[11px] text-muted-foreground">
          {okCount}/{rows.length} 正常{tallyText ? `（${tallyText}）` : ''}
          {/* 0.6.321r：去掉 active!=='direct' 守卫——直连生效（智能模式直连胜出/
              全镜像 fail 兜底）时折叠摘要也如实显示「当前 直连 GitHub」（labelOf
              两组 options 均含 direct 条目，标签现成） */}
          {health?.active && ` · ${referenceOnly ? '最快' : '当前'} ${labelOf(health.active)}`}
        </p>
      ) : (
        <>
          <p className="text-[11px] leading-relaxed text-muted-foreground mb-2">
            {referenceOnly
              ? '仅供参考，实际拉取走系统镜像源。'
              : (speedNote ?? '点一行手动选用；失效自动切回智能优选。')}
          </p>
          {!referenceOnly && health?.last_switch && (
            <div className="mb-2.5 rounded-lg border border-primary/30 bg-primary/5 px-3 py-2 text-[12px] leading-relaxed text-primary">
              {health.last_switch.reason}
              <span className="ml-1 whitespace-nowrap">
                （{labelOf(health.last_switch.from)} → {labelOf(health.last_switch.to)}）
              </span>
            </div>
          )}
          {/* 0.6.525（用户令）：智能选源恢复为列表首行（与 0.6.522 及更早版本一致），
              标题行的开关与「智能选源」文字已移除。点该行 = 交回智能优选。 */}
          <div role="radiogroup" aria-label={title} className="grid grid-cols-2 gap-x-2 gap-y-1">
            {onSelect && (
              <div
                role="radio"
                aria-checked={selectedKey === 'auto'}
                tabIndex={0}
                onClick={() => onSelect('auto')}
                onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onSelect('auto'); } }}
                className={cn(
                  'min-w-0 rounded-lg px-1.5 py-1 outline-none',
                  'cursor-pointer hover:bg-muted/40 focus-visible:ring-1 focus-visible:ring-primary/40',
                  selectedKey === 'auto' && 'bg-primary/10',
                )}
              >
                <div className="flex min-w-0 items-center gap-1.5">
                  <span className="h-2 w-2 shrink-0 rounded-full bg-primary" />
                  <span className="min-w-0 flex-1 truncate text-[12px] leading-tight">智能 · 自动选最快</span>
                </div>
                <div className="mt-0.5 flex min-w-0 items-center gap-1 pl-3.5 text-[10px] leading-tight text-muted-foreground">
                  {selectedKey === 'auto' && <span className="shrink-0 font-medium text-primary">已选</span>}
                  <span className="truncate">按测速结果自动切换</span>
                </div>
              </div>
            )}
            {rows.map((row) => (
              <div
                key={row.key}
                role={onSelect ? 'radio' : undefined}
                aria-checked={onSelect ? row.key === selectedKey : undefined}
                tabIndex={onSelect ? 0 : undefined}
                onClick={onSelect ? () => onSelect(row.key) : undefined}
                onKeyDown={onSelect ? (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onSelect(row.key); } } : undefined}
                title={row.resume_ok === true ? '支持断点续传' : row.resume_ok === false ? '可能不支持断点续传（断流后需从头下载）' : undefined}
                className={cn(
                  'min-w-0 rounded-lg px-1.5 py-1 outline-none',
                  onSelect && 'cursor-pointer hover:bg-muted/40 focus-visible:ring-1 focus-visible:ring-primary/40',
                  onSelect && row.key === selectedKey && 'bg-primary/10',
                )}
              >
                <div className="flex min-w-0 items-center gap-1.5">
                  <span
                    className={cn(
                      "h-2 w-2 rounded-full shrink-0",
                      row.status === 'ok' && "bg-emerald-500",
                      // 0.6.333 F4：限流 = 琥珀（链路可达但被临时限流，区别于真实故障的红）
                      row.status === 'limited' && "bg-amber-500",
                      row.status === 'fail' && "bg-red-500",
                      !row.status && "bg-muted-foreground/30"
                    )}
                  />
                  {/* 0.6.527（用户令）：续传标记改用 ✓ 且紧跟源名——名字与 ✓ 放进
                      同一个 flex-1 容器，× 被推到最右，避免「✓ 挨着 ×」的歧义。 */}
                  <span className="flex min-w-0 flex-1 items-center gap-0.5">
                    <span className="min-w-0 truncate text-[12px] leading-tight">{row.label}</span>
                    {row.resume_ok === true && (
                      <span
                        className="flex shrink-0 items-center"
                        title="支持断点续传：镜像认 Range，断流后可从中断处继续下载"
                        aria-label="支持断点续传"
                      >
                        {/* 0.6.528（用户令）：续传标记改心电图折线（lucide Activity）——
                            ✓ 与删除 × 仍易混，折线与叉形状完全不同。 */}
                        <Activity className="h-3.5 w-3.5 text-emerald-500" strokeWidth={2.5} />
                      </span>
                    )}
                  </span>
                  {/* 0.6.521：自定义条目也可移除（本地列表，点保存生效）；默认源仍走本机屏蔽。 */}
                  {onDelete && row.key !== 'auto' && row.key !== 'direct' && (
                    <button
                      type="button"
                      onClick={(e) => handleDeleteClick(e, row.key)}
                      disabled={listBusy}
                      className={cn(
                        // 0.6.529（用户令）：× 由 16×16 文字缩放到 20×20 点击区、
                        // flex 水平+垂直居中（原 w-4 + text-center 只做了水平居中，
                        // 行高变化时偏上）；字号 12→16 更醒目。
                        'flex h-5 w-5 shrink-0 items-center justify-center rounded-md text-[16px] leading-none transition-colors disabled:opacity-40',
                        confirmDel === row.key
                          ? 'bg-red-500/10 font-bold text-red-500'
                          : 'text-muted-foreground/50 hover:bg-red-500/10 hover:text-red-500',
                      )}
                      title={confirmDel === row.key
                        ? '再点一次确认删除'
                        : row.key === 'custom' || row.key.startsWith('custom-')
                          ? '移除该自定义加速源（点页面底部「保存」生效）；需连点两次确认'
                          : '从默认清单删除该加速源（仅本机，可用「恢复」找回）；需连点两次确认'}
                      aria-label={confirmDel === row.key ? `确认删除 ${row.label}` : `删除 ${row.label}`}
                    >
                      ×
                    </button>
                  )}
                </div>
                {/* 0.6.523（用户令）：延迟 / 速率移到源名称下方；「当前 / 已选」徽标也放在这一行，
                    避免窄屏两列时把源名称挤断。失败与限流原因同样在这里显示。 */}
                <div className="mt-0.5 flex min-w-0 items-center gap-1 pl-3.5 text-[10px] leading-tight tabular-nums text-muted-foreground">
                  {(row.key === health?.active || (!!onSelect && row.key === selectedKey)) && (
                    <span className="shrink-0 font-medium text-primary">
                      {row.key === health?.active ? (referenceOnly ? '最快' : '当前') : '已选'}
                    </span>
                  )}
                  <span
                    className={cn(
                      'truncate',
                      row.status === 'fail' && 'text-red-400/80',
                      row.status === 'limited' && 'text-amber-500/80',
                    )}
                  >
                    {row.status === 'ok'
                      ? `${row.latency_ms}ms${showSpeed && row.speed_bps > 0 ? ` · ${row.speed_bps >= 1048576 ? `${(row.speed_bps / 1048576).toFixed(1)}MB/s` : `${Math.round(row.speed_bps / 1024)}KB/s`}` : ''}`
                      : row.status === 'limited'
                        ? `限流${row.error ? ` · ${row.error}` : ''}`
                        : row.status === 'fail'
                          ? `${row.consec_fails > 1 ? `失败×${row.consec_fails}` : '失败'}${row.error ? ` · ${row.error}` : ''}`
                          : '未测速'}
                  </span>
                </div>
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

// 0.6.311r：「最新更新日志」卡跟随最新版本——取「当前构建内嵌发布说明
//（about.changelog，build.sh 从 moo.json 生成随包发布）」与「源
// changelog_entries 条目」中版本号更大者：运行构建比源新（版本尚未
// 发布/源未同步）时显示本构建自己的日志；源比运行版本新时预览最新版
// 日志。两者都缺失/离线时整卡隐藏（0.6.308 演示数据移除）。

// 0.6.311r：版本号数值比较（0.6.311 > 0.6.310；-r 后缀逐段按数字比较）
const verCmp = (a: string, b: string): number => {
  const pa = a.split(/[.-]/).map((n) => parseInt(n, 10) || 0);
  const pb = b.split(/[.-]/).map((n) => parseInt(n, 10) || 0);
  const len = Math.max(pa.length, pb.length);
  for (let i = 0; i < len; i++) {
    const x = pa[i] || 0;
    const y = pb[i] || 0;
    if (x !== y) return x - y;
  }
  return 0;
};

// 0.6.384（用户令）：关于页文档清单条目——只显示标题与说明，
// 链接不落文本，统一给「打开」「复制」两个按钮。
const AboutDocRow: React.FC<{ item: AboutDocItem }> = ({ item }) => {
  const onCopy = async () => {
    const ok = await copyTextToClipboard(item.url);
    if (ok) toast.success('链接已复制');
    else toast.error('复制失败，请手动复制');
  };
  return (
    <div className="flex items-center gap-2 py-2">
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px]">{item.title}</div>
        {item.desc && <div className="mt-0.5 truncate text-[11px] text-muted-foreground">{item.desc}</div>}
      </div>
      <a
        href={item.url}
        target="_blank"
        rel="noreferrer"
        title="打开链接"
        className="shrink-0 rounded-full border border-white/10 bg-card/60 px-2.5 py-1 text-[11px] text-primary transition-colors hover:bg-card/90"
      >
        打开
      </a>
      <button
        type="button"
        onClick={onCopy}
        title="复制链接"
        className="shrink-0 rounded-full border border-white/10 bg-card/60 px-2.5 py-1 text-[11px] text-muted-foreground transition-colors hover:bg-card/90"
      >
        复制
      </button>
    </div>
  );
};

// 折叠卡：标题行即开关；无条目时整卡隐藏（远程清单不可达）。
const AboutDocsCard: React.FC<{ title: string; items?: AboutDocItem[]; icon: React.ReactNode }> = ({ title, items, icon }) => {
  const [open, setOpen] = React.useState(false);
  const list = items || [];
  if (!list.length) return null;
  return (
    <div className="bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore px-4 py-3">
      <button type="button" onClick={() => setOpen((v) => !v)} aria-expanded={open} className="flex w-full items-center gap-2 text-left">
        <span className="shrink-0 text-muted-foreground">{icon}</span>
        <span className="text-sm font-medium">{title}</span>
        <Badge variant="outline" className="text-xs tabular-nums">{list.length} 条</Badge>
        {/* 0.6.385（用户令）：不放「展开/收起」文字，只用箭头指示（rotate 表达状态） */}
        <ChevronDown className={cn('ml-auto h-4 w-4 shrink-0 text-muted-foreground transition-transform', open && 'rotate-180')} />
      </button>
      {open && (
        <div className="mt-1 divide-y divide-white/5">
          {list.map((it) => <AboutDocRow key={it.url} item={it} />)}
        </div>
      )}
    </div>
  );
};

const AboutTab: React.FC = () => {
  const [info, setInfo] = React.useState<AboutInfo | null>(null);
  const [mem, setMem] = React.useState<AboutMem | null>(null);
  const [entries, setEntries] = React.useState<{ version?: string; text: string }[]>([]);

  React.useEffect(() => {
    fetchAbout().then(setInfo).catch(() => setInfo(null));
    // 0.6.531（用户令）：版本信息里显示 Moo 内存实时占用——独立轻量端点 3s 轮询，
    // 不触发 about 的远程清单刷新；页面卸载即停表。
    let alive = true;
    const loadMem = () => {
      fetchAboutMem()
        .then((v) => { if (alive) setMem(v); })
        .catch(() => { /* 轮询失败保留上一次值 */ });
    };
    loadMem();
    const memTimer = window.setInterval(loadMem, 3000);
    (async () => {
      try {
        const list = await fetchApps();
        const self = list.apps.find((a) => a.appname === 'moo' && a.installed && a.key);
        if (!self) return;
        const detail = await fetchAppDetail(self.key);
        setEntries(detail.changelog_entries || []);
      } catch {
        /* 自源缺失或离线：卡片隐藏 */
      }
    })();
    return () => {
      alive = false;
      window.clearInterval(memTimer);
    };
  }, []);

  const logEntry = React.useMemo(() => {
    // 0.6.384：远程清单优先——内容由远端维护，不再与源条目比版本
    //（远程文件可同时包含多个版本；版本徽章取文件里第一个版本文）。
    if (info?.changelog_source === 'remote' && info.changelog) {
      return { version: info.changelog_version || info.version, text: info.changelog };
    }
    const cands: { version: string; text: string }[] = [];
    if (info?.version && info.changelog) cands.push({ version: info.version, text: info.changelog });
    for (const e of entries) if (e.version && e.text) cands.push({ version: e.version, text: e.text });
    if (!cands.length) return null;
    // 0.6.311r：跟随最新版本——版本号最大者（内嵌 vs 源条目）
    return cands.reduce((a, b) => (verCmp(b.version, a.version) > 0 ? b : a));
  }, [entries, info]);

  return (
    <div className="px-3 py-4 sm:px-6 sm:py-4 space-y-4">
      {/* 应用信息 */}
      <div className="bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore px-4 py-4 space-y-3.5">
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

      {/* 最近更新日志：与详情页 README 同款渲染（markdown + 滚动盒），
          全部日志一次渲染、盒内上下滑动（0.6.386 用户令：取消左右翻页）。
          0.6.537（用户令）：去掉「镶嵌式内框」——卡片自身 overflow-hidden，
          正文面板整卡铺满（与日志页同款），不再套一层圆角描边小框。 */}
      {logEntry && (
        <div className="overflow-hidden bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore">
          {/* 0.6.385（用户令）：标题后不再显示版本号（右上角应用信息已有）。 */}
          <div className="px-4 pt-4 pb-2 text-sm font-medium">最近更新日志</div>
          {/* 桌面 ~172px、移动端窄屏折行多放宽到 320px；超出的内容上下滚动查看 */}
          <ChangelogPager text={logEntry.text} appKey="moo" maxH="max-h-[320px] sm:max-h-[172px]" scroll plain />
        </div>
      )}

      {/* 0.6.385（用户令）：「文档和技能」「开发者文档」两张折叠卡回到更新日志下面
          （远程 docs.md / devdocs.md；无条目自动隐藏） */}
      <AboutDocsCard title="文档和技能" items={info?.docs} icon={<BookOpen className="h-4 w-4" />} />
      <AboutDocsCard title="开发者文档" items={info?.devdocs} icon={<Code2 className="h-4 w-4" />} />

      {/* 版本信息 */}
      <div className="bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore px-4 py-4 space-y-3">
        <div className="text-sm font-medium">版本信息</div>
        <div className="space-y-2.5">
          <AboutRow k="当前版本" v={info ? `v${info.version}` : '—'} />
          <AboutRow k="服务端口" v={info?.port || '—'} />
          <AboutRow k="运行架构" v={info ? `${info.platform} · ${info.arch}` : '—'} />
          {info?.go_version && <AboutRow k="运行时" v={info.go_version} />}
          {info && <AboutRow k="启动时间" v={new Date(info.started_at * 1000).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })} />}
          {info && <AboutRow k="运行时长" v={formatUptime(info.uptime_s)} />}
          {/* 0.6.531（用户令）：Moo 进程内存实时占用（3s 轮询 /api/about/mem）。
              主值 = RSS 常驻集；悬停给出 Go 堆与协程数。 */}
          {mem && (
            <AboutRow k="内存占用" v={
              <span
                className="inline-flex items-center gap-1.5 tabular-nums"
                title={`RSS 常驻 ${formatBytes(mem.rss_bytes) || '—'} · Go 堆 ${formatBytes(mem.heap_bytes) || '—'} · 协程 ${mem.goroutines}`}
              >
                <span className="h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-emerald-500" aria-hidden="true" />
                <span>{formatBytes(mem.rss_bytes) || '—'}</span>
                <span className="text-[11px] font-normal text-muted-foreground">RSS · 实时</span>
              </span>
            } />
          )}
        </div>
      </div>

      {/* 致谢（0.6.131 用户定稿；0.6.137 标题互换；0.6.138 整体优化用户定稿）：
          血缘收敛去功能宣传 /「反编译」柔化为「参考实现」/ fn-knock 参考范围补全
          （通知事件中心形式 + 设置备份模型）/ 补 GitHub 加速镜像维护者 /
          补人机协作开发一句（不带版本号）/ 末尾补许可声明 */}
      <div className="bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore px-4 py-4 space-y-3">
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
            <a href="https://github.com/SwrodKing" target="_blank" rel="noreferrer" className="text-primary hover:underline">@SwrodKing</a>
            {' '}（早期贡献者），多项体验改进与加速源能力源自他的提交。
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
  initialTab,
  onTabChange,
  aurora,
  onStoreUpdate,
  onCatalogChanged,
  onOpenSourceApps,
}) => {
  // 0.6.282：初始 tab 支持深链恢复（#settings=<tab>），无效值回落 system
  const [tab, setTab] = useState<SettingsTab>(() =>
    initialTab && TABS.some(t => t.key === initialTab) ? (initialTab as SettingsTab) : 'system'
  );

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
  // 0.6.532（用户令）：GitHub / Docker 加速源的「标题行（含开关）」也要整行可点开合。
  // 与「测速与监控」行共用同一折叠状态（沿用原 health-panel 持久化键，升级不丢偏好）。
  const [accelListCollapsed, setAccelListCollapsed] = useState<boolean>(() => {
    try {
      const v = localStorage.getItem('health-panel-collapsed:测速与监控');
      return v === null ? true : v === 'true';
    } catch { return true; }
  });
  const toggleAccelList = () => {
    setAccelListCollapsed((v) => {
      try { localStorage.setItem('health-panel-collapsed:测速与监控', String(!v)); } catch { /* ignore */ }
      return !v;
    });
  };
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
  // 0.6.321 F7：「智能选源包含直连」（默认关=零行为变化）
  const [mirrorIncludeDirect, setMirrorIncludeDirect] = useState(false);
  const [dockerMirror, setDockerMirror] = useState<string>('daocloud');
  const [dockerMirrorOptions, setDockerMirrorOptions] = useState<MirrorOption[]>([]);
  // 0.6.269 Docker 优选接入：系统 daemon.json 当前镜像源状态 + 应用流程
  const [dkStatus, setDkStatus] = useState<DockerMirrorStatus | null>(null);
  const [dkApplyOpen, setDkApplyOpen] = useState(false);
  const [dkApplying, setDkApplying] = useState(false);
  const loadDkStatus = useCallback(async () => {
    try {
      setDkStatus(await fetchDockerMirrorStatus());
    } catch {
      /* 状态获取失败不阻塞设置页（应用按钮仍可用，失败会另行报错） */
    }
  }, []);
  useEffect(() => {
    if (open) void loadDkStatus();
  }, [open, loadDkStatus]);

  // 0.6.521：自定义加速源支持多条。数组为完整列表，单值变量=第一条（旧逻辑兼容）。
  const [customGithubMirrors, setCustomGithubMirrors] = useState<string[]>([]);
  const [customDockerMirrors, setCustomDockerMirrors] = useState<string[]>([]);
  const customGithubMirror = customGithubMirrors[0] ?? '';
  const customDockerMirror = customDockerMirrors[0] ?? '';
  const setCustomGithubAt = (idx: number, value: string) =>
    setCustomGithubMirrors((prev) => {
      const next = [...prev];
      while (next.length <= idx) next.push('');
      next[idx] = value;
      return next;
    });
  const setCustomDockerAt = (idx: number, value: string) =>
    setCustomDockerMirrors((prev) => {
      const next = [...prev];
      while (next.length <= idx) next.push('');
      next[idx] = value;
      return next;
    });
  // 应用当前所选镜像到系统 Docker（0.6.269：写 daemon.json + 重启，可回滚）。
  // 镜像地址由后端按 key 解析（custom 除外，需带 custom_url）。
  const handleApplyDockerMirror = useCallback(async () => {
    setDkApplying(true);
    try {
      const dkIdx = dockerMirror === 'custom'
        ? 0
        : /^custom-(\d+)$/.test(dockerMirror)
          ? Number(dockerMirror.slice(7)) - 1
          : -1;
      const res = await applyDockerMirror(
        dockerMirror,
        dkIdx >= 0 ? (customDockerMirrors[dkIdx] ?? '').trim() : '',
      );
      setDkApplyOpen(false);
      // 0.6.325 安全加固 R2：mirror=auto 时后端已回传实际选中源，明确告知用户
      const picked = res.auto_label
        ? `${res.auto_label}${typeof res.auto_latency_ms === 'number' ? `（${res.auto_latency_ms}ms）` : ''}`
        : '';
      toast.success(picked ? `${res.message || '已应用'} · 已选 ${picked}` : (res.message || '已应用'));
      void loadDkStatus();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '应用失败');
    } finally {
      setDkApplying(false);
    }
  }, [dockerMirror, customDockerMirrors, loadDkStatus]);
  // 科学加速（0.6.206，加速源设置末卡片）：本机代理，仅 GitHub 域名改道
  const [proxyEnabled, setProxyEnabled] = useState<boolean>(false);
  const [proxyUrl, setProxyUrl] = useState<string>('');
  const [proxyTesting, setProxyTesting] = useState<boolean>(false);
  const [installVolume, setInstallVolume] = useState<number>(0);
  const [volumeOptions, setVolumeOptions] = useState<VolumeOption[]>([]);
  // 自动更新应用（周期检查发现更新时后台自动安装，无需打开应用）
  const [autoUpdate, setAutoUpdate] = useState<boolean>(false);
  // 0.6.326x：「自动纠正」已并入更新策略第四档「强制上游」，不再有独立 state。
  // 已装应用跨源更新策略（0.6.272）：严格/友好/宽松（替换 0.6.269 目录语言入口）
  const [updatePolicy, setUpdatePolicy] = useState<string>('strict');
  // 0.6.548：自动更新遇到依赖占用时的策略 / 允许自动停用的依赖方 / 自动停用时段
  // policy 切走再切回时保留名单与时段（不清空），方便用户来回对比
  const [depPolicy, setDepPolicy] = useState<'skip' | 'notify' | 'stop'>('skip');
  const [depAllow, setDepAllow] = useState<string[]>([]);
  const [depWindowStart, setDepWindowStart] = useState('00:00');
  // 0.6.549：时段改用齿轮选择（与加速源间隔同款），需要 时/分 拆分与回写
  const parseHM = (v: string): [number, number] => {
    const m = /^(\d{1,2}):(\d{2})$/.exec((v || '').trim());
    if (!m) return [0, 0];
    return [Math.min(23, Number(m[1])), Math.min(59, Number(m[2]))];
  };
  const fmtHM = (h: number, m: number) => `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`;
  const [depWindowEnd, setDepWindowEnd] = useState('23:59');
  // 0.6.551（用户令）：时段即时校验——规则实体在 src/lib/depWindow.ts（带单测），
  // 与后端 internal/api/autoupdate_deps.go validateAutoUpdateWindow 同口径
  // （HH:MM、开始≠结束、窗口 ≥ 1 小时）。以前只有点「保存」后由后端 400 回一句
  // toast，滚轮调错要试错一轮才知道；现在改完即红字并拦住保存（后端仍是最终权威）。
  const depWindowSpanMin = depWindowSpanMinutes(depWindowStart, depWindowEnd);
  const depWindowRawError = depWindowIssue(depWindowStart, depWindowEnd);
  // 选择器只在 stop 策略下可见：只有那时才红字 + 拦保存（否则用户看不见、也改不了）。
  const depWindowError = depPolicy === 'stop' ? depWindowRawError : '';
  const [depPickerOpen, setDepPickerOpen] = useState(false);
  // 0.6.533（用户令）：自动策略改为开关；关掉时回到上次的手动档（默认 strict）。
  // 0.6.534（用户令）：删除「手动选择策略」折叠胶囊——自动关掉即直接展示四档手动策略。
  const lastManualPolicyRef = useRef<string>('strict');
  // 0.6.533：自动策略开关——开=auto；关=回到上次手动档（不静默改服务端，仍需点保存）
  const handleAutoPolicyToggle = (v: boolean) => {
    if (v) {
      if (updatePolicy !== 'auto') lastManualPolicyRef.current = updatePolicy;
      setUpdatePolicy('auto');
      return;
    }
    setUpdatePolicy(lastManualPolicyRef.current || 'strict');
  };
  // FPK 下载目录（0.6.325 C4：已下载列表段抽入 FpkDownloads 组件，此处只留
  // 目录选择 + 轻量 dir 同步 + 上传；列表/安装/删除/向导/折叠态全在组件内）
  // 目录选择对话框：可下钻浏览（卷根 → 共享目录 → 任意子层），切换中状态
  const [dirApplying, setDirApplying] = useState(false);
  const [dirDialogOpen, setDirDialogOpen] = useState(false);
  const [browsePath, setBrowsePath] = useState<string>('/');
  const [browseParent, setBrowseParent] = useState<string>('');
  const [browseAllowed, setBrowseAllowed] = useState(false);
  const [browseEntries, setBrowseEntries] = useState<{ name: string; path: string }[]>([]);
  const [browseLoading, setBrowseLoading] = useState(false);
  const [fpkDir, setFpkDir] = useState<string>('');
  // 列表段（FpkDownloads）刷新信号：自增即静默重拉（上传完成/保存设置/切换目录）
  const [fpkRefreshSignal, setFpkRefreshSignal] = useState(0);
  const [storeInfo, setStoreInfo] = useState<StoreUpdateInfo | null>(null);
  // 0.6.387（用户令）：更新提醒弹窗的日志改读远程清单，只显示最新版（第一段）；
  // 远程不可达时回落探测得到的 release 正文（storeInfo.latest_changelog）。
  const [remoteChangelogLatest, setRemoteChangelogLatest] = useState('');
  useEffect(() => {
    fetchAbout()
      .then((i) => setRemoteChangelogLatest((i.changelog_latest || '').trim()))
      .catch(() => setRemoteChangelogLatest(''));
  }, []);
  // 0.6.299：探测失败（last_error）≠ 无更新——chip 显示琥珀告警态，点击强制重探
  const [probeRetrying, setProbeRetrying] = useState(false);
  const retryStoreProbe = async () => {
    setProbeRetrying(true);
    try {
      const store = await fetchStoreUpdate(true);
      setStoreInfo(store);
      if (store.has_update) {
        toast.success(`发现新版本 v${store.available_version}，点旁边红色标签更新`);
      } else if (store.last_error) {
        toast.error(`版本检查失败：${store.last_error}`);
      } else {
        toast.info(`已是最新版本 v${store.current_version}`);
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '版本检查失败，请稍后再试');
    } finally {
      setProbeRetrying(false);
    }
  };
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
  // 0.6.532（用户令）：删除类按钮统一「点一次变红确认，再点才真删」——
  // 备份删除 / 下载任务删除此前是单击即删，与 FPK 行内删除等交互不一致。
  const [backupConfirmDel, setBackupConfirmDel] = useState<string | null>(null);
  const backupConfirmTimer = useRef<number | null>(null);
  const [taskConfirmDel, setTaskConfirmDel] = useState<string | null>(null);
  const taskConfirmTimer = useRef<number | null>(null);
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
  // 0.6.319g（用户定稿）：底部保存 dock 改悬浮 overlay（与详情页下载胶囊同款）——
  // 旧「冻结底栏」在按钮上方留死区：滚动框裁在按钮上方，文字在那条隐形线
  // 「消失」，且死区挡滚轮/触摸。dock 高度实测（callback ref + ResizeObserver，
  // 键盘态/显隐变化自动跟随），内容滚动框据此设 paddingBottom：文字可滚到
  // 按钮下方穿过，滚到底静止时最后一行正好停在按钮上方不被遮。
  const [saveDockH, setSaveDockH] = useState(0);
  const saveDockRoRef = useRef<ResizeObserver | null>(null);
  const onSaveDockRef = useCallback((el: HTMLDivElement | null) => {
    saveDockRoRef.current?.disconnect();
    saveDockRoRef.current = null;
    if (!el) { setSaveDockH(0); return; }
    const measure = () => setSaveDockH(el.offsetHeight);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    saveDockRoRef.current = ro;
  }, []);
  // 0.6.316 ①：手机桌面模拟（飞牛 app 桌面模式）→ 对话框挂 .mds-sim 去玻璃
  const mdsSim = useMobileDesktopSim();
  // 0.6.225（用户 2026-10-01 定稿）：「保存按钮**不管键盘有没有弹出都一直在底部**，
  // 不要贴键盘上沿」。做法＝键盘弹出时**不改对话框高度**，保持键盘弹出前的整屏高度
  // （baseHeight）→ 对话框底边 = **物理屏幕底边**，按钮待在原地不动；键盘只是盖住它
  // （收起键盘即见可点）。这样布局零重排，按钮位置与键盘无关。
  // 若拿不到基线（异常）→ 不套用内联样式（退回自然布局，不劣化）。
  const narrowViewport = typeof window !== 'undefined' && window.innerWidth < 640;
  // 0.6.313 A1（飞牛 app WebView 毛玻璃修复，WEBVIEW_SETTINGS_BUG.md §6 候选 A）：
  // 条件加 kbOpen——**键盘未开时不写内联 px**，回落到类 inset-0（fixed inset-0
  // 恒等于当前布局视口，无基线）。0.6.226「常驻写入」已废止。
  // 0.6.313r（用户实测报「飞牛 app 设置页不居中、很长」）三档分流：
  // a) 真键盘实锤才写 px 基线——Android virtualKeyboard API 报非空
  //    boundingClientRect = 系统键盘真开；无 API/报空 = 无真键盘（含
  //    useKeyboardDock 误判 kbOpen 的壳时序场景），不写陈旧 kbBaseH。
  // b) 兜底：窄屏且无真键盘 → 显式钉 top/left/right + height=visualViewport
  //    高 → 即使壳报的布局视口比可视区大，对话框也恒等于可视区（根治
  //    「很长/偏移」类几何漂移，替代裸 inset-0 对异常壳的裸奔）。
  // c) 真键盘开（API 实锤）→ 保留 0.6.225 定稿行为（height=开键盘前基线
  //    kbBaseH，按钮物理底边原地、键盘盖住底边）。
  const vkbRealOpen = (() => {
    try {
      const vk = (navigator as unknown as { virtualKeyboard?: { boundingClientRect?: DOMRect } }).virtualKeyboard;
      const r = vk?.boundingClientRect;
      return !!r && r.height > 0;
    } catch {
      return false;
    }
  })();
  const dialogKeyboardStyle: React.CSSProperties | undefined =
    !narrowViewport
      ? undefined
      : kbOpen && kbBaseH > 240 && vkbRealOpen
        ? { top: 0, height: kbBaseH, bottom: 'auto' }
        : kbVvHeight > 240
          ? { top: 0, left: 0, right: 0, bottom: 'auto', height: kbVvHeight }
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

  // FPK 下载目录轻量同步（0.6.325 C4：列表本体在 FpkDownloads 组件内自拉，
  // 设置页只需 dir 给目录选择卡；上传/保存/切目录后另经 refreshSignal 重拉列表）
  const loadFpkDir = useCallback(async () => {
    try {
      const res = await fetchFpkDownloads();
      setFpkDir(res.dir || '');
    } catch { /* 目录不存在等场景：保持旧值 */ }
  }, []);
  // 0.6.531（用户令）：上传入口统一由 FpkDownloads 组件内置（enableUpload），
  // 设置页与首页下载弹窗共用同一套 input/handler，设置页不再单独维护。
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
      loadFpkDir();
      setFpkRefreshSignal((n) => n + 1); // 目录已迁移 → 列表段重拉
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
  // 0.6.532：备份删除两段式确认（第一下只变红，3s 内再点才删）
  const armDeleteBackup = (name: string) => {
    if (backupConfirmDel === name) {
      if (backupConfirmTimer.current !== null) {
        window.clearTimeout(backupConfirmTimer.current);
        backupConfirmTimer.current = null;
      }
      setBackupConfirmDel(null);
      void handleDeleteBackup(name);
      return;
    }
    setBackupConfirmDel(name);
    if (backupConfirmTimer.current !== null) window.clearTimeout(backupConfirmTimer.current);
    backupConfirmTimer.current = window.setTimeout(() => {
      backupConfirmTimer.current = null;
      setBackupConfirmDel(null);
    }, 3000);
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
  // 直接安装已下载的 FPK / 安装向导（0.6.325 C4：整段抽入 FpkDownloads 组件）

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
  // 0.6.532：下载任务删除两段式确认（第一下只变红，3s 内再点才删）
  const armDlClear = (t: BackgroundTask, key: string) => {
    if (taskConfirmDel === key) {
      if (taskConfirmTimer.current !== null) {
        window.clearTimeout(taskConfirmTimer.current);
        taskConfirmTimer.current = null;
      }
      setTaskConfirmDel(null);
      void handleDlClear(t);
      return;
    }
    setTaskConfirmDel(key);
    if (taskConfirmTimer.current !== null) window.clearTimeout(taskConfirmTimer.current);
    taskConfirmTimer.current = window.setTimeout(() => {
      taskConfirmTimer.current = null;
      setTaskConfirmDel(null);
    }, 3000);
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
      // 0.6.333 F4：限流单列（既不算正常也不算失败），为 0 的项省略
      const limited = (h.mirrors || []).filter((m) => m.status === 'limited').length;
      const fail = (h.mirrors || []).filter((m) => m.status === 'fail').length;
      return `${ok} 个正常${limited > 0 ? `，${limited} 个限流` : ''}${fail > 0 ? `，${fail} 个失败` : ''}`;
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
  // 0.6.512（用户令）：加速源清单目录化——同步 / 恢复 / 删除默认源。
  const [mirrorListBusy, setMirrorListBusy] = useState(false);
  const reloadMirrorListOptions = useCallback(async () => {
    try {
      const s2 = await fetchSettings();
      setMirrorOptions(s2.mirror_options || []);
      setDockerMirrorOptions(s2.docker_mirror_options || []);
    } catch { /* ignore */ }
    try { setMirrorHealth(await fetchMirrorHealth()); } catch { /* ignore */ }
    try { setDockerMirrorHealth(await fetchDockerMirrorHealth()); } catch { /* ignore */ }
  }, []);
  const handleMirrorListSync = useCallback(async () => {
    setMirrorListBusy(true);
    try {
      const r = await syncMirrorLists();
      if (!r.ok) throw new Error(r.error || '同步失败');
      toast.success(`加速源清单已同步：GitHub ${r.github ?? 0} 条 / Docker ${r.docker ?? 0} 条`);
      await reloadMirrorListOptions();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '同步加速源清单失败');
    } finally {
      setMirrorListBusy(false);
    }
  }, [reloadMirrorListOptions]);
  const handleMirrorListRestore = useCallback(async () => {
    setMirrorListBusy(true);
    try {
      const r = await restoreMirrorLists();
      if (!r.ok) throw new Error(r.error || '恢复失败');
      toast.success('已恢复默认加速源');
      await reloadMirrorListOptions();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '恢复默认加速源失败');
    } finally {
      setMirrorListBusy(false);
    }
  }, [reloadMirrorListOptions]);
  // 0.6.521（用户令）：GitHub / Docker / 科学加速 三个子 tab；「添加」弹窗
  // 改为与「添加渠道」同款 Dialog，输入框支持多行（每行一个地址，可一次加多个）。
  const [accelListTab, setAccelListTab] = useState<'github' | 'docker' | 'science'>('github');
  const [addMirrorOpen, setAddMirrorOpen] = useState(false);
  const [addMirrorText, setAddMirrorText] = useState('');
  const addMirrorLines = addMirrorText.split('\n').map((l) => l.trim()).filter(Boolean);
  const handleAddMirror = useCallback(() => {
    const lines = addMirrorText.split('\n').map((l) => l.trim()).filter(Boolean);
    if (lines.length === 0) { toast.error('请填写加速源地址'); return; }
    const normalized: string[] = [];
    for (const raw of lines) {
      if (!/^https?:\/\//i.test(raw) || /\s/.test(raw)) {
        toast.error(`地址格式不正确：${raw}`);
        return;
      }
      normalized.push(raw.endsWith('/') ? raw : `${raw}/`);
    }
    const isGh = accelListTab !== 'docker';
    const current = (isGh ? customGithubMirrors : customDockerMirrors)
      .map((u) => u.trim())
      .filter(Boolean)
      .map((u) => (u.endsWith('/') ? u : `${u}/`));
    const merged = [...current];
    const added: string[] = [];
    for (const u of normalized) {
      if (!merged.includes(u)) { merged.push(u); added.push(u); }
    }
    if (added.length === 0) { toast.info('这些地址已在自定义列表中'); return; }
    if (isGh) setCustomGithubMirrors(merged); else setCustomDockerMirrors(merged);
    const idx = merged.indexOf(added[0]);
    const key = idx === 0 ? 'custom' : `custom-${idx + 1}`;
    if (isGh) setMirror(key); else setDockerMirror(key);
    setAddMirrorOpen(false);
    setAddMirrorText('');
    toast.success(`已添加 ${added.length} 个加速源，点「保存」后生效`);
  }, [addMirrorText, accelListTab, customGithubMirrors, customDockerMirrors]);
  const handleMirrorDelete = useCallback(async (key: string) => {
    // 0.6.521：自定义条目 = 本地列表（点保存生效）；默认源仍走本机屏蔽 API。
    if (key === 'custom' || key.startsWith('custom-')) {
      const idx = key === 'custom' ? 0 : Number(key.slice(7)) - 1;
      const isGh = accelListTab !== 'docker';
      const list = isGh ? customGithubMirrors : customDockerMirrors;
      const next = list.filter((_, i) => i !== idx);
      if (isGh) setCustomGithubMirrors(next); else setCustomDockerMirrors(next);
      const selected = isGh ? mirror : dockerMirror;
      if (selected === key) {
        if (isGh) setMirror(next.length ? 'custom' : 'auto'); else setDockerMirror(next.length ? 'custom' : 'auto');
      }
      toast.success('已移除自定义加速源，点「保存」后生效');
      return;
    }
    setMirrorListBusy(true);
    try {
      await setMirrorHidden({ add: [key] });
      toast.success('已从默认清单删除（可用「恢复」找回）');
      await reloadMirrorListOptions();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '删除失败');
    } finally {
      setMirrorListBusy(false);
    }
  }, [reloadMirrorListOptions, accelListTab, customGithubMirrors, customDockerMirrors, mirror, dockerMirror]);
  // KSpeeder 独立应用状态：列表行探测 127.0.0.1:5443（ok=运行中）
  const kspeederRunning = (dockerMirrorHealth?.mirrors || []).find((m) => m.key === 'kspeeder')?.status === 'ok';
  // 0.6.316b：运行中/已停止可点击互切（点击「运行中」=停止，点击「已停止」=启动）。
  // ksInstalled null=首查未回（显示检测中）；daemon status 瞬态（starting/stopping）
  // 禁用按钮防连点撞操作队列 409。
  const [ksInstalled, setKsInstalled] = useState<boolean | null>(null);
  const [ksStatus, setKsStatus] = useState('');
  const [ksToggling, setKsToggling] = useState(false);
  const refreshKsApp = useCallback(async () => {
    try {
      const list = await fetchApps();
      const self = list.apps.find((a) => a.appname === 'kspeeder');
      setKsInstalled(Boolean(self?.installed));
      setKsStatus(self?.status || '');
    } catch { /* ignore */ }
  }, []);
  // 0.6.317 修复：启停后强制同步重探收敛（绕开 runManualSpeedTest 60s 快路径 +
  // 服务端 5min mirror 缓存），否则按钮（5443 探测态）最长 5 分钟停留旧值
  // （.2 实测：停止后探测已 fail，UI 却恒「运行中」）。
  // stop 等探测 ≠ ok（daemon 实亡，实测 <5s）；start 等 = ok（5443 就绪）；
  // 3s 间隔、20s 上限、每轮即时刷 UI；超时即止，后续 5s 轮询自然收敛。
  const settleKsHealth = useCallback(async (wantOk: boolean) => {
    const deadline = Date.now() + 20000;
    for (;;) {
      let h: MirrorHealth;
      try { h = await fetchDockerMirrorHealth(true); } catch { return; }
      setDockerMirrorHealth(h);
      const st = (h.mirrors || []).find((m) => m.key === 'kspeeder')?.status;
      if ((wantOk && st === 'ok') || (!wantOk && st !== 'ok')) return;
      if (Date.now() > deadline) return;
      await new Promise((r) => setTimeout(r, 3000));
    }
  }, []);
  const handleToggleKSpeeder = useCallback(async () => {
    if (ksToggling) return;
    const stopping = kspeederRunning;
    setKsToggling(true);
    try {
      await controlApp('kspeeder', stopping ? 'stop' : 'start');
      toast.success(stopping ? 'KSpeeder 已停止' : 'KSpeeder 已启动，本地镜像缓存生效中');
      await settleKsHealth(!stopping);
      refreshKsApp();
    } catch (e) {
      toast.error((stopping ? '停止' : '启动') + ' KSpeeder 失败：' + (e instanceof Error ? e.message : String(e)));
    } finally {
      setKsToggling(false);
    }
  }, [ksToggling, kspeederRunning, settleKsHealth, refreshKsApp]);
  const [ksInstalling, setKsInstalling] = useState(false);
  // 0.6.524：远端清单里的 KSpeeder 版本（仅展示，说明安装来源是清单而非应用源）
  const [ksListVersion, setKsListVersion] = useState('');
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
        refreshKsApp();
      }
    };
    // 0.6.524（用户令）：不再依赖应用源目录，按远端清单 lists/kspeeder.txt
    // 下载 + sha256 校验 + 安装（后端 /api/kspeeder/install）。
    installKspeeder(handler).promise
      .catch((e) => {
        toast.error('安装 KSpeeder 失败：' + (e instanceof Error ? e.message : String(e)));
      })
      .finally(() => setKsInstalling(false));
  }, [ksInstalling, refreshDkHealth, refreshKsApp]);

  useEffect(() => {
    if (!open || tab !== 'accel') return;
    let cancelled = false;
    refreshKsApp();
    fetchKspeederLatest()
      .then((v) => { if (!cancelled) setKsListVersion(v.version); })
      .catch(() => { /* 清单不可达时保持空，安装时后端会明确报错 */ });
    const load = async () => {
      try {
        const h = await fetchDockerMirrorHealth();
        if (!cancelled) setDockerMirrorHealth(h);
      } catch { /* ignore */ }
    };
    load();
    const t = window.setInterval(load, 5000);
    return () => { cancelled = true; window.clearInterval(t); };
  }, [open, tab, refreshKsApp]);

  // 记住最后一次非 direct 选择，切回 ON 时恢复
  const prevMirrorRef = useRef<string>('gh-proxy');
  const prevDockerMirrorRef = useRef<string>('daocloud');
  // 0.6.312：记住服务端当前的 update_policy——保存时若变化，更新判定（has_update
  // 的候选源）会变，须触发主列表重拉，否则「有更新」badge 停在旧策略算出的结果
  const loadedUpdatePolicyRef = useRef<string>('strict');

  const githubEnabled = mirror !== 'direct';
  const dockerEnabled = dockerMirror !== 'direct';

  /* ── 0.6.364（用户令）：「保存」按钮默认不出现 ────────────────────────
     原行为：除「关于」「日志」外，每个 tab 底部恒定挂一条全宽「保存」胶囊
     ——用户看着像一条常驻蓝条。现改为「只有改过当前 tab 的设置才浮出」：
     把 handleSave 真正提交的 21 个字段序列化成快照，与「已保存基线」比较；
     基线在①每次打开对话框、数据加载完成后 ②点过保存成功后 重新记录。
     不纳入：Dock/设置 tab 排序（拖拽落位即时落库）、FPK 目录切换（即时
     生效）、源管理（子组件自理）、通知设置（子组件即时保存）。
     归一化与 handleSave 提交值保持一致（天数钳制 1-30、代理地址 trim），
     否则「保存后按钮仍不消失」。 */
  const formSnapshot = JSON.stringify({
    interval,
    mirror,
    mirrorIncludeDirect,
    dockerMirror,
    customGithubMirror,
    customDockerMirror,
    customGithubMirrors,
    customDockerMirrors,
    installVolume,
    autoUpdate,
    updatePolicy,
    // 0.6.548：依赖占用策略三件套一起纳入脏态比较
    depPolicy,
    depAllow,
    depWindowStart,
    depWindowEnd,
    backupDir,
    backupAuto,
    backupIntervalDays: String(parseDays(backupIntervalDays, 7)),
    cacheCleanOn,
    cacheCleanDays: cacheCleanOn ? String(parseDays(cacheCleanDays, 7)) : '0',
    cacheCleanEveryDays: String(parseDays(cacheCleanEveryDays, 1)),
    ghProbeH,
    ghProbeM,
    dkProbeH,
    dkProbeM,
    proxyEnabled,
    proxyUrl: proxyUrl.trim(),
  });
  const formBaselineRef = useRef<string | null>(null);
  const [formDirty, setFormDirty] = useState(false);
  useEffect(() => {
    if (!open || loading) return;
    if (formBaselineRef.current === null) {
      formBaselineRef.current = formSnapshot;
      setFormDirty(false);
      return;
    }
    setFormDirty(formSnapshot !== formBaselineRef.current);
  }, [open, loading, formSnapshot]);

  // 每次打开重置状态并拉取数据
  useEffect(() => {
    if (!open) return;
    // 0.6.282：深链恢复时落回 URL 指定的 tab，其余情况仍默认「系统设置」
    setTab(initialTab && TABS.some(t => t.key === initialTab) ? (initialTab as SettingsTab) : 'system');
    // 0.6.364：重开对话框 → 丢弃上轮基线，加载完成后再记录（见 formSnapshot effect）
    formBaselineRef.current = null;
    setFormDirty(false);
    setLoading(true);
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
        // 0.6.325 C12：docker_mirror 按「后端下发的选项集合」判定——空值或
        // 未知 key（如已下线的 registry-cyou）回落 auto，避免下拉显示空白/
        // 生 key。后端 GET 已做同样归一，此处为前端侧双保险。
        const dmOpts = settings.docker_mirror_options || [];
        const dmRaw = settings.docker_mirror || '';
        const dm = dmOpts.some((o) => o.key === dmRaw) ? dmRaw : 'auto';
        setMirror(m);
        setMirrorOptions(settings.mirror_options || []);
        setMirrorIncludeDirect(!!settings.mirror_include_direct);
        setDockerMirror(dm);
        setDockerMirrorOptions(dmOpts);
        setCustomGithubMirrors(
          settings.custom_github_mirrors?.length
            ? settings.custom_github_mirrors
            : settings.custom_github_mirror
              ? [settings.custom_github_mirror]
              : [],
        );
        setCustomDockerMirrors(
          settings.custom_docker_mirrors?.length
            ? settings.custom_docker_mirrors
            : settings.custom_docker_mirror
              ? [settings.custom_docker_mirror]
              : [],
        );
        setProxyEnabled(!!settings.proxy_enabled);
        setProxyUrl(settings.proxy_url || '');
        setInstallVolume(settings.install_volume || 0);
        setVolumeOptions(settings.volume_options || []);
        setAutoUpdate(!!settings.auto_update);
        // 0.6.326x：auto_correct 已并入 update_policy（服务端读旧配置自动迁移）。
        setUpdatePolicy(settings.update_policy || 'strict');
        // 0.6.548：依赖占用策略 / 允许自动停用的应用 / 自动停用时段
        setDepPolicy(
          settings.auto_update_dep_policy === 'stop' || settings.auto_update_dep_policy === 'notify'
            ? settings.auto_update_dep_policy
            : 'skip',
        );
        setDepAllow(settings.auto_update_dep_allow || []);
        setDepWindowStart(settings.auto_update_window_start || '00:00');
        setDepWindowEnd(settings.auto_update_window_end || '23:59');
        // 0.6.534：手动四档的显隐直接由 updatePolicy 派生（非 auto 即显示），不再有独立折叠 state
        if ((settings.update_policy || 'strict') !== 'auto') lastManualPolicyRef.current = settings.update_policy || 'strict';
        loadedUpdatePolicyRef.current = settings.update_policy || 'strict';
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
        // 0.6.522：记住上次手动源（auto/direct 不算），关闭智能选源时回到它
        if (m !== 'direct' && m !== 'auto') lastManualGithubRef.current = m;
        if (dm !== 'direct' && dm !== 'auto') lastManualDockerRef.current = dm;
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
    loadFpkDir();
    loadBackups();
    return () => { cancelled = true; };
  }, [open, loadFpkDir, loadBackups]);

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

  /* ── 0.6.525（用户令）：智能选源回到列表首行，标题行不再有独立开关 ──────
     记忆「最后一次手动选的源」：关闭加速再打开时回到它，绝不回落 direct。
     手动选源 = 点下方「测速与健康」列表里的行（selectXxxMirror）；
     点首行「智能 · 自动选最快」= 交回智能优选。 */
  const lastManualGithubRef = useRef<string>('gh-proxy');
  const lastManualDockerRef = useRef<string>('daocloud');
  /** 列表点选：auto = 交回智能；direct = 关闭加速；其它 = 手动选用该源 */
  const selectGithubMirror = (key: string) => {
    if (key === 'auto') { setMirror('auto'); return; }
    if (key !== 'direct') {
      lastManualGithubRef.current = key;
      prevMirrorRef.current = key;
    }
    setMirror(key);
  };
  const selectDockerMirror = (key: string) => {
    if (key === 'auto') { setDockerMirror('auto'); return; }
    if (key !== 'direct') {
      lastManualDockerRef.current = key;
      prevDockerMirrorRef.current = key;
    }
    setDockerMirror(key);
  };

  const handleSave = async () => {
    // 科学加速：开启时地址必填（后端同样整单拒绝，前端先拦给提示）
    if (proxyEnabled && !proxyUrl.trim()) {
      toast.error('开启科学加速须先填写代理地址');
      return;
    }
    // 0.6.551：时段非法时前端先拦——后端同样整单 400，先拦可少一次往返，
    // 且提示直接指到「自动停用时段」那一行（不在保存失败后让人猜哪里错）。
    if (depWindowError) {
      toast.error(depWindowError);
      return;
    }
    setSaving(true);
    try {
      // 0.6.551：时段字段总是成对提交（后端无条件校验）。非 stop 策略时选择器不可见，
      // 若历史配置本身非法（旧版允许开始=结束），提交空串让后端回默认 00:00–23:59
      // ——后端本就把「开始=结束」当全天，语义等效，且不会把整张设置卡在 400 上。
      const winStart = depPolicy !== 'stop' && depWindowRawError ? '' : depWindowStart;
      const winEnd = depPolicy !== 'stop' && depWindowRawError ? '' : depWindowEnd;
      await updateSettings({
        check_interval_hours: interval,
        mirror,
        mirror_include_direct: mirrorIncludeDirect,
        docker_mirror: dockerMirror,
        // 后端指针语义：空串 = 显式清除（此前 `|| undefined` 会把「清空」
        // 变成「不提交」，用户清掉自定义镜像/地址后保存无效）。
        custom_github_mirror: customGithubMirror,
        custom_docker_mirror: customDockerMirror,
        custom_github_mirrors: customGithubMirrors,
        custom_docker_mirrors: customDockerMirrors,
        install_volume: installVolume,
        auto_update: autoUpdate,
        update_policy: updatePolicy,
        // 0.6.548：依赖占用策略 + 白名单 + 自动停用时段（时段成对提交）
        auto_update_dep_policy: depPolicy,
        auto_update_dep_allow: depAllow,
        auto_update_window_start: winStart,
        auto_update_window_end: winEnd,
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
      // 0.6.364：保存成功 → 当前表单成为新基线，底部「保存」随即收起
      formBaselineRef.current = formSnapshot;
      setFormDirty(false);
      loadFpkDir();
      setFpkRefreshSignal((n) => n + 1); // 列表段（FpkDownloads）重拉
      // 0.6.312：更新策略变化 → 目录里每张卡的 has_update/候选源按新策略重算，
      // 触发主列表重拉（App 侧 onCatalogChanged 带 2.5s 延迟，给后端重算留时间）
      if (updatePolicy !== loadedUpdatePolicyRef.current) {
        loadedUpdatePolicyRef.current = updatePolicy;
        onCatalogChanged?.();
      }
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

  /* 0.6.530（用户令，回退 0.6.529 的子 tab 内保存按钮）：
     加速源三个子 tab 不再各带常驻「保存」按钮 —— 统一跟随全局底部保存 dock，
     只有本页配置改动（formDirty）时才浮出，未改动时不占位。
     0.6.529 的 accelSaveButton（常驻卡片下方）已删除。 */

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

  // 0.6.521：把本地「待保存」的自定义地址也合并进选项表——
  // 添加后无需先保存就能在下拉/健康列表里看到并选中。
  const mergeCustomOptions = (opts: MirrorOption[], customs: string[]): MirrorOption[] => {
    const base = opts.filter((o) => o.key !== 'custom' && !o.key.startsWith('custom-'));
    const list = customs.map((u) => u.trim()).filter(Boolean);
    const customOpts: MirrorOption[] = list.map((u, i) => ({
      key: i === 0 ? 'custom' : `custom-${i + 1}`,
      label: i === 0 ? '自定义' : `自定义 ${i + 1}`,
      url: u,
      description: '使用自定义加速地址',
    }));
    const out = [...base];
    const at = out.findIndex((o) => o.key === 'direct');
    if (at >= 0) out.splice(at, 0, ...customOpts); else out.push(...customOpts);
    return out;
  };
  const githubOptionsAll = mergeCustomOptions(mirrorOptions, customGithubMirrors);
  const dockerOptionsAll = mergeCustomOptions(dockerMirrorOptions, customDockerMirrors);
  // 0.6.522：原「镜像下拉框」的选项集合（githubSelectOptions/dockerSelectOptions）
  // 已随下拉框一起删除——选源改由下方 MirrorHealthPanel 列表点选。

  const customIndexOf = (key: string) =>
    key === 'custom' ? 0 : /^custom-(\d+)$/.test(key) ? Number(key.slice(7)) - 1 : -1;

  const mirrorLabel = (key: string) =>
    githubOptionsAll.find((o) => o.key === key)?.label || (key === 'custom' ? '自定义' : key);
  const dockerMirrorLabel = (key: string) =>
    dockerOptionsAll.find((o) => o.key === key)?.label || (key === 'custom' ? '自定义' : key);
  const dockerCustomSelectedUrl =
    customIndexOf(dockerMirror) >= 0 ? (customDockerMirrors[customIndexOf(dockerMirror)] ?? '') : '';

  return (
    <>
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
        onInteractOutside={(e) => {
          // 0.6.500（用户实报「设置页点通知栏会跳回首页」）：顶部通知层
          // （sonner toast / 后台任务通知栏）在对话框 DOM 之外，默认会被当成
          // 外点关闭设置页。点在通知层上时不关闭，让点击只作用于通知本身。
          if (isNoticeLayerInteract(e)) e.preventDefault();
        }}
        style={dialogKeyboardStyle}
        className={cn("inset-0 w-full h-full max-w-none rounded-none sm:rounded-[18px] translate-x-0 translate-y-0 flex flex-col !p-0 gap-0 overflow-visible sm:overflow-hidden sm:inset-auto sm:left-[50%] sm:top-[50%] sm:h-[88vh] sm:max-w-3xl sm:translate-x-[-50%] sm:translate-y-[-50%] [&>button.absolute]:hidden sm:[&>button.absolute]:inline-flex sm:[&>button.absolute]:z-20", // 0.6.325-C15（用户报「网页端极光模式下设置页右上角 × 关不掉」）：
// 极光分支顶栏是 `relative z-10`（为压在 aurora 玻璃遮罩之上，桌面也生效），
// Radix 关闭钮 `button.absolute` 无 z-index（auto）→ 命中测试被顶栏右侧
// 版本 chip 行（ml-auto pr-1 sm:pr-12）抢走，点 × 等于点顶栏空白区、对话框
// 不关（标准主题桌面顶栏无 z-10，故只有极光模式中招；实测 elementFromPoint
// = 版本 chip 行 DIV）。桌面给 × 抬到 z-20（> 顶栏 10）= 恢复可点；移动端
// （sm 以下）本就不渲染 ×，零影响。
// 0.6.306（用户定稿）：标准主题玻璃化（与详情页同款 /70+blur-2xl），
// 极光路径 aurora-1 不变。
// 0.6.313 A2（飞牛 app WebView 毛玻璃修复，WEBVIEW_SETTINGS_BUG.md §6）：
// 移动端（<640）玻璃层移到下方内层 absolute 子层——backdrop-filter 不再
// 作用于含内容的整屏 fixed 层本体（WebView GPU 合成异常经典绕法，零行为
// 变化）；桌面（sm+）本体保留玻璃（sm: 前缀），桌面分支零改动。
aurora ? "aurora-1" : "sm:bg-background/70 sm:backdrop-blur-2xl",
// 0.6.316 ①：手机桌面模拟命中 → 去 backdrop-filter + 玻璃底提 /95（index.css .mds-sim）
mdsSim && "mds-sim")}>
        {/* 极光模式（0.6.297）：底板渐变 + 一层背景色玻璃遮罩把渐变压成
            温润底色染色；上方元素全部保持原样式。Radix 自带的右上关闭钮
            (button.absolute) 在遮罩之后渲染，不受影响。 */}
        {aurora && (
          // 0.6.315（iOS 真机修复）：移动端去掉 backdrop-filter（WKWebView GPU
          // 合成异常：玻璃层出得来、其上的内容不出→「只出毛玻璃」）；桌面
          // （sm+）保持原玻璃（sm:bg/70+sm:blur-2xl 与 0.6.306 定稿逐字一致）。
          <div aria-hidden className="pointer-events-none absolute inset-0 bg-background/75 sm:bg-background/70 sm:backdrop-blur-2xl rounded-none sm:rounded-[18px]" />
        )}
        {/* 0.6.313 A2：标准主题玻璃层移入内层 absolute 子层（仅移动端渲染，
            sm:hidden）——blur 不再挂在含内容的整屏 fixed 层本体上。层级：
            本层 z-0 → 内容子元素 max-sm:relative max-sm:z-10 压其上；
            Radix 关闭钮（button.absolute，dialog.tsx 在 children 之后渲染为
            DialogContent 直接子元素）移动端本就隐藏（[&>button.absolute]:hidden），
            桌面玻璃仍在本体、不受影响。 */}
        {!aurora && (
          // 0.6.315（iOS 真机修复）：移动端玻璃层去 backdrop-filter、底色提至
          // /95 近实心（WEBVIEW_SETTINGS_BUG.md §6 候选 B 止血版）——0.6.313 A2
          // 的「内层玻璃」绕法已被用户真机证伪不够，直接归零合成面。
          <div aria-hidden className="pointer-events-none absolute inset-0 z-0 bg-background/95 sm:hidden" />
        )}
        {/* 键盘几何调试浮层（**长按版本 chip** 才显示；真机排查用，默认不打扰） */}
        {kbDebug && (
          <div className="pointer-events-none fixed left-1 top-1 z-[9999] max-w-[97vw] rounded bg-black/85 px-1.5 py-1 font-mono text-[9px] leading-tight text-lime-300">
            {kbDebugText}
          </div>
        )}
        {/* 顶栏：← 返回 + 标题
            0.6.313 A2：标准主题移动端压到玻璃层（z-0）之上（max-sm 限定，
            桌面 sm+ 零改动）；极光分支保持原 aurora 抬升。 */}
        <div className={cn("flex items-center gap-1 border-b border-border/60 px-2 py-2 shrink-0", aurora ? "relative z-10" : "max-sm:relative max-sm:z-10")}>
          {/* 0.6.311：网页端隐藏返回箭头（与右上角 × 重复；移动端无 ×，保留箭头） */}
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8 sm:hidden"
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
            {/* 商店版本 chip（0.6.130 用户定稿；0.6.299 三态）：
                有更新=红底「有更新 vX」→ 点击弹窗确认后应用内自更新；
                探测失败=琥珀「⚠ vX」→ 点击立即强制重探（此前失败被吞、
                误报「已是最新版本」——GitHub 匿名 API 按出口 IP 限流 403 即中招）；
                探测成功且无更新=灰「vX」→ 点击顶部通知「已是最新版本 vX」 */}
            {storeInfo?.has_update && onStoreUpdate ? (
              <button
                onClick={() => setStoreUpdateConfirm(true)}
                className="rounded-full bg-destructive px-2 py-0.5 text-[11px] font-medium tabular-nums text-white transition-colors hover:bg-destructive/80"
                title={`有更新 v${storeInfo.available_version}，点击确认更新`}
              >
                有更新 v{storeInfo.available_version}
              </button>
            ) : storeInfo && !storeInfo.has_update && storeInfo.last_error ? (
              <button
                onClick={retryStoreProbe}
                disabled={probeRetrying}
                className="inline-flex items-center gap-0.5 rounded-full bg-amber-500/90 px-2 py-0.5 text-[11px] font-medium tabular-nums text-black transition-colors hover:bg-amber-400 disabled:opacity-60"
                title={`版本检查失败（${storeInfo.last_error}），点击重试；上次检查 ${storeInfo.last_check ? new Date(storeInfo.last_check * 1000).toLocaleString() : '未知'}`}
              >
                {probeRetrying ? <Loader2 className="h-3 w-3 animate-spin" /> : <AlertTriangle className="h-3 w-3" />}
                v{storeInfo.current_version}
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
                className="rounded-full bg-muted/60 border border-white/10 px-2 py-0.5 text-[11px] font-medium tabular-nums text-muted-foreground dark:text-foreground/80 transition-colors hover:bg-muted"
                title={`Moo 版本 v${storeInfo?.current_version || '…'}，点击查看更新（长按显示键盘调试信息）`}
              >
                v{storeInfo?.current_version || '…'}
              </button>
            )}
          </div>
        </div>

        {/* 0.6.313 A2：中列（tab 行 + 内容区 + 保存 dock）标准主题移动端
            同样抬到玻璃层之上（max-sm 限定，桌面零改动）。
            0.6.319g：relative 全视口——保存 dock 改 absolute 悬浮定位基准。 */}
        <div className={cn("relative flex-1 min-h-0 flex flex-col", aurora ? "relative z-10" : "max-sm:relative max-sm:z-10")}>
          {/* 顶部 tab：与首页分类胶囊、底部 Dock 完全同款语言（0.6.306 用户定稿）——
              选中=Dock 款 bg-primary/15 蓝字+primary/40 描边（原实心蓝 0.6.287 定稿，
              0.6.306 用户要求与 Dock 选中色对齐、不要太蓝）/ 未选=Dock 毛玻璃胶囊
              （bg-card/55 + blur + white/10 描边）。容器左右滑动（横向滚动不换行）。 */}
          <div className="shrink-0 px-4 sm:px-6 pt-3 overflow-x-auto [-ms-overflow-style:none] [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
            <div className="inline-flex items-center gap-2 pr-1" role="tablist">
              {orderedTabs.map(({ key, label, icon: Icon }) => (
                <button
                  key={key}
                  role="tab"
                  aria-selected={tab === key}
                  onClick={() => { setTab(key); onTabChange?.(key); }}
                  className={cn(
                    "h-8 rounded-full px-3.5 flex items-center gap-1.5 border border-white/10 sm:backdrop-blur-xl text-[13px] font-medium transition-colors focus:outline-none shrink-0 whitespace-nowrap",
                    tab === key
                      ? "bg-primary/15 text-primary border-primary/40"
                      : "bg-card/55 text-foreground hover:bg-card/80"
                  )}
                >
                  <Icon className="h-3.5 w-3.5 shrink-0" />
                  {label}
                </button>
              ))}
            </div>
          </div>

          {/* 内容区
              0.6.319g：滚动框贯穿到对话框最底（保存 dock 不再占 flex 段），
              paddingBottom=dock 实测高度——文字滚到底正好停在按钮上方。
              0.6.358（用户实报「设置页卡片和顶部 tab 胶囊间距太大」）：滚动框原 pt-3
              叠加各 tab 自己的 py 让桌面 32 / 移动 28，现由各 tab 的 py-4(py-4 桌面同款)
              独占 16px——tab 胶囊底到首卡顶统一 16px，与卡片间距齐平。 */}
          {/* 0.6.526（用户实报「加速源展开/折叠时卡片宽度会变」）：滚动条出现/
              消失会改变内容区宽度（实测 766→756，卡片 718→708，抖动 10px）。
              scrollbar-gutter:stable 常驻预留滚动条槽 → 宽度恒定；移动端
              overlay 滚动条不占位，此属性无副作用。 */}
          <div className="flex-1 min-h-0 min-w-0 overflow-y-auto overscroll-contain [scrollbar-gutter:stable]"
            style={saveDockH > 0 ? { paddingBottom: saveDockH } : undefined}>
            {tab === 'backup' ? (
              <div className="px-3 py-4 sm:px-6 sm:py-4 space-y-4">
                {/* 设置备份 */}
                <div className="bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore px-4 py-4 space-y-3">
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
                  {/* 备份周期（手动输入天数，1–30）
                      0.6.517（用户实报移动端「一团乱」）：右侧控件原 `w-full shrink-0`
                      在窄屏占满整行 → 左侧标签被压成每行 1 个字的竖排。改为标签
                      `flex-1 min-w-0`、控件 `shrink-0` 固定宽度，两端对齐。 */}
                  <div className="flex items-center justify-between gap-3 border-t border-border/30 pt-3">
                    <div className="min-w-0 flex-1">
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
                  <div className="flex items-center justify-between gap-3">
                    <div className="min-w-0">
                      <div className="text-[13px] font-medium">自动备份</div>
                      <p className="text-[11px] text-muted-foreground">同目录保留最近 20 份，更早自动淘汰</p>
                    </div>
                    <Switch checked={backupAuto} onCheckedChange={setBackupAuto} />
                  </div>
                  <div className="flex items-center justify-between gap-3">
                    <span className="min-w-0 truncate text-[11px] text-muted-foreground">
                      {lastBackupAt ? `最近备份 ${lastBackupAt.replace('T', ' ').slice(0, 16)}` : '尚未备份'}
                    </span>
                    <Button size="sm" variant="secondary" className="h-8 shrink-0 gap-1.5" onClick={handleBackupNow} disabled={backupRunning}>
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
                    <div className="space-y-2 border-t border-border/30 pt-3">
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
                                className={cn(
                                  'h-6 w-6 shrink-0',
                                  backupConfirmDel === b.name
                                    ? 'bg-red-500/15 text-red-500 hover:bg-red-500/20 hover:text-red-500'
                                    : 'hover:bg-destructive/10 hover:text-destructive',
                                )}
                                onClick={() => armDeleteBackup(b.name)}
                                disabled={backupRemoving === b.name}
                                title={backupConfirmDel === b.name ? '再点一次确认删除（3 秒内有效）' : '删除此备份（点一次先变红确认）'}
                                aria-label={backupConfirmDel === b.name ? `确认删除备份 ${b.name}` : `删除备份 ${b.name}`}
                              >
                                {backupRemoving === b.name ? <Loader2 className="h-3 w-3 animate-spin" /> : <Trash2 className="h-3 w-3" />}
                              </Button>
                            </div>
                          ))}
                        </div>
                      )}
                    </div>
                  )}
                </div>

                {/* Moo 应用缓存清理（只动 app 自身缓存，不碰已下载 FPK） */}
                <div className="bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore px-4 py-4 space-y-3">
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
                      <div className="min-w-0 flex-1">
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
                    <div className="min-w-0 flex-1">
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
                  {/* 0.6.529（用户令）：恢复「提示在左、两个按钮在右」同一行布局——
                      0.6.517 曾把手机端改成上下两行（按钮掉到下一行左对齐），
                      与桌面/其它操作行不一致；此处改为始终单行：提示可截断、
                      按钮 shrink-0，窄屏也不会溢出。 */}
                  <div className="flex items-center justify-between gap-2 border-t border-border/30 pt-2.5">
                    <span className="min-w-0 truncate text-[11px] text-muted-foreground">
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
              <div className="px-3 py-4 sm:px-6 sm:py-4">
                <SourceManager
                  onCatalogChanged={onCatalogChanged}
                  saveCounter={saveCounter}
                  onOpenSourceApps={(src) => {
                    // 0.6.535：先关设置再跳主页（关闭动画与列表筛选并行，无跳闪）
                    onOpenChange(false);
                    onOpenSourceApps?.(src.id, src.name);
                  }}
                />
              </div>
            ) : tab === 'accel' ? (
              loading ? (
                <div className="flex justify-center py-12">
                  <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
                </div>
              ) : (
                <div className="px-3 py-4 sm:px-6 sm:py-4 space-y-4">
                {/* 0.6.518/0.6.519（用户令）：GitHub / Docker 加速做成子 tab（联排分段样式，放进卡片内）；
                    0.6.525：智能选源回到加速源列表首行（标题行开关与文字移除）；去掉闪电测速；
                    同步/恢复/添加 三等分一行；科学加速并入 GitHub 子 tab。 */}
                <div className="bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore px-4 py-4 space-y-4">
                  <div className="inline-flex rounded-full border border-border/40 bg-muted/30 p-0.5" role="tablist" aria-label="加速类型">
                    <button
                      type="button"
                      role="tab"
                      aria-selected={accelListTab === 'github'}
                      onClick={() => setAccelListTab('github')}
                      className={cn(
                        'h-7 rounded-full px-2.5 text-[11px] transition-colors',
                        accelListTab === 'github'
                          ? 'bg-background text-foreground shadow-sm'
                          : 'text-muted-foreground hover:text-foreground',
                      )}
                    >
                      GitHub 加速
                    </button>
                    <button
                      type="button"
                      role="tab"
                      aria-selected={accelListTab === 'docker'}
                      onClick={() => setAccelListTab('docker')}
                      className={cn(
                        'h-7 rounded-full px-2.5 text-[11px] transition-colors',
                        accelListTab === 'docker'
                          ? 'bg-background text-foreground shadow-sm'
                          : 'text-muted-foreground hover:text-foreground',
                      )}
                    >
                      Docker 加速
                    </button>
                    <button
                      type="button"
                      role="tab"
                      aria-selected={accelListTab === 'science'}
                      onClick={() => setAccelListTab('science')}
                      className={cn(
                        'h-7 rounded-full px-2.5 text-[11px] transition-colors',
                        accelListTab === 'science'
                          ? 'bg-background text-foreground shadow-sm'
                          : 'text-muted-foreground hover:text-foreground',
                      )}
                    >
                      科学加速
                    </button>
                  </div>
                  {accelListTab === 'github' ? (
                    <div className="space-y-3">
                      {/* 0.6.523（用户令）：页面结构自上而下 = 加速开关 → 加速源列表
                          （同步 / 恢复 / 添加 在列表正上方；首行=智能 · 自动选最快）。
                          说明文字精简；「智能 · 自动选最快」列表行由开关取代。 */}
                      <div
                        className="flex cursor-pointer items-center justify-between gap-3"
                        onClick={toggleAccelList}
                        title={accelListCollapsed ? '点击展开 GitHub 加速源列表' : '点击折叠 GitHub 加速源列表'}
                      >
                        <span className="text-sm font-medium leading-none whitespace-nowrap">GitHub 下载加速</span>
                        {/* 0.6.534（用户令）：标题行右侧的折叠按钮与「测速与监控」行重复，删除；
                            整行点击仍可展开/折叠，折叠入口统一保留下方那一个 */}
                        <div className="flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
                          <Switch checked={githubEnabled} onCheckedChange={handleGithubToggle} aria-label="GitHub 加速开关" />
                        </div>
                      </div>
                      {githubEnabled && customIndexOf(mirror) >= 0 && (
                        <Input
                          placeholder="https://your-proxy.example.com/"
                          value={customGithubMirrors[customIndexOf(mirror)] ?? ''}
                          onChange={(e) => setCustomGithubAt(customIndexOf(mirror), e.target.value)}
                        />
                      )}
                      <MirrorHealthPanel
                        title="测速与监控"
                        collapsed={accelListCollapsed}
                        onToggleCollapsed={toggleAccelList}
                        health={mirrorHealth}
                        options={githubOptionsAll}
                        customConfigured={customGithubMirrors.length > 0}
                        labelOf={mirrorLabel}
                        intervalH={ghProbeH}
                        intervalM={ghProbeM}
                        onIntervalChange={(h, m) => { setGhProbeH(h); setGhProbeM(m); }}
                        refreshing={healthRefreshing}
                        onRefresh={refreshHealth}
                        showSpeed
                        includeDirect={{ value: mirrorIncludeDirect, onChange: setMirrorIncludeDirect }}
                        onSync={handleMirrorListSync}
                        onRestore={handleMirrorListRestore}
                        onAdd={() => { setAddMirrorText(''); setAddMirrorOpen(true); }}
                        onDelete={handleMirrorDelete}
                        listBusy={mirrorListBusy}
                        plain
                        selectedKey={mirror}
                        onSelect={selectGithubMirror}
                      />
                    </div>
                  ) : accelListTab === 'docker' ? (
                    <div className="space-y-3">
                      {/* 0.6.525：与 GitHub 组同构——开关 → 加速源列表（首行=智能 · 自动选最快）。 */}
                      <div
                        className="flex cursor-pointer items-center justify-between gap-3"
                        onClick={toggleAccelList}
                        title={accelListCollapsed ? '点击展开 Docker 加速源列表' : '点击折叠 Docker 加速源列表'}
                      >
                        <span className="text-sm font-medium leading-none whitespace-nowrap">Docker 镜像加速</span>
                        {/* 0.6.534（用户令）：同上，删除与「测速与监控」行重复的折叠按钮 */}
                        <div className="flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
                          <Switch checked={dockerEnabled} onCheckedChange={handleDockerToggle} aria-label="Docker 加速开关" />
                        </div>
                      </div>
                      {dockerEnabled && customIndexOf(dockerMirror) >= 0 && (
                        <Input
                          placeholder="https://your-mirror.example.com/"
                          value={customDockerMirrors[customIndexOf(dockerMirror)] ?? ''}
                          onChange={(e) => setCustomDockerAt(customIndexOf(dockerMirror), e.target.value)}
                        />
                      )}
                      <MirrorHealthPanel
                        title="测速与监控"
                        collapsed={accelListCollapsed}
                        onToggleCollapsed={toggleAccelList}
                        health={dockerMirrorHealth}
                        options={dockerOptionsAll}
                        customConfigured={customDockerMirrors.length > 0}
                        intervalH={dkProbeH}
                        intervalM={dkProbeM}
                        onIntervalChange={(h, m) => { setDkProbeH(h); setDkProbeM(m); }}
                        labelOf={dockerMirrorLabel}
                        refreshing={dkHealthRefreshing}
                        onRefresh={refreshDkHealth}
                        showSpeed
                        referenceOnly
                        speedNote="仅供参考：实际拉取走系统镜像源，照此配置。"
                        onSync={handleMirrorListSync}
                        onRestore={handleMirrorListRestore}
                        onAdd={() => { setAddMirrorText(''); setAddMirrorOpen(true); }}
                        onDelete={handleMirrorDelete}
                        listBusy={mirrorListBusy}
                        plain
                        selectedKey={dockerMirror}
                        onSelect={selectDockerMirror}
                      />
                      {/* 0.6.269 Docker 优选接入：把所选镜像写入系统 daemon.json（可回滚） */}
                      <div className="flex items-center justify-between gap-3 rounded-xl bg-muted/30 border border-border/20 px-3 py-2.5">
                        <div className="min-w-0">
                          {/* 0.6.287：标题强制一行（CJK 默认可在任意字间断行，窄屏被拆成
                              两行 = 用户实报「本来一行显示被改成两行」）。「已应用」短徽章
                              随行显示；两条长徽章移动端隐藏（下方说明行已列出当前
                              registry-mirrors，信息不丢），桌面端保持完整 */}
                          <div className="text-[13px] font-medium flex items-center gap-2 flex-wrap">
                            <span className="whitespace-nowrap">应用到系统 Docker</span>
                            {dkStatus ? (
                              dkStatus.applied ? (
                                <Badge className="gap-1.5 text-[11px] whitespace-nowrap">
                                  <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
                                  已应用
                                </Badge>
                              ) : dkStatus.mirrors?.length ? (
                                <Badge variant="secondary" className="text-[11px] whitespace-nowrap hidden sm:inline-flex">
                                  系统已有其它镜像源
                                </Badge>
                              ) : (
                                <Badge variant="secondary" className="text-[11px] whitespace-nowrap hidden sm:inline-flex">
                                  直连（无镜像源）
                                </Badge>
                              )
                            ) : null}
                          </div>
                          <p className="text-[11px] text-muted-foreground leading-relaxed break-all">
                            {dkStatus?.mirrors?.length
                              ? `当前 registry-mirrors：${dkStatus.mirrors.join('，')}`
                              : '写入 daemon.json 并重启 Docker（失败自动回滚）'}
                          </p>
                        </div>
                        <Button
                          size="sm"
                          variant={dkStatus?.applied ? 'outline' : 'secondary'}
                          className="shrink-0 h-8"
                          onClick={() => setDkApplyOpen(true)}
                          disabled={dockerMirror === 'direct' && !dkStatus?.mirrors?.length}
                        >
                          {dkStatus?.applied ? '重新应用' : '应用'}
                        </Button>
                      </div>
                      <AlertDialog open={dkApplyOpen} onOpenChange={setDkApplyOpen}>
                        <AlertDialogContent className="rounded-[18px] border-border/20 shadow-appstore bg-card">
                          <AlertDialogHeader>
                            <AlertDialogTitle>应用 Docker 镜像源到系统</AlertDialogTitle>
                            <AlertDialogDescription className="space-y-1.5">
                              {dockerMirror === 'direct' ? (
                                <p>将<b>清除</b> /etc/docker/daemon.json 的 registry-mirrors（回到 Docker Hub 直连）。</p>
                              ) : (
                                <p>
                                  将把 <b>{dockerCustomSelectedUrl || (customIndexOf(dockerMirror) >= 0 ? '自定义地址' : dockerMirrorLabel(dockerMirror))}</b>{dockerCustomSelectedUrl ? `（${dockerCustomSelectedUrl}）` : ''} 写入 /etc/docker/daemon.json 的 registry-mirrors。
                                </p>
                              )}
                              {/* 0.6.325 安全加固 R2：智能选源=由系统替你选第三方源，须知情 */}
                              {dockerMirror === 'auto' ? (
                                <p className="text-muted-foreground">
                                  智能选源会在<b>应用时</b>对候选源逐个测速，并把<b>最快且可达</b>的一个写入配置；候选含第三方公共加速源，<b>镜像拉取请求会经该源</b>。实际选中结果会在应用后提示。
                                </p>
                              ) : null}
                              <p>
                                需要<b>重启 Docker</b>：本机所有容器会停止数秒后自动恢复（Docker 类应用短暂不可用）。写前自动备份，任一步失败自动回滚。
                              </p>
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel disabled={dkApplying}>取消</AlertDialogCancel>
                            <Button onClick={handleApplyDockerMirror} disabled={dkApplying}>
                              {dkApplying ? (
                                <Loader2 className="h-4 w-4 animate-spin mr-1.5" />
                              ) : null}
                              {dkApplying ? '应用中…' : '确认应用'}
                            </Button>
                          </AlertDialogFooter>
                        </AlertDialogContent>
                      </AlertDialog>
                      {/* KSpeeder 镜像加速：独立应用依赖（同 New Store 依赖外部 iStoreEnhance 的做法），
                          未运行时提供安装入口，走标准应用安装管线（Blue-Mink 源 appname=kspeeder） */}
                      <div className="flex items-center justify-between gap-3 rounded-xl bg-muted/30 border border-border/20 px-3 py-2.5">
                        <div className="min-w-0">
                          <div className="text-[13px] font-medium">KSpeeder 镜像加速</div>
                          <p className="text-[11px] text-muted-foreground leading-relaxed">
                            本地缓存 127.0.0.1:5443，需系统镜像源指向它{ksListVersion ? ` · 清单 v${ksListVersion}` : ''}
                          </p>
                        </div>
                        {ksToggling || ksStatus === 'starting' || ksStatus === 'stopping' ? (
                          <Button size="sm" variant="secondary" className="shrink-0 h-8" disabled>
                            <Loader2 className="h-3.5 w-3.5 animate-spin" />
                            处理中…
                          </Button>
                        ) : kspeederRunning ? (
                          <Button
                            size="sm"
                            variant="ghost"
                            className="shrink-0 h-8 gap-1.5 text-[11px] text-emerald-700 dark:text-emerald-300 bg-emerald-500/10 border border-emerald-500/30 hover:bg-emerald-500/20"
                            onClick={handleToggleKSpeeder}
                            title="点击停止 KSpeeder"
                          >
                            <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
                            运行中
                          </Button>
                        ) : ksInstalled ? (
                          <Button
                            size="sm"
                            variant="secondary"
                            className="shrink-0 h-8"
                            onClick={handleToggleKSpeeder}
                            title="点击启动 KSpeeder"
                          >
                            <Power className="h-3.5 w-3.5" />
                            已停止
                          </Button>
                        ) : ksInstalled === null ? (
                          <Button size="sm" variant="secondary" className="shrink-0 h-8" disabled>
                            <Loader2 className="h-3.5 w-3.5 animate-spin" />
                            检测中…
                          </Button>
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
                    </div>
                    ) : (
                      <div className="space-y-3">
                        <div className="flex items-start justify-between gap-2">
                          <div className="min-w-0">
                            <div className="text-sm font-medium">科学加速</div>
                            <p className="mt-0.5 text-[11px] text-muted-foreground leading-relaxed">
                              GitHub 流量走本机代理，其余直连；代理失效自动回退加速源。
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
                              支持 HTTP / SOCKS4 / SOCKS5（含 socks5h）；地址仅本机使用。
                            </p>
                          </div>
                        )}
                      </div>
                    )}
                </div>
                {/* 0.6.521（用户令）：添加加速源弹窗与「添加渠道」同款 Dialog；
                    输入框支持多行（每行一个地址，Ctrl/Cmd+Enter 提交）。 */}
                <Dialog open={addMirrorOpen} onOpenChange={setAddMirrorOpen}>
                  <DialogContent className="sm:w-[720px]">
                    <DialogHeader>
                      <DialogTitle>添加 {accelListTab === 'github' ? 'GitHub' : 'Docker'} 加速源</DialogTitle>
                      <DialogDescription>每行一个地址，可一次添加多个。</DialogDescription>
                    </DialogHeader>
                    <div className="space-y-3 px-1.5 py-1">
                      <div>
                        <div className="flex items-center gap-2">
                          <label className="text-xs text-muted-foreground">加速源地址</label>
                          {addMirrorLines.length > 1 && (
                            <Badge variant="secondary" className="h-4 px-1 text-[10px]">
                              {addMirrorLines.length} 行
                            </Badge>
                          )}
                        </div>
                        <textarea
                          value={addMirrorText}
                          onChange={(e) => setAddMirrorText(e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                              e.preventDefault();
                              handleAddMirror();
                            }
                          }}
                          placeholder={
                            accelListTab === 'github'
                              ? 'https://gh-proxy.example.com/\nhttps://gh2.example.com/'
                              : 'https://docker.example.com/\nhttps://docker2.example.com/'
                          }
                          rows={3}
                          spellCheck={false}
                          className="mt-1 w-full resize-y rounded-md border border-input bg-background px-3 py-2 font-mono text-[13px] leading-relaxed placeholder:text-muted-foreground/60 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                          aria-label="加速源地址"
                        />
                      </div>
                    </div>
                    <DialogFooter className="gap-1.5">
                      <Button variant="ghost" size="sm" onClick={() => setAddMirrorOpen(false)}>取消</Button>
                      <Button size="sm" onClick={handleAddMirror} disabled={addMirrorLines.length === 0}>
                        添加{addMirrorLines.length > 1 ? ` ${addMirrorLines.length} 个` : ''}
                      </Button>
                    </DialogFooter>
                  </DialogContent>
                </Dialog>
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
              <div className="px-3 py-4 sm:px-6 sm:py-4 space-y-4">
                {/* Dock 栏排序（0.6.122）：移动端底部主导航，长按进入拖动；
                    头部「恢复默认 + 折叠」与「应用源列表」同款（默认折叠，持久化） */}
                <div className="bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore px-4 py-4">
                  <div
                    className="flex items-center justify-between gap-2"
                    onClick={() => toggleOrderCollapsed('dock')}
                    title={dockOrderCollapsed ? '点击展开 Dock 栏排序' : '点击折叠 Dock 栏排序'}
                  >
                    <div className="cursor-pointer text-sm font-medium leading-none">Dock 栏排序</div>
                    <div className="flex shrink-0 items-center gap-1" onClick={(e) => e.stopPropagation()}>
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
                <div className="bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore px-4 py-4">
                  <div
                    className="flex items-center justify-between gap-2"
                    onClick={() => toggleOrderCollapsed('tab')}
                    title={tabOrderCollapsed ? '点击展开设置 tab 排序' : '点击折叠设置 tab 排序'}
                  >
                    <div className="cursor-pointer text-sm font-medium leading-none">设置 tab 排序</div>
                    <div className="flex shrink-0 items-center gap-1" onClick={(e) => e.stopPropagation()}>
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
                <div className="bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore px-4 py-4 space-y-4">
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
                                  {/* 0.6.325 C1：卷友好名优先级（有 name=「name · /volN」→ 无=「存储空间 N · /volN」）；
                                      长名截断（max-w+truncate+title 悬停全名） */}
                                  <span className="max-w-[180px] truncate" title={volumeOptionLabel(vol)}>
                                    {volumeOptionLabel(vol)}
                                  </span>
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

                {/* FPK 下载目录 + 已下载列表（独立卡片，不与常规设置混在一起） */}
                <div className="bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore px-4 py-4">
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
                    {/* 0.6.530（用户令）：目录按钮里已显示完整路径 → 删掉下方
                        「当前生效：<目录>」重复行（同一信息出现两遍）。 */}
                    {/* 0.6.325 C4：已下载列表段抽入 FpkDownloads 组件（state/handler/渲染全在
                        组件内）；0.6.531 起上传按钮也由组件内置（enableUpload），
                        设置页只保留进行中的下载任务块（tasksBlock）。 */}
                    <FpkDownloads
                      enableUpload
                      // 后台下载（进行中/已暂停）：下载可暂停/继续（按钮与安装同款，下载中显示「暂停」）
                      tasksBlock={dlTasks.length > 0 && (
                      <div className="rounded-lg border border-blue-500/30 bg-blue-500/5 divide-y divide-border/40">
                        {dlTasks.map((t) => {
                          const paused = t.status === 'paused';
                          const failed = t.status === 'error' || t.status === 'failed';
                          // 后台下载管理器任务带 downloaded/total 字段；官方 cloud 下载走
                          // 长操作视图（无此二字段）——暂停/继续仅对前者生效（0.6.147）。
                          // 删除对两者均生效（0.6.261）：cloud 失败行走长操作队列回退删除，
                          // 后端 removeTask 下载管理器未命中时回退 Ops.Remove。
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
                                {/* 0.6.147：全部任务可删除（运行中先停止；断点文件随任务清除，成品 FPK 保留）；
                                    0.6.261：cloud 长操作失败行（非下载管理器、无 downloaded/total）也可删除——
                                    此前 isMgr 门槛把 red 失败行的删除按钮整个吞掉，且后端点删恒 404 */}
                                {(isMgr || failed) && (
                                  <Button
                                    variant="ghost"
                                    size="icon"
                                    className={cn(
                                      'h-6 w-6 shrink-0',
                                      taskConfirmDel === (t.id || `${t.appname}:${t.op}`)
                                        ? 'bg-red-500/15 text-red-500 hover:bg-red-500/20 hover:text-red-500'
                                        : 'text-muted-foreground hover:text-red-500 hover:bg-red-500/10',
                                    )}
                                    onClick={() => armDlClear(t, t.id || `${t.appname}:${t.op}`)}
                                    disabled={dlBusyApp !== null}
                                    title={taskConfirmDel === (t.id || `${t.appname}:${t.op}`)
                                      ? '再点一次确认删除（3 秒内有效）'
                                      : '删除该下载任务（运行中会先停止；断点文件一并清除，成品 FPK 保留）'}
                                    aria-label={taskConfirmDel === (t.id || `${t.appname}:${t.op}`)
                                      ? `确认删除 ${t.appname} 下载任务`
                                      : `删除 ${t.appname} 下载任务`}
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
                      refreshSignal={fpkRefreshSignal}
                      onCatalogChanged={onCatalogChanged}
                    />
                  </div>
                </div>


                {/* 自动更新应用 + 更新策略（0.6.538b 用户令）：合并为一张卡（同属「更新」语义；
                    0.6.538 曾拆两卡）。两者仍是独立字段（auto_update / update_policy），
                    策略不 gate 自动更新：关掉自动更新后，策略照常决定「有更新」判定与手动更新目标。 */}
                <div className="bg-card/95 sm:bg-card/55 sm:backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore px-4 py-3.5">
                  <div className="space-y-3">
                    <div className="flex items-start justify-between gap-3">
                      <div className="space-y-0.5 min-w-0">
                        <label className="text-sm font-medium leading-none flex items-center gap-1.5">
                          <Zap className="h-3.5 w-3.5 text-muted-foreground" />
                          自动更新应用
                        </label>
                        <p className="text-xs text-muted-foreground">
                          周期检查时自动在后台安装更新
                        </p>
                      </div>
                      <Switch checked={autoUpdate} onCheckedChange={setAutoUpdate} className="shrink-0 mt-0.5" />
                    </div>

                    <div className="border-t border-white/10 pt-3 space-y-2">
                      <div className="flex items-start justify-between gap-3">
                        <div className="space-y-0.5 min-w-0">
                          <label className="text-sm font-medium leading-none flex items-center gap-1.5">
                            <SlidersHorizontal className="h-3.5 w-3.5 text-muted-foreground" />
                            更新策略（推荐）
                          </label>
                          <p className="text-xs leading-relaxed text-muted-foreground">
                            决定哪些来源的版本算更新，手动更新同样按它执行
                          </p>
                        </div>
                        <Switch
                          checked={updatePolicy === 'auto'}
                          onCheckedChange={handleAutoPolicyToggle}
                          className="shrink-0 mt-0.5"
                          aria-label="更新策略"
                        />
                      </div>
                      {!autoUpdate && (
                        <p className="text-xs leading-relaxed text-muted-foreground/80">
                          自动更新已关闭，策略仍决定「有更新」判定与手动更新的目标版本。
                        </p>
                      )}
                      {updatePolicy !== 'auto' && (
                        <div className="space-y-2">
                          <div className="grid grid-cols-2 sm:grid-cols-4 gap-1.5">
                            {UPDATE_POLICY_OPTIONS.map((o) => (
                              <button
                                key={o.value}
                                type="button"
                                onClick={() => {
                                  lastManualPolicyRef.current = o.value;
                                  setUpdatePolicy(o.value);
                                }}
                                aria-pressed={updatePolicy === o.value}
                                className={cn(
                                  'h-8 rounded-full text-xs font-medium transition-colors',
                                  // 0.6.307（用户点名）：选中=胶囊同款浅蓝，未选=灰底保对比
                                  updatePolicy === o.value
                                    ? 'bg-primary/15 text-primary border border-primary/40'
                                    : 'bg-muted/60 text-muted-foreground hover:text-foreground border border-transparent',
                                )}
                              >
                                {o.label}
                              </button>
                            ))}
                          </div>
                          <p className="text-xs leading-relaxed text-muted-foreground">
                            {`当前为手动策略「${UPDATE_POLICY_OPTIONS.find((o) => o.value === updatePolicy)?.label || '严格'}」：${UPDATE_POLICY_DESCS[updatePolicy] || UPDATE_POLICY_DESCS.strict}`}
                          </p>
                        </div>
                      )}
                    </div>

                    {/* 0.6.548：依赖占用策略 —— 仅自动更新开启时显示（关闭即整组隐藏，不显示禁用态） */}
                    {autoUpdate && (
                      <>
                        <div className="border-t border-white/10 pt-3 space-y-2">
                          <div className="space-y-0.5 min-w-0">
                            <label className="text-sm font-medium leading-none flex items-center gap-1.5">
                              <Power className="h-3.5 w-3.5 text-muted-foreground" />
                              依赖被占用时
                            </label>
                            <p className="text-xs leading-relaxed text-muted-foreground">
                              {DEP_POLICY_DESCS[depPolicy]}
                            </p>
                          </div>
                          <div className="grid grid-cols-3 gap-1.5">
                            {DEP_POLICY_OPTIONS.map((o) => (
                              <button
                                key={o.value}
                                type="button"
                                onClick={() => setDepPolicy(o.value)}
                                aria-pressed={depPolicy === o.value}
                                className={cn(
                                  'h-8 rounded-full text-xs font-medium transition-colors',
                                  depPolicy === o.value
                                    ? 'bg-primary/15 text-primary border border-primary/40'
                                    : 'bg-muted/60 text-muted-foreground hover:text-foreground border border-transparent',
                                )}
                              >
                                {o.label}
                              </button>
                            ))}
                          </div>
                        </div>

                        {/* 0.6.548：允许自动停用的应用白名单 —— 只有「自动停用后升级」档才需要 */}
                        {depPolicy === 'stop' && (
                          <div className="border-t border-white/10 pt-3 space-y-2">
                            <button
                              type="button"
                              onClick={() => setDepPickerOpen(true)}
                              className="flex w-full items-start justify-between gap-3 text-left"
                            >
                              <div className="space-y-0.5 min-w-0">
                                <label className="text-sm font-medium leading-none flex items-center gap-1.5">
                                  <SlidersHorizontal className="h-3.5 w-3.5 text-muted-foreground" />
                                  允许自动停用的应用
                                  <span className="text-xs font-normal text-muted-foreground">
                                    ({depAllow.length})
                                  </span>
                                </label>
                                <p className="text-xs leading-relaxed text-muted-foreground">
                                  只有全部依赖方都在名单里才会动手，否则整体跳过
                                </p>
                              </div>
                              <span className="shrink-0 rounded-full border border-primary/40 bg-primary/15 px-2.5 py-1 text-[11px] font-medium text-primary">
                                选择
                              </span>
                            </button>
                            {depAllow.length === 0 && (
                              <p className="text-destructive text-[11px] leading-relaxed">
                                尚未选择应用，本轮不会自动停用任何应用
                              </p>
                            )}
                          </div>
                        )}
                        {/* 0.6.549（用户令）：时段改用与加速源间隔同款齿轮选择，不再弹浏览器时间选择器 */}
                        {depPolicy === 'stop' && (
                          <div className="border-t border-white/10 pt-3 space-y-2">
                            <div className="space-y-0.5 min-w-0">
                              <label className="text-sm font-medium leading-none flex items-center gap-1.5">
                                <Clock className="h-3.5 w-3.5 text-muted-foreground" />
                                自动停用时段
                              </label>
                              <p className="text-xs leading-relaxed text-muted-foreground">
                                仅在该时段内自动停用依赖；跨零点显示「次日」
                              </p>
                            </div>
                            <div
                              className={cn(
                                'flex flex-wrap items-center gap-x-3 gap-y-1.5',
                                // 0.6.551：非法时只加描边提示（ring 不占布局，避免跳动）
                                depWindowError && 'rounded-lg ring-1 ring-destructive/50',
                              )}
                              aria-describedby="dep-window-hint"
                            >
                              <div className="flex items-center gap-1.5">
                                <span className="whitespace-nowrap text-[11px] text-muted-foreground">开始</span>
                                <GearTimePicker
                                  hours={parseHM(depWindowStart)[0]}
                                  minutes={parseHM(depWindowStart)[1]}
                                  onChange={(h, m) => setDepWindowStart(fmtHM(h, m))}
                                  ariaLabel="自动停用时段开始"
                                />
                              </div>
                              <span className="whitespace-nowrap text-xs text-muted-foreground">
                                {depWindowStart > depWindowEnd ? '至次日' : '至'}
                              </span>
                              <div className="flex items-center gap-1.5">
                                <span className="whitespace-nowrap text-[11px] text-muted-foreground">结束</span>
                                <GearTimePicker
                                  hours={parseHM(depWindowEnd)[0]}
                                  minutes={parseHM(depWindowEnd)[1]}
                                  onChange={(h, m) => setDepWindowEnd(fmtHM(h, m))}
                                  ariaLabel="自动停用时段结束"
                                />
                              </div>
                            </div>
                            {/* 0.6.551：合法时给时长（跨零点一眼看得出），非法时红字说明原因 */}
                            {depWindowError ? (
                              <p id="dep-window-hint" role="alert" className="text-destructive text-[11px] leading-relaxed">
                                {depWindowError}
                              </p>
                            ) : (
                              <p id="dep-window-hint" className="text-[11px] leading-relaxed text-muted-foreground">
                                共 {formatSpanMinutes(depWindowSpanMin)}
                                {depWindowStart > depWindowEnd ? '（跨零点）' : ''}
                              </p>
                            )}
                          </div>
                        )}
                      </>
                    )}
                  </div>
                  {/* 0.6.326x（用户令）：原独立开关「自动纠正应用更新」已并入
                      「更新策略」做第四档「强制上游」，不再单独渲染；逻辑见
                      internal/api resolveUpdateTargetSelection。 */}
                </div>

                {/* 0.6.548：允许自动停用的应用白名单勾选弹窗 */}
                <DepAllowPickerDialog
                  open={depPickerOpen}
                  onOpenChange={setDepPickerOpen}
                  value={depAllow}
                  onSave={setDepAllow}
                />

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
                {/* 0.6.387（用户令）：日志改读远程清单，只显示最新版（第一段）；
                    远程不可达时回落探测到的 release 正文；两者都无则不渲染。
                    0.6.314r2：日志内容分页显示（短日志=单页零变化） */}
                {(remoteChangelogLatest || storeInfo?.latest_changelog) && (
                  <ChangelogPager text={remoteChangelogLatest || storeInfo?.latest_changelog || ''} appKey="moo" maxH="max-h-44" />
                )}
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
          {/* 0.6.343：日志 tab 不显示底部「保存」——该页没有待保存项（行数选择即时
              落库），此前悬浮胶囊会压在日志列表最后一行上（用户报「日志页面很乱」）。
              0.6.364（用户令）：默认不显示——只有当前表单改过（formDirty）才浮出；
              通知 tab 的设置是子组件逐项即时保存，父级无待提交项，一并排除。
              0.6.530（用户令）：加速源 tab 恢复「跟随全局」——改动才浮出，
              未改动不显示（0.6.529 的常驻卡内保存按钮已删除）。 */}
          {tab !== 'about' && tab !== 'log' && tab !== 'notify' && formDirty && !kbDockHidden && (
            // 0.6.319g（用户定稿）：absolute 悬浮 overlay（与详情页下载胶囊同款）。
            // 内容滚动框贯穿到底，文字从按钮下方滚过（按钮=307 定稿浅蓝玻璃
            // 半透）；按钮之外全 pointer-events-none=滚轮/触摸穿透。
            // 键盘行为不变（0.6.225 定稿：键盘不改对话框高度，按钮恒在物理底边）。
            // 0.6.319i-r2（用户定稿）：底 padding 走 .save-dock-pad（index.css）——
            // 移动端按钮底边「挨着 home 线、留一点点距离」：安卓=安全区+8px、
            // iOS=安全区-16px（319d「位置很好」位）；319i 的 pb-0（顶死屏底、
            // 压 home 指示条=操作冲突）已废弃；桌面 sm+ 保持 8px。
            <div
              ref={onSaveDockRef}
              className="pointer-events-none absolute inset-x-0 bottom-0 px-3 pt-2 save-dock-pad sm:px-6"
            >
              {/* 0.6.221：不再做 translateY 上抬——对话框底边已等于可见底边
                  （见上方 dialogKeyboardStyle），再抬就会过冲：按钮浮在键盘上方、
                  不像"固定在底部"（用户实报）。absolute bottom-0 同样恒钉底边。
                  0.6.302（用户定稿）：0.6.301 的卡框+贴底改动回退，恢复原胶囊按钮。
                  0.6.319g（用户定稿）：shadow-lg 底阴影去掉——悬浮感改由内容
                  从按钮下穿过体现（与详情页下载胶囊逐字同款语言）。 */}
              <div className="pointer-events-auto mx-auto w-full sm:w-72">
                {/* 0.6.367（用户令）：「保存按钮也不要蓝底色了」——与 0.6.364 的
                    安装/打开胶囊同款中性药丸（bg-muted/60 + 描边 + 前景字），
                    去掉默认主色的浅蓝玻璃底。 */}
                <Button
                  className="w-full h-11 rounded-2xl bg-muted/60 text-foreground border border-border/50 hover:bg-muted shadow-none"
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
    {/* 已下载 FPK 安装向导：0.6.325 C4 随列表段移入 FpkDownloads 组件内部 */}
    </>
  );
};

export default SettingsPage;
