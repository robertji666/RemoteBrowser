# RemoteBrowser

[简体中文](README.md) | [English](README.en.md)

RemoteBrowser 是一个面向个人和可信小团队的自托管多用户远程浏览器平台。管理员创建邮箱账号并分配实例配额；每个浏览器实例拥有独立的 Chromium profile、Cookie、站点存储和下载目录。

实例默认持续运行。关闭页面、退出登录、改密码和长时间无人连接都不会回收实例。用户可以手动停止、再次启动或明确删除；停止仍占配额。删除实例，或管理员二次确认删除用户，才会销毁对应容器和持久数据。

详细需求与验收条件见 [多用户与持久实例需求](PRD-MULTIUSER-PERSISTENT-INSTANCES.md)。

> [!IMPORTANT]
> Manager 挂载 Docker socket，因此实际上拥有宿主机 root 等级的控制能力。请部署在专用虚拟机上，不要与敏感工作负载混部，也不要将 Docker API 暴露到网络。当前威胁模型是可信同事，而不是彼此敌对的公网租户。

![远程浏览器](images/remote-browser.jpg)

<details>
<summary>更多截图</summary>

### 浏览器实例管理

![浏览器实例管理](images/manager-index.png)

### 用户管理

![用户管理](images/manager-user.jpg)

### 资源管理

![资源管理](images/manager-resource.jpg)

</details>

## 适用场景

RemoteBrowser 的核心是把独立、持久的浏览器工作区运行在你自己的服务器上，并通过普通网页访问。它适合以下场景：

- **使用自有服务器的网络出口**：不想在每台设备上搭建和维护完整 VPN，只希望浏览器流量从自己合法运营的服务器网络访问网站。适用于跨境电商运营、海外站点维护、区域化内容检查等需要固定服务器出口的工作。
- **轻量级多账号与多工作区隔离**：每个实例拥有独立且持久的 Chromium profile、Cookie、站点存储和下载目录。如果只需要商业多开或指纹浏览器的基础工作区能力，它可以作为透明、可自托管的轻量选择。
- **中小组织的浏览器级访问入口**：为成员统一提供需要从特定服务器网络访问的后台、SaaS 或 Web 管理工具，而不必开放服务器桌面或直接交付服务器凭据。它更接近简易的浏览器访问网关，而不是完整堡垒机。
- **跨地区团队的浏览器型远程办公**：让不同地区的成员通过浏览器使用同一部署中的独立工作区，保留登录状态和下载文件。适合主要工作都在 Web 应用中的小团队，不要求完整远程桌面或本地应用交付。
- **测试、演示与临时协作**：为网站区域化检查、持久登录测试、产品演示或临时协作者提供可撤销、相互隔离的浏览器环境。

## 能力边界

- RemoteBrowser 只承载远程 Chromium 流量，不是设备级 VPN、通用代理或整机网络隧道。
- Profile 隔离不等于浏览器指纹伪装。本项目不提供代理池、自动化编排或反检测保证，也不能保证第三方平台不会关联不同账号。
- 它不是企业级堡垒机，当前不包含 MFA、审批流、会话录像、数据防泄漏或完整合规审计；设计的信任模型是个人和可信的小团队。
- 它不是完整远程桌面方案，主要服务于浏览器内的工作流程。
- 使用者应遵守适用法律、网络政策及网站服务条款；本项目不用于绕过访问控制或平台风控。

## 部署

要求 Go 1.25+（源码开发）、Docker Engine / Docker Desktop 和 Docker Compose。Manager 需要 Docker API 权限；Compose 通过挂载 Docker socket 提供该权限。WebRTC UDP 端口也需要让客户端可达。

### Docker Compose

在仓库根目录准备配置：

```bash
cp .env.example .env
```

