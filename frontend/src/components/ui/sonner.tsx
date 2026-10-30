import { useTheme } from "next-themes"
import { Toaster as Sonner } from "sonner"

type ToasterProps = React.ComponentProps<typeof Sonner>

const Toaster = ({ ...props }: ToasterProps) => {
  const { theme = "system" } = useTheme()

  return (
    <Sonner
      theme={theme as ToasterProps["theme"]}
      className="toaster group"
      toastOptions={{
        classNames: {
          toast:
            // 0.6.278（用户要求统一风格）：顶部通知栏与底部 Dock 同款玻璃材质 ——
            // 24px 大圆角（与 Dock 一致）+ bg-card/55 半透明 + backdrop-blur-2xl
            // 毛玻璃 + white/10 hairline 描边 + dock 同款投影。
            // `!` 后缀（Tailwind v4 important）用于压过 sonner 内置
            // [data-sonner-toast][data-styled='true'] 规则里的内联变量
            // （--normal-bg / --normal-border / --border-radius）。
            "group toast rounded-[24px]! bg-card/55! border-white/10! backdrop-blur-2xl! shadow-2xl! shadow-black/40 group-[.toaster]:text-foreground",
          description: "group-[.toast]:text-muted-foreground",
          actionButton:
            "group-[.toast]:bg-primary group-[.toast]:text-primary-foreground",
          cancelButton:
            "group-[.toast]:bg-muted group-[.toast]:text-muted-foreground",
        },
      }}
      {...props}
    />
  )
}

export { Toaster }
