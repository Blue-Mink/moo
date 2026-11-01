#!/usr/bin/env python3
"""moo_invite.py — 邀请上游作者把项目做成 Moo 应用源：一条命令出全套产物。

两种模式：

  自动模式（推荐；仓库里**还没有** moo.json 时用）
      python3 tools/moo_invite.py --repo OWNER/REPO
    自动从 GitHub 仓库 + Release 识别应用：下载 FPK 读包内 manifest 拿 appname /
    display_name / 版本 / 平台 / 最低系统版本，sha256 与字节数取自 Release 资产
    （下载后逐字节复核），图标与 README 从仓库树里挑。**不用任何手工参数。**

  手工模式（Release 不在 GitHub / 包不在 Release 里时）
      python3 tools/moo_invite.py --owner OWNER --repo NAME --appname app \\
        --display-name 名称 --version 1.0.0 --platform all \\
        --fpk-url https://… --size 1234567 --sha256 <64hex>

产物（默认写到 invite_<repo>/）：
  moo.json         可直接提交的索引（写完已自检，含 sha256 / 字节数 / 架构 / 图标 / README）
  oneclick.txt     「点开即新建并预填」链接（仅创建模式；作者点开 → Commit 即完成）
  issue_body.md    给开发者看的正文（一句话 + 一个按钮 + 可折叠 JSON 兜底）
  issue_title.txt  建议的 issue 标题
  issue_url.txt    开 issue 的预填链接；正文太长时退化为「只预填标题」+ 说明
  create_issue.sh  一条命令开 issue（走 GitHub API，不受 URL 长度限制）
  comment.md       在已有讨论里回复用的版本（正文前加过渡句）
  summary.txt      本次识别的全部依据（便于人工复核）

退出码：0 成功；1 参数 / 网络 / 校验失败（错误信息带下一步动作）。
"""
import argparse, hashlib, html, json, os, re, shutil, subprocess, sys, tarfile, tempfile, urllib.error
import urllib.parse, urllib.request

UA = "moo-invite/2.0 (+https://github.com/Blue-Mink/moo)"
# 索引维护脚本：更新模式正文里要告诉作者怎么拿到它（本仓 tools/update_moo_json.py）
UPDATE_SCRIPT_URL = "https://raw.githubusercontent.com/Blue-Mink/moo/main/tools/update_moo_json.py"
UPDATE_SCRIPT_MIRROR = "https://cdn.jsdelivr.net/gh/Blue-Mink/moo@main/tools/update_moo_json.py"
# 站点抽象：默认 GitHub；给完整 URL（如本地 Gitea）就切到 Gitea API
FORGE = {"kind": "github", "base": "https://github.com", "api": "https://api.github.com",
         "token": "", "user": "", "pass": ""}
BASE = FORGE["base"]
API = FORGE["api"]


def set_forge(kind, base):
    global BASE, API
    FORGE["kind"] = kind
    FORGE["base"] = (base or "").rstrip("/")
    FORGE["api"] = ("https://api.github.com" if kind == "github"
                    else FORGE["base"] + "/api/v1")
    BASE, API = FORGE["base"], FORGE["api"]
# 镜像优先、直连兜底（国内直连 GitHub 资产域名常见 0 字节/超时）
MIRRORS = ("https://gh-proxy.com/", "https://hk.gh-proxy.org/", "",
           "https://ghproxy.net/", "https://gh-proxy.org/")
DOWNLOAD_TIMEOUT = 15
# §4.3 固定分类清单（不能自造；中文名 / key 都收）
CATS = {
    "ai": "AI", "media": "影音娱乐", "automation": "媒体自动化",
    "game": "游戏", "photo": "摄影摄像", "efficiency": "实用效率",
    "devtools": "开发工具", "lifestyle": "生活服务", "backup": "备份同步",
    "download": "下载", "network": "网络工具", "browser": "浏览器",
    "driver": "驱动", "system": "系统工具",
}
VARIANT_RE = re.compile(
    r"(^|[-_.])(full|lite|mini|slim|light|small|big|pro|debug|static)([-_.]|$)", re.I)
VERSION_TOKEN_RE = re.compile(r"\d+(?:\.\d+)+")


def is_variant(name):
    """文件名里「版本号之后」出现 full/lite/pro… 才算变体包（pro-tool-1.0.0.fpk 不算）。"""
    stem = name.rsplit(".", 1)[0]
    hits = list(VERSION_TOKEN_RE.finditer(stem))
    tail = stem[hits[-1].end():] if hits else stem
    return bool(VARIANT_RE.search(tail))
APPNAME_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*$")
PLACEHOLDER_RE = re.compile(r"\$\{[^}]*\}")  # 未替换的构建变量，如 ${common.displayName}


def clean_field(s):
    """manifest 字段清洗：整段就是未替换的 ${...} 变量才算没写。

    只有「整个值 = ${xxx}」才丢弃；正文里出现 ${HOME} 这类字样不影响。
    """
    s = (s or "").strip()
    if not s:
        return ""
    if PLACEHOLDER_RE.fullmatch(s):
        return ""
    return s
# 正文里的 JSON 兜底块（`with_json=False` 时换成一句「回我一声」——URL 预填链接太长会被截断）
FALLBACK_JSON = """
<details><summary>备用：moo.json 全文（链接打不开时复制提交）</summary>

```json
%s
```

</details>
"""
FALLBACK_ASK = "链接打不开或被截断的话回我一声，我把 JSON 全文贴给你。\n"
_TOK = None


def die(msg):
    print("错误：" + msg, file=sys.stderr)
    sys.exit(1)


def warn(msg):
    print("提示：" + msg, file=sys.stderr)


def log(msg):
    print(msg, flush=True)