编辑 `.env`，至少填写首次初始化用的 `RB_ADMIN_PASSWORD`（至少 12 个字符）及 `RB_SESSION_HOST_DATA_DIR`（Docker 宿主机上的绝对数据目录）。已有部署升级时必须沿用原数据路径，不能换成空目录。配置文件含密码，限制其读取权限并妥善保存。

```bash
docker build -t remotebrowser/session:latest docker/session
cd deploy
docker compose --env-file ../.env config --quiet
docker compose --env-file ../.env up -d --build
```

本地访问 `http://localhost`，管理员登录名为 `admin`。公网部署时在 `.env` 中将 `RB_SITE_ADDRESS` 设为域名，并将 `RB_PUBLIC_URL` 设为用户可访问的同一 HTTPS 地址；Caddy 会据此自动申请和续期证书。远程访问还须将 `RB_WEBRTC_ICE_IP` 设为客户端可达的宿主机 IP。

请勿将本地配置原样复制到直接面向公网的服务器。公网部署请按照 [公网部署指南](docs/public-deployment.md) 配置 DNS、Caddy 免费自动 HTTPS、云防火墙和 Docker 防火墙规则，并完成上线验收。

Compose 管理网络默认名为 `deploy_remotebrowser`，可通过 `RB_DOCKER_NETWORK` 调整。每个浏览器实例使用带可核验归属标签的独立网络，Manager 自动加入这些网络进行鉴权代理，避免不同用户的容器直接互访。多个独立部署使用不同的管理网络名和数据目录。Session TCP 服务只在 Docker 网络内访问，不直接公开到宿主机。

### 本地源码开发

先构建上面的 Session 镜像。启动 Manager 时显式提供至少 12 个字符的初始管理员密码，例如通过当前终端的私密环境变量输入，再执行：

```bash
./scripts/dev.sh
```

开发服务默认监听 `http://localhost:8080`，数据写入 `./data`。脚本没有通用默认密码，也不打印密码。若管理员已存在，可不再设置 `RB_ADMIN_PASSWORD`。根据本机 Docker 配置设置标准 `DOCKER_HOST`，并确保实例可与 Manager 通信。

## 用户与密码

管理员在“用户管理”中新增邮箱用户、设置实例配额、查看当前实例数、调整配额、启用 / 禁用用户和重置密码。普通用户通过邮箱与密码登录，初始密码必须先修改。所有设置密码入口要求至少 12 个字符；密码不会出现在用户列表中。

未配置 SMTP 时，管理员必须指定临时密码并自行交付。有 SMTP 时，可生成随机临时密码并发邮件，或显式选择手工交付。邮件失败保留账号和失败状态，不重复创建用户。重发初始通知会生成新临时密码；用户已经完成首次改密时，应使用明确的重置密码操作。“邮件已提交”仅表示 SMTP 接受发送，不代表收件人已经收到。

修改密码、管理员重置、禁用或删除账号会撤销目标账号的登录及远程访问凭据。正常退出只结束当前登录。浏览器实例不会因此停止或丢失。管理员可查看其他用户实例的管理信息，但没有进入其他用户桌面或读取文件的产品权限。

删除用户需要再次确认邮箱与实例数量，将删除该用户的全部实例、profile 和下载文件。系统先冻结账号，再清理全部资源；清理失败时保留关联记录和原因，支持重试，全部清理完成后才删除账号。内置管理员不能通过这个入口删除。

### 初始化与管理员密码恢复

`RB_ADMIN_PASSWORD` 仅创建首次管理员，之后改变环境变量不会修改数据库里的密码。升级保留原来的管理员密码，即使旧密码不符合新建密码规则，也不会在启动时强行重置。

忘记管理员密码时，运维人员可显式执行 `-reset-admin-password`；无需删除数据库。这会立即撤销该管理员的旧登录，但保留所有实例。以下命令在 Bash 中运行，不把实际密码写入命令参数或终端输出：

