import React, { useState } from 'react';
import { Package } from 'lucide-react';
import { cn } from "@/lib/utils";
import { apiUrl } from '../api/base';
import type { AppInfo } from '../api/client';

/**
 * 应用图标统一渲染。
 *
 * - 外部源应用（source 非 fnos-apps）走后端图标代理
 *   `/api/apps/{appname@source}/asset?type=icon`：后端先取应用声明的
 *   icon_url，为空/失效时自动按源仓库布局探测 `<appname>/ICON.PNG`
 *   等候选路径，并经 GitHub 镜像链抓取 —— 解决社区源普遍不写
 *   icon_url 或直连 raw 失败导致图标缺失的问题。
 * - URL 带 `&v=<版本>`（0.6.200）：应用升级换图标（icon_url 指向仓库
 *   main 分支，URL 不变内容更新）时新 URL 绕过浏览器 HTTP 24h 缓存与
 *   SW Cache Storage 的旧条目，后端图标缓存 key 也带版本（双重保险）。
 * - 内置目录（fnos-apps）图标本身已是加速后的直链，直接加载。
 * - 加载失败回退占位图标，不出现破图。
 */
const AppIcon: React.FC<{
  app: AppInfo;
  /** 容器尺寸/类（图标与占位框共用） */
  className?: string;
  /** 占位图标类（默认 h-6 w-6） */
  iconClassName?: string;
}> = ({ app, className, iconClassName }) => {
  const [failed, setFailed] = useState(false);
  const isExternal = !!app.source && app.source !== 'fnos-apps';
  // 版本戳：目录最新版优先，其次已装版本（已装无目录条目时兜底）
  const iconVer = app.latest_version || app.installed_version || '';
  const iconUrl = app.icon_url || '';
  // 应用中心安装的应用（无源条目）图标是面板相对路径
  // （/app-center-static/icon/<app>/icon.png）：Moo 所在 origin 无面板
  // 登录态，浏览器直载必 404 → 统一改走后端 asset 代理，后端回退读本机
  // 已装应用目录图标（/vol1/@appcenter/<app>/ui/images/）。仅 http(s)
  // 绝对直链（官方 CDN / 社区源外链）保持浏览器直载。
  const isAbsoluteHttp = /^https?:\/\//i.test(iconUrl);
  const useProxy = iconUrl !== '' && (isExternal || !isAbsoluteHttp);
  const src = useProxy
    ? apiUrl(`/api/apps/${encodeURIComponent(app.key)}/asset?type=icon${iconVer ? `&v=${encodeURIComponent(iconVer)}` : ''}`)
    : iconUrl;

  if (!src || failed) {
    return (
      <div className={cn("bg-muted/60 squircle flex items-center justify-center text-muted-foreground", className)}>
        <Package className={cn("opacity-40", iconClassName || "h-6 w-6")} />
      </div>
    );
  }
  return (
    <img
      src={src}
      alt={app.display_name}
      loading="lazy"
      onError={() => setFailed(true)}
      className={cn("squircle object-cover bg-muted/40", className)}
    />
  );
};

export default AppIcon;
