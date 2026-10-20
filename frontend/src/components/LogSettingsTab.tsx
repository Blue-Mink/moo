import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { apiUrl } from '../api/base';
import { apiFetch, fetchSettings, updateSettings } from '../api/client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { FileText, Loader2, RefreshCw, AlertTriangle, Copy, Check, Search, X, ChevronDown, ChevronUp } from 'lucide-react';
import { toast } from 'sonner';
import { parseLine, collapseDetail, LEVEL_ORDER, type Level, type Parsed } from '../lib/logParse';

interface LogData {
  file: string;
  size: number;
  total: number;
  returned: number;
  lines: string[];
  archives: string[];
  note?: string;
}

// moo.log 无显式 level 字段（格式 `YYYY/MM/DD HH:MM:SS.\u03bcs [module] message`），
// 前端按内容关键词 + module 启发式分级。级别 → 颜色/文案。
// 0.6.343：解析与折叠规则抽到 lib/logParse.ts（纯函数，可单测）。
// 0.6.344：msg = 详情文字色（对齐 Dog-dev：warn/error 的正文也带一点色，扫一眼能分辨）。
const LEVELS: Record<Level, { label: string; dot: string; text: string; row?: string; msg?: string }> = {
  fatal: { label: 'FATAL', dot: 'bg-red-600', text: 'text-red-500 dark:text-red-400 font-semibold', row: 'bg-red-500/5', msg: 'text-red-600/85 dark:text-red-300' },
  error: { label: 'ERROR', dot: 'bg-red-500', text: 'text-red-500 dark:text-red-400', row: 'bg-red-500/[0.03]', msg: 'text-red-600/80 dark:text-red-300/85' },
  warn:  { label: 'WARN',  dot: 'bg-amber-500', text: 'text-amber-600 dark:text-amber-400', msg: 'text-amber-700/90 dark:text-amber-200/90' },
  info:  { label: 'INFO',  dot: 'bg-sky-500', text: 'text-sky-600 dark:text-sky-400' },
  debug: { label: 'DEBUG', dot: 'bg-zinc-400', text: 'text-zinc-500 dark:text-zinc-500' },
};

// 0.6.268：缩短时间格式（MM-DD HH:MM:SS，13 字符）后收窄两列，
// 间距 gap-3→gap-2——把宽度让给日志详情（移动端尤其需要）。
// 104px = 13 字符 @12px mono + 余量（96px 在部分字体下会折行）。
// 0.6.344（用户令：日志做成「时间｜级别｜详情」一行，多余内容走展开/收起）：
// 移动端只显示 HH:MM:SS（8 字符），桌面显示 MM-DD HH:MM:SS——把宽度让给详情。
const TIME_W = 'w-[62px] sm:w-[104px]';
const LEVEL_W = 'w-[44px]';