def token():
    """GitHub token：环境变量 → 本地 0600 文件 → 工作区 gh_token 模块。"""
    global _TOK
    if _TOK is not None:
        return _TOK
    t = (os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN") or "").strip()
    if not t:
        for p in ("~/.moo-gh-token", ".gh-token"):
            p = os.path.expanduser(p)
            if os.path.exists(p):
                try:
                    lines = open(p, encoding="utf-8").read().split()
                    if lines:
                        t = lines[0].strip()
                        break
                except OSError:
                    pass
    if not t:
        try:
            import gh_token as g  # 工作区辅助模块（可选）
            t = (g.token() or "").strip()
        except Exception:  # noqa: BLE001
            t = ""
    _TOK = t
    return t


def auth_header():
    if FORGE["kind"] == "github":
        t = token()
        return {"Authorization": "Bearer " + t} if t else {}
    if FORGE["token"]:
        return {"Authorization": "token " + FORGE["token"]}
    if FORGE["user"]:
        import base64
        raw = ("%s:%s" % (FORGE["user"], FORGE["pass"])).encode()
        return {"Authorization": "Basic " + base64.b64encode(raw).decode()}
    return {}


def api(url):
    """GET 仓库 API（相对路径按 API 基址补全）；404 返回 None。"""
    if not url.startswith("http"):
        url = API + url
    h = {"Accept": "application/vnd.github+json", "User-Agent": UA}
    if FORGE["kind"] == "gitea":
        h["Accept"] = "application/json"
    h.update(auth_header())
    try:
        with urllib.request.urlopen(urllib.request.Request(url, headers=h), timeout=30) as r:
            return json.loads(r.read().decode("utf-8", "replace"))
    except urllib.error.HTTPError as e:
        if e.code == 404:
            return None
        body = e.read()[:200].decode("utf-8", "replace")
        site = "GitHub" if FORGE["kind"] == "github" else "Gitea"
        if e.code in (403, 429) and "rate limit" in body.lower():
            die("%s API 限流（%s）。\n  下一步：设 GH_TOKEN 环境变量，或等一小时再跑。" % (site, e.code))
        if e.code in (401, 403):
            die("%s API 拒绝访问（%s）：%s\n  下一步：给凭据——GitHub 设 GH_TOKEN；"
                "Gitea 设 GITEA_TOKEN 或 GITEA_USER/GITEA_PASS。" % (site, e.code, body))
        die("%s API %s：%s\n  %s" % (site, e.code, url, body))
    except Exception as e:  # noqa: BLE001
        die("请求失败 %s：%s\n  下一步：检查网络/代理后重试。" % (url, e))


def repo_parts(raw, owner=""):
    """OWNER/REPO、https://host/O/R(.git)、git@host:O/R.git → (owner, name)。

    带完整 http(s) 地址时顺便切换站点：github.com 走 GitHub API，其它主机按 Gitea API。
    """
    s = (raw or "").strip()
    m = re.match(r"^(https?://[^/]+)/(.+?)(?:\.git)?/?$", s)
    if m:
        host, rest = m.group(1), m.group(2)
        parts = [x for x in rest.split("/") if x]
        if len(parts) >= 2:
            if "github.com" in host:
                set_forge("github", "https://github.com")
            else:
                set_forge("gitea", host)
            return parts[-2], parts[-1]
    m = re.search(r"github\.com[:/]+([^/]+)/([^/#?]+?)(?:\.git)?/?$", s)
    if m:
        return m.group(1), m.group(2)
    if "/" in s and not s.startswith("http"):
        o, n = s.split("/", 1)
        return o.strip(), n.strip().rstrip("/")
    if owner and s:
        return owner.strip(), s
    die("定位仓库失败：--repo 写 OWNER/REPO 或完整 https://github.com/OWNER/REPO 地址。\n"
        "  手工模式也可用 --owner OWNER --repo NAME。")


def normalize_arch(p):
    """manifest.platform / 文件名 → Moo 三档（x86 / arm / all）。"""
    s = (p or "").strip().lower()
    if s in ("", "all", "any", "universal", "noarch"):
        return "all"
    if s in ("x86", "x86_64", "amd64", "x64", "i386", "i686"):
        return "x86"
    if s in ("arm", "arm64", "aarch64", "armv7", "armv7l", "armhf"):
        return "arm"
    return s


def arch_from_name(name):
    s = (name or "").lower()
    if re.search(r"(^|[-_.])(x86|x86_64|amd64|x64|i386|i686)([-_.]|$)", s):
        return "x86"
    if re.search(r"(^|[-_.])(arm|arm64|aarch64|armv7|armv7l|armhf)([-_.]|$)", s):
        return "arm"
    return ""


def read_manifest(path):
    """FPK（tar.gz）根目录的 manifest → dict（appname/version/display_name/…）。"""
    info = {}
    try:
        with tarfile.open(path, "r:gz") as tf:
            names = tf.getnames()
            info["_compose"] = any(
                n.strip("./").lower() in ("docker-compose.yml", "docker-compose.yaml",
                                          "compose.yml", "compose.yaml",
                                          "app/docker-compose.yml", "app/docker-compose.yaml")
                for n in names)
            name = next((n for n in names if n.strip("./").lower() == "manifest"), "")
            if not name:
                raise RuntimeError("包里没有 manifest（不是标准 FPK？）")
            f = tf.extractfile(name)
            text = f.read().decode("utf-8", "replace") if f else ""
    except SystemExit:
        raise
    except Exception as e:  # noqa: BLE001
        raise RuntimeError("读包失败：%s" % e)
    for line in text.splitlines():
        if "=" in line and not line.lstrip().startswith("#"):
            k, v = line.split("=", 1)
            info[k.strip()] = v.strip()
    if not info.get("appname"):
        raise RuntimeError("manifest 里没有 appname")
    return info


def asset_url(asset, tag, owner, name):
    """索引里该写的直链：GitHub 用资产自带地址，Gitea 用稳定的 releases/download 路径。"""
    if FORGE["kind"] == "gitea":
        return "%s/%s/%s/releases/download/%s/%s" % (
            BASE, owner, name, urllib.parse.quote(tag, safe=""), urllib.parse.quote(asset["name"], safe=""))
    return asset.get("browser_download_url") or ""


def download(url, dest, max_bytes, expect_sha="", mirrors=True):
    """按镜像链下载（镜像优先、直连兜底）；返回 (字节数, sha256, 实际命中的前缀)。

    给了 expect_sha（GitHub 资产 digest）就逐字节核对，不一致视为该通道不可信、换下一个。
    """
    last = ""
    os.makedirs(os.path.dirname(dest) or ".", exist_ok=True)
    for pre in (MIRRORS if mirrors else ("",)):
        target = (pre + url) if pre else url
        try:
            hdr = {"User-Agent": UA}
            hdr.update(auth_header())
            req = urllib.request.Request(target, headers=hdr)
            with urllib.request.urlopen(req, timeout=DOWNLOAD_TIMEOUT) as r:
                total = int(r.headers.get("Content-Length") or 0)
                if total and total > max_bytes:
                    raise RuntimeError("文件 %d 字节，超过 --max-download-mb 上限" % total)
                h = hashlib.sha256()
                n = 0
                with open(dest, "wb") as f:
                    while True:
                        chunk = r.read(1 << 20)
                        if not chunk:
                            break
                        n += len(chunk)
                        if n > max_bytes:
                            raise RuntimeError("下载超过 --max-download-mb 上限，已中断")
                        h.update(chunk)
                        f.write(chunk)
                if n <= 0:
                    raise RuntimeError("下载到 0 字节")
                sha = h.hexdigest()
                if expect_sha and sha != expect_sha:
                    raise RuntimeError("sha256 %s… 与 GitHub 记录的 digest %s… 不一致"
                                       % (sha[:12], expect_sha[:12]))
                return n, sha, (pre or "direct")
        except Exception as e:  # noqa: BLE001
            last = "%s → %s" % (target, e)
            warn("下载通道失败，换下一个：%s" % last)
            try:
                if os.path.exists(dest):
                    os.remove(dest)
            except OSError:
                pass
            continue
    die("下载失败（全部通道）：\n  %s\n  下一步：本机到 GitHub 资产域名常被墙，"
        "可加大 --max-download-mb 或换网络重试。" % last)


def repo_tree(owner, name, branch):
    """仓库文件清单（浅层够用；取不到就当空，不影响主流程）。"""
    ref = urllib.parse.quote(branch or "main", safe="")
    if FORGE["kind"] == "gitea":
        url = "/repos/%s/%s/git/trees/%s?recursive=true&per_page=1000" % (owner, name, ref)
    else:
        url = "/repos/%s/%s/git/trees/%s?recursive=1" % (owner, name, ref)
    t = api(url)
    if not t or "tree" not in t:
        return []
    return [e["path"] for e in t["tree"] if e.get("type") == "blob"]


ICON_PRI = {"icon_256.png": 0, "ic_256.png": 0, "icon_512.png": 1, "icon_128.png": 2,
            "icon.png": 3, "icon_64.png": 4, "logo_256.png": 5, "logo.png": 6}


def pick_icon(paths, match=""):
    """挑图标；给了 match（appname）就优先只在该应用自己的目录里挑。"""
    if match:
        pool = [p for p in paths if match.lower() in p.lower()]
        if any(p.rsplit("/", 1)[-1].lower() in ICON_PRI for p in pool):
            paths = pool
    best = None
    for p in paths:
        base = p.rsplit("/", 1)[-1].lower()
        if base not in ICON_PRI:
            continue
        cand = (ICON_PRI[base], p.count("/"), len(p), p)
        if best is None or cand < best:
            best = cand
    return best[3] if best else ""


README_NAMES = ("readme.md", "readme.zh.md", "readme_zh.md", "readme.zh-cn.md",
                "readme.markdown", "readme.txt")


def pick_readme(paths, match=""):
    """挑 README；给了 match（appname）就优先 <appname>/README.md 这类同目录命名。"""
    if match:
        cands = []
        for p in paths:
            if p.rsplit("/", 1)[-1].lower() not in README_NAMES:
                continue
            d = p.rsplit("/", 1)[0].lower() if "/" in p else ""
            if match.lower() in d:
                cands.append(p)
        if cands:
            return sorted(cands, key=lambda x: (x.count("/"), len(x)))[0]
    root = {p.lower(): p for p in paths if "/" not in p}
    for n in README_NAMES:
        if n in root:
            return root[n]
    for p in paths:
        if p.lower().rsplit("/", 1)[-1] in README_NAMES and p.count("/") <= 1:
            return p
    return ""


def raw_url(owner, name, branch, path):
    br = urllib.parse.quote(branch or "main", safe="")
    p = urllib.parse.quote(path, safe="/")
    if FORGE["kind"] == "gitea":
        return "%s/%s/%s/raw/branch/%s/%s" % (BASE, owner, name, br, p)
    return "https://raw.githubusercontent.com/%s/%s/%s/%s" % (owner, name, br, p)


def asset_rank(a):
    """越小越优先：非变体包 → 名字带架构 → 体积大。"""
    n = a.get("name", "")
    return (1 if is_variant(n) else 0, 0 if arch_from_name(n) else 1,
            -int(a.get("size") or 0), len(n), n)


def fpk_assets(release):
    """该 Release 里的 FPK 资产：同 digest 去重（保留排名最好的名字），按优先级排序。"""
    seen = {}
    for a in release.get("assets") or []:
        if not (a.get("name") or "").lower().endswith(".fpk"):
            continue
        key = a.get("digest") or ("name:" + a["name"])
        old = seen.get(key)
        if old is None or asset_rank(a) < asset_rank(old):
            seen[key] = a
    return sorted(seen.values(), key=asset_rank)


def strip_html(s):
    s = re.sub(r"(?is)<(script|style)[^>]*>.*?</\1>", " ", s or "")
    s = re.sub(r"(?i)<br\s*/?>|</p>|</div>|</li>", "\n", s)
    s = re.sub(r"(?s)<[^>]+>", "", s)
    s = html.unescape(s)
    s = re.sub(r"[ \t\r\f\v]+", " ", s)
    s = re.sub(r"\n\s*\n+", "\n", s)
    return s.strip()


def clean_changelog(body, limit=300):
    s = (body or "").strip()
    if not s:
        return ""
    s = re.sub(r"(?m)^\s{0,3}#{1,6}\s*", "", s)
    s = re.sub(r"(?m)^\s*[-*+]\s+\[[ xX]\]\s*", "- ", s)
    s = re.sub(r"\n{3,}", "\n\n", s)
    if limit and len(s) > limit:
        # 优先断在整行，避免把一句话截成半截
        cut = s.rfind("\n", 0, limit)
        s = (s[:cut] if cut > limit // 2 else s[:limit]).rstrip() + "…"
    return s


def day(ts):
    return (ts or "")[:10]


def norm_categories(raw):
    out, bad = [], []
    for c in re.split(r"[,，/|]", raw or ""):
        c = c.strip()
        if not c:
            continue
        key = c.lower()
        val = CATS.get(key) or (c if c in CATS.values() else "")
        if val:
            if val not in out:
                out.append(val)
        else:
            bad.append(c)
    if bad:
        warn("忽略未知分类 %s（可选：%s）" % ("/".join(bad), "、".join(CATS.values())))
    return out


def entry_common(a, man, meta, owner, icon_url, readme_url, cats):
    raw_dn = clean_field(man.get("display_name"))
    if man.get("display_name") and not raw_dn:
        warn("包内 manifest 的 display_name 是未替换的构建变量（%s），已回退到 appname"
             % man.get("display_name"))
    forced = (a.app_type or "").strip().lower()
    if forced:
        app_type = forced
    else:
        flag = (man.get("app_type") or "").strip().lower()
        docker = (man.get("is_docker") or man.get("isdocker") or "").strip().lower()
        if flag in ("docker", "fpk", "native"):
            app_type = flag
        elif docker in ("true", "1", "yes", "y"):
            app_type = "docker"
        elif man.get("_compose"):
            app_type = "docker"
        else:
            app_type = "fpk"
    app = {
        "display_name": raw_dn or a.display_name or man.get("appname"),
        "app_type": app_type,
    }
    desc = strip_html(clean_field(man.get("desc"))) or (meta.get("description") or "").strip()
    if desc:
        limit = getattr(a, "desc_limit", 200) or 0
        app["desc"] = (desc[:limit].rstrip() + "…") if limit and len(desc) > limit else desc
    if cats:
        app["categories"] = cats
    lic = a.license or ((meta.get("license") or {}).get("spdx_id") or "")
    if lic and lic.upper() != "NOASSERTION":
        app["license"] = lic
    if clean_field(man.get("maintainer")):
        app["author"] = clean_field(man.get("maintainer"))
    if clean_field(man.get("maintainer_url")):
        app["author_url"] = clean_field(man.get("maintainer_url"))
    html_url = meta.get("html_url") or ""
    if man.get("maintainer_url") or html_url:
        app["homepage"] = man.get("maintainer_url") or html_url
    mu = clean_field(man.get("maintainer_url") or "")
    if mu.count("/") >= 4 and "github.com" in mu:
        app["bug_report_url"] = mu.rstrip("/") + "/issues"      # 指向作者自己的仓库
    elif html_url and meta.get("has_issues"):
        app["bug_report_url"] = html_url + "/issues"
    if icon_url:
        app["icon_url"] = icon_url
    if readme_url:
        app["readme_url"] = readme_url
    if clean_field(man.get("os_min_version")):
        app["min_fnos"] = clean_field(man.get("os_min_version"))
    if a.install_type:
        app["install_type"] = a.install_type
    return app


def pick_icon_url(a, owner, name, meta, paths, appname=""):
    if a.icon_url:
        return a.icon_url
    p = pick_icon(paths, appname)
    return raw_url(owner, name, a.branch or meta.get("default_branch"), p) if p else ""


def pick_readme_url(a, owner, name, meta, paths, appname=""):
    p = pick_readme(paths, appname)
    return raw_url(owner, name, a.branch or meta.get("default_branch"), p) if p else ""


def build_auto(a, owner, name, meta, releases, paths):
    """从 Release 资产 + 包内 manifest 直接组装索引（多应用 / 双架构都支持）。"""
    icon_path = pick_icon(paths)
    icon = a.icon_url or (raw_url(owner, name, a.branch or meta.get("default_branch"),
                                  icon_path) if icon_path else "")
    readme_path = pick_readme(paths)
    readme = raw_url(owner, name, a.branch or meta.get("default_branch"), readme_path) if readme_path else ""
    cats = norm_categories(a.categories)

    apps, archs, summary = {}, {}, []
    tmp = tempfile.mkdtemp(prefix="moo-invite-dl-")   # 临时下载目录：不污染产物目录
    scanned = 0
    for rel in releases[: max(1, a.releases)]:
        assets = fpk_assets(rel)[: max(1, a.max_assets)]
        if not assets:
            continue
        scanned += 1
        tag, pub = rel.get("tag_name") or "", day(rel.get("published_at") or rel.get("created_at"))
        chl = clean_changelog(rel.get("body"), a.changelog_limit)
        for asset in assets:
            url = asset.get("browser_download_url") or asset.get("url") or ""
            if not url:
                continue
            log("  ↓ %s（%.1f MB）" % (asset["name"], (asset.get("size") or 0) / 1e6))
            dest = os.path.join(tmp, re.sub(r"[^A-Za-z0-9._-]", "_", asset["name"]))
            digest = (asset.get("digest") or "").split(":")[-1].strip().lower()
            size, sha, via = download(url, dest, a.max_download_mb * 1024 * 1024, digest,
                                      mirrors=(FORGE["kind"] == "github"))
            if asset.get("size") and int(asset["size"]) != size:
                warn("%s 的 size 字段 %s 与实下 %d 不一致（以实下为准）"
                     % (asset["name"], asset["size"], size))
            man = read_manifest(dest)
            appname = man["appname"].strip()
            if not APPNAME_RE.match(appname):
                die("包内 appname %r 不符合协议命名（小写字母/数字/. - _）——请先修包再邀请。" % appname)
            ver = clean_field(man.get("version")) or re.sub(r"^[^0-9]*", "", tag)
            if not ver:
                die("%s 里没有 version，tag 也推不出——请用 --fpk-url 走手工模式。" % asset["name"])
            arch = normalize_arch(man.get("platform") or arch_from_name(asset["name"]))
            if appname not in apps:
                apps[appname] = entry_common(
                    a, man, meta, owner, pick_icon_url(a, owner, name, meta, paths, appname) or icon,
                    pick_readme_url(a, owner, name, meta, paths, appname) or readme, cats)
                apps[appname]["releases"] = {}
                archs[appname] = set()
            node = apps[appname]["releases"].setdefault(ver, {"packages": {}})
            if chl and not node.get("changelog"):
                node["changelog"] = chl
            if pub:
                node["updated_at"] = pub
            if arch in node["packages"]:
                warn("%s %s 的 %s 包出现多次，保留先到的一个" % (appname, ver, arch))
            else:
                node["packages"][arch] = {"download_url": asset_url(asset, tag, owner, name),
                                          "sha256": sha, "size": size}
                archs[appname].add(arch)
            summary.append("%s | %s | %s | %d B | %s | %s" % (
                appname, ver, arch, size, sha, via))
            os.remove(dest)
    shutil.rmtree(tmp, ignore_errors=True)
    if not apps:
        die("没在 Release 里找到可用的 .fpk 资产。\n"
            "  下一步：确认这个仓库的发布包在 GitHub Release 里；否则走手工模式（--fpk-url）。")
    for appname, app in apps.items():
        app["platform"] = sorted(archs[appname], key=lambda x: (x != "all", x))
        # releases 只保留最近若干版（与协议 / update_moo_json 口径一致）
        items = list(app["releases"].items())[: max(1, a.keep)]
        app["releases"] = dict(items)
    src_name = (list(apps.values())[0].get("display_name") if len(apps) == 1
                else "%s 应用源" % (meta.get("name") or name))
    doc = {
        "schema_version": "moo",
        "source_info": {
            "name": "%s（%s）" % (src_name, list(apps)[0]) if len(apps) == 1 else src_name,
            "author": owner,
            "homepage": meta.get("html_url") or "",
            "description": a.source_desc or ("%s 官方应用源。" % (meta.get("name") or name)),
        },
        "apps": apps,
    }
    stamp = day(releases[0].get("published_at") or "")
    if scanned and stamp:
        doc["source_info"]["updated_at"] = stamp
    return doc, summary, scanned


def validate(doc):
    errs = []
    if doc.get("schema_version") != "moo":
        errs.append('schema_version 必须是 "moo"')
    if not doc.get("apps"):
        errs.append("apps 为空")
    for appname, app in (doc.get("apps") or {}).items():
        if not APPNAME_RE.match(appname):
            errs.append("应用键 %r 不符合命名规范（字母数字 . - _）" % appname)
        rel = app.get("releases") or {}
        if not rel:
            errs.append("%s 没有 releases 节点" % appname)
        for ver, node in rel.items():
            pk = node.get("packages") or {}
            if not pk:
                errs.append("%s %s 没有 packages" % (appname, ver))
            for arch, p in pk.items():
                if arch not in ("x86", "arm", "all"):
                    errs.append("%s %s 的架构键 %r 不在 x86/arm/all 里" % (appname, ver, arch))
                u = p.get("download_url") or ""
                if not re.match(r"^https?://", u):
                    errs.append("%s %s 的 download_url 不是 http(s) 直链" % (appname, ver))
                if not re.match(r"^[0-9a-f]{64}$", (p.get("sha256") or "").lower()):
                    errs.append("%s %s 的 sha256 不是 64 位十六进制" % (appname, ver))
                if not isinstance(p.get("size"), int) or p["size"] <= 0:
                    errs.append("%s %s 的 size 必须是正的字节数" % (appname, ver))
        for c in app.get("categories") or []:
            if c not in CATS.values():
                errs.append("%s 的分类 %r 不在固定清单里" % (appname, c))
    if errs:
        die("生成的 moo.json 未通过自检：\n  - " + "\n  - ".join(errs))


def pkg_rows(app):
    rows = []
    for ver, node in app["releases"].items():
        for arch, p in node["packages"].items():
            rows.append((ver, arch, p["size"], p["sha256"], p["download_url"]))
    return rows


def render_issue(doc, owner, oneclick, mode, their_ver="", with_json=True):
    apps = doc["apps"]
    first = list(apps.values())[0]
    ver, node = next(iter(first["releases"].items()))
    archs = " / ".join(sorted(node["packages"]))
    if len(apps) > 1:
        archs = "%d 个应用：" % len(apps) + "、".join(apps)
    head = first.get("display_name") or list(apps)[0]
    if mode == "update":
        if their_ver and their_ver == ver:
            return ("@%s 你索引里的 `%s` 已经是 `%s`，那我直接按索引收录，这边不用改任何东西 👍\n\n"
                    "以后发新版时，`releases` 里加一个版本节点（或把 `moo-autoupdate.yml` 放进 "
                    "`.github/workflows/` 让它自动跟版）就行。想一条命令搞定：\n\n"
                    "```bash\n"
                    "curl -fsSL %s -o update_moo_json.py\n"
                    "python3 update_moo_json.py --version <新版本> --fpk <你的包> --keep 0\n"
                    "```\n\n"
                    "- `--keep 0` = 保留全部历史版本节点；不加则默认只留最近 2 版\n"
                    "- 老式索引（`releases.<版本>` 里没有 `packages`）不支持自动迁移，脚本会明确报错，"
                    "请先按 `packages.<架构>` 整理\n"
                    % (owner, list(apps)[0], ver, UPDATE_SCRIPT_URL))
        rows = "\n".join("| %s | %s | %.1f MB | `%s` | %s |" % (v, ar, s / 1e6, sh, u)
                         for v, ar, s, sh, u in pkg_rows(first))
        return ("@%s 感谢回复 🙏 仓库里已经有 `moo.json` 了，那就不用新建——只差把最新版本补进去。\n\n"
                "- 你索引里的 `%s`：`%s`\n- 本次 Release：`%s`\n\n"
                "两种补法任选：\n\n"
                "**1. 一条命令**（在你本机跑，改完自己提交）\n\n"
                "```bash\n"
                "curl -fsSL %s -o update_moo_json.py\n"
                "python3 update_moo_json.py --version %s --fpk <你的包> --keep 0 --dry-run  # 先看 diff\n"
                "python3 update_moo_json.py --version %s --fpk <你的包> --keep 0          # 确认后写入\n"
                "```\n\n"
                "- 不加 `--keep` 时默认只保留最近 2 个版本节点（会打印「裁剪旧版本节点：…」）；"
                "想留全部历史就带 `--keep 0`\n"
                "- 老式索引（`releases.<版本>` 里没有 `packages`、只有平铺下载字段）不支持自动迁移，"
                "脚本会明确报错退出，请先按 `packages.<架构>` 整理\n"
                "- raw 被墙时换镜像：`%s`\n\n"
                "**2. 手动**：`releases` 里加 `%s` 节点，`packages.<架构>` 填下表的三个值\n\n"
                "| 版本 | 架构 | 大小 | sha256 | 直链 |\n|---|---|---|---|---|\n%s\n\n"
                "如果索引已经是最新版，忽略这条即可，我按索引收录。\n"
                % (owner, list(apps)[0], their_ver or "（未匹配到）", ver,
                   UPDATE_SCRIPT_URL, ver, ver, UPDATE_SCRIPT_MIRROR, ver, rows))
    body = """@{owner} 打扰一下 —— 想把「{head}」收进 Moo（飞牛 fnOS 的第三方应用中心）。

索引就是**你仓库根目录**的一个 `moo.json`，以后由你维护；我们不建 fork、不提 PR，所以下面这条链接点一下 = 你自己提交：

### 👉 [一键创建 moo.json（文件名与内容都已填好）]({url})

打开 → 滚到底 → **Commit changes**，完事。

**里面填了什么**（可以先扫一眼再提交）

- 应用键 `{appname}`（与包内 manifest 一致），版本 `{ver}`
- 平台：{archs}
{rows}
- 之后发版只在 `releases` 里加一个版本节点；想全自动就把 `moo-autoupdate.yml` 放进 `.github/workflows/`

**提交之后我这边会做**：把仓库加进 Moo 的精选应用源，并在发现页推荐位放上这个项目。想换文件名或放子目录也行，回一句我改。

{fallback}""".format(
        owner=owner, head=head, url=oneclick, appname=list(apps)[0], ver=ver, archs=archs,
        rows="\n".join("- `%s`：%d 字节，`sha256=%s`" % (ar, s, sh)
                       for v, ar, s, sh, u in pkg_rows(first)),
        fallback=FALLBACK_JSON % json.dumps(doc, ensure_ascii=False, indent=1)
        if with_json else FALLBACK_ASK)
    return body


def render_mini(doc, owner, oneclick):
    """极简版：只留「一句话 + 一键链接 + 收尾」。给预填 issue URL 用（URL 有长度上限）。"""
    first = list(doc["apps"].values())[0]
    head = first.get("display_name") or list(doc["apps"])[0]
    return """@{owner} 想把「{head}」收进 Moo（飞牛 fnOS 的第三方应用中心）。

索引 = 你仓库根目录的一个 `moo.json`，以后由你维护；我们不建 fork、不提 PR，所以这条链接点开就是你自己的提交：

### 👉 [一键创建 moo.json（文件名与内容都已填好）]({url})

打开 → 滚到底 → **Commit changes**，完事。

收录后我会把这个仓库加进 Moo 的精选应用源和发现页推荐位；想换文件名或放子目录，回一句我改。
""".format(owner=owner, head=head, url=oneclick)


def shrink_doc(doc, desc_limit=120, changelog_limit=160):
    """一键链接太长时逐级瘦身（只动简介/日志，不动链接与版本信息）。

    desc_limit=0 表示删掉简介；changelog_limit=0 表示删掉更新日志。
    """
    for app in doc["apps"].values():
        d = app.get("desc") or ""
        if desc_limit == 0:
            app.pop("desc", None)
        elif d:
            app["desc"] = (d[:desc_limit].rstrip() + "…") if len(d) > desc_limit else d
        for node in (app.get("releases") or {}).values():
            c = node.get("changelog") or ""
            if changelog_limit == 0:
                node.pop("changelog", None)
            elif c:
                node["changelog"] = (c[:changelog_limit].rstrip() + "…") \
                    if len(c) > changelog_limit else c
    return doc


def build_manual(a):
    """手工模式：Release 不在 GitHub / 包不在 Release 里时用（字段全靠参数）。"""
    owner, name = repo_parts(a.repo, a.owner)
    miss = [k for k, v in (("--appname", a.appname), ("--display-name", a.display_name),
                           ("--version", a.version), ("--fpk-url", a.fpk_url),
                           ("--size", a.size), ("--sha256", a.sha256)) if not v]
    if miss:
        die("手工模式缺参数：%s\n  下一步：补上，或去掉 --fpk-url 走自动模式（只需 --repo OWNER/REPO）。"
            % "、".join(miss))
    if not re.match(r"^[0-9a-fA-F]{64}$", a.sha256):
        die("--sha256 必须是 64 位十六进制")
    platform = normalize_arch(a.platform or "all")
    app = {
        "display_name": a.display_name,
        "app_type": a.app_type or "fpk",
        "platform": [platform],
    }
    cats = norm_categories(a.categories or "实用效率")
    if cats:
        app["categories"] = cats
    app["license"] = a.license or "MIT"
    app["author"] = owner
    app["author_url"] = "%s/%s" % (BASE, owner)
    app["homepage"] = "%s/%s/%s" % (BASE, owner, name)
    app["bug_report_url"] = "%s/%s/%s/issues" % (BASE, owner, name)
    app["icon_url"] = a.icon_url or raw_url(owner, name, a.branch or "main",
                                            a.icon_path or "package/ICON_256.PNG")
    app["readme_url"] = raw_url(owner, name, a.branch or "main", "README.md")
    if a.desc:
        app["desc"] = a.desc
    if a.min_fnos:
        app["min_fnos"] = a.min_fnos
    if a.install_type:
        app["install_type"] = a.install_type
    app["releases"] = {a.version: {
        "changelog": a.changelog,
        "packages": {platform: {"download_url": a.fpk_url, "sha256": a.sha256.lower(),
                                "size": int(a.size)}}}}
    doc = {
        "schema_version": "moo",
        "source_info": {
            "name": "%s（%s）" % (a.display_name, a.appname),
            "author": owner,
            "homepage": "%s/%s/%s" % (BASE, owner, name),
            "description": a.source_desc or ("%s 官方应用源。" % a.display_name),
        },
        "apps": {a.appname: app},
    }
    summary = ["%s | %s | %s | %d B | %s | manual" % (
        a.appname, a.version, platform, int(a.size), a.sha256.lower())]
    return doc, summary, 1


CREATE_ISSUE_SH = '''#!/bin/sh
# 一条命令在 %(slug)s 开一张邀请 issue（避开 URL 长度限制；正文取同目录 issue_body.md）。
#   FORGE_TOKEN=xxx sh create_issue.sh
set -e
cd "$(dirname "$0")"
: "${FORGE_TOKEN:?先 export/内联 FORGE_TOKEN=<带 repo 写权限的 token>}"
python3 - "$FORGE_TOKEN" <<'PY'
import json, sys, urllib.request
tok = sys.argv[1]
title = open("issue_title.txt", encoding="utf-8").read().strip()
body = open("issue_body.md", encoding="utf-8").read()
req = urllib.request.Request(
    "%(api)s/repos/%(slug)s/issues",
    data=json.dumps({"title": title, "body": body}).encode("utf-8"),
    headers={"Authorization": "%(auth)s " + tok, "Accept": "application/json",
             "Content-Type": "application/json", "User-Agent": "moo-invite"})
print("created:", json.load(urllib.request.urlopen(req, timeout=30))["html_url"])
PY
'''


def write_outputs(a, doc, body, oneclick, issue_url, summary, mode, owner="", title="", slug=""):
    os.makedirs(a.outdir, exist_ok=True)
    jpath = os.path.join(a.outdir, "moo.json")
    open(jpath, "w", encoding="utf-8").write(json.dumps(doc, ensure_ascii=False, indent=1) + "\n")
    if oneclick:
        open(os.path.join(a.outdir, "oneclick.txt"), "w", encoding="utf-8").write(oneclick + "\n")
    open(os.path.join(a.outdir, "issue_body.md"), "w", encoding="utf-8").write(body)
    open(os.path.join(a.outdir, "issue_title.txt"), "w", encoding="utf-8").write(title + "\n")
    open(os.path.join(a.outdir, "issue_url.txt"), "w", encoding="utf-8").write(issue_url + "\n")
    if slug:
        sh = os.path.join(a.outdir, "create_issue.sh")
        with open(sh, "w", encoding="utf-8") as f:
            f.write(CREATE_ISSUE_SH % {"slug": slug, "api": API,
                                       "auth": "token" if FORGE["kind"] == "gitea" else "Bearer"})
        os.chmod(sh, 0o755)
    lead = ""
    if mode == "create" and owner:
        lead = ("@%s 感谢回复 🙏 那我把收录推进起来：索引放你仓库根目录、由你维护，"
                "我不建 fork、不提 PR，下面这条链接点一下就是你自己的提交。\n\n" % owner)
    open(os.path.join(a.outdir, "comment.md"), "w", encoding="utf-8").write(lead + body)
    open(os.path.join(a.outdir, "summary.txt"), "w", encoding="utf-8").write(
        "mode=%s\napplications=%d\noneclick=%s\nissue_url=%s\nlengths: oneclick=%d issue_url=%d\n\n%s\n" % (
            mode, len(doc.get("apps") or {}),
            "oneclick.txt" if oneclick else "-", "issue_url.txt",
            len(oneclick), len(issue_url), "\n".join(summary)))
    return jpath


def url_status(url, timeout=20):
    """GET 一次（不跟跳转）判断 GitHub 收不收这个 URL：302=正常，414=太长。"""
    class _NoRedir(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):  # noqa: D102
            return None

    try:
        r = subprocess.run(["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
                            "--max-time", str(timeout), "-A", UA, url],
                           capture_output=True, text=True, timeout=timeout + 5)
        code = (r.stdout or "").strip()
        if code.isdigit():
            return int(code)
    except Exception:  # noqa: BLE001  没有 curl 就走 urllib
        pass
    op = urllib.request.build_opener(_NoRedir)
    try:
        with op.open(urllib.request.Request(url, headers={"User-Agent": UA}), timeout=timeout) as r:
            return r.status
    except urllib.error.HTTPError as e:
        return e.code
    except Exception as e:  # noqa: BLE001
        return "ERR(%s)" % str(e)[:40]


def main():
    p = argparse.ArgumentParser(
        description="邀请上游作者把项目做成 Moo 应用源：一条命令出 moo.json + 一键链接 + issue 正文")
    p.add_argument("--repo", default="", help="OWNER/REPO 或完整仓库地址（自动模式只需这一个）")
    p.add_argument("--owner", default="", help="手工模式下的 owner（也可省略，写进 --repo）")
    p.add_argument("--appname", default="")
    p.add_argument("--display-name", default="", help="自动模式下用于覆盖包内 display_name")
    p.add_argument("--version", default="")
    p.add_argument("--fpk-url", default="", help="给了它就走手工模式")
    p.add_argument("--size", type=int, default=0, help="字节")
    p.add_argument("--sha256", default="")
    p.add_argument("--platform", default="")
    p.add_argument("--categories", default="", help="逗号分隔；取值见协议 §4.3")
    p.add_argument("--app-type", default="", choices=["", "fpk", "native", "docker"],
                   help="留空=自动识别（manifest 标记 / compose 文件）")
    p.add_argument("--license", default="")
    p.add_argument("--min-fnos", default="")
    p.add_argument("--install-type", default="")
    p.add_argument("--changelog", default="")
    p.add_argument("--desc", default="")
    p.add_argument("--source-desc", default="")
    p.add_argument("--icon-path", default="")
    p.add_argument("--icon-url", default="")
    p.add_argument("--branch", default="")
    p.add_argument("--outdir", default="")
    p.add_argument("--releases", type=int, default=1, help="扫最近几个 Release（默认 1）")
    p.add_argument("--keep", type=int, default=2, help="索引里保留几个版本节点（默认 2）")
    p.add_argument("--max-assets", type=int, default=4, help="每个 Release 最多读几个包（默认 4）")
    p.add_argument("--desc-limit", type=int, default=120, help="简介截断字数（0=不截；默认 120，控制一键链接长度）")
    p.add_argument("--changelog-limit", type=int, default=160, help="更新日志截断字数（0=不截；默认 160）")
    p.add_argument("--max-download-mb", type=int, default=300, help="单个包大小上限（默认 300）")
    p.add_argument("--include-prerelease", action="store_true")
    p.add_argument("--no-verify", action="store_true", help="跳过 URL 自检（少发两个请求）")
    p.add_argument("--forge-token", default="", help="Gitea/GitLab 等非 GitHub 站点的 token")
    p.add_argument("--forge-user", default="", help="非 GitHub 站点的用户名（配 --forge-pass）")
    p.add_argument("--forge-pass", default="", help="非 GitHub 站点的密码")
    p.add_argument("--dry-run", action="store_true", help="只打印，不写文件")
    p.add_argument("--print-json", action="store_true", help="把 moo.json 打到 stdout")
    a = p.parse_args()

    if not a.repo:
        die("必须给 --repo（自动模式写 OWNER/REPO；手工模式写仓库名并配 --owner）。")
    auto = not a.fpk_url
    owner, name = repo_parts(a.repo, a.owner)
    FORGE["token"] = a.forge_token or os.environ.get("GITEA_TOKEN") or ""
    FORGE["user"] = a.forge_user or os.environ.get("GITEA_USER") or ""
    FORGE["pass"] = a.forge_pass or os.environ.get("GITEA_PASS") or ""
    a.outdir = a.outdir or ("invite_%s" % re.sub(r"[^A-Za-z0-9._-]", "_", name))
    their_ver, mode, their_doc = "", "create", {}

    if auto:
        log("① 读仓库 %s/%s …" % (owner, name))
        meta = api("/repos/%s/%s" % (owner, name))
        if not meta:
            die("读不到仓库 %s/%s（不存在 / 私有 / 名字写错）。" % (owner, name))
        if meta.get("fork"):
            warn("这是 fork 仓库：Moo 收录讲究溯源，建议找到上游原始仓库再邀请。")
        a.branch = a.branch or meta.get("default_branch") or "main"
        try:
            import base64
            has = api("/repos/%s/%s/contents/moo.json?ref=%s" % (
                owner, name, urllib.parse.quote(a.branch, safe="")))
            if isinstance(has, dict) and has.get("content"):
                their = json.loads(base64.b64decode(has["content"]).decode("utf-8", "replace"))
            elif isinstance(has, dict) and has.get("download_url"):
                with urllib.request.urlopen(has["download_url"], timeout=20) as r:
                    their = json.loads(r.read().decode("utf-8", "replace"))
            else:
                raise LookupError("no moo.json")
            if isinstance(their, dict) and (their.get("apps") or {}):
                mode = "update"
                their_doc = their
            else:
                raise LookupError("moo.json 里没有 apps 条目")
        except Exception:  # noqa: BLE001
            mode = "create"
        log("   分支 %s｜%s" % (a.branch, "仓库里已有 moo.json（更新模式）" if mode == "update"
                              else "仓库里没有 moo.json（创建模式）"))

        log("② 读 Release …")
        rels = api("/repos/%s/%s/releases?%s" % (
            owner, name, "limit=30" if FORGE["kind"] == "gitea" else "per_page=30")) or []
        rels = [r for r in rels if not r.get("draft")]
        if not a.include_prerelease:
            rels = [r for r in rels if not r.get("prerelease")]
        if not rels:
            die("这个仓库没有可用 Release。\n  下一步：确认发布包在 Release 里，或用手工模式 --fpk-url。")
        log("   最新 %s，共扫 %d 个" % (rels[0].get("tag_name"), min(len(rels), a.releases)))

        log("③ 读仓库文件树（图标 / README）…")
        paths = repo_tree(owner, name, a.branch)

        log("④ 下载包并读 manifest …")
        doc, summary, _ = build_auto(a, owner, name, meta, rels, paths)
    else:
        doc, summary, _ = build_manual(a)

    validate(doc)
    if mode == "update" and their_doc:
        key = next(iter(doc["apps"]))
        tapp = (their_doc.get("apps") or {}).get(key) or {}
        their_ver = (next(iter(tapp.get("releases") or {}), "")
                     or clean_field(str(tapp.get("version") or "")))

    def make_oneclick():
        br = urllib.parse.quote(a.branch or "main", safe="")
        content = json.dumps(doc, ensure_ascii=False, indent=1) + "\n"
        if FORGE["kind"] == "gitea":
            # Gitea 的新建页不支持预填内容（实测 value/content 都被忽略）→ 只预填文件名
            return "%s/%s/%s/_new/%s?filename=moo.json" % (BASE, owner, name, br)
        return ("%s/%s/%s/new/%s?filename=moo.json&message=%s&value=%s" % (
            BASE, owner, name, br,
            urllib.parse.quote("Create moo.json (Moo app source index)", safe=""),
            urllib.parse.quote(content, safe="")))

    oneclick = make_oneclick() if mode == "create" else ""
    # 一键链接越长，GitHub 打开越慢（实测 5k 字符要 20s+，2k 以内 <1s）→ 按长度逐级瘦身
    for dl, cl, why in ((120, 160, "简介 120 字 / 日志 160 字"),
                        (100, 80, "简介 100 字 / 日志 80 字"),
                        (60, 0, "简介 60 字 / 去掉更新日志"),
                        (0, 0, "去掉简介与更新日志")):
        if not oneclick or len(oneclick) <= 4000:
            break
        shrink_doc(doc, dl, cl)
        validate(doc)
        oneclick = make_oneclick()
        warn("一键链接 %d 字符偏长，已瘦身（%s）后重算" % (len(oneclick), why))
    if oneclick and len(oneclick) > 4500:
        warn("一键链接仍有 %d 字符：部分网络下打开很慢，正文里已附 JSON 全文兜底。"
             % len(oneclick))
    body = render_issue(doc, owner, oneclick, mode, their_ver)
    title = "邀请：把「%s」做成一个 Moo 应用源（仓库根目录放一份 moo.json 即可）" % (
        list(doc["apps"].values())[0].get("display_name") or list(doc["apps"])[0])

    def issue_link(text):
        return "%s/%s/%s/issues/new?title=%s&body=%s" % (
            BASE, owner, name, urllib.parse.quote(title, safe=""), urllib.parse.quote(text, safe=""))

    issue_url = issue_link(body)
    for cand_body in (render_issue(doc, owner, oneclick, mode, their_ver, with_json=False),
                      render_mini(doc, owner, oneclick)):
        if len(issue_url) <= 4500 or not oneclick:
            break
        cand = issue_link(cand_body)
        if len(cand) < len(issue_url):
            issue_url = cand
    if len(issue_url) > 4500:
        # 超长 URL 在 GitHub 侧会被 414，实测 6.8k 起也慢到打不开 —— 只预填标题，正文用文件
        warn("正文预填不可用（issue 链接 %d 字符：GitHub 会 414 / 打开极慢）——"
             "issue_url.txt 改成「只预填标题」，发帖用 create_issue.sh 或粘贴 issue_body.md。"
             % len(issue_url))
        issue_url = ("# 正文预填不可用：正文里的一键链接 %d 字符，套进 issue URL 后超长（GitHub 414）。\n"
                     "# 推荐：sh create_issue.sh（走 API，无长度限制）；或打开下面链接（标题已填）粘贴 issue_body.md。\n"
                     "%s/%s/%s/issues/new?title=%s\n"
                     % (len(oneclick), BASE, owner, name, urllib.parse.quote(title, safe="")))
    if not a.no_verify:
        st = url_status(oneclick) if oneclick else "-"
        si = url_status(issue_url) if issue_url.startswith("http") else "skip"
        def fmt(x):
            return {0: "超时未判定", "-": "无", "skip": "跳过"}.get(x, str(x))
        log("⑤ 链接自检：一键链接 %s｜issue 预填 %s（302/200=正常，414=太长）"
            % (fmt(st), fmt(si)))
        if str(st) == "414":
            warn("一键链接被 GitHub 判为 414：把 --desc-limit / --changelog-limit 调小后重跑。")
        if str(si) == "414":
            warn("issue 预填链接 414：改用 create_issue.sh（走 API，无长度限制）。")

    if a.dry_run:
        print(json.dumps(doc, ensure_ascii=False, indent=2) if a.print_json else body)
        log("\n（--dry-run：没写文件）")
        return
    jpath = write_outputs(a, doc, body, oneclick, issue_url, summary, mode, owner, title,
                          "%s/%s" % (owner, name))
    log("\n模式：%s｜应用 %d 个｜产物目录 %s" % (mode, len(doc["apps"]), os.path.abspath(a.outdir)))
    for line in summary:
        log("  " + line)
    log("  moo.json → %s" % jpath)
    if oneclick:
        log("  一键链接 → oneclick.txt")
    log("  issue 正文 → issue_body.md｜标题 → issue_title.txt｜预填链接 → issue_url.txt")
    log("  一键开 issue → create_issue.sh（走 API，不受 URL 长度限制）")


if __name__ == "__main__":
    main()
