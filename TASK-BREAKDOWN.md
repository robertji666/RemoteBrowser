# RemoteBrowser MVP 任务拆解

> 基于 `TECH-SPEC.md` v2.0  
> 日期：2026-04-24  
> 目标：把 MVP 技术方案拆成可执行、可排期、可验收的开发任务

---

## 1. 拆解原则

- 只覆盖 MVP：远程浏览器访问、多会话管理、会话守护与重连、双向文本剪贴板、文件上传下载
- 先打通主链路，再补增强体验
- 优先保证控制面稳定性，再做前端细节
- 每个阶段结束后都应有可运行产物，而不是只完成代码骨架

---

## 2. 总体实施顺序

推荐按以下顺序推进：

1. Session 镜像与单容器验证
2. Manager 基础框架与存储
3. Session 生命周期管理与 Docker 调度
4. 会话代理与稳定访问路径
5. 管理面板基础功能
6. 会话守护、重连、回收
7. 文件服务与文件浮层
8. 剪贴板双向同步
9. 部署收口、验收与文档

关键原因：

- `Session 镜像` 是一切能力的前提，不先验证会把后续问题都堆到控制面
- `生命周期管理 + 代理` 是 MVP 核心链路，没有这两块就没有真正的多会话产品
- `文件` 和 `剪贴板` 都依赖运行中的会话页与稳定代理路径

---

## 3. 阶段拆解

## 阶段 A：项目脚手架与基础约束

目标：建立可持续开发的仓库结构、配置和最小运行骨架。

任务：

- A1. 初始化 Go 项目结构
- A2. 建立 `cmd/manager` 入口
- A3. 建立 `internal/auth`、`internal/session`、`internal/docker`、`internal/proxy`、`internal/files`、`internal/clipboard`、`internal/store`、`internal/httpapi`、`internal/view`
- A4. 建立 `web/templates`、`web/static`、`migrations`、`deploy`、`docker/session`
- A5. 增加统一配置加载层，支持 `RB_ADMIN_PASSWORD`、`RB_MAX_SESSIONS`、`RB_SESSION_IDLE_TIMEOUT`、`RB_DATA_DIR`、`RB_MAX_UPLOAD_SIZE`
- A6. 增加结构化日志、基础错误处理、健康检查入口

交付标准：

- 仓库结构与 `TECH-SPEC.md` 建议一致
- Manager 能启动 HTTP 服务并返回基础健康检查
- 本地开发和容器启动方式明确

依赖：无

---

## 阶段 B：Session 镜像与单容器验证

目标：先证明单个浏览器容器可稳定运行，支持 noVNC 和文件服务。

任务：

- B1. 编写 Session Dockerfile
- B2. 安装与配置 `Chromium + Xvfb + Fluxbox + TigerVNC + noVNC/websockify`
- B3. 建立非 root 运行用户 `rbuser`
- B4. 约定目录 `/home/rbuser/profile`、`/home/rbuser/downloads`
- B5. 编写容器入口脚本，按顺序拉起桌面栈和 Chromium
- B6. 增加容器内文件服务，提供 `POST /upload`、`GET /files`、`GET /download/{name}`、`GET /healthz`
- B7. 增加 Session 健康检查
- B8. 验证 Chromium 启动参数和 `shm` 配置

交付标准：

- 单独运行 Session 容器后，访问 `6080` 可看到桌面
- Chromium 能正常启动并可操作
- 文件服务可上传、列出、下载文件
- `/healthz` 可用于 Manager 探活

依赖：阶段 A

---

## 阶段 C：SQLite、Migration 与数据访问层

目标：先把会话元数据模型稳定下来，避免后续状态流转失控。

任务：

- C1. 建立 SQLite 初始化与连接管理
- C2. 编写首版 migration：`users`、`sessions`、`session_events`
- C3. 编写第二版 migration：`session_files`
- C4. 实现 store 层 CRUD
- C5. 建立事件写入能力，用于记录 `created/started/connected/disconnected/stopped/expired/error`
- C6. 初始化默认管理员账户或首启密码逻辑

交付标准：

- 数据库可自动初始化
- 会话、用户、事件、文件记录均可写入和查询
- 状态字段和值与技术方案一致

依赖：阶段 A

---

## 阶段 D：鉴权与基础 HTTP 框架

目标：先把“谁能访问系统、谁能访问某个会话”这件事收紧。

任务：

- D1. 实现登录页与登录接口
- D2. 使用安全 Cookie 实现登录态
- D3. 中间件校验未登录访问
- D4. 中间件校验 `session_id` 所属关系
- D5. 实现登出接口
- D6. 增加基础页面渲染框架与布局模板

交付标准：

- 未登录无法访问管理面和会话页
- 登录后可进入管理面
- 会话相关 API 都经过登录态和所属关系校验

