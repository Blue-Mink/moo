# FPK 构建规范（目录结构 / manifest / 环境变量 / 生命周期脚本）

> manifest 与生命周期的官方权威契约以 fn-fpk-builder/references/official-contract.md 为准（若该技能可用）；本页为独立可用的速查版。
> Moo 专属字段/差异见 [moo-mate-notes.md](moo-mate-notes.md)；安装/卸载/配置/升级向导与 190s 看门狗见 [wizard-lifecycle.md](wizard-lifecycle.md)。

## 三、FPK 构建规范（完整版）

### 3.1 开发目录结构

```
my-app-fpk/
├── manifest                          # [必填] key=value（等号前后无空格）
├── cmd/                              # [必填] 生命周期脚本（chmod +x）
│   ├── main                          #   [必填] start/stop/status
│   ├── install_init / install_callback      # [可选] 安装前/后
│   ├── uninstall_init / uninstall_callback  # [可选] 卸载前/后
│   ├── upgrade_init / upgrade_callback      # [可选] 更新前/后
│   └── config_init / config_callback        # [可选] 配置前/后
├── config/                           # [可选] 权限与资源
│   ├── privilege                     #       权限声明（JSON）
│   └── resource                      #       资源声明（JSON）
├── wizard/                           # [可选] 向导 JSON
│   ├── install / uninstall / upgrade / config
├── app/                              # [必填] 应用文件 → target/
│   ├── docker-compose.yml
│   ├── ui/                           # [可选] 桌面图标 Web UI
│   │   ├── config
│   │   └── images/ (icon_0/64/128/256/512.png，全部 ≥512)
│   └── ...
├── ICON.PNG                          # [必填] ≥512 高清母版（详情弹窗；槽位见 3.11）
├── ICON_256.PNG                      # [推荐] ≥512（与 ICON.PNG 同一张）
└── .prerun.sh                        # [可选] 构建前脚本
```

### 3.2 开发目录 → 安装目录映射

| 开发时 | 安装后 | 环境变量 | 说明 |
|--------|--------|---------|------|
| `app/` | `target/` | `TRIM_APPDEST` | 可执行文件、UI 资源 |
| `cmd/` | `cmd/` | — | 生命周期脚本 |
| `config/` | `config/` | — | privilege + resource |
| `wizard/` | `wizard/` | — | 向导定义 |
| `manifest` | `manifest` | — | 应用元信息 |
| — | `var/` | `TRIM_PKGVAR` | 运行时数据（系统创建） |
| — | `etc/` | `TRIM_PKGETC` | 静态配置（系统创建） |
| — | `home/` | `TRIM_PKGHOME` | 用户数据（系统创建） |
| — | `tmp/` | `TRIM_PKGTMP` | 临时文件（系统创建） |
| — | `meta/` | `TRIM_PKGMETA` | 元数据（系统创建） |

> ⚠️ **关键**：`app/ → target/`，`ui/` 必须放 `app/ui/` 下！放在根目录会导致 fnpack 打包报 "no such file or directory"。

### 3.3 manifest 完整字段

