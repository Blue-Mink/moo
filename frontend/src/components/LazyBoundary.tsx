import React from 'react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';

interface Props {
  children: React.ReactNode;
  /** 出问题时给用户的动作提示（默认：刷新页面）。 */
  hint?: string;
}

interface State {
  failed: boolean;
  dismissed: boolean;
}

/**
 * 懒加载弹窗的兜底边界（0.6.5xx）。
 *
 * 背景：升级的「向导 / 依赖占用 / 许可协议」弹窗都是 React.lazy 分包加载的。
 * 如果 chunk 因发版缺文件、磁盘或网关异常加载失败，React 只会把错误抛到
 * console，Suspense 的 fallback 也已经结束 → 界面完全没有反馈，用户点「更新」
 * 像没反应一样（假实例实测：依赖占用场景 8/8 复现）。
 *
 * 这里把错误接住：弹一条可读 toast + 屏上一个可关闭的提示卡，给出「刷新页面」，
 * 让失败可见、可恢复。不影响正常加载路径。
 */
class LazyBoundary extends React.Component<Props, State> {
  state: State = { failed: false, dismissed: false };

  static getDerivedStateFromError(): Partial<State> {
    return { failed: true };
  }

  componentDidCatch(error: Error) {
    console.error('[lazy] 界面组件加载失败', error);
    toast.error('界面组件加载失败，请刷新页面后重试', { duration: 8000 });
  }

  render() {
    // 用户已关闭提示：不再重试渲染（lazy 的失败已被缓存，重试只会再次抛错，
    // 表现为「关闭按钮点了没反应」）。
    if (this.state.dismissed) return null;
    if (!this.state.failed) return this.props.children;
    return (
      <div className="fixed inset-0 z-[80] flex items-center justify-center bg-black/40 p-4">
        <div className="w-full max-w-sm rounded-[18px] border border-border/20 bg-card p-5 shadow-appstore">
          <div className="text-sm font-semibold">界面组件加载失败</div>
          <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
            更新所需的弹窗未能载入（可能是网络中断或应用文件不完整）。
            {this.props.hint || '请刷新页面后重试。'}
          </p>
          <div className="mt-4 flex justify-end gap-2">
            <Button variant="outline" onClick={() => this.setState({ failed: false, dismissed: true })}>
              关闭
            </Button>
            <Button onClick={() => window.location.reload()}>刷新页面</Button>
          </div>
        </div>
      </div>
    );
  }
}

export default LazyBoundary;