依赖：阶段 A、阶段 C

---

## 阶段 E：Session 生命周期管理与 Docker 调度

目标：完成 MVP 的核心能力，Manager 可以真正管理多个会话容器。

任务：

- E1. 封装 Docker Client
- E2. 实现 allocator，负责生成 `session_id`、容器名、宿主目录
- E3. 新建会话时写元数据并创建目录
- E4. 调用 Docker API 创建 Session 容器并注入固定环境变量
- E5. 实现启动流程：`creating -> starting -> running`
- E6. 实现停止流程：`running/disconnected -> stopping -> stopped`
- E7. 实现删除流程：删除容器、元数据和可选数据目录
- E8. 实现容器与应用级健康检查
- E9. 实现错误状态与有限重试
- E10. 实现 `RB_MAX_SESSIONS` 限制

交付标准：

- 可创建多个独立 Session 容器
- 会话状态切换与技术方案一致
- 容器启动失败时能进入 `error`，而不是卡死
- 每个会话目录、令牌、容器名独立

依赖：阶段 B、阶段 C、阶段 D

---

## 阶段 F：会话代理与稳定路由

目标：让用户通过稳定路径访问会话，而不是依赖动态端口。

任务：

- F1. 实现 `/sessions/{id}/view` 页面壳
- F2. 实现 `/sessions/{id}/novnc/*` 反向代理
- F3. 处理 WebSocket 透传
- F4. 建立 `router registry`，维护 `session_id -> container upstream`
- F5. 在代理层更新连接态与最后活跃时间
- F6. 处理会话不存在、状态不可连接、容器未就绪等错误页面

交付标准：

- 用户通过固定 URL 可进入任意运行中会话
- 不暴露 Session 容器宿主机端口
- WebSocket 连接稳定，断开后可识别为 `disconnected`

依赖：阶段 E

---

## 阶段 G：管理面板 MVP

目标：提供完整的会话管理闭环。

任务：

- G1. 管理首页布局与会话列表页
- G2. `GET /api/sessions`
- G3. `POST /api/sessions`
- G4. `POST /api/sessions/{id}/start`
- G5. `POST /api/sessions/{id}/stop`
- G6. `DELETE /api/sessions/{id}`
- G7. `GET /api/sessions/{id}`
- G8. 用 HTMX 实现列表局部刷新与操作反馈
- G9. 展示状态、创建时间、最后活跃时间

交付标准：

- 用户可以从面板完成新建、进入、停止、删除、重连
- 列表状态与后台状态一致
- 多会话同时存在时可清晰区分和切换

依赖：阶段 D、阶段 E、阶段 F

---

## 阶段 H：会话守护、重连与回收

目标：实现“关标签页不丢会话”的产品承诺。

任务：

- H1. 定义连接态与容器态分离逻辑
- H2. 浏览器断开时把状态置为 `disconnected`
- H3. 重新接入时恢复为 `running`
- H4. 定时 reaper 扫描空闲会话
- H5. 超时后执行 `disconnected -> expired`
- H6. `expired` 默认删除容器并保留 profile 目录
- H7. 管理面板支持 stopped 会话重启、expired 会话禁用重连
- H8. 记录最后活跃时间和相关事件

交付标准：

- 关闭标签页后 5 分钟内能重连原会话
- 超过 `RB_SESSION_IDLE_TIMEOUT` 的会话会被正确回收
- `running`、`disconnected`、`stopped`、`expired` 行为符合文档定义

依赖：阶段 E、阶段 F、阶段 G

---

## 阶段 I：文件上传下载

目标：完成会话级文件传输能力。

任务：

- I1. Manager 侧实现文件网关
- I2. `GET /api/sessions/{id}/files`
- I3. `POST /api/sessions/{id}/files/upload`
- I4. `GET /api/sessions/{id}/files/{name}`
- I5. 代理转发到 Session file service
- I6. 文件名与路径安全校验
- I7. 上传大小限制 `RB_MAX_UPLOAD_SIZE`
- I8. 前端会话页增加文件浮层
- I9. 支持拖拽上传、进度显示、列表刷新、点击下载
- I10. 同步 `session_files` 元数据

交付标准：

- 可拖拽上传文件到当前会话下载目录
- 可列出当前会话文件并下载到本地
- 不能越权访问其他会话或宿主机路径

依赖：阶段 B、阶段 D、阶段 F、阶段 G

---

## 阶段 J：双向文本剪贴板

目标：补齐高频交互体验，但控制复杂度，只做文本。

任务：

- J1. 前端接入 noVNC 剪贴板能力
- J2. 实现本地复制到远端：`POST /api/sessions/{id}/clipboard/push`
- J3. 实现远端复制到本地：`GET /api/sessions/{id}/clipboard/pull`
- J4. 前端请求 Clipboard API 权限并显示状态
- J5. 不支持静默写入时提供降级“复制到本地”按钮
- J6. 处理中文、英文、多行文本编码
- J7. 跨重连后的剪贴板状态恢复