```ini
# === 必填 ===
appname=com.example.myapp                # 唯一标识；实机约束 3–32 字符，发布后不要改
version=1.0.0                            # 新版 fnpack 要三段式 x.y.z；1.2.4 接受两段但发布统一三段
display_name=我的应用                      # 显示名
desc=应用描述                              # 支持 HTML，建议加“首次使用看日志”
source=thirdparty                        # [固定值]
maintainer=开发者                         # 维护者名
maintainer_url=https://example.com        # 可选
distributor=发布者                        # 发布者名（与开发者相同可省）
distributor_url=https://example.com       # 可选

# === 架构 ===
platform=x86                             # 字符串：x86 | arm | all（替代废弃的 arch）

# === 端口与服务 ===
service_port=8080                        # 服务端口
checkport=true                           # 常驻入口/守护型应用必须 false，否则停用后再启用报 11000
ctl_stop=true                            # 显示启停按钮

# === 安装 ===
install_type=                            # 空=用户选位置；root=系统分区
install_dep_apps=mariaDB:redis           # 依赖应用（冒号分隔，可写 name>version）
os_min_version=0.9.0                     # 最低系统版本
os_max_version=                           # 最高系统版本（空=不限）
disable_authorization_path=false         # 可选：隐藏授权目录设置

# === 桌面图标 ===
desktop_uidir=ui                         # UI 目录（相对 app/，默认 ui）
desktop_applaunchname=com.example.myapp.main  # 入口 ID，必须与 ui/config 的 .url 键一致

# === 应用类型/构建 ===
micro_app=false                          # Moo/Mate 等常驻微应用常用 true（实测字段，非官方全量文档）
beta=no                                  # 可选；发布稳定版写 no
changelog=修复已知问题                      # 更新日志（构建期可被 moo.json/releases 覆盖）
checksum=                                # 不要手填：fnpack build 自动写 app.tgz 的 MD5（32 hex）
tar_size=                                # 不要手填：1.2.4 不自动写，也不是必填
```

### 3.4 完整环境变量表

**路径变量**（cmd/ 脚本中可用）：

| 变量 | 说明 | 典型值 |
|------|------|--------|
| `TRIM_APPDEST` | 应用可执行文件目录 | `/vol[x]/@appcenter/[appname]` |
| `TRIM_PKGVAR` | 运行时数据目录 | `/vol[x]/@appdata/[appname]` |
| `TRIM_PKGETC` | 静态配置目录 | `/vol[x]/@appconf/[appname]` |
| `TRIM_PKGHOME` | 用户数据目录 | `/vol[x]/@apphome/[appname]` |
| `TRIM_PKGTMP` | 临时文件目录 | `/vol[x]/@apptemp/[appname]` |
| `TRIM_PKGMETA` | 应用元数据目录 | `/vol[x]/@appmeta/[appname]` |

**信息变量**：

| 变量 | 说明 |
|------|------|
| `TRIM_APPNAME` | 应用名称（来自 manifest appname） |
| `TRIM_APPVER` | 版本号 |
| `TRIM_OLD_APPVER` | 升级前版本号（仅 upgrade 脚本） |
| `TRIM_SERVICE_PORT` | manifest 声明的服务端口 |
| `TRIM_USERNAME` / `TRIM_GROUPNAME` | 应用专用用户/组 |
| `TRIM_RUN_USERNAME` | 实际运行用户 |
| `TRIM_TEMP_LOGFILE` | 错误日志路径（写入后系统以弹窗展示） |
| `TRIM_PKG_ARCH` | 当前安装架构 |
| `TRIM_APPDEST_VOL` | 应用安装卷路径 |
| `TRIM_APP_STATUS` | 平台记录的应用状态（不要只信它，结合进程/端口） |
| `TRIM_SYS_VERSION` / `TRIM_SYS_ARCH` / `TRIM_KERNEL_VERSION` | 系统版本/架构/内核 |
| `TRIM_DATA_SHARE_PATHS` / `TRIM_DATA_ACCESSIBLE_PATHS` | 已声明共享目录路径（Docker 卷优先用 `TRIM_PKGVAR`） |
| `TRIM_TEMP_UPGRADE_FOLDER` / `TRIM_PKGINST_TEMP_DIR` | 升级/安装临时目录（可能被清理，不要长期依赖） |
| `wizard_*` | 向导表单值（仅 callback 脚本） |

### 3.5 fnpack 下载与安装

```bash
# 方式一：手动下载
# 官方地址：https://developer.fnnas.com/docs/cli/fnpack
# 下载后可执行 chmod +x fnpack && sudo mv fnpack /usr/local/bin/

# 方式二：通过 fn-fpk-builder-skill 自动安装（推荐）
SKILL_DIR=/path/to/skills/fn-fpk-builder
python3 "$SKILL_DIR/scripts/fpk.py" toolchain --install --json
```

