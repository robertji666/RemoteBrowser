# RemoteBrowser 公网部署指南

[English](public-deployment.en.md)

本文以一台全新的 Linux 云服务器、一个独立域名和 Docker Compose 为例。命令以 Ubuntu / Debian 为主；其他发行版请替换包管理和防火墙命令。

## 1. 先确认安全边界

RemoteBrowser 的 Manager 通过 `/var/run/docker.sock` 创建和管理浏览器容器。任何能控制 Manager 进程的人都可能获得宿主机 root 等价权限。因此：

- 使用专用 VM，不与数据库、CI runner、公司内网代理或其他敏感服务混部。
- 不公开 Docker TCP API，也不要把 socket 转发给其他容器。
- 将使用者限定为可信同事；当前版本不是敌对租户之间的强隔离平台。
- 使用云厂商安全组作为第一层防火墙，再配置主机防火墙和 `DOCKER-USER` 链。
- 为系统盘或独立数据盘启用静态加密、快照访问控制和磁盘告警。

建议的最低服务器规格取决于并发实例数。默认每个实例最多使用 2 CPU、3 GiB 内存和 1 GiB `/dev/shm`，应根据机器容量降低默认配额，避免超卖。

## 2. 端口规划

| 方向 | 协议 / 端口 | 用途 | 建议来源 |
| --- | --- | --- | --- |
| 入站 | TCP 22 | SSH 运维 | 仅管理员固定 IP 或 VPN |
| 入站 | TCP 80 | ACME HTTP 验证和 HTTPS 跳转 | 公网；证书签发后也建议保留 |
| 入站 | TCP 443 | HTTPS 与 WebSocket | 同事来源网段；无法固定时公网 |
| 入站 | UDP 动态范围 | WebRTC 媒体 | 同事来源网段；无法固定时公网 |
| 出站 | TCP 80/443、DNS | 镜像、证书、用户访问网站 | 按组织策略限制 |

Session 的 noVNC、输入、音频和文件服务 TCP 端口不会发布到宿主机；它们只通过 Manager 鉴权代理。不要另行开放这些内部端口。

WebRTC 端口由 Linux 临时端口范围动态选择。Manager 启动后查看实际范围：

```bash
cd deploy
docker compose --env-file ../.env exec manager \
  sh -c 'cat /proc/sys/net/ipv4/ip_local_port_range'
```

把输出的起止值配置到云安全组和 `DOCKER-USER` UDP 规则。若不开放 UDP，系统仍可回退到 noVNC，但首次连接会等待 WebRTC 超时，画面和音频体验也会下降。

## 3. 域名与 DNS

1. 准备子域名，例如 `browser.example.com`。
2. 创建 `A` 记录指向服务器公网 IPv4。
3. 只有在服务器已配置 IPv6 防火墙和监听时才创建 `AAAA` 记录；否则删除错误的 `AAAA` 记录。
4. 初次部署建议使用普通 DNS 解析。如果 DNS 服务商提供 HTTP/CDN 代理，先关闭代理；即使 HTTPS 能被代理，WebRTC UDP 仍需要客户端直连 `RB_WEBRTC_ICE_IP`。

等待解析后检查：

```bash
dig +short browser.example.com A
dig +short browser.example.com AAAA
```

## 4. 安装 Docker

使用 Docker 官方仓库安装 Docker Engine 和 Compose plugin，不要运行来源不明的一键脚本。安装完成后检查：

```bash
docker version
docker compose version
```

Docker 官方 Ubuntu 安装说明：<https://docs.docker.com/engine/install/ubuntu/>

## 5. 获取代码和准备数据目录

用专门的系统账号克隆仓库。以下路径只是示例：

```bash
sudo install -d -m 0750 -o "$USER" -g "$USER" /opt/remotebrowser
sudo install -d -m 0700 -o "$USER" -g "$USER" /srv/remotebrowser/data
git clone https://github.com/robertji666/RemoteBrowser.git /opt/remotebrowser/app
cd /opt/remotebrowser/app
cp .env.example .env
chmod 600 .env
```

