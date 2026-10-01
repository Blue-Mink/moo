import React, { useEffect, useMemo, useState } from 'react';
import { Loader2, PackageOpen, CheckCircle2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogDescription,
} from '@/components/ui/dialog';
import type { PanelDetailResponse, PanelInstallParams } from '@/api/client';

interface PanelInstallDialogProps {
  detail: PanelDetailResponse | null;
  loading: boolean;
  onCancel: () => void;
  onConfirm: (params: PanelInstallParams) => void;
}

/** 官方目录描述是 HTML 片段（发布者写的 <h3>/<p>），弹窗里只取纯文本摘要。 */
const stripHtml = (html?: string): string =>
  (html ?? '').replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim().slice(0, 120);

/**
 * 官方应用中心安装确认 + 依赖选择弹窗（v1.20.0）。
 *
 * 官方目录应用走面板 cloud 通道（无可直链 FPK），安装前展示：
 *  1. 应用本身的实时依赖（installDepApps，来自面板 detail 接口）；
 *  2. 每个未安装依赖的处置：装官方依赖（默认）或跳过——当商店目录里已有
 *     同名应用（其他源）时列出，由用户自选（用户需求 2026-09-19）。
 *
 * detail 为 null 时显示加载态（防御性；App.tsx 通常在有 detail 后才渲染本组件）。
 */
