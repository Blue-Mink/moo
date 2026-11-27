// 日志行解析与折叠摘要（纯函数，可单测）。
// 0.6.343：从 LogSettingsTab.tsx 抽出——判级规则是日志页可读性的核心，
// 需要有回归测试（尤其 iconfail 压缩摘要不能被「失败」字样误判成 ERROR）。

export type Level = 'fatal' | 'error' | 'warn' | 'info' | 'debug';

export const LEVEL_ORDER: Level[] = ['fatal', 'error', 'warn', 'info', 'debug'];

const FATAL_RE = /\b(panic|fatal|segmentation|core dump|out of memory)\b/i;
const ERROR_RE = /\b(error|errors|failed|failure|exception|errno|refused|denied|unauthoriz|forbidden|timeout|timed out|invalid|not found|no such|unreachable|connection reset|bad gateway|service unavailable)\b/i;
const WARN_RE = /\b(warn|warning|deprecat|skip|skipped|fallback|conflict|retry|retried|retrying|throttl|rate limit|discarded|dropped|ignored|degraded)\b/i;
const DEBUG_RE = /\b(debug|trace|verbose)\b/i;
const FATAL_ZH = /崩溃|致命|内存溢出|panic/;
const ERROR_ZH = /错误|失败|异常|超时|拒绝|无法|不能|非法|无效|不存在|限流|丢失|断连|不可达|panic/;

// 每轮镜像探测摘要行：`[mirror] gh 探测 12 源 ok=8 fail=4 limited=0 优选=… 耗时=20s（异常：…）`。
// 2026-11-26（用户实报「这些为什么会是错误日志，误导用户」）：部分镜像超时/403 属于
// **常态波动**（地域、限流、并发），不代表 Moo 出故障——文案里的「异常/超时/拒绝」会命中
// 下面的 ERROR 关键词，把一条观测指标刷成红色。规则：每轮摘要判 info；只有「本轮全挂」
// （ok=0）才升 warn（另有 mirrorsAllFailedCheck 的专门通知兜底）。
const MIRROR_ROUND_RE = /探测\s*\d+\s*源\s+ok=\d+/; // 行首可能带组名（gh / dk）

// 源列表自动同步的每轮摘要：`[list-sync] 本轮完成：列表 105 条 / 已存在 105 / 新增 0 / 失败 0 / 耗时 437ms`。
// 同类问题（2026-11-26）：这条是**例行进度**，失败数 >0 也只是一次重试即可恢复的波动，
// 不该按 ERROR 刷红；判 info（失败 0）/ warn（有失败），单条「跳过 <URL>」仍按原因判级。
const LISTSYNC_ROUND_RE = /^本轮完成[:：]/;
const WARN_ZH = /警告|降级|回退|跳过|重试|冲突|忽略|丢弃|不完整/;
const DEBUG_ZH = /调试|跟踪/;

// 高频/低价值模块 → 归 DEBUG（避免刷屏行被误判成 info/error，如 [race] 的 404 候选）
const VERBOSE_MODS = new Set(['race', 'probe', 'prober', 'http', 'httpdb', 'mirror', 'mirrors',
  'fetch', 'download', 'warm', 'cache', 'sync', 'netguard', 'netx', 'apiscope', 'readme-warm', 'readme']);

export interface Parsed {
  time: string;
  timeShort: string;
  /** 0.6.344：仅 HH:MM:SS——移动端单行三列（时间｜级别｜详情）用，省出宽度给详情。 */
  timeClock: string;
  level: Level;
  mod: string;
  detail: string;
  raw: string;
}

