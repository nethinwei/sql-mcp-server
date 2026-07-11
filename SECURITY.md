# Security Policy

## 报告漏洞

**请勿在公开 Issue 中披露可被利用的安全问题。**

请通过 GitHub Security Advisories 私下报告（仓库 **Security → Advisories →
Report a vulnerability**），或联系维护者私下说明复现步骤与影响范围。

我们会在确认后尽快回复，并与报告者协调修复与披露时间。

## 安全模型

本项目的威胁模型、信任边界、默认 fail-closed 行为与 provider 差异，以
[`docs/security.md`](docs/security.md) 为单一真相源。部署与配置请参阅该文档及
[`docs/configuration.md`](docs/configuration.md)。

## 支持的版本

仅维护当前主分支上的最新代码；请使用 [CHANGELOG](CHANGELOG.md) 中记录的
已发布版本，并关注各版本的 [发布说明](docs/releases/) 中的迁移与不兼容变更。
