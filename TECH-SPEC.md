# RemoteBrowser 技术方案文档

> 版本：vNext 多用户与持久实例
> 日期：2026-09-16
> 状态：实现与验证中

本版以 [多用户与持久实例专项需求](PRD-MULTIUSER-PERSISTENT-INSTANCES.md) 为准；部署变量和运维命令以 [README.md](README.md) 为准。基础远程桌面架构延续，用户权限、个人配额、密码交付和持久实例生命周期替换原来的单用户与空闲回收设计。

---

## 1. 文档目标

本文档用于将 [PRD.md](./PRD.md) 的 MVP 需求落到可实现、可排期、可验收的技术方案。

本版明确覆盖 PRD 的 MVP 范围：

- 远程浏览器访问
- 多会话管理
- 会话守护与重连
- 文本剪贴板双向同步
- 文件上传下载

---

## 2. 设计原则

### 2.1 范围原则

- MVP 只做“远程浏览器”单一能力，不扩展到数据库工具、应用发布、审批流、IP 白名单等企业堡垒机场景
- MVP 先解决“能用、稳定、隔离、易部署”，不追求极致低延迟
- 所有设计以单机 VPS 自托管为第一优先级

### 2.2 技术原则

- 不从零造轮子，优先复用成熟组件
- 控制面与会话面分离，避免多会话逻辑散落在容器脚本中
- 默认安全收敛，不能依赖“用户自己懂安全”
- 动态会话必须走稳定路由，不能靠人工端口管理
- 每个会话必须具备独立存储、独立令牌、独立生命周期

### 2.3 参考项目

| 项目 | 用途 |
|------|------|
| `m1k1o/neko` | 参考房间模型、会话管理思路、后续 WebRTC 演进 |
| `vital987/chrome-novnc` | 参考 Chromium + NoVNC 容器打包方式 |
| `ConSol/docker-headless-vnc-container` | 参考非 root 容器、桌面进程编排、镜像组织 |
| `noVNC` | 浏览器端 VNC 客户端与剪贴板能力 |
| `TigerVNC` | 容器内 VNC Server |

---

## 3. 需求对齐

### 3.1 PRD MVP 到技术能力映射

| PRD 需求 | 技术实现 | 本版状态 |
|----------|----------|----------|
| 网页打开远程浏览器，3 秒内看到桌面 | 预热控制面 + 会话容器 + WebRTC 音视频 + NoVNC 输入回退 | 覆盖 |
| 同时运行多个独立 Chrome 实例 | Manager 动态创建 Session 容器 | 覆盖 |
| 关闭标签页后实例继续运行，可重连 | 持久实例状态机 + 状态核对 + 所有者与登录凭据校验 | 覆盖 |
| 本地与远程文本双向剪贴板 | Clipboard Bridge + noVNC 剪贴板能力 + 浏览器 Clipboard API | 覆盖 |
| 文件上传下载 | 会话级文件服务 + 管理面板文件浮层 | 覆盖 |

### 3.2 非目标

以下能力不纳入本版实现：

- 审批流、IP 黑白名单、访问事由
- 角色权限系统、部门体系
- 多应用远程发布
- 企业级 TURN 中继与公网 NAT 穿透优化
- 图片剪贴板
- SaaS 多租户计费

---

## 4. 核心技术决策

| 决策项 | 选择 | 原因 |
|--------|------|------|
| 流传输协议 | WebRTC + NoVNC 输入回退 | WebRTC 负责音视频体验，NoVNC 保留键鼠输入、剪贴板和失败回退 |
| 浏览器 | Chromium | 避免 Chrome 分发与商标风险 |
| 容器运行时 | Docker Engine | 自托管 VPS 最普遍 |
| 控制面后端 | Go | 静态编译、部署简单、适合做单机控制面 |
| 反向代理 | Caddy | 自动 HTTPS，适合 Clipboard API 权限要求 |
| 会话数据存储 | SQLite + 本地卷 | 单机足够，便于一键部署 |
| 会话容器桌面栈 | Xvfb + Fluxbox + TigerVNC + noVNC + WebRTC service | 兼顾成熟输入链路与音视频体验 |
| 鉴权 | 邮箱 / admin 密码登录 + 签名 Cookie + 数据库登录会话 + 凭据版本 | 固定 admin / user 角色；支持目标用户撤销 |

### 4.1 明确技术栈清单

#### 4.1.1 后端栈

| 层 | 选型 | 说明 |
|----|------|------|
| 语言 | Go 1.24+ | 单二进制、低内存、适合做代理和控制面 |
| HTTP 框架 | `chi` | 路由轻量，足够支撑 API、页面、代理入口 |
| HTTP 基础库 | `net/http` | 文件流转发、反向代理、WebSocket/SSE 直接用标准库能力 |
| HTML 模板 | `html/template` | 服务端渲染，避免前后端分离开销 |
| 数据访问 | `database/sql` + `modernc.org/sqlite` | 无 CGO，跨平台构建更简单 |
| SQL 管理 | `sqlc` | 保持 SQL 可控，同时避免手写扫描代码 |
| Migration | `golang-migrate` 或自维护 SQL 文件 | 需求简单，不引入重 ORM |
| 日志 | `log/slog` | 标准库统一日志接口，够轻 |
| 配置 | 环境变量 + 少量配置文件 | 符合 Docker 自托管场景 |

