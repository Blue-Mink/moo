import * as React from "react"
import * as SwitchPrimitives from "@radix-ui/react-switch"

import { cn } from "@/lib/utils"

const Switch = React.forwardRef<
  React.ElementRef<typeof SwitchPrimitives.Root>,
  React.ComponentPropsWithoutRef<typeof SwitchPrimitives.Root>
>(({ className, ...props }, ref) => (
  <SwitchPrimitives.Root
    className={cn(
      "peer inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent shadow-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:bg-primary data-[state=unchecked]:bg-input",
      className
    )}
    {...props}
    ref={ref}
  >
    <SwitchPrimitives.Thumb
      className={cn(
        // 0.6.226：滑钮改用**布局定位**（margin-left）而不是 transform —— 真机录屏实锤
        // 「键盘弹出那一刻开关会闪一下」：那一帧滑钮整块没画出来（纯蓝药丸），
        // 原因是 translate 让滑钮进了独立合成层，WebView 在窗口尺寸变化时丢了它一帧。
        // 用 margin 后滑钮与父级同层绘制，不再有"丢帧"问题（动画幅度仅 16px，观感无差）。
        "pointer-events-none block h-4 w-4 rounded-full bg-background shadow-lg ring-0 transition-[margin] data-[state=checked]:ml-4 data-[state=unchecked]:ml-0"
      )}
    />
  </SwitchPrimitives.Root>
))
Switch.displayName = SwitchPrimitives.Root.displayName

export { Switch }