```bash
read -r -s -p '新的管理员密码（至少 12 字符）: ' RB_RESET_ADMIN_PASSWORD
export RB_RESET_ADMIN_PASSWORD
# 在 deploy 目录，使用当前部署的同一份 .env：
docker compose --env-file ../.env exec -T -e RB_RESET_ADMIN_PASSWORD manager manager -reset-admin-password
unset RB_RESET_ADMIN_PASSWORD
```

源码运行时使用相同的数据目录执行 `go run ./cmd/manager -reset-admin-password`，密码也可由标准输入传入。不要将修改 `RB_ADMIN_PASSWORD` 或删除数据库作为密码恢复手段。

## 配置

| 变量 | 作用与默认值 |
| --- | --- |
| `RB_ADMIN_PASSWORD` | 首次初始化必需，至少 12 字符；已有账号不覆盖。 |
| `RB_ADMIN_EMAIL` | 可选管理员通知邮箱。 |
| `RB_PUBLIC_URL` | 发信必需的外部访问地址；Compose 本地默认 `http://localhost`。 |
| `RB_DEFAULT_INSTANCE_QUOTA` | 新建用户表单默认配额、首次管理员及旧用户迁移配额；默认 5，允许 0。保存后以每个用户的数据库配额为准。 |
| `RB_DATA_DIR` | Manager 看到的数据目录；源码默认 `./data`，Compose 固定 `/data`。 |
| `RB_SESSION_HOST_DATA_DIR` | Docker daemon 看到的同一数据目录；Compose 要求显式绝对路径。 |
| `RB_COOKIE_SECRET` | 可选的独立登录签名密钥，至少 32 字节；留空时自动生成并持久保存到数据目录的 `cookie-secret`。不要在重启时随机更换。 |
| `RB_HTTP_ADDR` | Manager 监听地址，默认 `:8080`。 |
| `RB_DOCKER_NETWORK` | 管理网络与部署标识的一部分；源码默认 `remotebrowser`，Compose 默认 `deploy_remotebrowser`。每个实例另建隔离网络。 |
| `RB_MAX_UPLOAD_SIZE` | 单次上传上限，默认 104857600 字节。 |
| `RB_SESSION_CPUS` | 单实例 CPU 上限，默认 2。 |
| `RB_SESSION_MEMORY_MB` | 单实例内存上限，默认 3072 MiB。 |
| `RB_SESSION_SHM_MB` | 单实例共享内存，默认 1024 MiB。 |
| `RB_WEBRTC_ICE_IP` | 媒体流公布给客户端的宿主机 IP；本地默认 `127.0.0.1`。 |
| `RB_PUBLISH_SESSION_TCP_PORTS` | 源码默认 `true`，Compose 固定 `false`，保持内部服务经 Manager 鉴权。 |
| `RB_SCREEN_WIDTH` / `RB_SCREEN_HEIGHT` / `RB_SCREEN_DEPTH` | 桌面默认 `1280` / `752` / `24`。 |
| `RB_CHROME_WINDOW_TOP` / `RB_CHROME_WINDOW_BOTTOM` | 浏览器窗口上下预留默认 `32` / `0`。 |
| `RB_WEBRTC_WIDTH` / `RB_WEBRTC_HEIGHT` | 默认输出 `1280` / `720`。 |
| `RB_WEBRTC_FRAMERATE` / `RB_WEBRTC_VIDEO_CODEC` | 默认 `24` 帧 / `vp8`；也支持 `h264`。 |
| `RB_WEBRTC_VIDEO_BITRATE` / `RB_WEBRTC_AUDIO_BITRATE` | 默认 `1800k` / `96k`。 |

`RB_SESSION_IDLE_TIMEOUT` 已弃用；即使配置也只提示并忽略，没有空闲过期机制。`RB_MAX_SESSIONS` 仅在未设置 `RB_DEFAULT_INSTANCE_QUOTA` 时提供兼容默认值，不是全站上限，也不覆盖已有用户配额。

### 可选 SMTP

