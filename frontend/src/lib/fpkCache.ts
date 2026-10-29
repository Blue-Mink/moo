import type { AppInfo } from '../api/client';
import { fetchFpkDownloads, type FpkDownloadFile } from '../api/client';

/* ── 0.6.365（用户令）：卡面 / 移动行列表 共用同一份 FPK 缓存清单 ─────────
   0.6.362 时这段逻辑内嵌在 WebAppDetailCard；移动端行列表也要在 ⋯ 里提供
   「下载 / 安装本地包」，两处必须是同一口径（同一份缓存、同一匹配规则），
   故抽到 lib 供 Card 与 RowActions 共用。
   缓存包清单是全局一份（几十张卡/几十行同时挂载时只发一次请求），60s 内复用；
   下载/安装完成时 force=true 置脏重拉。 */

const normFpk = (s?: string) => (s || '').toLowerCase().replace(/[^a-z0-9]/g, '');

let fpkFilesCache: { at: number; files: FpkDownloadFile[] } | null = null;
let fpkFilesInflight: Promise<FpkDownloadFile[]> | null = null;

export const loadFpkFiles = (force = false): Promise<FpkDownloadFile[]> => {
  if (!force && fpkFilesCache && Date.now() - fpkFilesCache.at < 60_000) {
    return Promise.resolve(fpkFilesCache.files);
  }
  if (!fpkFilesInflight) {
    fpkFilesInflight = fetchFpkDownloads()
      .then((r) => {
        const files = r.files || [];
        fpkFilesCache = { at: Date.now(), files };
        return files;
      })
      .catch(() => fpkFilesCache?.files ?? [])
      .finally(() => { fpkFilesInflight = null; });
  }
  return fpkFilesInflight;
};

/** 匹配口径与详情页/卡面一致：优先缓存包 appname 归一 == 应用 appname 归一，
    解析失败（appname 为空）时回落 display_name 归一相等。 */
export const pickCachedFpk = (files: FpkDownloadFile[], app: AppInfo): FpkDownloadFile | null => {
  const na = normFpk(app.appname);
  const nd = normFpk(app.display_name);
  return (files || []).find((f) => {
    const fa = normFpk(f.appname);
    if (fa && na && fa === na) return true;
    if (!fa) return !!(nd && normFpk(f.display_name) === nd);
    return false;
  }) || null;
};