后端原则：

- 不使用 ORM 作为首选
- 不拆微服务
- 不引入消息队列
- 不引入 Redis

#### 4.1.2 前端栈

| 层 | 选型 | 说明 |
|----|------|------|
| 页面渲染 | SSR HTML | 页面结构由 Go 服务端输出 |
| 局部交互 | HTMX | 处理列表刷新、表单提交、局部替换 |
| 轻状态管理 | Alpine.js | 处理弹窗、浮层、按钮状态、上传进度显示 |
| 特殊能力 | 原生 JavaScript | 处理 noVNC、Clipboard API、拖拽上传 |
| 样式 | 手写 CSS | 控制体积，不引入大型 UI 框架 |

前端原则：

- 不做 SPA
- 不引入构建复杂度优先的前端工程体系
- 能用服务端返回 HTML 片段解决的问题，不额外设计 JSON + 前端渲染链路

#### 4.1.3 基础设施栈

| 层 | 选型 | 说明 |
|----|------|------|
| 公网入口 | Caddy | 自动 HTTPS，配置简单 |
| 数据库 | SQLite | 单机足够，最省资源 |
| 容器运行时 | Docker Engine | Manager 直接调度 Session |
| 桌面环境 | Xvfb + Fluxbox | 比完整桌面更省资源 |
| 浏览器流转发 | noVNC + websockify | 成熟方案 |
| 文件服务 | Session 内置 Go file service | 只解决会话级上传下载 |

### 4.2 依赖建议

#### 4.2.1 Go 依赖

MVP 建议只保留少量核心依赖：

- `github.com/go-chi/chi/v5`
- `github.com/docker/docker/client`
- `github.com/docker/docker/api/types`
- `github.com/gorilla/websocket` 或继续使用 `net/http` 升级接口
- `modernc.org/sqlite`
- `github.com/pressly/goose` 或 `github.com/golang-migrate/migrate/v4`

能不用第三方就不用第三方的部分：

- 日志用 `log/slog`
- 模板用 `html/template`
- 配置解析优先环境变量
- 反向代理优先 `httputil.ReverseProxy`

#### 4.2.2 前端依赖

MVP 前端依赖控制在最小集合：

- `HTMX`
- `Alpine.js`
- `noVNC`

不建议在首版引入：

- React
- Vue
- Vite
- Tailwind
- Zustand / Pinia / Redux 一类状态管理

原因不是这些技术不好，而是当前场景不需要。

### 4.3 为什么前端推荐 HTMX + Alpine.js

这套方案适合当前产品形态：

- 管理面板是表单、列表、状态切换为主
- 会话页只有少量工具栏交互
- 真正复杂的浏览器画面由 noVNC 承担，不在前端框架内渲染

职责划分建议：

- `HTMX`：新建会话、刷新会话列表、停止/删除会话、局部错误提示
- `Alpine.js`：文件浮层开关、剪贴板状态、上传进度、按钮 loading
- 原生 JS：绑定 noVNC、Clipboard API、drag-and-drop、下载触发

### 4.4 为什么不选 React / Vue

当前阶段不选 React/Vue，不是因为性能不够，而是因为性价比不高。

主要原因：

- 这个产品的前端不是复杂业务前端，而是“轻管理面板 + 会话工具壳”
- 采用 React/Vue 往往会自然走向 SPA、JSON API、前端构建链、状态管理、组件体系
- 这些东西会提高开发和部署复杂度，但不会显著提升核心体验
- 你的瓶颈主要在 Session 容器和 VNC 流，不在前端渲染性能

对单机小 VPS 的影响：

- 构建产物更多
- 前端工程链更复杂
- API 设计更容易被前后端分离牵着走
- 调试路径更长

什么时候再考虑 React/Vue：

- 后续真的出现复杂控制台
- 页面状态显著增多
- 需要大量组件复用
- 团队已有成熟前端工程体系

### 4.5 为什么不直接只写原生 HTML + JS

纯原生 HTML + JS 也能完成 MVP，但问题在于：

- 页面一多，事件绑定和 DOM 更新会很快变散
- 列表局部刷新、表单提交、弹窗状态这些重复逻辑会被反复手写
- 最终容易演变成“没有框架，但写出了一个隐形框架”

因此推荐：

- 页面主体用 SSR HTML
- 简单局部交互用 HTMX
- 小状态管理用 Alpine.js
- 特殊浏览器能力继续用原生 JS

这比 SPA 轻，也比全手写原生 JS 更稳。

### 4.6 性能与资源预期

按“单用户 + 单浏览器容器”的目标，推荐组合为：