不要把 `.env`、`data/`、数据库、浏览器 profile、下载文件或备份提交到 Git。

## 6. 生产环境配置

编辑 `.env`，至少设置以下值：

```dotenv
RB_ADMIN_PASSWORD=首次初始化使用的高强度唯一密码
RB_ADMIN_EMAIL=admin@example.com

RB_SESSION_HOST_DATA_DIR=/srv/remotebrowser/data
RB_SITE_ADDRESS=browser.example.com
RB_PUBLIC_URL=https://browser.example.com
RB_WEBRTC_ICE_IP=203.0.113.10

RB_DEFAULT_INSTANCE_QUOTA=2
RB_SESSION_CPUS=2
RB_SESSION_MEMORY_MB=3072
RB_SESSION_SHM_MB=1024
RB_MAX_UPLOAD_SIZE=104857600
```

注意：

- `RB_SITE_ADDRESS` 只填域名，不带路径；Caddy 看到域名后自动启用 HTTPS。
- `RB_PUBLIC_URL` 必须是同事实际访问的完整 HTTPS 地址。
- `RB_WEBRTC_ICE_IP` 填客户端可达的服务器公网 IP，不填域名，不填容器 IP。
- 管理员密码至少 12 字符，建议由密码管理器生成 20 字符以上随机密码。
- `RB_COOKIE_SECRET` 可以留空，应用会生成并保存到数据目录；若手工设置，至少使用 32 个随机字节，并在所有重启中保持一致。
- SMTP 不使用时，所有 `RB_SMTP_*` 保持为空；使用时必须启用 TLS 或 STARTTLS。

## 7. 免费 HTTPS 证书

部署配置使用 Caddy。只要域名解析正确，TCP 80/443 可从公网访问，且 `RB_SITE_ADDRESS` 是域名，Caddy 就会通过 ACME 自动申请并续期受信任的免费证书，同时把 HTTP 重定向到 HTTPS。证书与 ACME 状态保存在 `caddy_data` volume，升级时不得删除。

Caddy 自动 HTTPS说明：<https://caddyserver.com/docs/automatic-https>  
Let's Encrypt 验证方式说明：<https://letsencrypt.org/docs/challenge-types/>

如需使用组织自有证书，可自行调整 `deploy/Caddyfile`，但不要把私钥提交到 Git。通常没有必要手工申请 Let's Encrypt 证书，也不要同时在 Caddy 外再运行一个 Certbot 定时任务。

## 8. 配置防火墙

### 8.1 云安全组

先在云厂商控制台配置：

- TCP 22：只允许管理员公网 IP 或 VPN 网段。
- TCP 80：允许公网，用于证书验证和重定向。
- TCP 443：优先只允许公司出口或 VPN 网段；否则允许公网。
- UDP 临时端口范围：使用第 2 节查到的范围，优先只允许公司出口或 VPN 网段。
- 其余入站全部拒绝。

### 8.2 UFW（Ubuntu 示例）

先保持当前 SSH 会话，不要关闭终端：

```bash
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow from YOUR_ADMIN_PUBLIC_IP to any port 22 proto tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable
sudo ufw status verbose
```

把 `YOUR_ADMIN_PUBLIC_IP` 替换为真实地址。若 SSH 由 VPN 或非 22 端口提供，相应调整规则。

### 8.3 Docker 的关键例外

Docker 发布的端口会在 UFW 处理之前经过 Docker 自己的规则，单独配置 UFW 不能可靠阻止已发布容器端口。Docker 官方文档明确建议把额外过滤规则放在 `DOCKER-USER` 链：<https://docs.docker.com/engine/network/packet-filtering-firewalls/>

在专用服务器上，可先确认公网接口名：

```bash
ip route show default
```

然后设置实际接口与 UDP 范围，再添加规则。下面是模板，不要原样复制占位值：