export function parseLine(line: string): Parsed {
  let time = '';
  let timeShort = '';
  let timeClock = '';
  let rest = line;
  const tm = line.match(/^(\d{4})\/(\d{2})\/(\d{2})\s+(\d{2}:\d{2}:\d{2})(?:\.\d+)?\s*(.*)$/);
  if (tm) {
    time = tm[1] + '-' + tm[2] + '-' + tm[3] + ' ' + tm[4];
    // 0.6.268：列表内用短格式 MM-DD HH:MM:SS（省出 ~30% 宽度给详情），
    // 完整时间戳放 hover title。
    timeShort = tm[2] + '-' + tm[3] + ' ' + tm[4];
    timeClock = tm[4];
    rest = tm[5];
  }
  let mod = '';
  const mm = rest.match(/^\[([^\]]+)\]\s*/);
  if (mm) {
    mod = mm[1];
    rest = rest.slice(mm[0].length);
  }
  const detail = rest.trim();
  const ml = mod.toLowerCase();
  // 判级前剔除计数片段：
  //  a) 「0 失败 / 失败 0」这类"零失败"→ 否则 "156 成功 / 0 失败" 被判 ERROR
  //  b) 0.6.343 [iconfail] 压缩摘要的「确定缺失 6 / 其它 18」→ 那是计数不是事件
  const probe = detail
    .replace(/0\s*个?\s*失败|失败\s*[:：]?\s*0(?!\d)/g, '')
    .replace(/确定缺失\s*\d+|网络失败\s*\d+|其它\s*\d+/g, '');
  const mirrorRound = ml === 'mirror' && MIRROR_ROUND_RE.test(detail);
  const listSyncRound = ml === 'list-sync' && LISTSYNC_ROUND_RE.test(detail);
  let level: Level = 'info';
  if (FATAL_RE.test(detail) || FATAL_ZH.test(detail)) level = 'fatal';
  else if (mirrorRound) {
    // 阈值化（2026-11-26 批次 C）：全挂 / 有限流 / 成功率不足一半 → warn，
    // 其余（部分镜像超时、个别 403）是常态波动 → info。
    const m = detail.match(/ok=(\d+)\s+fail=(\d+)(?:\s+limited=(\d+))?/);
    const ok = m ? Number(m[1]) : 1;
    const fail = m ? Number(m[2]) : 0;
    const limited = m && m[3] ? Number(m[3]) : 0;
    const total = ok + fail;
    level = ok === 0 || limited > 0 || (total > 0 && ok * 2 < total) ? 'warn' : 'info';
  }
  else if (listSyncRound) level = /失败\s*[:：]?\s*0(?!\d)/.test(detail) ? 'info' : 'warn';
  else if (ERROR_RE.test(probe) || ERROR_ZH.test(probe)) level = 'error';
  else if (WARN_RE.test(probe) || WARN_ZH.test(probe)) level = 'warn';
  else if (DEBUG_RE.test(probe) || DEBUG_ZH.test(probe) || VERBOSE_MODS.has(ml)) level = 'debug';
  return { time, timeShort, timeClock, level, mod, detail, raw: line };
}

// 0.6.343（用户报「日志页面很乱」）：超长行折叠。
// 实测 200 行里 96 行 >200 字符，[iconfail] 单行 3.1~4.1KB（24 个候选 URL），
// 页面滚动高 23.5k px，一屏只看得到 1 条日志。
export const COLLAPSE_CHARS = 180;
const CAND_MARK = '候选=[';

export interface Collapsed {
  text: string;
  truncated: boolean;
}

export function collapseDetail(detail: string): Collapsed {
  if (!detail) return { text: '', truncated: false };
  const at = detail.indexOf(CAND_MARK);
  if (at >= 0) {
    const head = detail.slice(0, at).trim();
    const body = detail.slice(at);
    const total = (body.match(/https?:\/\//g) || []).length;
    const miss = (body.match(/miss=true/g) || []).length;
    const headShort = head.length > 96 ? head.slice(0, 96) + '…' : head;
    return {
      text: `${headShort} 候选 ${total} 个（404 ${miss} / 其它 ${Math.max(0, total - miss)}）`,
      truncated: true,
    };
  }
  if (detail.length > COLLAPSE_CHARS) {
    return { text: detail.slice(0, COLLAPSE_CHARS) + '…', truncated: true };
  }
  return { text: detail, truncated: false };
}