- `1 vCPU`
- `2 GiB RAM`
- `20 GiB SSD`

资源占用预期：

- `Manager + Caddy + SQLite` 占用应明显低于浏览器容器
- 主资源消耗来自单个 Chromium Session
- 前端技术栈选择对 VPS 运行时资源影响很小，但会显著影响实现复杂度

因此优化重点应放在：

- Session 镜像瘦身
- Chromium 启动参数
- `shm` 配置
- 会话并发上限

### 4.7 WebRTC 增强方案

本版增加 WebRTC 会话面，但保留 NoVNC 作为输入层和回退路径。

实现边界：

- Session 容器内新增 `webrtc-service`，监听 `6082`
- Manager 通过 `/sessions/<session_id>/webrtc/*` 反代信令，不直接暴露容器端口
- Manager 为每个 Session 分配一个 UDP ICE 端口，并映射到宿主机同端口
- `webrtc-service` 使用 Pion 单端口 UDP mux 承载媒体流
- `RB_WEBRTC_ICE_IP` 控制 SDP 中公布给浏览器的 ICE IP，本地 Docker Desktop 默认 `127.0.0.1`
- 参考 neko 的 broadcast/stream-sink 模型，Session 内共享一套音视频编码器：首个 peer 连接时启动，最后一个 peer 断开后停止，避免多标签页或重连导致编码器按连接数线性增长
- 容器桌面默认 `RB_SCREEN_WIDTH=1280`、`RB_SCREEN_HEIGHT=720`、`RB_SCREEN_DEPTH=24`，与 WebRTC 默认输出一致，避免 `x11grab` 捕获 1080p 后再缩放
- WebRTC 默认输出 `VP8 1280x720@24fps`，可用 `RB_WEBRTC_VIDEO_CODEC`、`RB_WEBRTC_WIDTH`、`RB_WEBRTC_HEIGHT`、`RB_WEBRTC_FRAMERATE`、`RB_WEBRTC_VIDEO_BITRATE`、`RB_WEBRTC_AUDIO_BITRATE` 覆盖
- 如果 `RB_SCREEN_WIDTH`/`RB_SCREEN_HEIGHT` 与 WebRTC 输出尺寸一致，ffmpeg 只执行 `fps` 滤镜；尺寸不一致时才启用 `scale=...:flags=fast_bilinear`
- 前端创建 WebRTC offer，容器返回 answer
- 视频源来自 `x11grab` 捕获 `DISPLAY=:1`
- 音频源复用 PulseAudio `rb_audio.monitor`，编码前使用 wallclock timestamp 和 async resample 稳定 Opus RTP 时间戳
- noVNC iframe 默认作为透明输入层继续发送键鼠事件
- WebRTC 失败时自动切回可见 noVNC

本地 Docker Desktop 场景默认不配置 STUN/TURN，ICE 使用 `127.0.0.1:<session_udp_port>` host candidate。公网部署需将 `RB_WEBRTC_ICE_IP` 设置为客户端可达的公网 IP 或入口地址；复杂 NAT 场景后续再加入 TURN。

---

## 5. 总体架构

### 5.1 逻辑架构

```text
┌──────────────────────────────────────────────────────────┐
│                       用户浏览器                         │
│  ┌───────────────┐  ┌───────────────┐  ┌──────────────┐ │
│  │ 管理面板       │  │ WebRTC 音视频   │  │ noVNC 输入/回退│ │
│  └──────┬────────┘  └──────┬────────┘  └──────┬───────┘ │
└─────────┼──────────────────┼──────────────────┼─────────┘
          ▼                  ▼                  ▼
┌──────────────────────────────────────────────────────────┐
│                    Caddy（公网入口）                     │
│            HTTPS / WebSocket / 静态资源 / 路由转发        │
└─────────┬──────────────────────────────────────┬─────────┘
          ▼                                      ▼
┌─────────────────────────────┐      ┌─────────────────────┐
│ RemoteBrowser Manager       │      │ Session Containers  │
│                             │      │                     │
│ - 登录鉴权                  │◄────►│ sess_xxx            │
│ - 会话状态机                │ Docker│ - Chromium          │
│ - 路由注册表                │ API   │ - Xvfb              │
│ - 剪贴板桥接 API            │      │ - Fluxbox           │
│ - WebRTC 信令代理           │      │ - TigerVNC          │
│ - 持久实例状态核对          │      │ - noVNC/websockify  │
│ - SQLite 元数据             │      │ - WebRTC service    │
│                             │      │ - file service      │
└─────────┬───────────────────┘      └─────────┬───────────┘
          ▼                                      ▼
┌──────────────────────────────────────────────────────────┐
│                       Host Data                          │
│ /data/manager.db /data/sessions/<session_id>/{profile,downloads} │
└──────────────────────────────────────────────────────────┘
```

### 5.2 运行模式