```bash
EXT_IF=eth0
UDP_MIN=32768
UDP_MAX=60999

sudo iptables -I DOCKER-USER 1 -i "$EXT_IF" \
  -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
sudo iptables -I DOCKER-USER 2 -i "$EXT_IF" -p tcp \
  -m multiport --dports 80,443 -j ACCEPT
sudo iptables -I DOCKER-USER 3 -i "$EXT_IF" -p udp \
  --dport "$UDP_MIN:$UDP_MAX" -j ACCEPT
sudo iptables -I DOCKER-USER 4 -i "$EXT_IF" -j DROP
sudo iptables -S DOCKER-USER
```

`UDP_MIN` / `UDP_MAX` 必须替换成实际查询值。若只允许公司网段，在 UDP 和 443 规则中增加 `-s COMPANY_CIDR`。规则必须使用发行版支持的持久化机制保存并在重启后复核。使用 nftables 或 firewalld 的服务器应实现等价规则，不要同时维护互相冲突的规则集。

如果服务器还有其他 Docker 服务，上述末尾 DROP 会影响它们；这也是建议使用专用 VM 的原因。IPv6 启用时还要配置等价的 IPv6 云安全组和主机规则，不能只保护 IPv4。

## 9. 构建、校验和启动

```bash
cd /opt/remotebrowser/app
docker build --pull -t remotebrowser/session:latest docker/session

cd deploy
docker compose --env-file ../.env config --quiet
docker compose --env-file ../.env pull caddy
docker compose --env-file ../.env up -d --build
docker compose --env-file ../.env ps
docker compose --env-file ../.env logs --tail=100 caddy manager
```

验证 HTTPS 和安全响应头：

```bash
curl --fail --show-error https://browser.example.com/healthz
```

应获得成功响应和受信任证书。若证书失败，先检查 DNS、80/443 入站、防火墙、Caddy 日志及 `RB_SITE_ADDRESS`，不要关闭 TLS 校验来绕过问题。

## 10. 上线验收

至少完成以下真实用户路径：

1. 管理员登录并立即确认强密码，创建一个普通用户。
2. 普通用户首次登录、修改临时密码、退出并重新登录。
3. 创建实例，确认 WebRTC 连接；反复切换 WebRTC / noVNC 和音频，检查没有重复声音或尾音。
4. 测试中文输入、剪贴板、文件上传与下载。
5. 重启 Manager 和服务器，确认账号、实例、Cookie/profile 和下载文件仍在。
6. 禁用或重置用户密码，确认旧登录和远程连接失效。
7. 用两个用户检查实例、文件和管理操作不能越权。
8. 从未授权网络扫描公网 IP，确认只暴露预期的 TCP 80/443、受限 SSH 和 UDP 范围。

## 11. 日常运维

- 为磁盘、inode、CPU、内存、容器重启和 TLS 到期配置监控；当前没有每用户磁盘配额。
- 定期安装宿主机、Docker、Go 依赖、Caddy、Alpine/Debian 和 Chromium 安全更新。
- 备份整个数据目录和 Compose 配置。SQLite 使用在线备份，或先停止 Manager 再复制；浏览器一致性快照最好在实例停止后执行。
- 恢复演练应在隔离服务器上进行，不能只确认“备份文件存在”。
- 更新 Session 镜像不会自动替换已存在实例。安排维护、保留 profile / downloads 挂载后重建实例容器。
- 不要在工单、聊天、截图或日志中泄露 `.env`、Cookie secret、SMTP 密码和用户浏览数据。

## 12. 上 GitHub 前的检查

1. 确认 `git status` 中没有 `.env`、数据文件、数据库、备份、构建产物或真实凭据。
2. 对完整 Git 历史执行秘密扫描，而不只是扫描当前目录。
3. 在 GitHub 启用 private vulnerability reporting、Dependabot alerts、secret scanning 和 push protection（取决于仓库与账号可用能力）。
4. 保护默认分支，要求 CI 通过后才能合并。
5. 发布版本时记录数据库迁移、环境变量、镜像重建和回退方法。

安全问题请按 [SECURITY.md](../SECURITY.md) 私下报告，不要先创建公开 issue。
