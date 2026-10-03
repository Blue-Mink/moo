import { useState, useEffect, useCallback, useMemo } from 'react';
import { apiUrl } from '../api/base';
import { apiFetch, fetchSettings, updateSettings } from '../api/client';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { FileText, Loader2, RefreshCw, AlertTriangle, Copy, Check } from 'lucide-react';
import { toast } from 'sonner';

interface LogData {
  file: string;
  size: number;
  total: number;
  returned: number;
  lines: string[];
  archives: string[];
  note?: string;
}

type Level = 'fatal' | 'error' | 'warn' | 'info' | 'debug';

// moo.log 无显式 level 字段（格式 `YYYY/MM/DD HH:MM:SS.μs [module] message`），
// 前端按内容关键词 + module 启发式分级。级别 → 颜色/文案。
const LEVELS: Record<Level, { label: string; dot: string; text: string; row?: string }> = {
  fatal: { label: 'FATAL', dot: 'bg-red-600', text: 'text-red-500 dark:text-red-400 font-semibold', row: 'bg-red-500/5' },
  error: { label: 'ERROR', dot: 'bg-red-500', text: 'text-red-500 dark:text-red-400', row: 'bg-red-500/[0.03]' },
  warn:  { label: 'WARN',  dot: 'bg-amber-500', text: 'text-amber-600 dark:text-amber-400' },
  info:  { label: 'INFO',  dot: 'bg-sky-500', text: 'text-sky-600 dark:text-sky-400' },
  debug: { label: 'DEBUG', dot: 'bg-zinc-400', text: 'text-zinc-500 dark:text-zinc-500' },
};
const LEVEL_ORDER: Level[] = ['fatal', 'error', 'warn', 'info', 'debug'];

const FATAL_RE = /\b(panic|fatal|segmentation|core dump|out of memory)\b/i;
const ERROR_RE = /\b(error|errors|failed|failure|exception|errno|refused|denied|unauthoriz|forbidden|timeout|timed out|invalid|not found|no such|unreachable|connection reset|bad gateway|service unavailable)\b/i;
const WARN_RE = /\b(warn|warning|deprecat|skip|skipped|fallback|conflict|retry|retried|retrying|throttl|rate limit|discarded|dropped|ignored|degraded)\b/i;
const DEBUG_RE = /\b(debug|trace|verbose)\b/i;
const FATAL_ZH = /崩溃|致命|内存溢出|panic/;
const ERROR_ZH = /错误|失败|异常|超时|拒绝|无法|不能|非法|无效|不存在|限流|丢失|断连|不可达|panic/;
const WARN_ZH = /警告|降级|回退|跳过|重试|冲突|忽略|丢弃|不完整/;
const DEBUG_ZH = /调试|跟踪/;
// 高频/低价值模块 → 归 DEBUG（避免刷屏行被误判成 info/error，如 [race] 的 404 候选）
const VERBOSE_MODS = new Set(['race', 'probe', 'prober', 'http', 'httpdb', 'mirror', 'mirrors', 'fetch', 'download', 'warm', 'cache', 'sync', 'netguard', 'netx', 'apiscope', 'readme-warm', 'readme']);

interface Parsed { time: string; level: Level; mod: string; detail: string; raw: string; }