const PanelInstallDialog: React.FC<PanelInstallDialogProps> = ({ detail, loading, onCancel, onConfirm }) => {
  const app = detail?.app;
  const volume = detail?.volume ?? 0;
  const sameNameApps = detail?.same_name_apps;

  const deps = app?.installDepApps ?? [];
  const [choice, setChoice] = useState<Record<string, 'install' | 'skip'>>({});

  useEffect(() => {
    const init: Record<string, 'install' | 'skip'> = {};
    for (const d of deps) {
      if (d.status === 'noinstall') init[d.appName] = 'install';
    }
    setChoice(init);
  }, [app?.appName, deps.length]);

  const missingDeps = useMemo(() => deps.filter((d) => d.status === 'noinstall'), [deps]);

  const confirm = () => {
    if (!detail || !app) return;
    onConfirm({
      volumeID: volume,
      deps: missingDeps.map((d) => ({ appName: d.appName, action: choice[d.appName] ?? 'install' })),
    });
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onCancel()}>
      {/* 移动端宽度对齐详情页卡片（详情卡 inset-0 全屏 + 内卡 px-3 → 100vw-24px；
          此前弹窗贴边全宽，比详情卡宽 24px，视觉不一致） */}
      <DialogContent className="rounded-[18px] max-w-[calc(100vw-1.5rem)] sm:max-w-lg max-h-[calc(100dvh-2rem)] flex flex-col border-border/20 shadow-appstore bg-card">
        {!detail || !app ? (
          <div className="flex flex-col items-center justify-center gap-3 py-16">
            <Loader2 className="h-7 w-7 animate-spin text-primary" />
            <p className="text-sm text-muted-foreground">正在获取官方应用详情…</p>
          </div>
        ) : (
          <>
            <DialogHeader>
              <div className="flex items-center gap-3">
                <img
                  src={app.icon}
                  alt=""
                  className="h-12 w-12 rounded-xl object-cover bg-muted shrink-0"
                  onError={(e) => { (e.target as HTMLImageElement).style.visibility = 'hidden'; }}
                />
                <div className="min-w-0">
                  <DialogTitle className="truncate text-base">
                    {app.name || app.appName}
                  </DialogTitle>
                  <DialogDescription className="truncate">
                    v{app.version}
                    {app.appDetail?.maintainer ? ` · ${app.appDetail.maintainer}` : ''}
                    {' · 官方应用中心'}
                  </DialogDescription>
                </div>
              </div>
            </DialogHeader>

            <div className="space-y-4 py-2 overflow-y-auto flex-1 min-h-0">
              {stripHtml(app.appDetail?.desc) && (
                <p className="text-xs text-muted-foreground leading-relaxed">
                  {stripHtml(app.appDetail?.desc)}
                </p>
              )}

              {deps.length > 0 && (
                <div className="space-y-2">
                  <p className="text-sm font-medium">
                    依赖（{deps.length}）
                    {missingDeps.length > 0 && (
                      <span className="ml-1 text-xs text-muted-foreground font-normal">
                        {missingDeps.length} 个未安装，将随主应用一并处理
                      </span>
                    )}
                  </p>
                  <div className="rounded-xl border border-border/40 divide-y divide-border/40 overflow-hidden">
                    {deps.map((d) => {
                      const installed = d.status !== 'noinstall';
                      const sameNames = sameNameApps?.[d.appName] ?? [];
                      const selectable = !installed && sameNames.length > 0;
                      const current = choice[d.appName] ?? 'install';
                      return (
                        <div key={d.appName} className="px-3 py-2.5 bg-card/40">
                          <div className="flex items-center gap-2.5">
                            <img
                              src={d.icon}
                              alt=""
                              className="h-8 w-8 rounded-lg object-cover bg-muted"
                              onError={(e) => { (e.target as HTMLImageElement).style.visibility = 'hidden'; }}
                            />
                            <div className="min-w-0 flex-1">
                              <div className="flex items-center gap-2">
                                <span className="text-sm font-medium truncate">{d.name}</span>
                                <span className="text-[11px] text-muted-foreground">v{d.version}</span>
                              </div>
                            </div>
                            {installed ? (
                              <span className="inline-flex items-center gap-1 text-[11px] text-emerald-600 dark:text-emerald-400 shrink-0">
                                <CheckCircle2 className="h-3.5 w-3.5" />
                                已安装
                              </span>
                            ) : (
                              <span className="inline-flex items-center gap-1 text-[11px] text-muted-foreground shrink-0">
                                <PackageOpen className="h-3.5 w-3.5" />
                                未安装
                              </span>
                            )}
                          </div>
                          {selectable && (
                            <div className="mt-2 space-y-1.5 pl-[42px]">
                              <label className="flex items-center gap-2 text-xs cursor-pointer">
                                <input
                                  type="radio"
                                  name={`dep-${d.appName}`}
                                  checked={current === 'install'}
                                  onChange={() => setChoice((c) => ({ ...c, [d.appName]: 'install' }))}
                                  className="accent-primary"
                                />
                                安装官方依赖（与主应用一起从官方应用中心下载）
                              </label>
                              <label className="flex items-center gap-2 text-xs cursor-pointer">
                                <input
                                  type="radio"
                                  name={`dep-${d.appName}`}
                                  checked={current === 'skip'}
                                  onChange={() => setChoice((c) => ({ ...c, [d.appName]: 'skip' }))}
                                  className="accent-primary"
                                />
                                <span>
                                  跳过，使用已有同名应用：
                                  <span className="text-muted-foreground">{sameNames.join('、')}</span>
                                </span>
                              </label>
                            </div>
                          )}
                          {!installed && !selectable && (
                            <p className="mt-1.5 pl-[42px] text-[11px] text-muted-foreground">
                              将随主应用一起从官方应用中心自动安装
                            </p>
                          )}
                        </div>
                      );
                    })}
                  </div>
                </div>
              )}

              <div className="flex items-start gap-2 text-[11px] text-muted-foreground leading-relaxed">
                <span className="mt-0.5 shrink-0">
                  <CheckCircle2 className="h-3.5 w-3.5 text-primary" />
                </span>
                <span>
                  将通过官方应用中心安装到默认存储卷（卷 {volume}），安装完成后自动启动。
                  等同于在系统应用中心安装，占用官方下载配额与协议条款。
                </span>
              </div>
            </div>
          </>
        )}

        <DialogFooter className="shrink-0">
          <Button variant="outline" onClick={onCancel}>
            取消
          </Button>
          <Button onClick={confirm} disabled={loading || !detail}>
            {loading ? (
              <span className="inline-flex items-center gap-2">
                <Loader2 className="h-4 w-4 animate-spin" />
                安装中…
              </span>
            ) : (
              '开始安装'
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};

export default PanelInstallDialog;