| 模式 | 说明 | 用途 |
|------|------|------|
| `all-in-one` | Caddy + Manager + 单 Docker Engine | MVP 默认部署方式 |
| `manager + dynamic sessions` | Manager 独立管理多个 Session 容器 | 标准多会话模式 |
| `webrtc enhanced` | WebRTC 音视频 + NoVNC 输入/回退 | 本版默认方案 |

---

## 6. 控制面设计

控制面是本方案的核心。MVP 是否成立，不取决于容器能否启动 Chromium，而取决于 Manager 是否能稳定管理多会话生命周期。

### 6.1 控制面职责

Manager 负责：

- 用户登录与会话级访问校验
- 创建、启动、停止、销毁 Session 容器
- 为 Session 分配稳定的路由键，而不是暴露随机端口给用户
- 记录会话状态、最后活跃时间、所属用户、数据目录
- 提供管理面板 API
- 处理实例守护、期望状态恢复、重连和用户明确确认的删除
- 提供文本剪贴板桥接接口
- 提供文件上传下载鉴权与代理入口

Manager 不负责：

- 渲染浏览器画面
- 保存业务级审计录像
- 运行文件管理器

### 6.2 控制面模块

| 模块 | 职责 |
|------|------|
| `auth` | 登录、签发会话 Cookie、校验访问权限 |
| `session service` | 创建/停止/销毁/列出会话 |
| `allocator` | 分配 `session_id`、容器名、数据目录 |
| `router registry` | 维护 `session_id -> upstream` 映射 |
| `maintenance` | 定期核对容器与期望状态，有限重试恢复和未完成删除；不按空闲时间回收 |
| `clipboard bridge` | 管理本地与远端文本剪贴板同步 |
| `file gateway` | 代理会话文件服务并校验路径与权限 |
| `store` | SQLite 元数据存储 |

### 6.3 控制面状态机

每个会话必须处于以下状态之一：

| 状态 | 含义 |
|------|------|
| `creating` | 元数据已创建，容器准备启动 |
| `starting` | 容器已创建，等待 noVNC 就绪 |
| `running` | 会话可连接 |
| `stopping` | 用户主动停止，等待容器停止确认 |
| `stopped` | 容器已停止，可重启 |
| `error` | 启动失败或健康检查失败 |
| `deleting` | 用户确认删除，正在清理容器与持久数据 |
| `unknown` | 无法联系 Docker 或实际状态暂不可确认 |

运行状态与访问连接数分开。`disconnected` / `expired` 仅用于旧记录迁移，不产生新的空闲过期。`desired_state` 保存 running / stopped / deleted；旧 expired 临时保存内部 legacy_expired，核对容器后确定 running 或 stopped，Docker 不可达时不会丢失迁移意图。

状态迁移规则：

```text
creating -> starting -> running
running -> stopping -> stopped
starting/running -> error
stopped -> starting
任一实例 -> deleting -> 完整清理后移除记录
Docker 不可达 -> unknown -> 核对实际与期望状态
```

### 6.4 会话标识与路由模型

MVP 不采用“给用户暴露动态端口”的方式，而采用“稳定路径 + 控制面反代”模式。

约定：

- `session_id`：系统主键，如 `sess_01HS...`
- `session_token`：每个会话独立签名令牌
- `container_name`：`rb-sess-<session_id>`
- `profile_dir`：`/data/sessions/<session_id>/profile`

用户访问路径固定为：

```text
/sessions/<session_id>/view
```

请求链路：

```text
Browser
  -> Caddy
  -> Manager auth check
  -> reverse_proxy to container internal endpoint
  -> WebRTC service 返回音视频流
  -> noVNC/websockify 保留输入和回退
```

这样做的好处：

- 用户侧 URL 稳定，重连不依赖端口
- Session 容器内部始终监听固定端口，例如 `6080`
- 外部不暴露任何 Session 容器端口
- Caddy 只需反代到 Manager；Manager 再作为应用层网关转发到会话容器

### 6.5 为什么不用 Caddy 动态改配置

MVP 采用“Manager 内建反向代理”而不是“运行时重写 Caddy 配置”，原因：

- 避免对 Caddy Admin API 做额外控制
- 避免路由变更与容器状态同步复杂度
- 减少动态 reload 对 WebSocket 长连接的影响

因此公网入口固定只有两类：

- `/` 管理面板与 API
- `/sessions/<session_id>/...` 会话访问代理

---

## 7. 会话面设计

### 7.1 单个 Session 容器组成

```text
Debian 12 Slim
  ├─ Xvfb
  ├─ Fluxbox
  ├─ TigerVNC Server
  ├─ noVNC + websockify
  ├─ file service
  ├─ WebRTC service
  ├─ Chromium
  └─ entrypoint / healthcheck
```

### 7.2 Session 容器约束

- 容器内部 noVNC 固定监听 `6080`
- 容器内部 WebRTC service 固定监听 `6082/tcp`
- 容器内部 WebRTC media 使用每会话分配的 UDP 端口
- VNC 仅容器内可见，不对宿主机暴露
- Chromium 使用独立 `user-data-dir`
- 容器以非 root 用户运行
- 默认 `read_only: false`，但仅 profile 和 downloads 目录可写
- 默认资源限制：`1 CPU / 2 GiB RAM / 512 MiB shm`

