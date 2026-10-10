# FPK 构建与本地验证（发布 Moo 前的必经阶段）

> 目的：把应用打成 **fnOS 可安装的 FPK**，并在本机/测试机装一次验证通过，再去写 `moo.json` 发布。
> 深度构建规范（完整字段表 / 环境变量 / 生命周期 / 资源声明）见姊妹技能 **`fn-fpk-builder`**；
> 本文件只给「发布 Moo 需要知道的那部分」+ 高频迭代做法。

## 一、开发目录与打包产物

```
<app>/
├── manifest            # 应用清单（appname/version/display_name/desc/…，见 §二）
├── ICON.PNG            # 256×256 PNG 图标（列表/桌面都用它）
├── app.tgz             # 应用主体（打包时由源码/运行目录生成）
├── cmd/                # 生命周期脚本（main/start/stop/…，见 §四）
└── config/             # 声明式配置：privilege / resource（见 §五）
```

打完包得到 `moo_<version>_x86.fpk`（fnOS 安装包）。**FNpack 首次使用会自动下载**（`fnpack` 由飞牛提供）。

## 二、`manifest` 关键字段（与 `moo.json` 必须一致）

| 字段 | 说明 | 与 Moo 的关系 |
|---|---|---|
| `appname` | 应用唯一标识 | **必须与 `moo.json` 里 `apps` 的 key 完全一致**（否则安装/更新匹配失败） |
| `version` | 版本号 | 建议与 `moo.json` 的 `version` 一致；写法规则见协议 §5.3 |
| `display_name` / `desc` | 展示名与简介 | 生成索引时自动带入 |
| `author` / `maintainer` / `distributor` | 开发者 / 维护者 / 发布者 | 生成索引时自动带入 |
| `platform` | `x86` / `arm` | 决定包的适用架构 |
| `service_port` | 服务端口 | 生成索引时自动带入，详情页显示 |

```ini
# 示例（`key = value`，行内 `#` 为注释）
appname               = demo-app
version               = 1.2.0
display_name          = 示例应用
desc                  = 这是一个示例应用
author                = Example Org
distributor           = Example Org
platform              = x86
service_port          = 8090
```

## 三、图标要求

- `ICON.PNG`：**PNG 格式、256×256、直角方形**（不要圆角/透明留白过多），飞牛列表与桌面统一用它；
- 需要额外尺寸可放 `ICON_256.PNG` 等，但**至少要有 `ICON.PNG`**；
- 生成索引时若 FPK 同目录存在 `ICON.PNG`，`gen-moo-json` 会自动写进 `icon_url`。

## 四、生命周期脚本 `cmd/`

| 脚本 | 何时被调用 |
|---|---|
| `cmd/main` | 安装/升级时执行（初始化数据目录、写默认配置） |
| `cmd/start` / `cmd/stop` / `cmd/restart` | 启停控制 |
| `cmd/uninstall` | 卸载清理 |

约定：脚本里用 `$TRIM_APPDEST` / `$TRIM_PKGVAR` / `$TRIM_PKGETC` 等环境变量定位安装目录与数据目录，
**不要写死绝对路径**；日志写到应用数据目录，便于 `moo.json` 之外的用户排障。

## 五、声明式配置 `config/`

| 文件 | 作用 | 与 Moo 的关系 |
|---|---|---|
| `config/privilege` | 运行身份与权限（`{"defaults":{"run-as":"root"}}`） | **决定详情页「运行方式」**：`root` → 显示 root；`package` → 显示「用户空间」 |
| `config/resource` | 需要的系统资源（端口/设备/共享等） | 安装时被平台校验 |

## 六、构建命令

```bash
# 自动化（项目自带 build.sh：前端构建 + Go 编译 + 打包，最常用）
./build.sh x86                 # 产出 moo_<version>_x86.fpk

# 手动（fnpack）
fnpack build -d <开发目录>       # 生成 <appname>.fpk
```

**发布前必做**：

```bash
# 1) 包结构自检：manifest 与 ICON 必须存在
tar -tzf app.fpk | head            # 或解包检查 manifest / ICON.PNG / app.tgz
# 2) 记下校验和（写进 moo.json 用）
sha256sum <appname>.fpk
# 3) 单元测试（Go/前端项目）
go test ./...   &&   (cd frontend && npm test)
```

## 七、本地安装验证（**别跳到发布**）

```bash
# 传包到测试机
scp <appname>.fpk root@<NAS>:/tmp/

# 装/升级（fnOS 面板里手动装，或用 appcenter-cli）
ssh root@<NAS> 'appcenter-cli install /tmp/<appname>.fpk'
# 热替换（改代码后只换二进制，开发阶段最快）
ssh root@<NAS> 'appcenter-cli stop <appname> && cp /tmp/<appname>-server <安装目录>/ && appcenter-cli start <appname>'
```

验证清单：

1. 面板里能装、能启、状态正常，数据目录生成正确；
2. 应用自身功能能用（打开页面、关键接口 200）；
3. **版本号与 `manifest` 一致**（否则 Moo 的更新判断会错）；
4. 卸载/重装数据是否保留符合预期。

## 八、构建/安装阶段的常见失败

| 现象 | 原因 | 解决 |
|---|---|---|
| 打包报缺 `manifest` / `ICON.PNG` | 开发目录结构不对、图标文件名大小写不符 | 放在根目录并严格用 `ICON.PNG` |
| 面板安装失败「appname 冲突」 | `appname` 与已装应用重复 | 换唯一 appname（发布后不要再改） |
| 装上了但启不来 | `cmd/main`/`start` 权限位不正确或路径写死 | `chmod +x cmd/*`；用环境变量定位目录 |
| 端口被占/外部访问不通 | `service_port` 与实际监听不一致 | 改 `manifest` 的 `service_port` 或应用监听端口 |
| 升级后配置丢失 | `cmd/main` 覆盖了数据目录 | 升级流程里做「存在即保留」判断 |
| 详情页「运行方式」与预期不符 | `config/privilege` 写的是 `package` | 想要 root 就写 `run-as: root`（协议 §4.5 的自适应展示规则） |

## 九、下一步

包与本地验证都过了 → 去写索引并发布：见 [publish-workflow.md](publish-workflow.md) 与
[docs/Moo应用源协议.md](../../../docs/Moo应用源协议.md)。