交付标准：

- 本地复制文本后可在远端粘贴
- 远端复制文本后可同步到本地或通过降级按钮获取
- 中文和多行文本可正确同步

依赖：阶段 F、阶段 G

---

## 阶段 K：部署、收口与验收

目标：把 MVP 变成可部署、可验收、可演示的完整交付物。

任务：

- K1. 编写 Manager Dockerfile
- K2. 编写 `deploy/docker-compose.yml`
- K3. 编写 `deploy/Caddyfile`
- K4. 校验仅暴露 `80/443`
- K5. 编写初始化与部署文档
- K6. 增加基础运行监控与日志说明
- K7. 编写验收清单与手工测试脚本
- K8. 压测 3 会话并发场景

交付标准：

- 通过 Compose 可一键部署
- 外网仅暴露 80/443
- 满足 TECH-SPEC 中 MVP 验收项

依赖：全部前序阶段

---

## 4. 并行开发建议

可并行组 1：

- 阶段 B：Session 镜像
- 阶段 C：SQLite 与 migration
- 阶段 D：鉴权与页面骨架

可并行组 2：

- 阶段 E：生命周期与 Docker 调度
- 阶段 G：管理面板基础页面

说明：

- 阶段 G 可以先做静态壳和列表模板，等 E 完成后接真实数据

可并行组 3：

- 阶段 I：文件能力
- 阶段 J：剪贴板能力

说明：

- 两者都依赖阶段 F 的会话页和代理链路
- 文件和剪贴板的后端模块边界独立，适合拆人并行

---

## 5. 里程碑视角的交付定义

### M1：单会话跑通

范围：

- 阶段 A、B、C、D 的核心内容

验收：

- 能登录
- 能手动启动单个 Session 容器
- 能通过 noVNC 看到 Chromium

### M2：多会话 MVP 主链路

范围：

- 阶段 E、F、G、H

验收：

- 可创建 3 个会话
- 会话隔离有效
- 可关闭页面后重连
- 超时自动回收

### M3：交互能力补齐

范围：

- 阶段 I、J

验收：

- 可上传下载文件
- 可双向复制文本

### M4：可部署交付

范围：

- 阶段 K

验收：

- Compose 一键部署
- 外网只暴露 80/443
- 按 TECH-SPEC 完成完整 MVP 验收

---

## 6. 推荐优先级

P0 必做：

- 阶段 B、C、D、E、F、G、H、I、J、K

P0.5 早做但允许简化：

- 文件列表元数据落库
- 错误页面优化
- 列表局部刷新体验

P1 可后补：

- warm pool
- 更细粒度事件日志查询
- 更完整的健康检查可视化

---

## 7. 关键依赖关系

- 没有阶段 B，就无法验证 Docker 调度的目标容器是否可靠
- 没有阶段 C，阶段 E/H 的状态流转无法稳定落库
- 没有阶段 F，文件和剪贴板都无法稳定绑定到会话页
- 没有阶段 H，就无法兑现“关闭网页后可重连”
- 没有阶段 K，项目仍然只是开发态工程，不算 MVP 交付完成

---

## 8. 风险最高的任务

优先盯紧这 5 项：

- Session 容器桌面栈稳定性
- WebSocket 代理与 noVNC 兼容性
- 状态机与实际容器状态一致性
- 剪贴板跨浏览器权限差异
- 文件网关的路径安全与流式转发

建议处理方式：

- 先做最小可运行链路验证，不要一开始就堆完整 UI
- 每完成一个高风险模块就做手工回归，不等全部做完再联调

---

## 9. 建议的首批开发任务清单

如果按最小风险顺序立刻开工，建议先开以下 10 个任务：

1. 初始化 Go 工程和目录结构
2. 完成 Session Dockerfile 与 entrypoint
3. 在单容器内跑通 Chromium + noVNC
4. 为 Session 增加 `/healthz` 与文件服务
5. 建立 SQLite migration 与 store 层
6. 完成 Manager 登录和鉴权中间件
7. 完成 Docker Client 封装与会话创建接口
8. 完成 `/sessions/{id}/novnc/*` 代理
9. 完成管理面会话列表和创建入口
10. 完成断开重连与 idle reaper

---

## 10. 完成定义

本项目的 MVP “完成” 不等于代码写完，而必须同时满足：

- 功能完整覆盖 TECH-SPEC 第 13.1 节 MVP
- 按固定路径访问会话，不暴露动态宿主机端口
- 3 个并发会话下可正常创建、切换、重连
- 文件上传下载与文本剪贴板可用
- Compose 可一键部署
- 有基本部署文档和验收清单
