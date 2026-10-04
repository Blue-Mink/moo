import React, { useState, useEffect, useCallback } from 'react';
import { fetchSources, addSourcesBatch, removeSource, renameSource, syncSource, syncAllSources, restoreDefaultSources, toggleSource, syncSourceList, fetchSettings, updateSettings, reorderSources, toggleSourceFavorite, type SourceEntry } from '../api/client';
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Switch } from "@/components/ui/switch"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogFooter,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogCancel,
} from "@/components/ui/alert-dialog"
import { Loader2, Plus, Trash2, ExternalLink, Link2, RefreshCw, ListTree, ChevronDown, Check, Activity, MoreHorizontal, Copy, GripVertical, Star, RotateCcw, ShieldAlert, KeyRound } from 'lucide-react'
import { cn } from '@/lib/utils'
import { shouldWarnPlainHttp } from '@/lib/sourceWarnings'
import { toast } from 'sonner'
import OfficialOAuthDialog from './OfficialOAuthDialog'

interface SourceManagerProps {
  /** 源列表变化后通知父组件刷新应用目录 */
  onCatalogChanged?: () => void;
  /** 父级「保存」动作计数；变化时退出抖动排序模式（界面收敛到静态） */
  saveCounter?: number;
}

/** 0.6.216 P1①：明文 http 源警示规则抽到 lib（官方源豁免，见 sourceWarnings） */