> **SKILL_DIR 常见位置**：
> - QwenPaw: `skills/fn-fpk-builder/`（在当前 workspace 下）
> - Codex: `$HOME/.codex/skills/fn-fpk-builder-skill/`
> - Claud: `$HOME/.claude/skills/fn-fpk-builder-skill/`

> **版本与命令边界（实测 1.2.4）**：`fnpack --help` 显示版本，但没有 `--version`；只有 `create` / `build` 等，**没有 `pack` / `crc32`**。发布构建统一 `fnpack build -d <staging>`；`manifest.checksum` 由它自动写（app.tgz 的 MD5）。下载 404 时用“已验证本地副本”兜底，版本+哈希必须与登记一致，禁止静默降级到未知版本。

### 3.6 cmd/main 脚本模板

```bash
#!/bin/sh
# main 只处理 start/stop/status；install/uninstall 放 install_init/callback、uninstall_init/callback。
# 启动/停止失败写 "${TRIM_TEMP_LOGFILE}" 并 exit 1；不认识的参数 exit 1。

start() {
    cd "${TRIM_APPDEST}/app" && docker compose up -d || exit 1
}

stop() {
    cd "${TRIM_APPDEST}/app" && docker compose stop || exit 1
}

status() {
    # Docker：以容器实际状态为准；native：PID 文件 + 进程/端口双重校验，防陈旧 PID/复用。
    if docker inspect -f '{{.State.Running}}' "${TRIM_APPNAME}" 2>/dev/null | grep -q true; then
        exit 0           # 运行中
    fi
    exit 3               # 未运行
}

case "$1" in start|stop|status) "$1" ;; *) exit 1 ;; esac
```

**退出码规则**：`status` → 0=运行中 / 3=未运行；错误写 `$TRIM_TEMP_LOGFILE` + exit 1。
平台实测：点「启用」只轮询 `status`，不会回调 `main start`；停用窗口内 `status=3` 会被判 `APP_CRASH`。常驻入口型应用要么 `status` 恒 0 + 入口页自己管开关，要么按官方契约接受停用语义。

### 3.7 回调流程

```
安装：install_init → 系统解压文件 → install_callback（wizard_* 在此可用）
卸载：uninstall_init → 系统删文件(保留var/shares) → uninstall_callback
更新：upgrade_init   → 系统替换文件 → upgrade_callback（TRIM_OLD_APPVER 在此可用）
配置：config_init    → 系统更新环境变量 → config_callback
```

- `install_callback` 约 **190 秒看门狗**：下载/建库/迁移等重活必须“秒回 + 后台 worker”，否则回调被斩首、`appcenter-cli install-fpk` 段错误。
- 平台实测点「启用」只轮询 `status`，不回调 `main start`；停用窗口 `status=3` 会被判 `APP_CRASH`。常驻入口型应用要么 `status` 恒 0 + 入口页自己管开关，要么按官方契约接受停用语义。
- `uninstall_callback` 只有在向导字段存在且为 `"true"` 时才删数据；没有该字段时删除分支永远不执行。

### 3.8 config/privilege 权限声明

```json
{
    "defaults": { "run-as": "package" },
    "username": "myapp",
    "groupname": "myapp"
}
```

`run-as`: `"package"` 是安全默认；`"root"` 仅在确有必要时使用（系统端口、AppCenter DB 同步、系统集成等）。长期服务启动前尽量降权；Moo/Mate 因需要写 AppCenter DB/系统路径而使用 root，不要把它当成所有应用的默认值。

### 3.9 config/resource 资源声明

只声明应用实际需要的资源，资源名跨版本保持稳定：

```jsonc
{
    // 共享目录：系统创建并按 ACL 授权；Docker 持久卷优先用 TRIM_PKGVAR
    "data-share": {
        "shares": [
            { "name": "myapp-data", "permission": { "rw": ["myapp"] } }
        ]
    },
    // 把稳定的 bin/lib/etc 接口链接到 /usr/local（按官方字段填写）
    "usr-local-linker": {},
    // 指向 app/ 下含 docker-compose.yaml 的项目目录
    "docker-project": { "path": "app" },
    // 自带 systemd 单元时声明（Moo/Mate 为空对象）
    "systemd-unit": {},
    // 端口协议文件：把端口/转发策略交给平台读取
    "port-config": { "protocol-file": "myapp.sc" }
}
```