### 7.3 Chromium 启动策略

默认启动参数：

```bash
chromium \
  --user-data-dir=/home/rbuser/profile \
  --start-maximized \
  --disable-dev-shm-usage \
  --disable-background-networking \
  --disable-sync \
  --no-first-run \
  --no-default-browser-check
```

说明：

- 不默认使用 `--no-sandbox`
- 只有在宿主环境无法满足 Chromium sandbox 依赖时，才允许通过显式配置降级
- 必须在部署文档中标记：启用 `--no-sandbox` 会降低“临时安全浏览”场景的安全性

### 7.4 会话隔离模型

每个 Session 必须具备以下隔离边界：

- 独立容器
- 独立 Chromium Profile 目录
- 独立会话令牌
- 独立最后活跃时间
- 独立下载目录

宿主机目录结构：

```text
/data
  /manager.db
  /sessions
    /sess_a
      /profile
      /downloads
      /tmp
    /sess_b
      /profile
      /downloads
      /tmp
```

不得出现共享目录挂载到多个 Session Profile 的情况。

---

## 8. 关键功能设计

### 8.1 远程浏览器访问

目标：

- 首次打开页面后 3 秒内看到桌面
- 支持鼠标和键盘输入

实现要点：

- Manager 在点击“新建会话”时先写入元数据，再调用 Docker API 创建容器
- 容器启动后通过 HTTP 健康检查探测 `http://container:6080/`
- 健康成功后将会话状态切到 `running`
- 前端跳转到 `/sessions/<session_id>/view`

优化策略：

- 可选预拉取 Session 镜像
- 可选保留 1 个 warm pool 容器作为后续优化，不纳入首版必做

### 8.2 多会话管理

管理面板提供：

- 会话列表
- 新建会话
- 进入会话
- 停止会话
- 删除会话
- 显示状态、创建时间、最后活跃时间

限制：

- 每用户 `instance_quota` 控制当前拥有量；全部尚未完整删除的实例都占名额。
- 创建时事务检查账号 active 状态、幂等请求键和剩余名额，防止并发超额。
- 管理员可以降低配额但不删除既有实例；普通用户只能访问本人实例，管理员管理信息视图不授予他人桌面访问。

### 8.3 会话守护与重连

规则：

- 关闭浏览器标签页时，只断开 WebSocket，不停止容器
- 会话最后活跃时间由 Manager 根据 WebSocket 存活和心跳更新
- `RB_SESSION_IDLE_TIMEOUT` 已忽略，任何空闲时长都不触发停止或删除。
- 用户退出或登录失效只撤销访问；profile、下载文件与实例继续保留。

重连流程：

```text
用户打开面板
  -> Manager 查询本人可见会话
  -> 用户点击“重连”
  -> Manager 校验状态
  -> running 直接进入
  -> stopped 则先启动再进入
  -> 旧 expired 经核对转换为可启动的 stopped 或实际 running
```

### 8.4 双向文本剪贴板同步

MVP 只支持文本，不支持图片与文件。

数据流 1：本地 -> 远端

```text
用户在本地复制文本
  -> 页面通过 Clipboard API 读取文本
  -> 前端调用 Manager clipboard API
  -> Manager 将文本写入对应 Session 的 noVNC 剪贴板通道
  -> 远端 X11/Chromium 可粘贴
```

数据流 2：远端 -> 本地

```text
用户在远端 Chromium 复制文本
  -> noVNC 接收远端剪贴板更新
  -> 前端在用户授权后写入本地 Clipboard API
```

实现说明：

- HTTPS 是必需条件，否则浏览器 Clipboard API 不可用
- 首次进入会话页时，前端必须请求剪贴板权限
- 浏览器不允许静默写入本地剪贴板时，降级为“复制到本地”按钮
- 前端需明确标识当前同步状态：`已授权` / `待授权` / `降级模式`

验收标准：

- 中文、英文、多行文本可双向复制
- 跨标签页重连后仍可继续使用
- 在不支持自动写本地剪贴板的浏览器中可降级使用

### 8.5 健康检查

Manager 维护两类健康检查：

- 容器级：Docker container state
- 应用级：Session 容器 `/healthz`

判定规则：

- 容器退出：状态标记 `error` 或 `stopped`
- 连续 3 次应用健康检查失败：状态标记 `error`
- `error` 不自动无限重启，避免崩溃循环；仅允许有限重试

### 8.6 文件传输

MVP 需要支持：

- 本地拖拽上传到远端会话
- 查看当前会话下载目录文件列表
- 从远端会话下载文件到本地

设计原则：

- 文件传输是“会话级能力”，不能做成全局共享文件区
- 文件访问必须绑定 `session_id`
- 不允许用户直接访问宿主机路径

数据流 1：上传

