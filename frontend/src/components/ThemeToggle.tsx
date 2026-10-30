import React, { useEffect, useState } from 'react';
import { useTheme } from 'next-themes';
import { Moon, Sun, Monitor } from 'lucide-react';
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
 * App Store 风格主题切换。
 * 基于 next-themes（attribute="class"），持久化到 localStorage（key=theme）。
 *
 * 2026-10-28 私人补丁：三态循环 亮色 → 暗色 → 跟随系统 → 亮色…
 * （原实现二值翻转，点过一次后 system 档丢失，无法回到跟随系统。）
 * next-themes 本身支持（main.tsx defaultTheme="system" + enableSystem），
 * 这里只是把按钮的语义补齐；跟随系统档图标 = 显示当前解析到的主题 + 小圆点。
 *
 * 切换过渡 0.6.302 起 = 全引擎统一「即时切换 + 单层遮罩交叉淡切」，
 * 见 src/lib/themeTransition.ts；用户开启「减少动态效果」时直接切换。
 * 同步 <html> 的 color-scheme，让滚动条、原生表单控件跟随主题。
 */
const ThemeToggle: React.FC<{ className?: string }> = ({ className }) => {
  const { theme, resolvedTheme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);

  useEffect(() => setMounted(true), []);

  // 原生控件（滚动条/输入框）颜色方案跟随当前解析主题
  useEffect(() => {
    if (!mounted) return;
    document.documentElement.style.colorScheme = effectiveTheme(theme, resolvedTheme);
  }, [mounted, theme, resolvedTheme]);

  const resolved = effectiveTheme(theme, resolvedTheme);

  const apply = (next: 'light' | 'dark' | 'system') => {
    if (next === 'system') {
      setTheme('system');
      return;
    }
    // light/dark 档走既有的遮罩交叉淡切（system→具体档也走淡切，
    // 但 system 档本身切换时 resolved 不变则无视觉跳变，淡切无害）
    void toggleThemeWithTransition(resolved, () => setTheme(next));
  };

  // 三态循环：light → dark → system → light…
  const cycle = () => {
    const order: Array<'light' | 'dark' | 'system'> = ['light', 'dark', 'system'];
    const idx = order.indexOf((theme ?? 'system') as 'light' | 'dark' | 'system');
    apply(order[(idx + 1) % order.length]);
  };

  // 首帧（主题水合前）显示占位，避免图标闪变
  if (!mounted) {
    return (
      <Button variant="ghost" size="icon" className={cn("h-8 w-8 rounded-full", className)} aria-label="切换主题">
        <Moon className="h-4 w-4" />
      </Button>
    );
  }

  const isSystem = theme === 'system' || theme === undefined;
  const label = isSystem
    ? `跟随系统（当前 ${resolved === 'dark' ? '暗色' : '亮色'}）· 点击切换到${resolved === 'dark' ? '亮' : '暗'}色`
    : isDark(resolved)
      ? '暗色 · 点击切换到跟随系统'
      : '亮色 · 点击切换到暗色';

  return (
    <Button
      variant="ghost"
      size="icon"
      className={cn(
        "h-8 w-8 rounded-full text-muted-foreground hover:text-foreground",
        "transition-[transform,color,background-color] duration-200 hover:-translate-y-px",
        className,
      )}
      onClick={cycle}
      aria-label={label}
      title={label}
    >
      {isSystem ? (
        <span className="relative flex h-4 w-4 items-center justify-center">
          <Monitor className="h-4 w-4" />
          <span
            className={`absolute -bottom-0.5 -right-0.5 h-1.5 w-1.5 rounded-full ring-1 ring-background ${
              resolved === 'dark' ? 'bg-slate-300' : 'bg-slate-600'
            }`}
          />
        </span>
      ) : isDark(resolved) ? (
        <Sun className="h-4 w-4" />
      ) : (
        <Moon className="h-4 w-4" />
      )}
    </Button>
  );
};

// isDark 的独立小函数（避免和上面 effectiveTheme 语义混淆）
function isDark(resolved: ResolvedThemeMode): boolean {
  return resolved === 'dark';
}

export default ThemeToggle;