export default function LogSettingsTab() {
  const [data, setData] = useState<LogData | null>(null);
  const [count, setCount] = useState(200);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState('');
  const [filter, setFilter] = useState<Level | 'all' | 'problem'>('all');
  const [copied, setCopied] = useState(false);
  // 0.6.343：关键词搜索 + 长行展开集合
  const [q, setQ] = useState('');
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  // 0.6.344：单行排版下，只有「被容器裁掉」的行才需要展开按钮。
  // 折叠摘要长度是估算的，真实是否溢出只能量 DOM（scrollWidth > clientWidth）。
  const listRef = useRef<HTMLDivElement | null>(null);
  const [overflow, setOverflow] = useState<Set<number>>(new Set());
  // 0.6.261：行数持久化在后端设置（此前纯本地态，切 tab 即复位 200）。
  // 挂载时先读已存值再发首次请求（countReady 门控，避免先用 200 打一轮）。
  const [countReady, setCountReady] = useState(false);
  useEffect(() => {
    fetchSettings()
      .then((s) => {
        if (s.log_lines === 50 || s.log_lines === 100 || s.log_lines === 500 || s.log_lines === 1000) {
          setCount(s.log_lines);
        }
      })
      .catch(() => {})
      .finally(() => setCountReady(true));
  }, []);
  // 行数变化即持久化（底部「保存」按钮负责系统类设置，行数不等它）
  const handleCountChange = (v: string) => {
    const n = Number(v);
    setCount(n);
    updateSettings({ log_lines: n }).catch(() => toast.error('日志行数未保存'));
  };

  // 复制当前显示（已按级别筛选）的日志原文到剪贴板
  const handleCopy = async () => {
    if (!data || shown.length === 0) {
      toast.error('没有可复制的日志');
      return;
    }
    const text = shown.map((p) => p.raw).join('\n');
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
      } else {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        ta.remove();
      }
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
      toast.success(`已复制 ${shown.length} 条日志`);
    } catch {
      toast.error('复制失败（浏览器不允许访问剪贴板）');
    }
  };

  const load = useCallback(async (n: number) => {
    setLoading(true);
    setErr('');
    try {
      const r = await apiFetch(apiUrl('/api/logs?lines=' + n));
      // 先按文本读，再解析——网关在会话令牌过期时会返回纯文本 "invalid token"
      //（非 JSON），直接 r.json() 会抛 "Unexpected token 'i'..." 这种原始报错。
      const text = await r.text();
      let body: LogData | null = null;
      try {
        body = JSON.parse(text) as LogData;
      } catch {
        const raw = (text || '').trim().slice(0, 120);
        if (/invalid\s*token|unauthoriz|login|session|expired|登录|令牌|会话/i.test(raw)) {
          throw new Error('登录会话可能已过期（网关返回：' + (raw || 'invalid token') + '）。请刷新 Moo 页面后重试。');
        }
        throw new Error('日志响应不是有效数据' + (raw ? '（' + raw + '）' : '，请刷新页面重试'));
      }
      if (!r.ok) throw new Error((body as { error?: string } | null)?.error || body?.note || '日志加载失败（HTTP ' + r.status + '）');
      if (!Array.isArray(body?.lines)) throw new Error('日志响应缺少数据，请刷新页面重试');
      setData(body);
      setExpanded(new Set());
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { if (countReady) void load(count); }, [count, load, countReady]);

  // 解析 + 倒序（文件内旧→新，倒序后最新在上）
  const parsed = useMemo(() => {
    if (!data) return [] as Parsed[];
    const all = data.lines.map(parseLine);
    all.reverse();
    return all;
  }, [data]);

  const counts = useMemo(() => {
    const c: Record<Level, number> = { fatal: 0, error: 0, warn: 0, info: 0, debug: 0 };
    for (const p of parsed) c[p.level]++;
    return c;
  }, [parsed]);

  // 0.6.343：级别筛选 + 关键词过滤（模块 / 应用名 / URL 片段，大小写不敏感）
  const shown = useMemo(() => {
    const base = filter === 'all'
      ? parsed
      : filter === 'problem'
        ? parsed.filter((p) => p.level === 'fatal' || p.level === 'error' || p.level === 'warn')
        : parsed.filter((p) => p.level === filter);
    const kw = q.trim().toLowerCase();
    if (!kw) return base;
    return base.filter((p) => p.detail.toLowerCase().includes(kw) || p.mod.toLowerCase().includes(kw));
  }, [parsed, filter, q]);

  // 0.6.344：量一次「哪些行的详情被裁掉」——窗口缩放（ResizeObserver）或换行数时重算。
  // 只对折叠态的文本节点打 data-logtext，展开的行不参与测量。
  useEffect(() => {
    const box = listRef.current;
    if (!box) return;
    const measure = () => {
      const next = new Set<number>();
      box.querySelectorAll<HTMLElement>('[data-logtext]').forEach((node) => {
        const idx = Number(node.dataset.logtext);
        if (Number.isFinite(idx) && node.scrollWidth > node.clientWidth + 1) next.add(idx);
      });
      setOverflow((prev) => (prev.size === next.size && [...next].every((v) => prev.has(v)) ? prev : next));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(box);
    return () => ro.disconnect();
  }, [shown]);

  const toggleExpand = (i: number) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(i)) next.delete(i);
      else next.add(i);
      return next;
    });

  const fmtSize = (n: number) =>
    n > 1048576 ? (n / 1048576).toFixed(1) + ' MB' : Math.max(1, Math.round(n / 1024)) + ' KB';

  return (
    <div className="px-3 py-4 sm:px-6 sm:py-5 space-y-4">
      <div className="bg-card/55 backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore px-4 py-4 space-y-3">
        {/* 0.6.293（用户定稿）：折行方案多占一行 → 恢复 0.6.283 单行布局
            （标题左 + 控件右，justify-between）；弹出同宽修复保留 */}
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2 text-sm font-semibold min-w-0">
            <FileText className="h-4 w-4 shrink-0" />
            <span className="truncate">应用日志<span className="hidden sm:inline">（moo.log）</span></span>
          </div>
          <div className="flex items-center gap-2 shrink-0">
            <Select value={String(count)} onValueChange={handleCountChange}>
              <SelectTrigger className="h-9 w-[104px] text-sm"><SelectValue /></SelectTrigger>
              {/* 0.6.288：align=start 左缘对齐；0.6.292：面板宽度锁定=触发框宽
                  （104px），不再出现弹出框比选择框宽的不齐平（用户实报）。
                  Radix popper 模式注入 --radix-select-trigger-width */}
              <SelectContent align="start" className="w-[var(--radix-select-trigger-width)] min-w-0">
                <SelectItem value="50">50 行</SelectItem>
                <SelectItem value="100">100 行</SelectItem>
                <SelectItem value="200">200 行</SelectItem>
                <SelectItem value="500">500 行</SelectItem>
                <SelectItem value="1000">1000 行</SelectItem>
              </SelectContent>
            </Select>
            <Button variant="outline" size="sm" onClick={() => void handleCopy()} disabled={!data || shown.length === 0}
              title={filter === 'all' ? '复制当前显示的日志' : '复制当前筛选出的日志'}>
              {copied ? <Check className="h-4 w-4 text-primary" /> : <Copy className="h-4 w-4" />}
            </Button>
            <Button variant="outline" size="sm" onClick={() => void load(count)} disabled={loading}>
              {loading ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
            </Button>
          </div>
        </div>
        {data && (
          <p className="text-xs text-muted-foreground break-all">
            文件 {data.file} ｜ {fmtSize(data.size)} ｜ 最新 {data.returned} 行{data.archives.length > 0 ? ' ｜ 归档 ' + data.archives.join('、') : ''}
          </p>
        )}
        {/* 0.6.343：关键词搜索（长日志里定位某应用/某次失败的最快路径） */}
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="搜索关键词 / 应用名 / URL"
            spellCheck={false}
            aria-label="搜索日志"
            className="h-9 pl-8 pr-8 text-sm"
          />
          {q && (
            <button
              type="button"
              onClick={() => setQ('')}
              aria-label="清除搜索"
              className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded-full p-1 text-muted-foreground hover:text-foreground">
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>
        {/* 级别图例 + 点击过滤（再点一次取消） */}
        <div className="flex flex-wrap items-center gap-1.5">
          <button
            onClick={() => setFilter('all')}
            className={`rounded-full border px-2.5 py-0.5 text-[11px] transition-colors ${filter === 'all' ? 'border-primary bg-primary/10 text-primary' : 'border-border text-muted-foreground hover:text-foreground'}`}>
            全部 {q ? `${shown.length}/${parsed.length}` : parsed.length}
          </button>
          {/* 0.6.343：一键只看异常（FATAL/ERROR/WARN）——排障时最常用的一屏 */}
          <button
            onClick={() => setFilter(filter === 'problem' ? 'all' : 'problem')}
            className={`rounded-full border px-2.5 py-0.5 text-[11px] transition-colors ${filter === 'problem' ? 'border-primary bg-primary/10 text-primary' : 'border-border text-muted-foreground hover:text-foreground'}`}>
            异常 {counts.fatal + counts.error + counts.warn}
          </button>
          {LEVEL_ORDER.map((lv) => (
            <button
              key={lv}
              onClick={() => setFilter(filter === lv ? 'all' : lv)}
              className={`flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-[11px] transition-colors ${filter === lv ? 'border-primary bg-primary/10' : 'border-border hover:text-foreground'}`}>
              <span className={`h-2 w-2 rounded-full ${LEVELS[lv].dot}`} />
              <span className={LEVELS[lv].text}>{LEVELS[lv].label}</span>
              <span className="text-muted-foreground">{counts[lv]}</span>
            </button>
          ))}
        </div>
        {err && (
          <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            <span>{err}</span>
          </div>
        )}
        {data?.note && !err && <p className="text-xs text-muted-foreground">{data.note}</p>}
      </div>
      {/* 日志列表（0.6.344，用户令）：对齐 Dog-dev 日志页的观感——
          等宽字体、深色代码面板、无分隔线的连续行，每条日志固定一行：
          时间｜级别｜详情；详情被裁掉时行尾出现展开/收起按钮（展开后整段换行显示）。
          此前「桌面三列 + 移动两行」的换行排版会让一条长日志占掉半屏。 */}
      <div className="overflow-hidden bg-card/55 backdrop-blur-xl rounded-[18px] border border-white/10 shadow-appstore">
        {/* 0.6.345（用户令）：右侧滑条不要——列表照常可用滚轮/触控板/手指滚动，
            但不渲染滚动条（.no-scrollbar，与 Dog-dev 日志页同为 0 宽滚动条，不再吃掉 10px）；
            深色代码面板改为整卡全出血（去掉外层 12px 内缩），日志正文铺满卡片宽度。 */}
        <div className="max-h-[60vh] overflow-auto no-scrollbar bg-zinc-950/[0.045] dark:bg-black/35">
          <div ref={listRef} className="px-1.5 py-1 sm:px-2 sm:py-1.5">
            {loading && !data ? (
              <div className="p-4 text-muted-foreground">加载中…</div>
            ) : shown.length === 0 ? (
              <div className="p-4 text-muted-foreground">（暂无日志）</div>
            ) : (
              shown.map((p, i) => {
                const col = collapseDetail(p.detail);
                const open = expanded.has(i);
                const body = open || !col.truncated ? p.detail : col.text;
                // 折叠摘要可能本身就短（iconfail 计数摘要），这种行仍要能展开看原文。
                // open 必须算进来：展开后该行不再参与溢出测量，否则按钮会消失、无法收起。
                const canExpand = col.truncated || open || overflow.has(i);
                return (
                  <div
                    key={i}
                    data-logrow={i}
                    className={'flex items-start gap-2 rounded-md px-1.5 py-[2px] font-mono flex-wrap sm:flex-nowrap text-[11px] leading-[1.7] sm:text-xs sm:leading-[1.75] hover:bg-black/[0.035] dark:hover:bg-white/[0.05] ' + (LEVELS[p.level].row || '')}>
                    <span
                      data-logtime={i}
                      className={TIME_W + ' shrink-0 whitespace-nowrap tabular-nums ' + (p.timeShort ? 'text-muted-foreground/75' : 'text-muted-foreground/40')}
                      title={p.time || '该行无时间戳（生命周期脚本输出）'}>
                      <span className="sm:hidden">{p.timeClock || '--:--:--'}</span>
                      <span className="hidden sm:inline">{p.timeShort || p.time || '--:--:--'}</span>
                    </span>
                    <span data-loglevel={i} className={LEVEL_W + ' shrink-0 font-semibold uppercase ' + LEVELS[p.level].text}>{LEVELS[p.level].label}</span>
                    <span
                      data-logmsg={i}
                      className={(open
                        ? 'order-4 mt-0.5 w-full min-w-0 sm:order-none sm:mt-0 sm:w-auto sm:flex-1 '
                        : 'min-w-0 flex-1 ') + (LEVELS[p.level].msg || '')}>
                      {open ? (
                        <span className="whitespace-pre-wrap break-all">
                          {p.mod ? <span className="text-muted-foreground">[{p.mod}] </span> : null}
                          {p.detail || '（无内容）'}
                        </span>
                      ) : (
                        <span data-logtext={i} className="block truncate">
                          {p.mod ? <span className="text-muted-foreground">[{p.mod}] </span> : null}
                          {body || '（无内容）'}
                        </span>
                      )}
                    </span>
                    {canExpand && (
                      <button
                        type="button"
                        data-logtoggle={i}
                        onClick={() => toggleExpand(i)}
                        title={open ? '收起' : `展开（${p.detail.length} 字）`}
                        aria-label={open ? '收起日志' : '展开日志'}
                        className={'mt-[1px] -mr-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded text-muted-foreground/50 transition-colors hover:bg-primary/10 hover:text-primary' + (open ? ' order-3 sm:order-none' : '')}>
                        {open ? <ChevronUp className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
                      </button>
                    )}
                  </div>
                );
              })
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