```text
用户在会话页拖拽文件
  -> 浏览器调用 Manager file API
  -> Manager 校验登录态与 session 所属关系
  -> Manager 将文件流式转发到 Session 容器 file service
  -> Session 容器写入 /home/rbuser/downloads
```

数据流 2：下载

```text
用户打开文件浮层
  -> Manager 拉取该 session 的文件列表
  -> 用户点击下载
  -> Manager 校验路径白名单
  -> Manager 代理 Session file service 响应流
  -> 浏览器下载到本地
```

Session 容器增加一个轻量文件服务，建议使用内置 Go HTTP 服务，职责仅限：

- `POST /upload`
- `GET /files`
- `GET /download/{name}`
- `GET /healthz`

文件服务目录边界：

- 根目录固定为 `/home/rbuser/downloads`
- 不允许访问上级目录
- 文件名必须做路径清洗，拒绝 `..`、绝对路径、隐藏控制字符

前端交互：

- 会话页右侧工具栏增加“文件”按钮
- 点击后打开文件浮层
- 支持拖拽上传、上传进度、文件列表刷新、点击下载

MVP 不做：

- 多级目录管理
- 文件重命名/删除
- 超大文件断点续传
- 跨会话文件共享

---

## 9. 安全设计

### 9.1 基础安全边界

MVP 不是企业堡垒机，但不能裸奔。默认要求：

- 公网只暴露 Caddy 的 `80/443`
- Manager 不直接暴露到公网独立端口
- Session 容器不暴露宿主机端口
- Session 容器以非 root 运行
- 容器默认 `cap_drop: [ALL]`
- 使用默认 seccomp 或更收敛的 seccomp 配置
- Session 网络不允许被外部直接访问，只允许经由 Manager 代理

### 9.2 登录鉴权

MVP 必须有基础鉴权，不放到后续版本。

方案：

- 首次启动要求配置至少 12 字符的 `RB_ADMIN_PASSWORD`；已有 admin 不覆盖，显式 `-reset-admin-password` 可在运行中重置。
- 普通用户使用规范化邮箱，管理员兼容 admin 登录名；初始密码须先修改。
- 签名密钥独立于密码，未显式配置时随机生成并持久保存；登录有效期 7 天。
- 密码修改 / 重置、禁用和删除通过 auth_version 撤销目标账号旧登录；长连接定期复核，WebRTC 使用短期授权租约。
- 状态变更校验 CSRF；登录和重置入口限制请求频率。
- Manager 签发 `HttpOnly + Secure + SameSite=Lax` 的登录 Cookie
- 每个会话页访问还需额外校验该用户是否拥有 `session_id`

这意味着：

- 管理面板受保护
- 会话代理路径受保护
- 文件传输接口纳入同一鉴权体系

### 9.3 Docker Socket 风险控制

Manager 若直接挂载 `/var/run/docker.sock`，等同于拥有宿主机高权限。MVP 允许这种实现，但必须满足：

- Manager 容器不对公网直接暴露
- Manager 所在网络仅接受 Caddy 转发
- 登录鉴权默认开启
- 文档明确标记该模式为“单机自托管可信宿主机”前提

远期优化方向：

- 使用受限的 docker-socket-proxy
- 或改成宿主机守护进程模式

### 9.4 临时安全浏览声明

本产品提供的是“容器级隔离增强”，不是强对抗沙箱。

因此文档需明确：

- 默认不使用 `--no-sandbox`
- 即便如此，仍不能宣称达到企业安全浏览器级别
- 如果用户主动启用 `--no-sandbox`，产品应显示风险提示

---

## 10. 数据模型

### 10.1 SQLite 表

`users`

| 字段 | 说明 |
|------|------|
| `id` | 用户 ID |
| `username` | 用户名 |
| `email` / `display_name` | 规范化且唯一的邮箱 / 可选显示名称 |
| `role` / `status` | admin 或 user；active / disabled / deleting / delete_failed |
| `instance_quota` | 个人当前实例上限，0 表示禁止新建 |
| `must_change_password` / `auth_version` | 首次改密标记 / 凭据撤销版本 |
| `delivery_status` / `delivery_error` / `last_error` | 密码交付结果与删除错误，不保存明文密码 |
| `password_hash` | 密码哈希 |
| `created_at` | 创建时间 |

`sessions`

| 字段 | 说明 |
|------|------|
| `id` | `session_id` |
| `user_id` | 所属用户 |
| `status` | 当前状态 |
| `name` / `desired_state` / `last_error` | 实例名称、期望状态和可见错误 |
| `create_request_key` | 每用户创建请求幂等标识，删除后的请求键保留，避免迟到重试重新创建 |
| `recovery_attempts` / `recovery_next_at` | 有界重试恢复信息 |
| `container_name` | 容器名 |
| `profile_dir` | Profile 目录 |
| `downloads_dir` | 下载目录 |
| `session_token_hash` | 会话访问令牌哈希 |
| `last_active_at` | 最后活跃时间 |
| `created_at` | 创建时间 |
| `expired_at` | 仅兼容旧库，不再产生空闲过期时间 |