const SourceManager: React.FC<SourceManagerProps> = ({ onCatalogChanged, saveCounter = 0 }) => {
  const [sources, setSources] = useState<SourceEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [input, setInput] = useState('');
  const [adding, setAdding] = useState(false);
  const [removingId, setRemovingId] = useState<string | null>(null);
  const [syncingId, setSyncingId] = useState<string | null>(null);
  // 0.6.171：一键刷新所有应用源（自动监测卡的圆形刷新按钮）
  const [syncingAll, setSyncingAll] = useState(false);
  // 0.6.172：一键恢复默认应用源列表（防误删 + 相同源去重）
  const [restoring, setRestoring] = useState(false);
  // 源列表折叠（本地持久化）
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    try {
      // 默认折叠（无持久化记录时）；用户手动展开/折叠后按保存值
      const v = localStorage.getItem('new-store.sources.collapsed');
      return v === null ? true : v === '1';
    } catch { return true; }
  });
  const [togglingId, setTogglingId] = useState<string | null>(null);
  // 内置源列表自动同步（列表地址固定用内置/配置值，界面不再暴露输入框）
  const [listAuto, setListAuto] = useState(true);
  const [syncingList, setSyncingList] = useState(false);
  const [savingList, setSavingList] = useState(false);
  // 应用源自动监测（连续无应用自动关闭 + 空源沉底）
  const [autoCare, setAutoCare] = useState(true);
  const [savingCare, setSavingCare] = useState(false);
  // 点击复制源地址（短暂高亮反馈）
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const copyTimerRef = React.useRef<ReturnType<typeof setTimeout> | null>(null);
  // 一键复制全部源地址（短暂 ✓ 反馈）
  const [copiedAll, setCopiedAll] = useState(false);
  const copyAllTimerRef = React.useRef<ReturnType<typeof setTimeout> | null>(null);
  // 重命名应用源（⋯ 设置按钮 → 弹窗）
  const [renameSrc, setRenameSrc] = useState<SourceEntry | null>(null);
  const [renameValue, setRenameValue] = useState('');
  const [removalSrc, setRemovalSrc] = useState<SourceEntry | null>(null);
  // 0.6.253：官方应用中心 OAuth 免登录连接对话框
  const [oauthOpen, setOauthOpen] = useState(false);
  const [renaming, setRenaming] = useState(false);
  // ── 长按抖动排序（参考 knock 子域排序）─────────────────────────────────
  // 非排序态：长按源行 500ms（手指不移）进入抖动模式；
  // 抖动态：再长按 150ms 抓住某行拖动换位，落位即保存；点「完成」/短按退出。
  // 飞牛应用中心（官方源）固定最顶部，不参与排序。
  const [wiggle, setWiggle] = useState(false);
  const [dragIdx, setDragIdx] = useState<number | null>(null);
  const [dragOffset, setDragOffset] = useState(0);
  const [targetIdx, setTargetIdx] = useState<number | null>(null);
  const [savingOrder, setSavingOrder] = useState(false);
  const lpTimerRef = React.useRef<ReturnType<typeof setTimeout> | null>(null);
  const pressRef = React.useRef<{ x: number; y: number; idx: number; pointerId: number; grabbed: boolean; moved: boolean; entered: boolean } | null>(null);
  // 刚由长按进入抖动模式：屏蔽该次按压的后续 click（避免 URL 复制按钮副作用）
  const wiggleJustEnteredRef = React.useRef(false);
  const rowElsRef = React.useRef<(HTMLDivElement | null)[]>([]);
  const ROW_GAP = 8; // space-y-2 行间距

  const openRename = (src: SourceEntry) => {
    setRenameValue(src.name);
    setRenameSrc(src);
  };

  // 关注源（星标，0.6.143）：⋯ 前一颗星；乐观更新 + 失败回滚。
  // 关注源新增应用时后端推 favorite_source_apps 通知。
  const handleFavorite = async (src: SourceEntry) => {
    const next = !src.favorite;
    setSources((prev) => prev.map((s) => (s.id === src.id ? { ...s, favorite: next } : s)));
    try {
      await toggleSourceFavorite(src.id, next);
    } catch (e) {
      setSources((prev) => prev.map((s) => (s.id === src.id ? { ...s, favorite: !next } : s)));
      toast.error(e instanceof Error ? e.message : '更新源关注失败');
    }
  };

  const handleRenameSave = async () => {
    if (!renameSrc || renaming) return;
    const name = renameValue.trim();
    if (name === '') {
      toast.error('源名称不能为空');
      return;
    }
    setRenaming(true);
    try {
      await renameSource(renameSrc.id, name);
      toast.success(`已将应用源「${renameSrc.name}」重命名为「${name}」`);
      setRenameSrc(null);
      await load();
      onCatalogChanged?.(); // 应用列表源徽章跟随新名
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '重命名应用源失败');
    } finally {
      setRenaming(false);
    }
  };

  const handleCopyAll = async () => {
    // 官方应用中心的 url 是面板地址（非源链接），不参与复制；去重保序
    // 0.6.141：无协议的存量地址复制时补 https://（与后端添加归一化双保险）
    const seen = new Set<string>();
    const urls = sources
      .filter((s) => s.id !== 'fnos-official' && s.url)
      .map((s) => (s.url as string).match(/^https?:\/\//i) ? s.url : `https://${s.url}`)
      .filter((u) => {
        const k = u.toLowerCase();
        if (seen.has(k)) return false;
        seen.add(k);
        return true;
      });
    if (urls.length === 0) {
      toast.error('没有可复制的应用源地址');
      return;
    }
    const text = urls.join('\n');
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      try {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      } catch { /* 剪贴板不可用：下面仅提示 */ }
    }
    setCopiedAll(true);
    toast.success(`已复制 ${urls.length} 个应用源地址（每行一个）`);
    if (copyAllTimerRef.current) clearTimeout(copyAllTimerRef.current);
    copyAllTimerRef.current = setTimeout(() => setCopiedAll(false), 2000);
  };

  const handleCollapse = () => {
    setCollapsed((v) => {
      try { localStorage.setItem('new-store.sources.collapsed', v ? '0' : '1'); } catch { /* ignore */ }
      return !v;
    });
  };

  const handleToggle = async (src: SourceEntry, enabled: boolean) => {
    if (togglingId === src.id) return;
    setTogglingId(src.id);
    try {
      await toggleSource(src.id, enabled);
      toast.success(`应用源「${src.name}」已${enabled ? '开启' : '关闭'}`);
      await load();
      onCatalogChanged?.();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '切换应用源状态失败');
    } finally {
      setTogglingId(null);
    }
  };

  const load = useCallback(async () => {
    try {
      const res = await fetchSources();
      setSources(res.sources || []);
    } catch (e) {
      console.error('Failed to load sources:', e);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    fetchSettings()
      .then((s) => {
        setListAuto(!(s.source_list_disabled ?? false));
        setAutoCare(!(s.source_auto_care_disabled ?? false));
      })
      .catch(() => {});
  }, [load]);

  // 保存源列表设置（带上现有设置全量回传，避免覆盖其它配置；
  // 列表地址沿用当前值，界面已不提供修改入口）
  const persistListSettings = useCallback(async (auto?: boolean, care?: boolean) => {
    const cur = await fetchSettings();
    await updateSettings({
      check_interval_hours: cur.check_interval_hours,
      mirror: cur.mirror,
      docker_mirror: cur.docker_mirror,
      custom_github_mirror: cur.custom_github_mirror,
      custom_docker_mirror: cur.custom_docker_mirror,
      install_volume: cur.install_volume,
      source_list_url: cur.source_list_url,
      source_list_disabled: !(auto ?? listAuto),
      // 全量回传：FPK 下载目录 / 自动监测不能被本组件的保存抹掉
      download_dir: cur.download_dir,
      source_auto_care_disabled: !(care ?? autoCare),
      // 0.6.255：官方源 = 纯 OAuth，无 panel_* 字段可回传
    });
  }, [listAuto, autoCare]);

  const handleListAutoChange = async (v: boolean) => {
    setListAuto(v);
    setSavingList(true);
    try {
      await persistListSettings(v);
      toast.success(v ? '已开启源列表自动同步' : '已关闭源列表自动同步');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '保存设置失败');
    } finally {
      setSavingList(false);
    }
  };

  const handleCareChange = async (v: boolean) => {
    setAutoCare(v);
    setSavingCare(true);
    try {
      await persistListSettings(undefined, v);
      toast.success(v ? '已开启应用源自动监测' : '已关闭应用源自动监测');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '保存设置失败');
    } finally {
      setSavingCare(false);
    }
  };

  // 点击源地址复制到剪贴板（短暂 ✓ 反馈）
  const handleCopyUrl = async (src: SourceEntry) => {
    // 0.6.141：无协议的存量地址（如早期的 Blue-Mink 源）复制时补 https://
    const fullUrl = (src.url as string).match(/^https?:\/\//i) ? src.url : `https://${src.url}`;
    try {
      await navigator.clipboard.writeText(fullUrl);
    } catch {
      try {
        const ta = document.createElement('textarea');
        ta.value = fullUrl;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      } catch { /* 剪贴板不可用时仅提示 */ }
    }
    setCopiedId(src.id);
    toast.success(`已复制应用源地址：${src.name}`);
    if (copyTimerRef.current) clearTimeout(copyTimerRef.current);
    copyTimerRef.current = setTimeout(() => setCopiedId(null), 2000);
  };

  const handleSyncList = async () => {
    setSyncingList(true);
    try {
      // 先落盘当前地址/开关，再触发同步
      try {
        await persistListSettings();
      } catch {
        /* 保存失败不阻断同步 */
      }
      const res = await syncSourceList();
      if (res.added > 0) {
        toast.success(
          `源列表同步完成：${res.fetched} 个条目，新增 ${res.added} 个源` +
            (res.added_names?.length ? `（${res.added_names.join('、')}）` : '')
        );
      } else {
        toast.success(`源列表同步完成：${res.fetched} 个条目，没有新源`);
      }
      if (res.failed > 0) {
        const errs = (res.errors || []).slice(0, 3).join('；');
        toast.warning(`源列表同步：${res.failed} 个地址无效（${errs}${(res.errors || []).length > 3 ? '…' : ''}）`);
      }
      await load();
      onCatalogChanged?.();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '同步源列表失败');
    } finally {
      setSyncingList(false);
    }
  };

  const lines = input.split('\n').map((l) => l.trim()).filter(Boolean);

  const handleAdd = async () => {
    if (lines.length === 0) {
      toast.error('请输入应用源地址（每行一个）');
      return;
    }
    setAdding(true);
    try {
      const res = await addSourcesBatch(lines.map((url) => ({ url })));
      const ok = res.results.filter((r) => r.ok);
      const dedup = res.results.filter((r) => r.deduped);
      const fail = res.results.filter((r) => !r.ok && !r.deduped);
      if (ok.length > 0) {
        const names = ok.map((r) => `「${r.name}」`).join('、');
        toast.success(`已添加 ${ok.length} 个应用源：${names}`);
      }
      // 0.6.173：重复源（与列表已有/本批已加同地址）只保留一个，友好提示而非报错
      if (dedup.length > 0) {
        const names = dedup.slice(0, 3).map((r) => `「${r.name || r.url}」`).join('、');
        toast.info(`已跳过 ${dedup.length} 个重复应用源（相同地址只保留一个）${names ? `：${names}${dedup.length > 3 ? ' 等' : ''}` : ''}`);
      }
      for (const r of fail) {
        toast.error(`${r.url}：${r.error || '添加失败'}`);
      }
      if (ok.length > 0) setInput('');
      await load();
      onCatalogChanged?.();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '批量添加应用源失败');
    } finally {
      setAdding(false);
    }
  };

  const handleRemove = async (src: SourceEntry) => {
    setRemovingId(src.id);
    try {
      await removeSource(src.id);
      toast.success(`已移除应用源「${src.name}」`);
      await load();
      onCatalogChanged?.();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '删除应用源失败');
    } finally {
      setRemovingId(null);
    }
  };

  // ── 抖动排序 handlers（knock 子域排序同款交互）────────────────────────
  const clearLpTimer = () => {
    if (lpTimerRef.current) { clearTimeout(lpTimerRef.current); lpTimerRef.current = null; }
  };
  const exitWiggle = () => {
    clearLpTimer();
    pressRef.current = null;
    setWiggle(false);
    setDragIdx(null);
    setDragOffset(0);
    setTargetIdx(null);
  };
  // 父级点过「保存」→ 退出抖动排序模式（跳过首次挂载；排序已随拖拽落位持久化）
  const lastSaveCounterRef = React.useRef(saveCounter);
  React.useEffect(() => {
    if (lastSaveCounterRef.current === saveCounter) return;
    lastSaveCounterRef.current = saveCounter;
    if (wiggle) exitWiggle();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [saveCounter]);
  const draggableSources = sources.filter((s) => s.id !== 'fnos-official');
  const dragIdxById: Record<string, number> = {};
  draggableSources.forEach((s, i) => { dragIdxById[s.id] = i; });

  // 拖拽只从行左侧 ⠿ 手柄发起（手柄 touch-action:none，移动端浏览器无法把
  // 该手势劫持成页面滚动）；行体本身保持可滚动——抖动态里划行 = 翻页，
  // 短按行体 = 退出排序。行体长按 500ms（手指不动）进入抖动模式。
  const onRowPointerDown = (idx: number, e: React.PointerEvent<HTMLDivElement>) => {
    if (savingOrder) return;
    if (e.pointerType === 'mouse' && e.button !== 0) return;
    // 长按右侧控制组/手柄/链接 不进入排序模式，避免误触；
    // 信息区（含 URL 复制按钮）允许长按进入——移动端窄屏下它占行体大部分
    const t = e.target as HTMLElement;
    if (t.closest('.src-controls, .src-handle, a, input, textarea')) return;
    if (pressRef.current && pressRef.current.idx === idx && pressRef.current.grabbed) return; // 手柄已抓住
    clearLpTimer();
    pressRef.current = { x: e.clientX, y: e.clientY, idx, pointerId: e.pointerId, grabbed: false, moved: false, entered: false };
    if (!wiggle) {
      // 非抖动态：长按 500ms 进入抖动模式
      lpTimerRef.current = setTimeout(() => {
        const p = pressRef.current;
        if (!p || p.idx !== idx || p.moved) return;
        p.entered = true; // 本次按压即进入抖动的那次：抬起时保持模式，不退出
        wiggleJustEnteredRef.current = true; // 屏蔽随后的 click（URL 复制等副作用）
        setWiggle(true);
      }, 500);
    } else if (e.pointerType === 'mouse') {
      // 桌面端抖动态：长按行体 150ms 抓住拖拽（鼠标无滚动劫持问题）
      lpTimerRef.current = setTimeout(() => {
        const p = pressRef.current;
        if (!p || p.idx !== idx || p.moved) return;
        p.grabbed = true;
        const el = rowElsRef.current[idx];
        if (el) { try { el.setPointerCapture(p.pointerId); } catch { /* 忽略 */ } }
        setDragIdx(idx);
        setDragOffset(0);
        setTargetIdx(idx);
      }, 150);
    }
    // 抖动态 + 触控行体按压 = 短按退出候选 / 划行滚动，不抓行
  };

  // 手柄按压：立即抓住该行开始拖（手柄上浏览器不可能发起滚动）
  const onHandlePointerDown = (idx: number, e: React.PointerEvent<HTMLButtonElement>) => {
    if (savingOrder || !wiggle) return;
    if (e.pointerType === 'mouse' && e.button !== 0) return;
    e.preventDefault();
    clearLpTimer();
    pressRef.current = { x: e.clientX, y: e.clientY, idx, pointerId: e.pointerId, grabbed: true, moved: false, entered: false };
    const el = rowElsRef.current[idx];
    if (el) {
      try { el.setPointerCapture(e.pointerId); } catch { /* 忽略 */ }
    }
    setDragIdx(idx);
    setDragOffset(0);
    setTargetIdx(idx);
  };

  const onRowPointerMove = (idx: number, e: React.PointerEvent<HTMLDivElement>) => {
    const p = pressRef.current;
    if (!p || p.idx !== idx) return;
    if (!p.moved && Math.abs(e.clientX - p.x) + Math.abs(e.clientY - p.y) > 10) {
      p.moved = true;
      if (!p.grabbed) clearLpTimer(); // 还没抓住就移动了 = 滚动，取消长按判定
      return;
    }
    if (!p.grabbed) return;
    // 已抓住：计算拖动偏移与目标槽位（按各行实际高度累计）
    const els = rowElsRef.current;
    const n = draggableSources.length;
    const heights: number[] = [];
    for (let i = 0; i < n; i++) {
      const el = els[i];
      heights.push((el ? el.offsetHeight : 72) + ROW_GAP);
    }
    const offset = e.clientY - p.y;
    let acc = 0;
    for (let i = 0; i < idx; i++) acc += heights[i];
    const center = acc + heights[idx] / 2 + offset;
    let t = idx;
    acc = 0;
    for (let i = 0; i < n; i++) {
      if (center >= acc + heights[i] / 2) t = i; else break;
      acc += heights[i];
    }
    setDragOffset(offset);
    setTargetIdx(t);
  };

  const onRowPointerUp = (idx: number) => {
    const p = pressRef.current;
    pressRef.current = null;
    clearLpTimer();
    if (!wiggle) return; // 非抖动态：进入抖动由定时器处理；短按/滚动无动作
    if (!p) return; // 无按压记录（如定时器已消费）：保持当前状态
    if (p.entered) return; // 本次按压刚进入抖动模式：保持，不退出
    if (p.grabbed) {
      // 落位：若目标槽位变化则保存排序
      const to = targetIdx;
      setDragIdx(null);
      setDragOffset(0);
      setTargetIdx(null);
      if (to === null || to === idx) return;
      const arr = draggableSources.slice();
      const [moved] = arr.splice(idx, 1);
      arr.splice(to, 0, moved);
      const order = arr.map((s) => s.id);
      setSavingOrder(true);
      reorderSources(order)
        .then(() => {
          setSources((prev) => {
            const off = prev.find((s) => s.id === 'fnos-official');
            return off ? [off, ...arr] : arr;
          });
          toast.success('应用源顺序已保存');
        })
        .catch((e) => toast.error(e instanceof Error ? e.message : '排序保存失败'))
        .finally(() => setSavingOrder(false));
      return;
    }
    if (p.moved) return; // 抖动态里划行滚动：什么都不做
    // 短按行体 = 退出抖动模式（与 iOS 一致）
    exitWiggle();
  };

  const onRowPointerCancel = (idx: number) => {
    const p = pressRef.current;
    pressRef.current = null;
    clearLpTimer();
    // 拖拽被系统手势打断：还原，不保存
    if (wiggle && p && p.idx === idx && p.grabbed) {
      setDragIdx(null);
      setDragOffset(0);
      setTargetIdx(null);
    }
  };

  const handleSync = async (src: SourceEntry) => {
    setSyncingId(src.id);
    try {
      const updated = await syncSource(src.id);
      if (updated.error) {
        toast.error(`源「${src.name}」同步失败：${updated.error}`);
      } else {
        toast.success(`源「${src.name}」同步完成，${updated.app_count} 个应用`);
      }
      await load();
      onCatalogChanged?.();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '同步应用源失败');
    } finally {
      setSyncingId(null);
    }
  };

  // 一键刷新所有应用源（0.6.171）：后端并发 8 只刷已启用源，单源失败不影响其他
  const handleSyncAll = async () => {
    if (sources.length === 0) return;
    setSyncingAll(true);
    try {
      const r = await syncAllSources();
      if (r.failed.length === 0) {
        toast.success(`已刷新全部 ${r.total} 个应用源`);
      } else {
        const names = r.failed.slice(0, 3).map((f) => f.name).join('、');
        const more = r.failed.length > 3 ? ` 等 ${r.failed.length} 个` : '';
        toast.warning(`已刷新 ${r.synced}/${r.total} 个应用源，失败：${names}${more}`);
      }
      await load();
      onCatalogChanged?.();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '刷新所有应用源失败');
    } finally {
      setSyncingAll(false);
    }
  };

  // 一键恢复默认应用源列表（0.6.172）：补齐被删的默认源 + 相同源地址只留一个
  const handleRestoreDefaults = async () => {
    setRestoring(true);
    try {
      const r = await restoreDefaultSources();
      const parts: string[] = [];
      if (r.restored > 0) parts.push(`恢复 ${r.restored} 个默认源`);
      if (r.deduped > 0) parts.push(`去重移除 ${r.deduped} 个重复源`);
      if (parts.length === 0) toast.info('默认源列表已是完整状态，无缺失');
      else toast.success(`默认源列表恢复完成：${parts.join('，')}`);
      if (r.failed > 0) toast.warning(`${r.failed} 个源添加失败，可在下方查看`);
      await load();
      onCatalogChanged?.();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '恢复默认应用源失败');
    } finally {
      setRestoring(false);
    }
  };

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium">
          应用源
          {sources.length > 0 && (
            <span className="ml-1.5 text-[11px] font-normal text-muted-foreground">
              {sources.length} 个
            </span>
          )}
        </h3>
        {wiggle ? (
          <Button
            variant="outline"
            size="sm"
            className="h-7 rounded-full px-3 text-xs font-medium"
            onClick={exitWiggle}
            disabled={savingOrder}
          >
            {savingOrder && <Loader2 className="mr-1 h-3 w-3 animate-spin" />}
            完成
          </Button>
        ) : (
          <span className="text-[11px] text-muted-foreground">FnDepot V1/V2</span>
        )}
      </div>
      {wiggle && (
        <p className="text-[11px] leading-relaxed text-muted-foreground">
          按住源左侧 ⠿ 手柄拖动可调整顺序（飞牛应用中心固定在最顶部）；点「完成」或短按源退出排序。
        </p>
      )}

      {/* 源列表自动同步 */}
      <div className="space-y-2 rounded-lg border border-border/20 bg-card p-3">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-1.5 text-xs font-medium text-foreground">
            <ListTree className="h-3.5 w-3.5 text-muted-foreground" />
            源列表自动同步
          </div>
          <Switch checked={listAuto} onCheckedChange={handleListAutoChange} disabled={savingList || syncingList} title="开启后每次目录检查自动添加列表中的新源" />
        </div>
        <div className="flex gap-2">
          <Button
            variant="outline"
            className="h-9 flex-1 gap-1.5 text-xs"
            onClick={handleSyncList}
            disabled={syncingList || savingList}
            title="立即抓取内置社区源列表并自动添加新源"
          >
            <RefreshCw className={`h-3.5 w-3.5 shrink-0 ${syncingList ? 'animate-spin text-primary' : ''}`} />
            {syncingList ? '同步中…' : '立即同步源列表'}
          </Button>
          {/* 0.6.172：一键恢复默认应用源列表（防误删；恢复时相同源地址只保留一个） */}
          <Button
            variant="outline"
            className="h-9 flex-1 gap-1.5 text-xs"
            onClick={handleRestoreDefaults}
            disabled={restoring || syncingList || savingList}
            title="重抓内置社区源列表补齐被删的默认源；相同源地址只保留一个（官方源不受影响）"
          >
            <RotateCcw className={`h-3.5 w-3.5 shrink-0 ${restoring ? 'animate-spin text-primary' : ''}`} />
            {restoring ? '恢复中…' : '恢复默认源列表'}
          </Button>
        </div>
        <p className="text-[11px] leading-relaxed text-muted-foreground">
          从内置社区源列表自动发现并添加新应用源，只增不删。「恢复默认源列表」可找回误删的默认源，并把相同地址的重复源去重为 1 个。
        </p>
      </div>

      {/* 应用源自动监测（连续无应用自动关闭 + 空源沉底；列表折叠也放在这里） */}
      <div className="space-y-2 rounded-lg border border-border/20 bg-card p-3">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-1.5 text-xs font-medium text-foreground">
            <Activity className="h-3.5 w-3.5 text-muted-foreground" />
            应用源自动监测
            {sources.length > 0 && (
              <span className="text-[11px] font-normal text-muted-foreground">
                {sources.length} 个 · {autoCare ? '监测中' : '已关闭'}
              </span>
            )}
          </div>
          <div className="flex items-center gap-2">
            {/* 0.6.171 顺序（用户定稿）：一键复制 → 开启/关闭 → 一键刷新 → 折叠
                （开关与工具按钮组 8px，两个工具按钮 4px） */}
            {/* 一键复制全部应用源地址（不含官方应用中心；每行一个，可直接粘贴到别的 Moo） */}
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7"
              onClick={handleCopyAll}
              title="复制全部应用源地址（每行一个）"
              aria-label="复制全部应用源地址"
            >
              {copiedAll ? (
                <Check className="h-3.5 w-3.5 text-primary" />
              ) : (
                <Copy className="h-3.5 w-3.5" />
              )}
            </Button>
            <Switch
              checked={autoCare}
              onCheckedChange={handleCareChange}
              disabled={savingCare}
              title="开启后，应用源连续 5 次无应用将自动关闭，空源自动沉底"
            />
            <div className="flex items-center gap-1">
              {/* 0.6.171：一键刷新所有应用源（与源列表行内同步按钮同款圆形 RefreshCw） */}
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7 rounded-full"
                onClick={handleSyncAll}
                disabled={syncingAll || sources.length === 0}
                title="一键刷新所有应用源（并发抓取，只刷已启用源）"
                aria-label="一键刷新所有应用源"
              >
                <RefreshCw className={`h-3.5 w-3.5 ${syncingAll ? 'animate-spin text-primary' : ''}`} />
              </Button>
              {/* 与加速源健康面板的折叠按钮同款（size=icon h-7 w-7 + ChevronDown 旋转） */}
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7"
                onClick={handleCollapse}
                title={collapsed ? '展开应用源列表' : '折叠应用源列表'}
                aria-label={collapsed ? '展开应用源列表' : '折叠应用源列表'}
              >
                <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", collapsed && "-rotate-90")} />
              </Button>
            </div>
          </div>
        </div>
        <p className="text-[11px] leading-relaxed text-muted-foreground">
          持续探测各应用源可用性：连续 5 次无应用将自动关闭该源，空源自动沉底，减少无效抓取。
        </p>
      </div>

      {!collapsed && (loading ? (
        <div className="flex justify-center py-3">
          <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        </div>
      ) : sources.length === 0 ? (
        <p className="text-xs leading-relaxed text-muted-foreground">
          暂无外部应用源。添加后，源中的应用会并入商店目录，来源与作者会在应用上标注。
        </p>
      ) : (
        <div className="space-y-2">
          {sources.map((src) => {
            const isOfficialSrc = src.id === 'fnos-official';
            const disabled = src.enabled === false;
            // ── 抖动排序（knock 子域排序同款）：本行拖拽状态；官方源 idx=-1 固定最顶 ──
            const idx = dragIdxById[src.id] ?? -1;
            const isDragging = !isOfficialSrc && dragIdx === idx;
            let shift = 0;
            if (!isOfficialSrc && dragIdx !== null && targetIdx !== null && !isDragging) {
              const draggedEl = rowElsRef.current[dragIdx];
              const h = (draggedEl ? draggedEl.offsetHeight : 72) + ROW_GAP;
              if (dragIdx < targetIdx && idx > dragIdx && idx <= targetIdx) shift = -h;
              else if (dragIdx > targetIdx && idx >= targetIdx && idx < dragIdx) shift = h;
            }
            // 行内容（官方行 / 可排序行共用）：拖动时控件淡出但保留占位，布局不跳
            const rowContent = (
              <>
              {wiggle && !isOfficialSrc && (
                <button
                  type="button"
                  onPointerDown={(e) => onHandlePointerDown(idx, e)}
                  onContextMenu={(e) => e.preventDefault()}
                  className="src-handle -ml-1.5 shrink-0 cursor-grab touch-none select-none rounded p-1 active:cursor-grabbing"
                  title="按住拖动排序"
                  aria-label={`拖动排序 ${src.name}`}
                >
                  <GripVertical className="h-4 w-4 text-muted-foreground" />
                </button>
              )}
              <div className={`min-w-0 flex-1 ${wiggle && !isOfficialSrc ? 'pointer-events-none' : ''}`}>
                <div className="flex items-center gap-2">
                  <span className="truncate text-sm font-medium">{src.name}</span>
                  {src.app_count > 0 && (
                    <Badge variant="secondary" className="h-5 shrink-0 whitespace-nowrap px-1.5 text-[10px] font-normal">
                      {src.app_count} 个应用
                    </Badge>
                  )}
                  {disabled && (
                    <Badge variant="outline" className="h-5 shrink-0 whitespace-nowrap px-1.5 text-[10px] font-normal text-muted-foreground">
                      已关闭
                    </Badge>
                  )}
                  {!disabled && (src.empty_streak ?? 0) > 0 && (
                    <Badge variant="secondary" className="h-5 min-w-0 max-w-[8.5rem] truncate px-1.5 text-[10px] font-normal text-amber-500"
                      title="连续抓取失败或 0 应用；再连续 5 次将自动关闭">
                      连续 {src.empty_streak} 次无应用
                    </Badge>
                  )}
                  {src.error && (
                    <Badge variant="destructive" className="h-5 shrink-0 whitespace-nowrap px-1.5 text-[10px] font-normal">
                      不可用
                    </Badge>
                  )}
                </div>
                <div className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
                  <button
                    type="button"
                    onClick={() => handleCopyUrl(src)}
                    className="min-w-0 flex-1 cursor-pointer truncate text-left hover:text-foreground hover:underline"
                    title="点击复制应用源地址"
                  >
                    {src.url}
                  </button>
                  {/* 0.6.216 P1①：http 明文源警示（展示层，不影响下载逻辑）。
                      官方应用源（fnos-official）豁免：它是平台本机面板地址，属可信来源，
                      对它标「未加密」只会误导；其余 http 明文源（社区源/自建源）仍保留警示。 */}
                  {shouldWarnPlainHttp(src) && (
                    <span
                      title="非加密 http 源：下载内容未做传输完整性保护，请核对 sha256"
                      className="shrink-0 text-amber-500"
                    >
                      <ShieldAlert className="h-3 w-3" />
                    </span>
                  )}
                  {copiedId === src.id && <Check className="h-3 w-3 shrink-0 text-primary" />}
                  {src.homepage && (
                    <a
                      href={src.homepage}
                      target="_blank"
                      rel="noreferrer"
                      className="shrink-0 hover:text-foreground"
                      title={src.homepage}
                    >
                      <ExternalLink className="h-3 w-3" />
                    </a>
                  )}
                </div>
                {src.error && (
                  <div className="mt-0.5 truncate text-[11px] text-red-500">{src.error}</div>
                )}
              </div>
              {/* 右侧两组：[⋯ 设置 + 开关] 与 [同步 + 移除]，组内 6px、组间 12px，
                  危险操作（移除）与日常操作（同步）视觉分离；抖动模式下控件隐藏（保留占位）。
                  .src-controls 供长按排序的守卫识别（长按控件不进入排序） */}
              <div className={`src-controls flex shrink-0 items-center gap-1.5 ${wiggle && !isOfficialSrc ? 'pointer-events-none opacity-0' : ''}`}>
                {/* 0.6.253：官方源 OAuth 免登录连接（iframe 内嵌授权页 + 验证码输入） */}
                {isOfficialSrc && (
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 rounded-full"
                    onClick={() => setOauthOpen(true)}
                    title="连接官方应用中心：新面板走「OAuth授权登录」，旧面板走「临时登录面板」"
                    aria-label="连接官方应用中心"
                  >
                    <KeyRound className="h-3.5 w-3.5" />
                  </Button>
                )}
                {/* 关注星标（0.6.143）：⋯ 前；关注源新增应用时推通知 */}
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7 rounded-full"
                  onClick={() => handleFavorite(src)}
                  title={src.favorite ? '取消关注该源' : '关注该源（新增应用时通知）'}
                  aria-label={src.favorite ? `取消关注源 ${src.name}` : `关注源 ${src.name}`}
                >
                  <Star
                    className={`h-3.5 w-3.5 ${src.favorite ? 'fill-amber-400 text-amber-400' : 'text-muted-foreground'}`}
                  />
                </Button>
                {/* 源设置（⋯ → 重命名；官方源名固定不可改） */}
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7 rounded-full"
                  onClick={() => openRename(src)}
                  disabled={isOfficialSrc || renaming}
                  title={isOfficialSrc ? '官方源名称固定不可改' : `重命名源 ${src.name}`}
                  aria-label={`重命名源 ${src.name}`}
                >
                  <MoreHorizontal className="h-3.5 w-3.5" />
                </Button>
                {/* 开/关（自动匹配：关后该源不参与同步，目录内已有应用保留可检索） */}
                <Switch
                  checked={src.enabled !== false}
                  onCheckedChange={(v) => handleToggle(src, v)}
                  disabled={isOfficialSrc}
                  title={src.enabled === false ? '开启该应用源' : '关闭该应用源'}
                />
              </div>
              <div className={`src-controls flex shrink-0 items-center gap-1.5 ${wiggle && !isOfficialSrc ? 'pointer-events-none opacity-0' : ''}`}>
                {/* 手动同步 */}
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7 rounded-full"
                  onClick={() => handleSync(src)}
                  disabled={src.enabled === false || syncingId === src.id || removingId === src.id}
                  title={src.enabled === false ? '源已关闭，先开启才能同步' : '立即同步该源'}
                  aria-label={`同步源 ${src.name}`}
                >
                  <RefreshCw className={`h-3.5 w-3.5 ${syncingId === src.id ? 'animate-spin text-primary' : ''}`} />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7 rounded-full hover:bg-destructive/10 hover:text-destructive"
                  onClick={() => setRemovalSrc(src)}
                  disabled={isOfficialSrc || removingId === src.id}
                  title={isOfficialSrc ? '官方应用中心不可删除' : '删除该应用源'}
                  aria-label={`删除源 ${src.name}`}
                >
                  {removingId === src.id ? (
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  ) : (
                    <Trash2 className="h-3.5 w-3.5" />
                  )}
                </Button>
              </div>
              </>
            );
            if (isOfficialSrc) {
              // 飞牛应用中心：固定最顶部，不参与抖动/排序
              return (
                <div key={src.id} className={`flex items-center gap-3 rounded-lg border px-3 py-2 ${disabled ? 'opacity-55' : ''}`}>
                  {rowContent}
                </div>
              );
            }
            // 可排序行：外层负责拖动位移（transform），内层负责抖动动画（rotate），两层不冲突
            return (
              <div
                key={src.id}
                ref={(el) => { rowElsRef.current[idx] = el; }}
                onPointerDown={(e) => onRowPointerDown(idx, e)}
                onPointerMove={(e) => onRowPointerMove(idx, e)}
                onPointerUp={() => onRowPointerUp(idx)}
                onPointerCancel={() => onRowPointerCancel(idx)}
                onContextMenu={(e) => e.preventDefault()}
                onClickCapture={(e) => {
                  // 长按刚进入抖动模式：吞掉该次按压的 click（URL 复制等副作用）
                  if (wiggleJustEnteredRef.current) {
                    e.preventDefault();
                    e.stopPropagation();
                    wiggleJustEnteredRef.current = false;
                  }
                }}
                className={`select-none ${isDragging ? 'relative z-20' : ''}`}
                style={{
                  transform: isDragging ? `translateY(${dragOffset}px)` : shift !== 0 ? `translateY(${shift}px)` : undefined,
                  transition: isDragging ? 'none' : shift !== 0 ? 'transform 150ms ease' : undefined,
                }}
              >
                <div
                  className={`flex items-center gap-3 rounded-lg border px-3 py-2 ${disabled ? 'opacity-55' : ''} ${isDragging ? 'shadow-lg' : ''} ${wiggle && !isDragging ? 'animate-wiggle' : ''}`}
                  style={wiggle && !isDragging ? { animationDelay: `${idx * 40}ms` } : undefined}
                >
                  {rowContent}
                </div>
              </div>
            );
          })}
        </div>
      ))}

      {/* 重命名应用源（⋯ 打开；应用列表源徽章自动跟随） */}
      <Dialog open={!!renameSrc} onOpenChange={(v) => { if (!v) setRenameSrc(null); }}>
        <DialogContent className="sm:max-w-md rounded-[18px] max-h-[calc(100dvh-2rem)] flex flex-col border-border/20 shadow-appstore bg-card">
          <DialogHeader>
            <DialogTitle>重命名应用源</DialogTitle>
            <DialogDescription>
              将「{renameSrc?.name}」改为新名称，应用列表中的源徽章会同步更新。
            </DialogDescription>
          </DialogHeader>
          <Input
            value={renameValue}
            onChange={(e) => setRenameValue(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') handleRenameSave(); }}
            maxLength={32}
            placeholder="源名称（最多 32 字符）"
            className="h-10 font-mono text-sm"
            autoFocus
          />
          <DialogFooter className="gap-2 sm:gap-2">
            <Button variant="ghost" onClick={() => setRenameSrc(null)}>
              取消
            </Button>
            <Button onClick={handleRenameSave} disabled={renaming || renameValue.trim() === ''}>
              {renaming && <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />}
              保存
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除应用源确认（危险操作二次确认） */}
      <AlertDialog open={!!removalSrc} onOpenChange={(v) => { if (!v && removingId === null) setRemovalSrc(null); }}>
        <AlertDialogContent className="sm:max-w-md rounded-[18px] border-border/20 shadow-appstore bg-card">
          <AlertDialogHeader>
            <AlertDialogTitle>删除应用源</AlertDialogTitle>
            <AlertDialogDescription>
              删除「{removalSrc?.name}」后，该源的应用将不再出现在目录中，也不再检测更新。
              已安装的应用与其数据不受影响。确定删除吗？
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={removingId !== null}>取消</AlertDialogCancel>
            <Button
              variant="destructive"
              onClick={async () => {
                if (!removalSrc) return;
                const target = removalSrc;
                setRemovalSrc(null);
                await handleRemove(target);
              }}
              disabled={removingId !== null}
            >
              {removingId !== null && <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />}
              删除
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* 0.6.253：官方应用中心 OAuth 免登录连接（iframe 内嵌授权页 + 返回钮 + 验证码输入） */}
      <OfficialOAuthDialog
        open={oauthOpen}
        onOpenChange={setOauthOpen}
        onCatalogChanged={onCatalogChanged}
      />

      <div className="space-y-2 rounded-lg border border-border/20 bg-card p-3">
        <div className="flex items-center gap-1.5 text-xs font-medium text-foreground">
          <Link2 className="h-3.5 w-3.5 text-muted-foreground" />
          添加应用源
          {lines.length > 1 && (
            <Badge variant="secondary" className="h-4 px-1 text-[10px]">
              {lines.length} 行
            </Badge>
          )}
        </div>
        <textarea
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && !adding) {
              e.preventDefault();
              handleAdd();
            }
          }}
          placeholder={'每行一个源地址，回车换行继续输入，例如：\nhttps://github.com/Blue-Mink/FnDepot\nhttps://github.com/SomeAuthor/Apps'}
          rows={3}
          className="w-full resize-y rounded-md border border-input bg-background px-3 py-2 text-xs leading-relaxed placeholder:text-muted-foreground/60 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        />
        <div className="flex items-center justify-between gap-2">
          <p className="text-[11px] leading-relaxed text-muted-foreground">
            支持 FnDepot V1/V2、 Moo协议（JSON 直链或 GitHub 仓库）与 conversun/fnos-apps。源名自动取仓库作者名，重名时自动追加仓库名区分；同一仓库的不同链接（仓库/直链/双协议）只保留一个。
          </p>
          <Button size="sm" onClick={handleAdd} disabled={adding || lines.length === 0} className="h-8 shrink-0">
            {adding ? (
              <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />
            ) : (
              <Plus className="mr-1 h-3.5 w-3.5" />
            )}
            {adding ? '验证中…' : `添加${lines.length > 1 ? ` ${lines.length} 个` : ''}`}
          </Button>
        </div>
      </div>
    </div>
  );
};

export default SourceManager;
