import React, { useEffect, useState } from 'react';
import { useTheme } from 'next-themes';
import { Moon, Sun } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import { toggleThemeWithTransition, type ResolvedThemeMode } from '@/lib/themeTransition';

/** next-themes 的 theme/resolvedTheme 声明为 string，这里收敛成 light|dark。 */
const effectiveTheme = (
  theme: string | undefined,
  resolvedTheme: string | undefined,
): ResolvedThemeMode =>
  resolvedTheme === 'dark' || theme === 'dark' ? 'dark' : 'light';

/**
 * App Store 风格亮/暗主题切换。
 * 基于 next-themes（attribute="class"），持久化到 localStorage（key=theme）。
 *
 * 切换过渡参考开源 fn-knock 的 View Transitions 圆形展开：新主题从屏幕
 * 中心以圆形遮罩向外铺开（1s expo-out），见 src/lib/themeTransition.ts；
 * 浏览器不支持 / 用户开启「减少动态效果」时直接切换。
 * 另与 fn-knock 一致：同步 <html> 的 color-scheme，让滚动条、原生
 * 表单控件等跟随主题。
 */
const ThemeToggle: React.FC<{ className?: string }> = ({ className }) => {
  const { theme, resolvedTheme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);

  useEffect(() => setMounted(true), []);

  // 原生控件（滚动条/输入框）颜色方案跟随当前主题
  useEffect(() => {
    if (!mounted) return;
    document.documentElement.style.colorScheme = effectiveTheme(theme, resolvedTheme);
  }, [mounted, theme, resolvedTheme]);

  const toggle = () => {
    void toggleThemeWithTransition(effectiveTheme(theme, resolvedTheme), setTheme);
  };

  // 首帧（主题水合前）显示占位，避免图标闪变
  if (!mounted) {
    return (
      <Button variant="ghost" size="icon" className={cn("h-8 w-8 rounded-full", className)} aria-label="切换主题">
        <Moon className="h-4 w-4" />
      </Button>
    );
  }

  const isDark = effectiveTheme(theme, resolvedTheme) === 'dark';
  return (
    <Button
      variant="ghost"
      size="icon"
      className={cn(
        "h-8 w-8 rounded-full text-muted-foreground hover:text-foreground",
        "transition-[transform,color,background-color] duration-200 hover:-translate-y-px",
        className,
      )}
      onClick={toggle}
      aria-label={isDark ? '切换到亮色' : '切换到暗色'}
      title={isDark ? '切换到亮色' : '切换到暗色'}
    >
      {isDark ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
    </Button>
  );
};

export default ThemeToggle;