`session_events`

| 字段 | 说明 |
|------|------|
| `id` | 事件 ID |
| `session_id` | 会话 ID |
| `type` | 创建、启动、连接、断开、停止、错误等资源事件；旧 expired 仅保留历史 |
| `payload_json` | 扩展字段 |
| `created_at` | 事件时间 |

`session_files`

| 字段 | 说明 |
|------|------|
| `id` | 文件记录 ID |
| `session_id` | 会话 ID |
| `name` | 文件名 |
| `size_bytes` | 文件大小 |
| `content_type` | MIME 类型 |
| `created_at` | 写入时间 |

另有 `schema_migrations`、`login_sessions`、`instance_requests`、`mail_deliveries`、`audit_events`。迁移在单个事务中保留原 ID、密码哈希、实例归属和目录。用户实例外键不级联删除资源，清理完成后才能删除账号；审计不采集浏览内容或密码。

SQLite 启用 foreign_keys、busy_timeout、即时写事务与 DELETE 回滚日志。长期 Manager 通过文件锁避免多个生命周期核对器共同管理一个目录，短期密码重置使用独立事务连接。启动进行数据库完整性快速检查；发现损坏拒绝启动，不静默重建数据库。实际持久性需在部署文件系统执行停止和重启验证。

### 10.2 为什么使用 SQLite

- 单机部署简单
- 便于打包到单容器/Compose
- 对当前会话量足够
- 后续可平滑替换为 PostgreSQL，不影响 API 抽象

---

## 11. API 草案

### 11.1 管理面 API

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/login` | 登录 |
| `POST` | `/api/logout` | 登出 |
| `GET` | `/api/sessions` | 获取会话列表 |
| `POST` | `/api/sessions` | 新建会话 |
| `POST` | `/api/sessions/{id}/start` | 启动已停止会话 |
| `POST` | `/api/sessions/{id}/stop` | 停止会话 |
| `DELETE` | `/api/sessions/{id}` | 删除会话 |
| `GET` | `/api/sessions/{id}` | 获取会话详情 |

### 11.2 剪贴板 API

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/sessions/{id}/clipboard/push` | 本地文本推送到远端 |
| `GET` | `/api/sessions/{id}/clipboard/pull` | 获取远端最近一次文本 |

说明：

- `pull` 可通过短轮询或 SSE 实现
- MVP 优先短轮询，减少实现复杂度

