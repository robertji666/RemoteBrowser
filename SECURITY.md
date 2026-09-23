# Security Policy / 安全策略

## 报告漏洞

请不要为尚未修复的漏洞创建公开 issue，也不要在公开讨论中附带凭据、用户数据或可直接利用的细节。

仓库创建后，维护者应在 GitHub 的 **Settings → Security & analysis** 中启用 Private vulnerability reporting。报告者可进入仓库的 **Security → Advisories → Report a vulnerability** 私下提交报告。如果该入口暂不可用，请通过维护者公开资料中标明的私密联系方式联系维护者。

报告请包含受影响版本、影响、复现步骤或最小 PoC、已知缓解措施及安全回复方式。维护者会尽快确认收到，在验证后协调修复与披露时间；请在双方约定公开时间之前保密。

## 支持范围

安全修复只保证进入最新发布版本和默认分支。部署者应持续更新宿主机、Docker、Caddy、Go 依赖、基础镜像和 Chromium，并在隔离环境验证升级。

## 安全边界与已知限制

- Manager 挂载 Docker socket，拥有宿主机 root 等价能力；只应运行在专用 VM。
- 设计目标是可信小团队，不是相互敌对的多租户浏览器执行平台。
- 登录当前没有 MFA。公网使用时建议增加 VPN、IP allowlist 或身份感知代理。
- 每个浏览器实例可以访问互联网；本项目不提供出站内容过滤或站点 allowlist。
- WebRTC 需要直达宿主机动态 UDP 端口；防火墙必须只放行实际范围。
- 当前没有每用户磁盘配额。部署者必须监控数据盘和 inode，防止磁盘耗尽。
- 宿主机管理员可以读取 SQLite、浏览器 profile、Cookie 和下载文件；必须保护磁盘、备份和快照。
- 浏览器沙箱需要 Session 容器放宽 Docker 默认 seccomp profile；容器仍以非 root 用户运行、丢弃全部 capabilities，并使用独立网络和资源限制。不能把容器隔离视为对内核零日漏洞的完整防护。

公网部署和加固步骤见 [docs/public-deployment.md](docs/public-deployment.md)。

---

## Reporting a vulnerability

Do not open a public issue for an unpatched vulnerability, and never attach credentials, user data, or immediately exploitable details to a public discussion.

After the repository is created, maintainers should enable **Private vulnerability reporting** under **Settings → Security & analysis**. Reporters can then use **Security → Advisories → Report a vulnerability**. If that entry is unavailable, contact a maintainer through a private channel listed in their public profile.

Include the affected version, impact, reproduction steps or a minimal proof of concept, known mitigations, and a secure reply method. Maintainers will acknowledge the report as soon as practical and coordinate remediation and disclosure. Keep the report private until the agreed disclosure date.

Security fixes are supported on the latest release and default branch. Deployers must keep the host OS, Docker, Caddy, Go dependencies, base images, and Chromium current.

The important boundaries and limitations listed in the Chinese section above apply to every deployment. See [docs/public-deployment.en.md](docs/public-deployment.en.md) for production hardening.
