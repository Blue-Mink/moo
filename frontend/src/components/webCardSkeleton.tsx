import React from 'react';
import { Loader2 } from 'lucide-react';

/** 网页版详情卡（WebAppDetailCard）占位骨架：与卡片等高同材质，
    懒加载 chunk 未就绪/详情未返回时维持网格统一高度。
    单独成文件：AppList / App 静态引用它，而 WebAppDetailCard 本体懒加载，
    避免 markdown 渲染依赖被静态拖进主包。 */
export const WebAppDetailCardSkeleton: React.FC = () => (
  <div className="h-[240px] rounded-[18px] border border-white/10 bg-card/55 backdrop-blur-xl shadow-appstore flex items-center justify-center text-muted-foreground/60">
    <Loader2 className="h-5 w-5 animate-spin" />
  </div>
);