| 变量 | 作用 |
| --- | --- |
| `RB_SMTP_HOST` | SMTP 主机名，启用时必填。 |
| `RB_SMTP_PORT` | 默认 587；隐式 TLS 通常使用 465。 |
| `RB_SMTP_USERNAME` / `RB_SMTP_PASSWORD` | 按 provider 设置，必须同时填写或同时为空。 |
| `RB_SMTP_FROM` | 有效发件地址，启用时必填。 |
| `RB_SMTP_ENCRYPTION` | `starttls`（默认）或 `tls`；`plain` 仅允许本机回环测试地址。 |
| `RB_SMTP_TIMEOUT` | 发送超时，默认 `10s`，最大 `1m`。 |

不使用邮件时，将主机、端口、账号、密码、发件人及加密方式全部留空；只设置端口或加密方式也会被视为邮件服务已配置，并显示配置不完整。后台展示配置状态并提供测试发送。凭据从部署配置读取，不通过网页明文展示。

## 配额、持久化与恢复

配额统计当前全部尚未完成删除的实例，包括创建中、停止、异常和删除失败。创建以数据库事务原子占位；重复提交相同创建请求不多建实例。降低配额不会停止或删除已有实例，只限制继续新建。

数据目录保存 `manager.db`、登录签名密钥以及各实例 profile / 下载目录。容器或宿主机重启后，系统核对实际状态并有限重试恢复应运行实例；手动停止的实例保持停止。无法联系 Docker 时显示待确认或错误，不假报已删除。持久化保存已落盘的数据，不保证未保存网页内容、进程内存或第三方站点登录永不过期。

无数据库记录的旧容器列为待处理资源，不猜测归属、不自动删除。不要手工删除数据库记录来“清理”仍有容器或文件的实例。

## 旧部署升级

1. 确认当前使用的数据绝对路径、数据库、网络及实例容器，保留原配置和镜像版本。
2. 备份账号和实例元数据、`cookie-secret`、profile、下载目录。需要一致的浏览器数据快照时先安排维护并正常停止相关实例；SQLite 使用在线备份能力，或在 Manager 停止后备份完整数据库状态。不要直接复制仍在写入的主文件；升级旧 WAL 数据库时还须保留其未完成写入，不能丢弃 WAL。新版本使用 DELETE 回滚日志，启动检查完整性，发现损坏会报错而不静默初始化。
3. 在新 `.env` 中沿用原路径。将旧 `RB_MAX_SESSIONS` 数值填入 `RB_DEFAULT_INSTANCE_QUOTA`，用于首次迁移旧用户；之后在用户管理页独立设置。
4. 构建新镜像并升级 Manager。迁移以事务运行并记录版本，保留旧管理员 ID / 密码、实例归属和目录；旧登录需要重新登录。`expired` 实例经核对后转换为运行或停止，提供原实例恢复入口。
5. 核验用户列表计数、个人配额、实例启停、profile 和下载文件。Session 镜像更新不会自动替换现有容器；需要载入新版本远程服务时安排容器维护并保留原数据挂载。

回退必须恢复与旧版本匹配的数据库和浏览器数据备份，不能直接让旧程序写入升级后的数据库。详细迁移说明见 [migrations/README.md](migrations/README.md)。

## 开发验证

```bash
go test ./...
go vet ./...
go build ./cmd/manager
```

远程画面、音频、中文输入、文件传输与跨用户权限还需要真实浏览器验证；编译通过或容器健康不代表这些端到端行为已通过。

后端使用 Go、chi、SQLite；页面使用服务端模板、HTMX / Alpine.js 与原生 JavaScript；远程实例包含 Chromium、WebRTC、noVNC、Xvfb、TigerVNC。入口在 `cmd/manager`，业务代码在 `internal/`，模板与静态资源在 `web/`，实例镜像在 `docker/session/`，部署文件在 `deploy/`。

## 许可证

MIT.  Bob ([bob](https://x.com/BigBigBobBob))