Moo 额外使用 `"api-scope": ["trim.app.center"]`（平台注入 `TRIM_API_TOKEN`，用于免登录调应用中心 API）；这不是所有应用都需要，按实际能力声明。`port-config` 的协议文件常用 `port_forward="no"` 表示不做端口转发（Moo/Mate 的 `moo.sc` / `moo-mate.sc`）。

### 3.10 wizard 用户向导

四份向导：`install`（首装）、`uninstall`（卸载）、`config`（应用设置）、`upgrade`（升级）。完整字段/回调/数据边界见 [wizard-lifecycle.md](wizard-lifecycle.md)。

**安装向导（示例）**：

```json
[
    {
        "stepTitle": "许可与基本配置",
        "items": [
            { "type": "radio", "field": "wizard_license_agree", "label": "许可协议",
              "initValue": "", "options": [
                { "label": "不同意", "value": "" },
                { "label": "同意", "value": "同意" }
              ], "rules": [{ "required": true, "message": "请先同意许可" }] },
            { "type": "text", "field": "wizard_access_port", "label": "管理端口",
              "initValue": "8080", "rules": [
                { "required": true, "message": "必填" },
                { "pattern": "^[0-9]+$", "message": "只能数字" }
              ] },
            { "type": "text", "field": "wizard_download_dir", "label": "下载目录",
              "initValue": "downloads" },
            { "type": "text", "field": "wizard_log_path", "label": "日志文件名",
              "initValue": "app.log" },
            { "type": "tips", "helpText": "端口/路径安装后可在应用设置中修改" }
        ]
    }
]
```

**卸载向导（示例）**：

```json
[
    {
        "stepTitle": "卸载选项",
        "items": [
            { "type": "radio", "field": "wizard_delete_data", "label": "是否删除应用数据",
              "initValue": "false", "options": [
                { "label": "保留数据（推荐）", "value": "false" },
                { "label": "删除数据", "value": "true" }
              ] }
        ]
    }
]
```

**关键规则**：

- **`field` 就是环境变量名**：自定义字段直接写 `wizard_access_port`，回调读 `${wizard_access_port}`；平台不会再自动加 `wizard_` 前缀。
- `initValue` 一律写字符串；`radio`/`select` 用 `options`；`tips` 不需要 `field`。
- 类型：`text` `password` `radio` `checkbox` `select` `switch` `tips`；规则：`required` / `min` / `max` / `len` / `pattern`。
- **UI 规则不是安全边界**：callback 里必须二次校验端口 1–65535、路径禁 `..`、日志路径是文件且目录可建可写、目录 700/文件 600。
- 非交互安装用 `appcenter-cli install-fpk <fpk> --env <file>`，文件每行 `KEY=VALUE`（如 `wizard_license_agree=同意`）；传字符串会报 `Something wrong with environment variables`；重装必须照抄首次的 env。
- `wizard_*` 只在 callback 可用；`install_init` 不能假设有向导值。`config_init` 要输出当前真实值覆盖 `initValue`；`config_callback` 要 apply→restart→同步 AppCenter DB。
- 安装回调约 **190 秒看门狗**：重活必须“秒回 + 后台 worker”，否则回调被斩首、`appcenter-cli install-fpk` 段错误。

### 3.11 桌面图标 Desktop UI

```
app/ui/config                   # 入口配置文件（JSON）
app/ui/images/icon_0.png        # ≥512
app/ui/images/icon_64.png       # ≥512
app/ui/images/icon_128.png      # ≥512
app/ui/images/icon_256.png      # ≥512
app/ui/images/icon_512.png      # ≥512
```

