import React, { useEffect, useMemo, useState } from 'react';
import { Loader2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog';
import type { AppWizard, WizardItem, WizardParam, WizardStep } from '@/api/client';

interface WizardDialogProps {
  /** App display name, for the title. */
  appDisplayName: string;
  wizard: AppWizard | null;
  loading: boolean;
  onCancel: () => void;
  onConfirm: (params: WizardParam[]) => void;
}

/**
 * Renders an app's install-time form.
 *
 * Apps declare this themselves (fnos/wizard/install) and the native App Center
 * renders the same definition. Without it the store silently installed with
 * defaults, so an app needing a token or password came up misconfigured.
 *
 * 0.6.264（用户定稿）：对齐飞牛官方应用中心向导——
 * ① 每个输入框下方显示应用的 helpText 提示；
 * ② 向导定义本身就是分步的（stepTitle 组）→ 一组一页，内容多时不再一屏到底；
 *    底部按钮同款「上一页 / 取消 / 下一页」，末页为「安装」；
 * ③ 逐页校验：点「下一页」只校验当前页，不通过不翻页。
 *
 * The field types come from fnOS, so unknown ones fall back to a text input
 * rather than being dropped — a field we cannot render is still a field the
 * app may require.
 */
const WizardDialog: React.FC<WizardDialogProps> = ({
  appDisplayName,
  wizard,
  loading,
  onCancel,
  onConfirm,
}) => {
  const steps = useMemo<WizardStep[]>(() => wizard?.content ?? [], [wizard]);
  const [values, setValues] = useState<Record<string, string>>({});
  const [touched, setTouched] = useState<Record<string, boolean>>({});
  const [page, setPage] = useState(0);

  // Seed defaults the app declares; reset paging when the wizard changes.
  useEffect(() => {
    const seed: Record<string, string> = {};
    for (const s of steps) {
      for (const it of s.items ?? []) {
        // 0.6.265：默认值预填（initValue 部分 FPK 里是数字，统一转字符串）
        if (it.field && it.initValue !== undefined && it.initValue !== '') {
          seed[it.field] = String(it.initValue);
        }
      }
    }
    setValues(seed);
    setTouched({});
    setPage(0);
  }, [steps]);

  const errorFor = (item: WizardItem): string | null => {
    if (!item.field) return null;
    const v = values[item.field] ?? '';
    for (const rule of item.rules ?? []) {
      if (rule.required && v.trim() === '') return rule.message || '此项为必填';
      if (rule.min !== undefined && v.length < rule.min) {
        return rule.message || `至少 ${rule.min} 位`;
      }
    }
    return null;
  };

  const fieldItemsOf = (idx: number): WizardItem[] =>
    (steps[idx]?.items ?? []).filter((it) => it.field && it.type !== 'tips');

  const pageValid = (idx: number) =>
    fieldItemsOf(idx).every((it) => errorFor(it) === null);

  const isMulti = steps.length > 1;
  const isLast = page >= steps.length - 1;
  const curTitle = steps[page]?.stepTitle?.trim() || '';

  const markTouched = (idx: number) => {
    setTouched((prev) => {
      const next = { ...prev };
      for (const it of fieldItemsOf(idx)) next[it.field as string] = true;
      return next;
    });
  };

  const allParams = (): WizardParam[] =>
    steps
      .flatMap((s) => s.items ?? [])
      .filter((it) => it.field && it.type !== 'tips')
      .map((it) => ({ key: it.field as string, value: values[it.field as string] ?? '' }));

  const next = () => {
    markTouched(page);
    if (!pageValid(page)) return;
    if (isLast) onConfirm(allParams());
    else setPage(page + 1);
  };

  const prev = () => {
    if (page > 0) setPage(page - 1);
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onCancel()}>
      {/* 移动端宽度对齐详情页卡片（100vw-24px，与详情卡内卡 px-3 同宽） */}
      <DialogContent className="rounded-[18px] max-w-[calc(100vw-1.5rem)] sm:max-w-md max-h-[calc(100dvh-2rem)] flex flex-col border-border/20 shadow-appstore bg-card">
        <DialogHeader className="shrink-0">
          <div className="flex items-center justify-between gap-3 pr-6">
            <DialogTitle>安装 {appDisplayName}</DialogTitle>
            {isMulti && (
              <span className="shrink-0 rounded-full bg-muted/80 px-2 py-0.5 text-[11px] tabular-nums text-muted-foreground">
                {page + 1} / {steps.length}
              </span>
            )}
          </div>
          {isMulti && curTitle && (
            <p className="text-xs text-muted-foreground">{curTitle}</p>
          )}
        </DialogHeader>

        {loading ? (
          <div className="flex flex-col items-center gap-3 py-8">
            <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
            <p className="text-xs text-muted-foreground text-center leading-relaxed">
              正在读取安装向导…
            </p>
          </div>
        ) : (
          // 0.6.144 补丁2 同款（渠道弹窗先例）：表单区左右各收 6px——
          // 微信 webview 下超宽输入框的 1px 边框/聚焦环两侧渲染不完整
          // （「两边竖线折叠」），收窄后输入框与弹窗内容区不再贴边
          <div className="space-y-4 py-2 px-1.5 overflow-y-auto flex-1 min-h-0">
            {(steps[page]?.items ?? []).map((item, idx) => {
              if (item.type === 'tips') {
                return (
                  <p
                    key={idx}
                    className="text-xs text-muted-foreground leading-relaxed [&_a]:text-primary [&_a]:underline"
                    // The help text is authored by the packager and may contain
                    // <b>/<a>; it ships inside the fpk, same trust level as the
                    // binary being installed.
                    dangerouslySetInnerHTML={{ __html: item.helpText ?? '' }}
                  />
                );
              }
              if (!item.field) return null;
              const err = touched[item.field] ? errorFor(item) : null;
              return (
                <div key={item.field} className="space-y-1.5">
                  <label className="flex items-center justify-between gap-2 text-sm font-medium leading-none">
                    <span>{item.label ?? item.field}</span>
                    {/* 0.6.265：应用声明了默认值的字段，标签行直接标出
                        （默认值已预填进输入框，无需手工输入） */}
                    {item.initValue ? (
                      <span className="shrink-0 text-[10px] font-normal text-muted-foreground">
                        默认 {item.initValue}
                      </span>
                    ) : null}
                  </label>
                  <Input
                    type={item.type === 'password' ? 'password' : 'text'}
                    value={values[item.field] ?? ''}
                    onChange={(e) =>
                      setValues((v) => ({ ...v, [item.field as string]: e.target.value }))
                    }
                    aria-invalid={err ? true : undefined}
                  />
                  {err ? (
                    <p className="text-xs text-destructive">{err}</p>
                  ) : item.helpText ? (
                    // 0.6.264：每个输入框下方显示应用自带的提示（对齐官方应用中心）
                    <p className="text-xs text-muted-foreground leading-relaxed">
                      {item.helpText}
                    </p>
                  ) : null}
                </div>
              );
            })}
          </div>
        )}

        {/* 0.6.266：人类友好布局（用户定稿）——取消靠左（退出动作），
            上一页 + 下一页/安装 靠右（前进动作）；同一排、间距 8px */}
        <DialogFooter className="shrink-0">
          <div className="flex w-full items-center justify-between gap-2">
            <Button variant="outline" onClick={onCancel} disabled={loading}>
              取消
            </Button>
            <div className="flex items-center gap-2">
              {isMulti && (
                <Button variant="outline" onClick={prev} disabled={page === 0 || loading}>
                  上一页
                </Button>
              )}
              <Button onClick={next} disabled={loading}>
                {isLast ? '安装' : '下一页'}
              </Button>
            </div>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};

export default WizardDialog;
