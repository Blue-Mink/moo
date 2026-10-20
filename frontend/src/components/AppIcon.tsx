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
/** 0.6.339：应用 key 的稳定色相（同一应用每次渲染同色），用于无图标时的首字母占位。 */
const letterHue = (s: string) => {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) % 360;
  return h;
};

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
  // 0.6.326d：外部源「无论 icon_url 是否为空」一律走后端代理。0.6.320 把
  // `iconUrl !== ''` 提到最外层后，无 icon_url 的社区源卡（全目录 1217/2156）
  // 直接落入占位分支、从不请求代理 —— 而后端 asset 代理对其中相当一部分
  // （源仓库根目录 ICON.PNG 等 6 候选）能返回真图。非外部源维持 320 语义：
  // 相对路径（应用中心面板图标）走代理，http(s) 绝对直链浏览器直载。
  const useProxy = isExternal || (iconUrl !== '' && !isAbsoluteHttp);
  const src = useProxy
    ? apiUrl(`/api/apps/${encodeURIComponent(app.key)}/asset?type=icon${iconVer ? `&v=${encodeURIComponent(iconVer)}` : ''}`)
    : iconUrl;

  if (!src || failed) {
    // 0.6.339：无图/加载失败不再一律灰框——用「应用名首字符 + 稳定色块」，
    // 让确实没有图标来源的卡（社区源自建应用常见）也有可辨识身份。
    // 无 display_name/appname 时才退回通用包图标。
    const label = (app.display_name || app.appname || '').trim().charAt(0).toUpperCase();
    const hue = letterHue(app.key || app.appname || '');
    return (
      <div
        className={cn("squircle flex items-center justify-center overflow-hidden select-none", className)}
        style={label ? { backgroundColor: `hsl(${hue} 42% 88%)`, color: `hsl(${hue} 52% 32%)` } : undefined}
        role="img"
        aria-label={app.display_name || app.appname}
      >
        {label ? (
          <svg viewBox="0 0 100 100" className="h-full w-full" aria-hidden="true">
            <text x="50" y="50" textAnchor="middle" dominantBaseline="central" fontSize="52" fontWeight="600" fill="currentColor">{label}</text>
          </svg>
        ) : (
          <Package className={cn("opacity-40", iconClassName || "h-6 w-6")} />
        )}
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