```json
{
    ".url": {
        "com.example.myapp.main": {
            "title": "我的应用",
            "icon": "images/icon_{0}.png",
            "type": "url", "protocol": "http", "port": "8080", "url": "/", "allUsers": true
        }
    }
}
```

**统一网关入口（示例）**：

```json
{
    ".url": {
        "com.example.myapp.main": {
            "title": "我的应用",
            "icon": "images/icon_{0}.png",
            "type": "iframe",
            "gatewaySocket": "app.sock",
            "gatewayPrefix": "/app/myapp",
            "url": "/app/myapp/",
            "allUsers": true
        }
    }
}
```

**CGI 入口（示例）**：

```json
{
    ".url": {
        "com.example.myapp.main": {
            "title": "我的应用",
            "icon": "images/icon_{0}.png",
            "type": "iframe",
            "url": "/cgi/ThirdParty/myapp/index.cgi",
            "allUsers": true
        }
    }
}
```

- CGI 走平台自己的登录校验（无需自己实现登录），映射根是 `/var/apps/<appname>/target`（= `TRIM_APPDEST`）；脚本必须可执行。
- 尾斜杠两种写法都在线上存在（`/index.cgi` 与 `/index.cgi/`），按实际路由验证；**不支持 WebSocket**，不适合长请求/高流量。
- 既有 CGI 又想要 WebSocket 时：CGI 只做页面，实时通道另起端口或改统一网关。

**关键规则**：

- `.url` 下 key **必须以 `appname` 为前缀**，并等于 manifest 的 `desktop_applaunchname`；多入口 = 多个键（每个桌面图标一条），共用端口/进程。
- `{0}` 槽位名以 config 的 `icon` 字段为准；HD 方案要求 0/64/128/256/512 五槽位都放同一张 ≥512，不要分尺寸降采样（用 `images/ICON.PNG` 的项目铺大写 trio 即可）。
- 端口入口：`type=url` + `protocol` + `port`（**字符串**）+ `url`，`port` 与 manifest `service_port` 一致。
- 网关入口：`type=iframe` + `gatewaySocket`（只写 socket 文件名，服务在 `${TRIM_APPDEST}` 创建）+ `gatewayPrefix`（`/app/<appname>`）；不要写 `port`。
- 网关 socket 的解析：`gatewaySocket` 只是文件名，平台落到 `${TRIM_APPDEST}/<文件名>`（`/var/apps/<app>/target` 是指向 `/vol1/@appcenter/<app>` 的软链）。代理以 root 连接，socket 0700 可用；应用以包用户运行时要放宽到 0666/0777。
- 网关**不改写路径**：应用收到的仍是 `/app/<appname>/...`，所以路由/静态资源/WebSocket 都要挂在该前缀下（实测带前缀 200、去前缀 404）。
- `noDisplay` 为 `false` 才显示；`allUsers` 控制所有用户可见；`fileTypes`/`control.accessPerm` 按官方契约。
- 其它实测字段：`width`/`height` 控制 iframe 弹窗尺寸；`fileTypes` 是**不带点**的扩展名数组，常配 `noDisplay:true` 注册文件关联；`${entry.title}`/`${entry.desc}` 是平台占位符；`microApp` 语义未验证，不要照抄。
- HTTPS 页面勿配 `protocol: "http"`（协议降级+端口不可达）；网关身份头是 `X-Trim-Userid`/`X-Trim-Isadmin`/`X-Trim-Username`，业务权限仍要应用自己校验。

### 3.12 错误处理

```bash
echo "错误信息" > "${TRIM_TEMP_LOGFILE}"
exit 1
```

系统以 Dialog 对话框展示给用户。

### 3.13 Bash 脚本黄金 5 条

1. 🔒 **变量加双引号**：`"$TRIM_PKGVAR"`（防路径含空格）
2. 📝 **错误写日志文件**：`"${TRIM_TEMP_LOGFILE}"`（不 echo stdout）
3. 🔢 **明确退出码**：成功 0 / 失败 1 / status 未运行 3
4. 🏗️ **用环境变量构建路径**：`/var/apps/${TRIM_APPNAME}`（不硬编码）
5. ⚠️ **heredoc 注意转义**：用户输入特殊字符可能破坏 JSON

