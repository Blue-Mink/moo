// 0.6.548：自动更新「允许自动停用的应用」白名单勾选弹窗
// 数据源复用 client.ts 现有的 fetchApps()，只列已安装应用；不预勾任何应用。
import { useEffect, useMemo, useState } from 'react';
import { fetchApps, fetchAutoUpdateDepMap, type AppInfo } from '../api/client';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Loader2, Search } from 'lucide-react';
import { cn } from '@/lib/utils';

interface DepAllowPickerDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 当前已保存的名单（daemon appName） */
  value: string[];
  /** 保存回调：父组件负责写入 settings 并纳入脏态比较 */
  onSave: (next: string[]) => void;
}

export default function DepAllowPickerDialog({ open, onOpenChange, value, onSave }: DepAllowPickerDialogProps) {
  const [apps, setApps] = useState<AppInfo[]>([]);
  const [loading, setLoading] = useState(false);
  const [keyword, setKeyword] = useState('');
  const [draft, setDraft] = useState<string[]>([]);
  // 0.6.549：应用 → 依赖的运行时（用于在应用名下方标注「依赖 nodejs_v24」）
  const [depMap, setDepMap] = useState<Record<string, string[]>>({});

  // 每次打开都从「已保存的名单」重新开始，不保留上次未保存的勾选
  useEffect(() => {
    if (!open) return;
    setDraft(value);
    setKeyword('');
    setLoading(true);
    let alive = true;
    fetchApps()
      .then((res) => { if (alive) setApps(res.apps || []); })
      .catch(() => { if (alive) setApps([]); })
      .finally(() => { if (alive) setLoading(false); });
    // 依赖映射单独拉（失败不影响列表，只是不显示依赖标注）
    fetchAutoUpdateDepMap()
      .then((m) => { if (alive) setDepMap(m.apps || {}); })
      .catch(() => { if (alive) setDepMap({}); });
    return () => { alive = false; };
  }, [open, value]);

  const installed = useMemo(() => apps.filter((a) => a.installed), [apps]);
  const installedNames = useMemo(() => new Set(installed.map((a) => a.appname)), [installed]);
  // 名单里但已卸载的 appname：灰显 + 「已卸载」，可移除
  const orphans = useMemo(() => draft.filter((n) => !installedNames.has(n)), [draft, installedNames]);

  const kw = keyword.trim().toLowerCase();
  const visible = useMemo(() => {
    const list = installed
      .map((a) => ({
        appname: a.appname,
        display_name: a.display_name || a.appname,
        runtimes: depMap[a.appname] || [],
      }))
      // 0.6.549（用户令）：有依赖的应用置顶，一眼看出哪些需要留意
      .sort((a, b) => {
        if ((a.runtimes.length > 0) !== (b.runtimes.length > 0)) return a.runtimes.length > 0 ? -1 : 1;
        return a.display_name.localeCompare(b.display_name, 'zh-Hans-CN');
      });
    if (!kw) return list;
    return list.filter(
      (a) => a.display_name.toLowerCase().includes(kw) || a.appname.toLowerCase().includes(kw),
    );
  }, [installed, kw, depMap]);

  const toggle = (appname: string) => {
    setDraft((prev) => (prev.includes(appname) ? prev.filter((n) => n !== appname) : [...prev, appname]));
  };

  // 0.6.548：全选只作用于「当前搜索可见」的应用
  const selectVisible = () => {
    setDraft((prev) => {
      const next = new Set(prev);
      visible.forEach((a) => next.add(a.appname));
      return Array.from(next);
    });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[calc(100vw-1.5rem)] sm:max-w-md">
        <DialogHeader>
          <DialogTitle>允许自动停用的应用</DialogTitle>
          <DialogDescription>
            只有全部依赖方都在名单里才会自动停用，否则整体跳过。
          </DialogDescription>
        </DialogHeader>
        <div className="relative">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            placeholder="搜索应用名或包名"
            className="h-9 pl-9 text-xs"
          />
        </div>

        <div className="-mx-1 max-h-[46vh] space-y-1 overflow-y-auto px-1">
          {loading ? (
            <div className="flex items-center justify-center gap-2 py-8 text-xs text-muted-foreground">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              加载已安装应用…
            </div>
          ) : (
            <>
              {orphans.map((name) => (
                <div
                  key={`orphan-${name}`}
                  className="flex items-center justify-between gap-2 rounded-lg border border-white/10 px-2.5 py-2"
                >
                  <div className="min-w-0">
                    <div className="truncate text-xs text-muted-foreground line-through">{name}</div>
                    <div className="truncate text-[11px] text-muted-foreground/70">已卸载</div>
                  </div>
                  <button
                    type="button"
                    onClick={() => setDraft((prev) => prev.filter((n) => n !== name))}
                    className="shrink-0 text-[11px] text-destructive hover:underline"
                  >
                    移除
                  </button>
                </div>
              ))}
              {visible.length === 0 ? (
                <div className="py-8 text-center text-xs text-muted-foreground">
                  {installed.length === 0 ? '当前没有已安装的应用' : '没有匹配的应用'}
                </div>
              ) : (
                visible.map((app) => {
                  const checked = draft.includes(app.appname);
                  return (
                    <label
                      key={app.appname}
                      className={cn(
                        'flex cursor-pointer items-center gap-2.5 rounded-lg border px-2.5 py-2 transition-colors',
                        checked ? 'border-primary/40 bg-primary/10' : 'border-white/10 hover:bg-white/5',
                      )}
                    >
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() => toggle(app.appname)}
                        className="h-3.5 w-3.5 shrink-0 accent-primary"
                      />
                      <div className="min-w-0">
                        <div className="truncate text-xs text-foreground">{app.display_name}</div>
                        <div className="truncate text-[11px] text-muted-foreground">{app.appname}</div>
                        {/* 0.6.549（用户令）：应用下方标注它依赖的运行时 */}
                        {app.runtimes.length > 0 && (
                          <div className="truncate text-[11px] text-primary/80">
                            依赖 {app.runtimes.join('、')}
                          </div>
                        )}
                      </div>
                    </label>
                  );
                })
              )}
            </>
          )}
        </div>

        <DialogFooter className="flex-row flex-wrap items-center justify-between gap-2 sm:justify-between">
          <div className="flex gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={selectVisible}
              disabled={visible.length === 0}
              className="h-8 text-xs"
            >
              全选
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => setDraft([])}
              disabled={draft.length === 0}
              className="h-8 text-xs"
            >
              清空
            </Button>
          </div>
          <div className="flex gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={() => onOpenChange(false)} className="h-8 text-xs">
              取消
            </Button>
            <Button
              type="button"
              size="sm"
              onClick={() => {
                onSave(draft);
                onOpenChange(false);
              }}
              className="h-8 text-xs"
            >
              保存
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
