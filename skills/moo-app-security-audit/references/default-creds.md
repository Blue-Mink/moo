# 弱默认口令库

> 审计时用于快速比对「装完即有已知默认口令」的应用（对应「漏洞」维度）。
> 口径：这些默认口令**均来自官方文档/默认配置**，非凭据泄漏；风险在于用户装后不改密即被接管。
> 每审一个新应用发现的默认口令，追加到本表（保持 `appname | 默认账号 | 默认密码 | 来源` 四列）。

| 应用（appname） | 默认账号 | 默认密码 | 来源 |
|---|---|---|---|
| Lucky | 666 | 666 | 官方文档 lucky666.cn 明示 |
| nps | admin | 123 | 默认配置（auth_key=123） |
| lxserver | — | 123456 | 默认口令 |
| easytier | admin | admin | Web 控制台默认 |
| fpk-oneapi | root | 123456 | one-api 默认 |
| fpk-napcatqq | — | qq123456 | NapCat 默认 Token |
| allinssl | allinssl | allinssldocker | 官方镜像默认 |
| flatnas | admin | — | 默认账号（密码需首登设置） |
| lunatv | — | admin_password | 默认配置字段 |

## 审计时如何用

1. 解包 FPK 后，在 `config/`、`cmd/*`、二进制 `strings`、`README` 里 grep 上表口令与「默认」「初始」「password」「admin」等关键字。
2. 命中即确认「弱默认口令」风险，评级「漏洞」维度给 40-70（弱默认口令但可改），若同时公网暴露（端口转发/内网穿透）则叠加到 70+。
3. 报告中务必提醒用户「装后立即改密」。

## 已知安全（无默认弱口令，供对照）

- fn-knock：密钥运行时随机生成，无默认口令（安全网关范例）。
