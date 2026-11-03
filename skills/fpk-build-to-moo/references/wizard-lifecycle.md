# 向导、生命周期与安装/卸载/升级运行链

> 适用：任何带 `wizard/` 或 `cmd/` 的 FPK。命令以 fnOS 实机为准：
> `appcenter-cli install-fpk <fpk> --volume <N> --env <file>`（必须 root）。
> 本文不写设备地址/凭据；示例统一用 `<app>`、`<NAS>`、`example.com`。

## 0. 先记五条
1. **`field` 就是环境变量名**：自定义字段写 `wizard_access_port`，回调读 `${wizard_access_port}`；平台不会自动加 `wizard_`。
2. **`--env` 是文件**：每行 `KEY=VALUE`；传字符串会报 `Something wrong with environment variables`；重装必须照抄首次那份。
3. **`appcenter-cli install-fpk` 对已装应用是 no-op**：rc=0 但包没换；换版走 停→卸→`check`→装→启，或 daemon RPC。
4. **`wizard_*` 只在 callback 可用**：`install_init` 里读不到；`install_callback` 才有。
5. **安装回调约 190 秒看门狗**：下载/建库/迁移必须“秒回 + 后台 worker”，否则回调被斩首、`appcenter-cli install-fpk` 段错误。

## 1. 四份向导
| 文件 | 触发 | 主要用途 |
|---|---|---|
| `wizard/install` | 首次安装 | 许可、端口、数据/日志路径、初始账号 |
| `wizard/uninstall` | 卸载 | 是否删除数据（`wizard_delete_data`） |
| `wizard/config` | 应用设置 | 修改端口/路径/参数；`config_init` 回显当前值 |
| `wizard/upgrade` | 升级 | 版本迁移选项；`TRIM_OLD_APPVER` |

每份都是“步骤数组”：`[{"stepTitle":"...","items":[...]}]`。item 通用字段：
`type`、`field`、`label`、`initValue`（字符串）、`options`、`rules`、`helpText`。
类型：`text` `password` `radio` `checkbox` `select` `switch` `tips`。
规则：`required` / `min` / `max` / `len` / `pattern`（JS 正则）。

## 2. 安装向导
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
      { "type": "text", "field": "wizard_data_dir", "label": "数据目录",
        "initValue": "data" },
      { "type": "text", "field": "wizard_log_path", "label": "日志文件名",
        "initValue": "app.log" },
      { "type": "tips", "helpText": "端口/路径安装后可在应用设置中修改" }
    ]
  }
]
```

### 2.1 字段命名
- 平台字段：`wizard_access_port`、`wizard_download_dir`、`wizard_log_path` 等，按设备实际面板字段为准。
- 自定义字段：直接写 `wizard_<name>`；回调读同名变量。
- 面板安装与 CLI 安装的键名可能不同（如 `wizard_access_port` / `access_port`）；回调要**双收**：
  `PORT="${wizard_access_port:-${access_port:-8080}}"`。
- `initValue` 一律写字符串；`radio`/`select` 的值也用字符串（`"true"`/`"false"`，不要布尔）。

### 2.2 非交互安装
```bash
# /tmp/<app>.env，权限 600，不要回显内容
cat > /tmp/<app>.env <<'EOF'
wizard_license_agree=同意
wizard_access_port=8080
wizard_data_dir=data
wizard_log_path=app.log
EOF
chmod 600 /tmp/<app>.env