function parseLine(line: string): Parsed {
  let time = '';
  let rest = line;
  const tm = line.match(/^(\d{4}\/\d{2}\/\d{2}\s+\d{2}:\d{2}:\d{2})(?:\.\d+)?\s*(.*)$/);
  if (tm) { time = tm[1].replace(/\//g, '-'); rest = tm[2]; }
  let mod = '';
  const mm = rest.match(/^\[([^\]]+)\]\s*/);
  if (mm) { mod = mm[1]; rest = rest.slice(mm[0].length); }
  const detail = rest.trim();
  const ml = mod.toLowerCase();
  // 关键：判级前先剔除「0 失败 / 0 个失败 / 失败 0」这类"零失败"片段，
  // 否则 "156 成功 / 0 失败" 这种成功摘要会被 "失败" 字样误判成 ERROR。
  // 真实的 "3 失败" / "同步失败" 不含 "0 失败"，不受影响，仍会判 error。
  const probe = detail.replace(/0\s*个?\s*失败|失败\s*[:：]?\s*0(?!\d)/g, '');
  let level: Level = 'info';
  if (FATAL_RE.test(detail) || FATAL_ZH.test(detail)) level = 'fatal';
  else if (ERROR_RE.test(probe) || ERROR_ZH.test(probe)) level = 'error';
  else if (WARN_RE.test(probe) || WARN_ZH.test(probe)) level = 'warn';
  else if (DEBUG_RE.test(probe) || DEBUG_ZH.test(probe) || VERBOSE_MODS.has(ml)) level = 'debug';
  return { time, level, mod, detail, raw: line };
}

const TIME_W = 'w-[128px]';
const LEVEL_W = 'w-[64px]';

export default function LogSettingsTab() {
  const [data, setData] = useState<LogData | null>(null);
  const [count, setCount] = useState(200);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState('');
  const [filter, setFilter] = useState<Level | 'all'>('all');
  const [copied, setCopied] = useState(false);
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

  const shown = filter === 'all' ? parsed : parsed.filter((p) => p.level === filter);

  const fmtSize = (n: number) =>
    n > 1048576 ? (n / 1048576).toFixed(1) + ' MB' : Math.max(1, Math.round(n / 1024)) + ' KB';

  return (
    <div className="px-3 py-4 sm:px-6 sm:py-5 space-y-4">
      <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-4 space-y-3">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2 text-sm font-semibold">
            <FileText className="h-4 w-4" />
            <span>应用日志（moo.log）</span>
          </div>
          <div className="flex items-center gap-2">
            <Select value={String(count)} onValueChange={handleCountChange}>
              <SelectTrigger className="h-9 w-[104px] text-sm"><SelectValue /></SelectTrigger>
              <SelectContent>
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
        {/* 级别图例 + 点击过滤（再点一次取消） */}
        <div className="flex flex-wrap items-center gap-1.5">
          <button
            onClick={() => setFilter('all')}
            className={`rounded-full border px-2.5 py-0.5 text-[11px] transition-colors ${filter === 'all' ? 'border-primary bg-primary/10 text-primary' : 'border-border text-muted-foreground hover:text-foreground'}`}>
            全部 {parsed.length}
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
      {/* 日志列表：三列 时间｜级别｜详情（gap-3 = 中间空一格），最新在上 */}
      <div className="overflow-hidden bg-card rounded-[18px] border border-border/20 shadow-appstore">
        <div className="flex items-center gap-3 border-b bg-muted/40 px-3 py-1.5 font-mono text-[10px] uppercase tracking-wide text-muted-foreground">
          <span className={TIME_W + ' shrink-0'}>时间</span>
          <span className={LEVEL_W + ' shrink-0'}>级别</span>
          <span className="min-w-0">日志详情</span>
        </div>
        <div className="max-h-[60vh] overflow-auto font-mono text-xs leading-relaxed">
          {loading && !data ? (
            <div className="p-4 text-muted-foreground">加载中…</div>
          ) : shown.length === 0 ? (
            <div className="p-4 text-muted-foreground">（暂无日志）</div>
          ) : (
            shown.map((p, i) => (
              <div key={i} className={`flex items-start gap-3 px-3 py-0.5 ${LEVELS[p.level].row || ''}`}>
                <span className={TIME_W + ' shrink-0 tabular-nums text-muted-foreground'}>{p.time || '—'}</span>
                <span className={LEVEL_W + ' shrink-0 ' + LEVELS[p.level].text}>{LEVELS[p.level].label}</span>
                <span className="min-w-0 break-words">
                  {p.mod ? <span className="text-muted-foreground">[{p.mod}] </span> : null}
                  <span>{p.detail || '（无内容）'}</span>
                </span>
              </div>
            ))
          )}
        </div>
      </div>
    </div>
  );
}
