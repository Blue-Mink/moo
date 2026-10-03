import React, { useState, useEffect, useRef } from 'react';
import { fetchTasks, recordNotifyEvent, type BackgroundTask } from '../api/client';
import { CheckCircle2, XCircle } from 'lucide-react'

// BackgroundTasksIndicator：全局后台任务完成通知（顶部，单条轮播）。
//
// 用户定稿（0.6.118 → 0.6.263 修订）：顶部通知栏**只提示成功结果**（失败不进
// 顶部：行内错误 toast / 下载红色失败行已有反馈），不显示实时进度——
// 实时进度只在列表行内展示（AppRowList：实时百分比 + 阶段文字快闪）。
//
// 轮询 GET /api/tasks（每 3s），只收「状态翻转到终态（done/failed）」的
// 任务，逐条闪现约 4 秒后收起，下一条再顶上——同一时刻最多 1 条
// （对齐 fn-knock/sonner 通知样式，绝不竖排堆叠挡视野）。
//
// 两条历史坑的修复（保留）：
// 1) 「退出 Moo 再进通知还在」：后端任务列表会累积 done/failed 终态
//    条目，若对「列表变化」整体重弹，每次重挂载都会把旧完成重弹一遍。
//    → 首轮轮询只建基线（按任务 ID）不通知；之后只通知 diff。
// 2) 「多条通知上下竖排很挡视野」：多个任务同轮完成时整列渲染很挡视野。
//    → 改为 FIFO 队列逐条展示，其余排队并显示「还有 N 条」。
// 终态：长操作后端归一为 done/failed；下载任务失败是 "error"。
const isFinished = (s: string) => s === 'done' || s === 'failed' || s === 'error';
// 只对「重操作」报结果：安装/更新/卸载/下载。start/stop 是秒级操作，
// 行内状态已即时反映（运行中/已停用），顶部再弹「启动成功」属冗余噪音。
const NOTIFY_OPS = new Set(['install', 'update', 'uninstall', 'download']);
const shouldNotify = (op: string) => NOTIFY_OPS.has(op);
const opLabel = (op: string) =>
  op === 'update' ? '更新' : op === 'uninstall' ? '卸载'
  : op === 'download' ? '下载' : '安装';
// 任务 (op, 成功?) → 通知事件 key（设置页「通知规则」的 10 类事件之一）。
// 0.6.120：被用户在通知设置里关掉的事件不弹也不入记录。
const eventKey = (op: string, ok: boolean) => {
  const e =
    op === 'update' ? 'update' : op === 'uninstall' ? 'uninstall'
    : op === 'download' ? 'download' : 'install';
  return ok ? `${e}_${e === 'download' ? 'done' : 'success'}` : `${e}_error`;
};

const BackgroundTasksIndicator: React.FC = () => {
  // 渲染状态：当前展示的任务（最新数据）+ 排队条数
  const [render, setRender] = useState<{ t: BackgroundTask | null; queued: number }>({ t: null, queued: 0 });
  // 展示状态机（ref 驱动，轮询内同步推进，避免 setState 时序竞态）
  const stRef = useRef<{ currentId: string | null; queue: string[]; expireAt: number }>({
    currentId: null,
    queue: [],
    expireAt: 0,
  });
  // 基线：任务 ID（或 appname:op）→ status。首轮建立；之后每轮更新。
  const baseRef = useRef<Map<string, string> | null>(null);

  useEffect(() => {
    let cancelled = false;
    const keyOf = (t: BackgroundTask) => t.id || `${t.appname}:${t.op}`;
    const poll = async () => {
      try {
        const list = await fetchTasks();
        if (cancelled) return;
        const st = stRef.current;
        const now = Date.now();
        // 1) 当前条展示到期 → 收起
        if (st.currentId && now >= st.expireAt) {
          st.currentId = null;
          st.expireAt = 0;
        }
        // 2) diff：首轮只建基线；之后只收「状态翻转到终态（done/failed）」——
        //    进行中的任务不通知（实时进度在列表行内展示，顶部只报结果）
        const base = baseRef.current;
        if (base === null) {
          baseRef.current = new Map(list.map((t) => [keyOf(t), t.status] as const));
        } else {
          for (const t of list) {
            const prev = base.get(keyOf(t));
            if (shouldNotify(t.op) && isFinished(t.status) && (prev === undefined || prev !== t.status)) {
              const k = keyOf(t);
              if (st.currentId === k) continue;
              // 0.6.126：应用内顶部通知栏始终开启，不受任何开关控制
              //（总开关 / 事件开关只管理外部渠道推送；基线照常更新）。
              const ok = t.status === 'done';
              // 0.6.263 用户定稿：顶部通知栏只在成功时显示；失败不再进顶部
              //（前台有行内错误 toast+「上报」，下载失败有红色失败行）。
              // 通知记录（notify-log）两种结果都照常落盘。
              if (ok && !st.queue.includes(k)) st.queue.push(k);
              // 上报通知记录（fire-and-forget；后端必落盘，外部 fan-out 按开关）
              const msg = `${t.appname} ${opLabel(t.op)}${ok ? '成功' : '失败'}`;
              void recordNotifyEvent(eventKey(t.op, ok), msg, ok);
            }
          }
          baseRef.current = new Map(list.map((t) => [keyOf(t), t.status] as const));
        }
        // 3) 从队列顶上下一条（约 4 秒一条）——必须在 diff 之后，
        //    否则本轮新入队的任务要等下一轮（3s）才显示
        if (!st.currentId && st.queue.length > 0) {
          st.currentId = st.queue.shift()!;
          st.expireAt = now + 4000;
        }
        // 4) 渲染：当前条取列表里该任务的最新数据（进度/状态实时）
        let cur: BackgroundTask | null = null;
        if (st.currentId) {
          cur = list.find((t) => keyOf(t) === st.currentId) || null;
          if (!cur) { st.currentId = null; st.expireAt = 0; }
        }
        setRender({ t: cur, queued: st.queue.length });
      } catch {
        // 忽略轮询错误（后端短暂不可用等），下一轮再试
      }
    };
    poll();
    const timer = setInterval(poll, 3000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, []);

  const t = render.t;
  if (!t) return null;

  // 现在只会排入终态任务（done/failed/error）；防御性兜底：非终态不渲染
  if (!isFinished(t.status)) return null;

  const ok = t.status === 'done';

  return (
    <div className="fixed top-14 left-1/2 z-50 -translate-x-1/2 w-[calc(100%-2rem)] max-w-sm">
      {/* 同一时刻最多 1 条（队列其余的排队等待，不竖排堆叠） */}
      <div className="rounded-2xl border border-border/20 bg-card/95 backdrop-blur px-3.5 py-2.5 shadow-appstore">
        <div className="flex items-center gap-2">
          {ok ? (
            <CheckCircle2 className="h-4 w-4 text-green-500 shrink-0" />
          ) : (
            <XCircle className="h-4 w-4 text-red-500 shrink-0" />
          )}
          <span className="text-sm font-medium truncate">
            {t.appname} {opLabel(t.op)}{ok ? '成功' : '失败'}
          </span>
          {render.queued > 0 && (
            <span className="text-xs text-muted-foreground shrink-0">· 还有 {render.queued} 条</span>
          )}
        </div>
        {!ok && t.message && (
          <div className="mt-1 text-xs text-muted-foreground truncate">{t.message}</div>
        )}
      </div>
    </div>
  );
};

export default BackgroundTasksIndicator;