appcenter-cli install-fpk /path/<app>.fpk --volume <N> --env /tmp/<app>.env
```

- 不传 `--env`：向导用 `initValue`（或平台默认），不要假设用户点过。
- 传字符串：`--env "wizard_access_port=8080"` 会失败；必须是文件路径。
- 重装/换版：照抄首次的 env，否则会静默改掉端口/路径。
- `appcenter-cli install-fpk` 还支持 `--volume <N>`；`appcenter-cli default-volume` 查默认卷。

### 2.3 二次校验（callback 必做）
- 端口：整数、1–65535、未被占用；管理端口与代理端口不能相同。
- 路径：禁 `..`、禁绝对路径逃逸；目录可建可写；日志路径必须是文件（不能是目录）。
- 权限：目录 700、文件 600；`umask` 可能让新建文件变成 000，显式 `chmod`。
- 许可：只有“向导确实运行”时才强制；非交互 CLI 没有 `wizard_*` 时跳过，避免误伤。
- 留痕：写 `wizard-install.json`（版本/时间/路径/许可输入），权限 600；不要记录密码/token。

## 3. 卸载向导与数据边界
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

- 只有字符串 `"true"` 才删；`"false"`/空/缺字段一律保留。
- 删除范围只允许：`/vol*/@appdata/<app>`、`@appconf/<app>`、`@appmeta/<app>`、`@apphome/<app>` 的**精确目录**；不碰用户下载目录，不整目录递归删除。
- 自定义数据目录：只删应用自己的 `index.json`、`config.json`、`backups/` 等文件；不删父目录。
- 外部副作用：`/etc/hosts` 标记行、Docker `daemon.json` 镜像条目、进程/容器/临时文件；改前备份、只删自己加的、幂等。
- 无 `wizard_delete_data` 字段时，生命周期里的删除分支永远不会执行——不要写“卸载一定清空/一定保留”的绝对结论。
- **卸载前备份** `@appdata`（运行中归档加 `--warning=no-file-changed`）；卸载后 `appcenter-cli check` 确认真的没了，不能只看 rc=0。
- 带卸载向导的应用，CLI `appcenter-cli uninstall` 可能被拒（`code 10150` = 本地应用禁止卸载）：走 daemon socket `POST /rpc/v1/uninstall/task`（`wizard_delete_data=false`）→ 轮询 `/rpc/v1/common/status`；任务完成后端点可能 404，以 `check` + 安装目录消失为准。
- 某些 native 应用卸载会清空 `@appconf`；升级/换包前强制备份配置目录。

## 4. config / upgrade 向导
### 4.1 `config_init` 回显当前值
- 不能只依赖 `wizard/config` 的静态 `initValue`；保存后要显示当前真实值。
- `config_init` 读取应用自己的配置/`ports.conf`/`ui/config`，输出：
  ```sh
  echo "wizard_access_port=${CURRENT_PORT}"
  echo "wizard_data_dir=${CURRENT_DATA_DIR}"
  ```
- 平台用这些输出覆盖向导初值；不输出 = 回显旧默认值。

### 4.2 `config_callback` 应用 + 重启 + 同步 DB
顺序必须是：校验 → 写应用配置 → 重启服务 → 同步 AppCenter DB → 延迟回写状态。
- 同步 `app.service_url` 与 `app_service.url/default_url`（端口/入口变了，面板入口不能停旧端口）。
- `run-as=package` 可能无权写 PostgreSQL；需要同步 DB 的应用用 `run-as=root`（Moo/Mate 即如此）。
- 重启后延迟 3/8/15 秒回写 `status=running`，避免面板显示 stopped。
- 真实验证必须走 AppCenter UI 或 `POST /app-center/v1/config/wizard`；只手工跑 `config_callback` 不算。
- 反向验证：改端口 → 保存 → 旧端口不再响应、新端口响应、面板入口/`config/detail`/实际监听三处一致；再改回。

### 4.3 `upgrade_init` / `upgrade_callback`
- `TRIM_OLD_APPVER` 只在 upgrade 脚本可用；用它判断数据迁移/配置升级。
- 同版本升级会被拒；先 bump manifest `version`。
- 迁移前备份 `@appdata`/`@appconf`；迁移脚本幂等；失败写 `TRIM_TEMP_LOGFILE` + exit 1。
- 升级完成后核对：DB 版本、运行进程版本、数据指纹、入口端口。

## 5. 生命周期与 `cmd/main`
### 5.1 顺序
```
安装：install_init → 解压 → install_callback（wizard_* 可用）
卸载：uninstall_init → 删文件（保留 var/shares）→ uninstall_callback
更新：upgrade_init → 替换文件 → upgrade_callback（TRIM_OLD_APPVER 可用）
配置：config_init → 更新 env → config_callback
```

### 5.2 `cmd/main`
- 只处理 `start` / `stop` / `status`；`install`/`uninstall` 逻辑放回调，不要放 main。
- `status`：0=运行中，3=未运行；错误写 `TRIM_TEMP_LOGFILE` + exit 1。
- PID 文件要防陈旧 PID/PID 复用；Docker 用容器实际状态，native 用进程/端口双重校验。
- 平台实测：点「启用」只轮询 `status`，不回调 `main start`；停用窗口 `status=3` 会被判 `APP_CRASH`。常驻入口型应用要么 `status` 恒 0 + 入口页自己管开关，要么按官方契约接受停用语义。
- `cmd/*` 必须 `chmod +x`；`main` 不认识的参数 exit 1。

### 5.3 190 秒看门狗
- `install_callback` 里同步跑下载/建库/迁移，超过约 190 秒会被平台斩首，`appcenter-cli install-fpk` 自身可能段错误。
- 正确做法：callback 只做校验 + 写状态文件 + 拉起后台 worker，然后立即 exit 0；worker 用 `systemd-run`/`setsid`，带锁文件和状态机。
- 重活失败要写状态文件/日志，用户可在应用页看到；不要静默。

## 6. 标准安装 / 卸载 / 升级运行链
### 6.1 完整卸载重装（改注册信息/向导/端口）
```text
备份 @appdata → stop → uninstall → appcenter-cli check 确认没了
→ appcenter-cli install-fpk --volume <N> --env /tmp/<app>.env
→ start（10500 忽略）→ 等冷启动
→ status + manifest.version + 载荷 md5 + 端口 HTTP + journal
```
- 装后四铁律：新 PID / 运行态二进制 sha == 包内 / 版本 + 前端 bundle 指纹 / 数据零回归（源数/卡数/已装数/禁用集合逐字一致）；冷启动前几秒偏低是瞬态，不是数据丢失。
- `appcenter-cli install-fpk` 对已装应用是 no-op；不要用“覆盖安装”换版。
- `start` 报 10500 = 平台已自动拉起，不是失败；不要重试。
- 卸载 rc=0 也可能没卸；必须 `check`。
- `code 10111` = `appcenter-cli install-fpk` 被拒，走人工安装路径。

### 6.2 daemon RPC 就地升级（保数据）
```text
记数据指纹（config sha256 + 数据目录项数）
→ FPK 放持久目录 → 检查 canUpgrade（同版本先 bump）
→ POST /rpc/v1/update/task（/var/run/com.trim.app.center.sock）
→ daemon 停应用→换包→恢复
→ 核对 DB 版本 + 数据指纹 + 进程 + 运行进程 /api/version
```
- daemon 换包后不一定重启进程：DB 新版、进程旧版时，精确 pid 直杀 + 平台 start 拉起。
- `/rpc/v1` 非契约接口，fnOS 大版本可能变，用前探测。

### 6.3 二进制热替换（开发）
- `ss -tlnp` 找真实 pid → SIGTERM → 等退出；运行中二进制不能 cp 覆盖（text file busy），先停或 `mv` 原子替换。
- 用 `cmd/main` 带全套 `TRIM_*` 拉起；验证 `/api/version` + 核心 API。

## 7. 错误码速查
| 码/现象 | 含义 | 处理 |
|---|---|---|
| `10500` | start 时应用已在运行 | 假错，不重试；看 status/端口/HTTP |
| `10150` | 本地应用禁止卸载 | 面板/人工路径；不要反复重试 |
| `10111` | `appcenter-cli install-fpk` 被拒 | 人工安装路径/检查包与权限 |
| `11000` / `APP_START_FAILED_PORT_USAGE` | 端口被占用/常驻入口被 checkport 误判 | 清残留进程；常驻入口 `checkport=false` |
| `APP_INSTALL_FAILED_INSTALL_CALLBACK_EXCEPTION` | 安装回调超时/异常 | 回调秒回 + 后台 worker |
| `APP_CRASH` | status 长期 3 或无监管 | 查进程/端口/日志；常驻型 status 语义按设计 |
| `text file busy` | 运行中二进制被覆盖 | 先停进程或 mv 原子替换 |

## 8. 发布前检查
- [ ] `wizard/install` 的 `field` 都是环境变量名；`--env` 文件能装上
- [ ] `wizard_delete_data` 默认 `"false"`；删除分支只删精确目录
- [ ] `config_init` 回显当前值；`config_callback` 同步 DB/重启
- [ ] `cmd/main` 只处理 start/stop/status；`status` 0/3 正确
- [ ] 190 秒内 callback 返回；重活在后台
- [ ] 完整卸载重装后数据保留；删除分支只删自己的数据
- [ ] `appcenter-cli check`/`status`/HTTP/版本/载荷 md5 全过