### 3.14 架构选择（Docker vs 原生）

| 应用类型 | manifest `platform` | 说明 |
|---------|---------------------|------|
| **Docker 多架构镜像**（如 `exampleuser/multiarch-app`（示意）） | `all` | 镜像自身支持多架构，fpk 仅编排 |
| **Docker 单架构镜像** | `x86` 或 `arm` | 镜像仅支持一种架构 |
| **原生 Go/Rust 编译** | `x86` 或 `arm` | 必须匹配二进制 ELF 架构，不能混用 |
| **纯脚本 / 静态资源** | `all` | 无架构依赖 |

> **关键判断**：`manifest.platform` 是**字符串** `x86|arm|all`，不是数组；数组只属于 Moo/FnDepot 索引字段。如果包内有原生二进制（ELF），必须声明具体架构，不能用 `all`。Docker 应用的架构由镜像决定，fpk 只负责编排，可以用 `all`。

**构建命令（不要只打 x86）**：

```bash
# 原生二进制按目标架构分别编译，不能复用同一份
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o artifacts/amd64/app-server ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o artifacts/arm64/app-server ./cmd/server

# ARM64 单架构：产物 ...-fnos-arm64.fpk，manifest platform=arm，ELF=EM_AARCH64(183)
python3 "$SKILL_DIR/scripts/fpk.py" build --project ./pkg --out ./dist \
  --arch arm64 --overlay-arm64 ./artifacts/arm64 --json
# x86 单架构：--arch amd64 --overlay-amd64 ./artifacts/amd64
# 双架构：--arch both --overlay-amd64 ./artifacts/amd64 --overlay-arm64 ./artifacts/arm64

# 验收
python3 "$SKILL_DIR/scripts/fpk.py" inspect ./dist/app-1.0.0-fnos-arm64.fpk \
  --target-arch arm64 --json
```

`--arch` 只接受 `amd64` / `arm64` / `all` / `both`；`x86`、`arm` 会被参数校验拒绝。
发布到 Moo 时两个架构的包、`.sha256`（和签名侧车）都要上传，`moo.json` 的
`releases.<版本>.packages.x86` / `packages.arm` 分开写；`update_moo_json.py --fpk` 可一次传两个包。

### 3.16 docker-compose.yml 模板

```yaml
version: '3'
services:
  my-app:
    image: username/my-app:latest
    container_name: com.example.myapp
    ports:
      - "${PORT:-32679}:3000"
    volumes:
      - ${TRIM_PKGVAR:-/var/apps/com.example.myapp/data}:/app/data
    environment:
      - PORT=${PORT:-3000}
      - COOKIE_SECURE=false          # HTTP 下必须！
    restart: always
    healthcheck:                     # ✅ fnOS 依赖此判断运行状态
      test: ["CMD", "curl", "-f", "http://localhost:3000/api/health"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 20s
```

> **端口说明**：`${PORT:-32679}` 是**宿主机端口**（用户通过此端口访问），`:3000` 是**容器内端口**（应用监听端口）。wizard 让用户配置的是宿主机端口。

### 3.17 构建产物 gzip -9 重压（可选，Moo 的做法）

- Moo `build.sh` 有 `repack_fpk`：解包 FPK → 对 `app.tgz` 做 `gzip -9 -n` → 把新的 `md5sum app.tgz` 写回 `manifest.checksum` → 重打外层；成员数和目录结构不变。
- `-n` 去掉时间戳，便于复现；重压后必须重算 MD5，否则 `fnpack`/安装校验会失败。
- Mate `build-mate.sh` 目前没有这一步；若加，按同一流程，并重新验证包结构/校验和。
- 不要用“重压”来改包内容；它只减小体积，不改变功能/权限/槽位。