### 11.3 文件 API

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/sessions/{id}/files` | 获取当前会话下载目录文件列表 |
| `POST` | `/api/sessions/{id}/files/upload` | 上传文件到当前会话 |
| `GET` | `/api/sessions/{id}/files/{name}` | 下载当前会话文件 |

约束：

- 仅允许访问当前 `session_id` 的下载目录
- `{name}` 只接受单文件名，不接受路径片段
- 上传默认大小限制由 `RB_MAX_UPLOAD_SIZE` 控制，MVP 建议 `100MB`

### 11.4 会话代理路径

| 路径 | 说明 |
|------|------|
| `/sessions/{id}/view` | 会话页面壳 |
| `/sessions/{id}/novnc/*` | Manager 代理到 Session 容器内 noVNC |

---

## 12. 部署设计

### 12.1 MVP 推荐部署

```yaml
services:
  caddy:
    image: caddy:2
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./deploy/Caddyfile:/etc/caddy/Caddyfile
      - caddy_data:/data
    depends_on:
      - manager

  manager:
    image: remotebrowser/manager:mvp
    environment:
      - RB_ADMIN_PASSWORD=${RB_ADMIN_PASSWORD}
      - RB_DEFAULT_INSTANCE_QUOTA=5
      - RB_PUBLIC_URL=https://browser.example.com
      # SMTP 配置可选；完整配置与绝对宿主机目录见 deploy/docker-compose.yml
      - RB_DATA_DIR=/data
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./data:/data
    expose:
      - "8080"

volumes:
  caddy_data:
```

设计要求：

- Session 容器由 Manager 动态创建，不写死在 Compose 中
- 每个 Session 容器使用带部署、用户、实例标签的独立 Docker network，Manager 加入该网络进行代理；实例之间不共用可直接互访的桥接网络
- Caddy 只反代 Manager

### 12.2 Session 容器启动参数

Manager 调用 Docker API 创建 Session 容器时，固定注入：

- `RB_SESSION_ID`
- `RB_PROFILE_DIR=/home/rbuser/profile`
- `RB_DOWNLOADS_DIR=/home/rbuser/downloads`
- `RB_NOVNC_PORT=6080`
- `RB_FILE_SERVICE_PORT=8081`

资源限制建议：

- `Memory: 2 GiB`
- `NanoCPUs: 1 CPU`
- `ShmSize: 512 MiB`

### 12.3 Caddy 路由要求

Caddy 只需要两条主路由：

- `/api/*` 与 `/sessions/*` 转发给 Manager
- `/` 转发管理面板静态资源

这样部署简单，而且不会因为 Session 数量增加而膨胀配置。

---

## 13. 里程碑

### 13.1 MVP

目标：

- 支持登录
- 支持多会话创建、切换、重连、停止、删除
- 支持文本剪贴板双向同步
- 支持拖拽上传和下载到本地
- 支持持久实例、个人配额、账号管理与可选 SMTP，不进行空闲回收
- 支持单机 Compose 一键部署

交付物：

- `manager` 服务
- `session` 镜像
- `docker-compose.yml`
- `Caddyfile`
- 基础 Web 管理面板
- 会话文件浮层与文件服务

验收：

- 同时创建 3 个会话，Cookie/LocalStorage 互不影响
- 关闭浏览器标签页超过旧 30 分钟阈值与至少 24 小时后，仍可重连同一实例；手动启停和服务重启保留持久数据
- 中文文本可双向复制
- 可向任意运行中会话拖拽上传文件，并可下载到本地
- 外网只暴露 80/443

### 13.2 v0.2

新增：

- 访问事件日志查询
- 基础会话统计

### 13.3 v0.3

新增：

- 持久实例已纳入本期；后续可扩展备份管理
- 录像能力
- 多用户固定角色、管理员用户列表和配额管理已纳入本期；复杂组织权限不在本期范围

### 13.4 v1.0

新增：

- WebRTC 变体
- 托管化部署能力

---

## 14. 技术风险与对策

| 风险 | 影响 | 对策 |
|------|------|------|
| Chromium 在低配 VPS 启动慢 | 影响首屏 3 秒目标 | 镜像预热、减少启动项、后续可引入 warm pool |
| Docker Socket 权限过大 | 宿主机风险高 | Manager 不公网暴露，默认鉴权开启，后续引入 socket proxy |
| 浏览器 Clipboard API 行为不一致 | 双向剪贴板体验不稳定 | 首次授权引导，提供降级复制按钮 |
| 多会话资源抢占 | VPS 卡顿 | 强制会话上限和资源限制 |
| 用户 Profile 目录持续膨胀 | 磁盘占满 | 增加容量告警与后续清理策略 |
| VNC 延迟较高 | 视频场景体验差 | 明确 MVP 不以视频播放为目标，后续增加 WebRTC 变体 |

---

## 15. 开发建议

实现顺序建议：

1. 先完成 Session 镜像，验证单容器 Chromium + noVNC 稳定运行
2. 再实现 Manager 的会话状态机与 Docker API 调度
3. 接着实现 Manager 内建会话代理，打通稳定路径访问
4. 然后补文件服务与文件浮层
5. 最后补管理面板细节与剪贴板桥接

不要先做：

- WebRTC
- 录像
- 复杂多用户权限系统

---

## 16. 附录

### 16.1 推荐仓库结构

```text
.
├─ cmd/
│  └─ manager/
│     └─ main.go
├─ internal/
│  ├─ auth/
│  ├─ config/
│  ├─ clipboard/
│  ├─ docker/
│  ├─ files/
│  ├─ httpapi/
│  ├─ model/
│  ├─ proxy/
│  ├─ session/
│  ├─ store/
│  └─ view/
├─ migrations/
│  ├─ 0001_init.sql
│  └─ 0002_session_files.sql
├─ sql/
│  ├─ queries/
│  └─ schema/
├─ web/
│  ├─ static/
│  │  ├─ css/
│  │  ├─ js/
│  │  └─ vendor/
│  └─ templates/
│     ├─ pages/
│     ├─ partials/
│     └─ layouts/
├─ docker/
│  ├─ session/
│  │  ├─ Dockerfile
│  │  ├─ entrypoint.sh
│  │  └─ supervisord.conf
│  └─ manager/
│     └─ Dockerfile
├─ deploy/
│  ├─ docker-compose.yml
│  └─ Caddyfile
├─ pkg/
│  └─ version/
├─ scripts/
│  ├─ dev.sh
│  └─ migrate.sh
└─ data/
```

目录职责建议：

- `cmd/manager`：程序入口
- `internal/auth`：登录、Cookie、会话校验
- `internal/session`：状态机、生命周期管理
- `internal/docker`：Docker API 封装
- `internal/proxy`：会话代理、WebSocket 代理
- `internal/clipboard`：剪贴板桥接
- `internal/files`：文件上传下载网关
- `internal/httpapi`：路由、handler、请求响应结构
- `internal/store`：SQLite 访问
- `internal/view`：模板渲染与页面装配
- `web/templates`：SSR 模板
- `web/static/vendor`：固定版本的 HTMX、Alpine.js、noVNC

### 16.2 后续演进边界

后续如果切到 WebRTC，建议保持以下抽象不变：

- `Manager` 仍作为唯一控制面
- `session_id`、状态机、数据目录模型不变
- 仅替换“会话面协议适配层”，不推翻控制面
