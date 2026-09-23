# 多用户与持久浏览器实例：验收覆盖矩阵

依据：`PRD-MULTIUSER-PERSISTENT-INSTANCES.md` v0.2，A-01～A-21。

## 最新收尾覆盖（2026-09-17 17:00 UTC后）

此节覆盖下文旧快照中“仍须完成”的状态，不改变既有测试证据层级。

- A-09的完整24h等待由用户明确接管，代理不再执行。原基线最后样本11.127h正常/pending；随后最终部署与重连结束该段计时，不声称24h通过。
- A-16补齐Alice真实播放测试音后的PCM捕获：1335296bytes、peak2622、RMS199.60，报告 `data-e2e/audio-report-sess_6dbacc820c313290.json`；双用户中文、文件、重连及PCM均有真实证据，非主观听感保证。
- 最终v3 manager已部署，HTTP静态JS哈希等于工作区。实际后台id7零实例账号二次确认点取消后刷新仍存在；A-19取消分支不再仅依赖隔离预览。
- 实际重连A后原中文站点存储、Cookie、收藏仍在；容器ID/启动时间不变、重启0次。详情见 `BROWSER-TEST-REPORT.md` 最新节。
- 其他A-01～A-21继续采用以下各行列明的实际部署、HTTP/SMTP/SQLite/故障注入证据；整机/Docker daemon重启、外部邮箱实收、主观听感与全输入法组合仍为披露边界，不声称实测。
- 最终全量 `GOCACHE=/private/tmp/rb-root-gocache go test ./...` 通过（HTTP36.635s）；确认交互Node8/8、diff-check通过。6份真实平台/清理/撤销/双用户音频JSON报告均重新读取为passed。按用户调整后的范围收尾，不再存在自动等待24h门禁。

审计快照：2026-09-17 UTC（温哥华 2026-09-16）。这份文档记录现有测试的**实际断言范围**，不将测试函数存在、编译成功、模拟 Docker、容器健康或 HTTP 成功响应等同于最终产品验收。

## 证据层级与本轮结果

| 层级 | 证据 | 能证明什么 / 不能证明什么 |
| --- | --- | --- |
| 数据与服务测试 | `internal/store/*_test.go`、`internal/session/service_test.go` | 使用真实临时 SQLite、真实临时目录；生命周期中的 Docker 使用 `fakeRuntime`。能验证事务、归属、错误处理和目录保留，不能证明 Docker daemon、Chrome 或实际网络的行为。 |
| Docker 客户端契约测试 | `internal/docker/client_test.go` | 使用本机 HTTP 测试服务器模拟 Docker API，核对独立网络、端口绑定和清理调用；不能替代真实容器互访测试。 |
| HTTP / SMTP 集成测试 | `internal/auth/*_test.go`、`internal/httpapi/*_test.go`、`internal/mail/smtp_test.go`、`internal/files/service_test.go` | 真实 SQLite、Go HTTP 处理链、本机 TCP/SMTP、真实请求取消。模板测试验证渲染内容，不包含浏览器布局、点击或播放。 |
| WebRTC 服务测试 | `docker/session/webrtc-service/leases_test.go` | 使用真实 Pion `PeerConnection` 验证租约到期及关闭；没有协商连接至浏览器，也没有实际画面或音频媒体流。 |
| 本机部署脚本 | `scripts/e2e-platform.mjs`、`scripts/e2e-resource-cleanup.mjs`、`scripts/e2e-revocation.mjs`、`scripts/e2e-transport-revocation.mjs` 及 `data-e2e/` 对应报告 | 真实部署上的 HTTP API、Docker 容器、物理目录 / 网络清理及文件传输；撤销脚本包含真实浏览器 WebRTC peer 和 PCM / input / noVNC TCP-WebSocket 连接。脚本自身没有浏览器引擎，不能单独代替交互或播放验收。 |
| 浏览器 / 长时间 / 基础设施验收 | `BROWSER-TEST-REPORT.md`、`IMPLEMENTATION-STATUS.md`、`data-e2e/soak-report.json` | 已记录 Alice 的中文及 Chrome 持久化、双用户登录与画面、真实 manager 重启和 WebRTC 撤销。声音及 24 小时静置仍未通过，不代填未完成结果。 |

已读取的部署报告快照：运行 `mu537vjz`，`2026-09-17T05:26:57.407Z` 开始，`05:28:05.416Z` 结束，报告 `result=passed`。它包含 14 条检查记录，其中部分是同类用户流程重复。报告后续可能被新的运行覆盖；此处不推定后续运行结果。

后续证据同步（只读取既有报告，未重跑或连接 soak 实例）：

- 资源清理报告 `mu53lu4n-886b07`，05:37:48～05:38:13 UTC，`passed`：3 个真实实例的容器、6 个挂载目录、父目录及专属网络均消失，A/B 资源身份和下载标记保持不变。第三个实例为外部停止，不假称已经形成平台 `error` 状态。
- 撤销报告 05:52:09～05:52:10 UTC，`passed`：真实 Bob 浏览器 WebRTC peer 从 1 到 0 为 325ms，A/B 容器继续运行且 ID 不变；浏览器显示重新登录且实例保留的说明。
- WS 撤销报告 `transport-revocation-report.json` 最新读取快照 06:01:39.861～06:01:46.130 UTC，`passed`：Bob 一个登录退出后 PCM / input / noVNC 真实连接分别于 1455 / 1470 / 1479ms 关闭，旧 Cookie HTTP 401；另一个独立登录的三条通道继续响应且 HTTP 200，B 的容器 ID / StartedAt / restartCount 不变且运行。不涉及 A，不把 PCM 字节流或 VNC 握手声称为播放 / 解码成功。
- A 的 24h soak 基线为 `2026-09-17T05:53:07.907Z`，最早结束 `2026-09-18T05:53:07.907Z`，当前报告仍为 `pending`。期间不得为补测试连接、启停或替换 A，也不得重启其 manager / Docker。

本轮 auth / config / mail / HTTP / files 最终包检查已完成：

```text
GOCACHE=/private/tmp/rb-auth-gocache go test -race ./internal/auth ./internal/config ./internal/mail ./internal/httpapi ./internal/files
internal/auth      PASS 36.881s
internal/config    PASS (cached)
internal/mail      PASS (cached)
internal/httpapi   PASS 71.340s
internal/files     PASS 1.519s
```

同范围 `go vet`、`git diff --check` 通过；这一组命令本身没有执行真实 Docker 或浏览器操作。后续 `IMPLEMENTATION-STATUS.md` 已记录 root 全量 `go test -race ./...` / `go vet ./...` / build 通过（httpapi 115.712s，session 8.252s），两个嵌套 Go 模块 race / vet、6 个 Python 测试、Shell / JS 语法、Compose 配置和差异检查通过；不再把全量静态 / 自动化门禁写作尚未运行。后续若改变生产代码，需按改动范围补验。

随后补充 A-20 / A-21 混合状态级联删除的 HTTP 组合回归：`go test -race ./internal/httpapi -run TestHTTPMixedState -count=1` 通过（29.597s）；`go vet ./internal/httpapi` 和对应差异检查通过。该组测试使用真实认证 / HTTP / SQLite / 临时目录，但 Docker 容器和网络仍是模拟对象。

新增连接计数专项回归：`go test -race ./internal/httpapi ./internal/session ./internal/proxy -run '(ConnectionCount|ConnectionOpen|TransportOpen|WebRTCFailedConnectionRegistration|NoIdleExpiration)' -count=1` 通过（HTTP 20.454s、session 1.650s，proxy 编译通过）；相同三包 `go vet` 和差异检查通过。`internal/httpapi/connection_count_test.go` 的 `TestAudioProxyConnectionCountReturnsToZero` 使用真实本机 TCP 101 连接，验证客户端关闭 / 退出登录 / 上游拒绝均不残留计数；`TestWebRTCProxyLeaseConnectionCountReturnsToZero` 验证信令响应结束后租约仍计连接，续期、退出登录和 peer 消失后正确归零。媒体 sender 和 Docker inspect API 是模拟服务，不证明真实音视频。

同文件 `TestCancelledTransportOpenDoesNotDecrementAnotherConnection`、`TestStoppedTransportOpenDoesNotDecrementAnotherConnection` 覆盖 PCM / Input / noVNC 在请求取消或并发停止后不误扣另一条连接；`TestWebRTCFailedConnectionRegistrationDeletesLeaseWithoutDecrement` 验证 WebRTC 登记失败立即删除租约、不续期且不扣其他连接。`internal/session/connection_count_test.go` 的 `TestConnectionOpenErrorsNeverIncrement` 保证成功返回才代表计数增加，失败不改变计数。取消用例曾明确复现 1 被误减为 0，修复后已通过。

计数修复后完整复跑 `go test -race ./internal/httpapi ./internal/session ./internal/proxy -count=1` 通过：HTTP 119.596s、session 6.877s、proxy 无独立测试但编译通过（其真实 handler 路径由 HTTP 计数回归调用）。

`scripts/e2e-audio.mjs` 已提供真实认证 PCM 捕获工具；编写阶段仅执行 `node --check`，没有由此自动获得音频通过结论。工具接收报告中的 A / B 实例，最多捕获 30 秒，输出字节量、峰值和 RMS；只有足量明显非零的 48kHz / 16bit LE / 双声道 PCM 才通过，静音流或超时明确失败。它不播放音频、不保存原始 PCM，也不声称代表听感或 WebRTC 音频。实际运行结果由主任务单独取证。

## A-01～A-21 逐项映射

下面的“已有覆盖”表示代码中有对应断言；测试结果以相应执行记录为准。“缺口”同时区分尚缺的自动化场景与必须在真实环境执行的验收。

| 验收项 | 现有测试函数 / 自动化证据 | 已有覆盖及明确缺口 |
| --- | --- | --- |
| A-01 首次部署与重复启动 | `internal/auth/auth_test.go`：`TestBootstrapPreservesExistingPassword`；`cmd/manager/main_test.go`：`TestCLIResetWorksWhileManagerOwnsDirectory`、`TestAdministratorInitFailureClosesDatabase`；`internal/store/store_test.go`：`TestUpgradeLegacyDatabasePreservesIdentityAndOwnership`；`BROWSER-TEST-REPORT.md` | 验证已有 admin ID / 哈希保留、环境密码不覆盖、显式 CLI 环境变量和 stdin 重置、旧凭据撤销、首次缺密码报错且释放锁。部署脚本验证当前 admin 可登录；“fresh bootstrap”报告标签本身不能证明仅首次创建，首次规则由前述测试证明。真实 manager 带 3 用户 / 2 实例多次停止、重启和重建后身份保留，SQLite 完整性检查通过。 |
| A-02 无邮件新增用户 | `internal/auth/auth_test.go`：`TestNormalizedLoginAndFirstPasswordChange`、`TestManualCreationRequiresPasswordWithoutCreatingAccount`；`internal/httpapi/accounts_test.go`：`TestAccountHTTPUserManagementAndTemplates`；部署脚本 `newUser()` | 验证手工密码必填且缺失不创建账号、邮箱规范化、首登改密、旧密码 / 登录撤销、无邮件可用。部署脚本在真实服务完成 Alice/Bob 登录与改密。浏览器表单操作仍由浏览器验收补齐。 |
| A-03 邮件新增用户 | `internal/auth/mail_integration_test.go`：`TestEmailInvitationLoginRetryAndNoPlaintextStorage`；`internal/mail/smtp_test.go`：`TestSMTPDeliverCredentialAndTestMessages`、`TestSMTPRequiresTLSAndValidConfiguration` | 本机测试 SMTP 通过真实 TCP 接收邮件，核对收件人、登录 URL、临时密码和首次改密说明，并用邮件密码登录。验证拒绝不安全远程明文和 STARTTLS 降级。当前成功路径是 plain 测试链路，不声称 TLS/STARTTLS 成功投递实测。PRD 要求区分 provider 接收与最终到达，不要求向真实外部邮箱发信才算测试；本机集成证据可覆盖账号交付流程。 |
| A-04 邮件失败与重试 | `internal/auth/auth_test.go`：`TestMailFailureKeepsSingleUserAndResetDoesNotAffectOthers`；`internal/auth/mail_integration_test.go`：`TestEmailInvitationLoginRetryAndNoPlaintextStorage`；`internal/mail/smtp_test.go`：`TestSMTPRejectSanitizesProviderError`、`TestSMTPTimeout` | 验证创建失败通知仍保留唯一用户、重复邮箱拒绝、重发换密码并撤销旧登录、已完成改密不能伪装重发、显式手工回退、其他用户登录不受影响、SMTP 拒绝与超时不泄漏响应原文。账号流程中的发送失败由无效配置触发；SMTP 550/超时是传输层独立用例，尚未组合成账号页面端到端失败演示。 |
| A-05 身份隔离 | `internal/httpapi/accounts_test.go`：`TestEveryInstanceSurfaceRejectsOtherUsersAndAdmin`、`TestAccountHTTPUserManagementAndTemplates`、`TestHTTPLoginAndCSRF`；`internal/auth/auth_test.go`：`TestCSRFAcrossFormsJSONAndWebSockets`；`internal/session/service_test.go`：`TestContainerOwnershipPreventsCrossInstanceRemoval`、`TestNewInstanceUsesDedicatedNetwork`；Docker 网络测试、部署脚本及 `BROWSER-TEST-REPORT.md` | 已覆盖普通用户和管理员均不能进入他人 15 个实例入口、列表不泄漏、管理权限、CSRF及跨站 WS 拒绝。真实 A/B 容器双向访问对方 6080 / 6081 / 6082 / 6084 / 8081 均超时，各自外网 HTTP 200；真实浏览器工作区只列本人实例。直接 VNC 5901 尚无独立网络探测记录，不将已测端口扩称为所有端口。 |
| A-06 停止实例占配额 | `internal/store/store_test.go`：`TestQuotaCountsAllStatesAndLoweringRetainsInstances`、`TestCreationIdempotencyAndCompletedDeletion`；`internal/session/service_test.go`：`TestDeleteFailureKeepsQuotaAndDataUntilRetry`；部署脚本停止后超额创建检查 | 覆盖运行 / 停止 / 异常均占配额、完整删除记录后新请求可占位、失败清理不释放。真实部署脚本验证停止仍占位及删除容器。脚本快照下调配额后删除一个实例，未再创建验证“释放后可创建”；该子条件目前由数据层测试证明。 |
| A-07 并发创建与失败 | `internal/store/store_test.go`：`TestQuotaConcurrentAcrossDatabaseConnections`、`TestCreationIdempotencyAndCompletedDeletion`；`internal/session/service_test.go`：`TestCreateIdempotencyDoesNotDuplicateOrConsumeQuota`、`TestRecoveryIsBoundedAcrossServiceRestart`、`TestDeleteFailureKeepsQuotaAndDataUntilRetry`；部署脚本 5 个请求争 1 个名额 | 数据层两个连接、32 个请求仅一个成功；同幂等键仅一条记录；服务层仅建一个模拟容器。真实脚本验证幂等和并发配额。资源分配中途故障、容量不足及残留资源主要是模拟故障，尚无真实 daemon 中途故障后的完整目录 / 网络清单证据。 |
| A-08 下调配额 | `internal/store/store_test.go`：`TestQuotaCountsAllStatesAndLoweringRetainsInstances`；部署脚本降低 Alice 配额 | 自动化验证三个实例降额到一后记录保留、阻止新建，允许零配额并拒绝负值。真实脚本验证两个实例降额到一仍保留并阻止新增。后续 Alice 浏览器持久化实测使用降额后保留的同一实例；没有专门录制降额瞬间的浏览器内容前后对照，不据此否定已证明的配额行为。 |
| A-09 长期无连接 | `internal/session/service_test.go`：`TestNoIdleExpirationAndConnectionStateIndependent`；`internal/config/config_test.go`：`TestConfigIgnoresIdleTimeoutAndSupportsZeroQuota`；`scripts/e2e-soak.mjs` 及报告 | 时间字段回拨单元测试证明无回收调用，但不能替代真实等待。A `sess_6dbacc820c313290` 已在 `2026-09-17T05:53:07.907Z` 建立零连接基线，报告 pending；核对 API 连接数、真实 peer、TCP 连接、墙钟 / kernel uptime、容器身份及数据标记。**必须等到至少 2026-09-18T05:53:07.907Z 且连续性检查通过，再真实重连核对同 ID/profile，方可通过。** |
| A-10 登录与实例独立 | `internal/auth/auth_test.go`：`TestLogoutRevokesOneLoginAndTokensRejectTampering`、`TestNormalizedLoginAndFirstPasswordChange`；`internal/store/store_test.go`：`TestLoginRevocationCASAndExpiration`；HTTP 撤销测试；平台 / WS 撤销部署报告 | 覆盖独立登录 nonce、过期凭据、改密撤销；真实部署禁用 / 重置后容器仍运行。最新 WS 报告验证单登录退出只关闭其 PCM / input / noVNC，另一登录三条通道仍响应；同一 B 容器未停止 / 重启，旧 Cookie 401。登录到期按 PRD 允许的模拟过期测试覆盖，不要求再等待自然 7 天。 |
| A-11 持久浏览数据 | `internal/session/service_test.go`：`TestPersistentReplacementAndStoppedSurviveManagerRestart`、`TestMissingProfileNotReplacedWithEmptyData`；部署脚本；`BROWSER-TEST-REPORT.md` | 单元测试核对真实目录标记、固定身份及缺 profile 时拒绝空白替代。真实 Alice 在远程 Chrome 建立中文 localStorage、持久 Cookie、书签和下载；维护替换容器后及后台停止 / 启动后均在浏览器看到原中文值、Cookie 和收藏星标，浏览器下载内容一致。此项不再是仅 API / 模拟 Docker 证据。 |
| A-12 服务 / 主机重启 | `internal/session/service_test.go`：`TestPersistentReplacementAndStoppedSurviveManagerRestart`、`TestRecoveryIsBoundedAcrossServiceRestart`；`internal/docker/client_test.go`：`TestManagerReplacementReconnectsToExistingInstanceNetwork`；`internal/store/persistence_test.go`；`BROWSER-TEST-REPORT.md` | 单元 / 契约测试验证重开 SQLite、持久期望状态、有界恢复、manager 网络重连及 `unless-stopped` 配置。真实 manager 已带浏览器数据停止、重启 / 重建；A 手动停止后仍停止且占名额，B 继续运行，A 可再次启动。Docker daemon / 整台主机重启尚未实做，不能声称通过该基础设施组合；无需因此重复已完成的 manager 测试。 |
| A-13 停止 / 删除故障 | `internal/session/service_test.go`：`TestStopFailureRetainsDesiredAndRetryConfirms`、`TestDockerUnavailableNeverAssumedMissing`、`TestDeleteFailureKeepsQuotaAndDataUntilRetry`、`TestDirectoryCleanupCannotFollowSymlinkIntoOtherInstance`、`TestNetworkCleanupFailureRetainsInstanceUntilRetry`；HTTP cascade 组合用例 | 注入 Docker 停止、检查、删除和网络清理错误；真实临时目录的符号链接防护失败；核对错误状态、数据 / 配额保留、重试及其他实例安全。HTTP 组合另验证删除失败状态 / 剩余数及冻结操作。浏览器错误显示仍并入管理界面检查，但 PRD 的“注入故障”不要求实际破坏宿主 Docker 或穷举磁盘满 / 权限 / I/O 故障；不把符号链接用例扩称所有磁盘故障。 |
| A-14 旧数据迁移 | `internal/store/store_test.go`：`TestUpgradeLegacyDatabasePreservesIdentityAndOwnership`、`TestMigrationFailureRollsBackSchemaAndData`；`internal/session/service_test.go`：`TestLegacyMigrationReflectsActualContainer`、`TestLegacyExpiredOutagePreservesStoppedIntentAcrossRestart`、`TestStoppedMissingContainerRemainsVisibleAndCanBeRecreated`、`TestOrphansReportedNeverRemoved`；`internal/docker/client_test.go`：`TestNetworkMigrationDropsEverySharedBridge` | 真实临时旧 schema 保留 admin 哈希、ID、归属、目录字段；状态按模拟容器迁移；旧 expired 故障恢复不误启动；孤儿仅报告；迁移失败事务回滚。实际旧版本带 Chrome 数据升级未在本矩阵验证。本机已获授权清空测试部署不能替代迁移兼容测试。 |
| A-15 禁用与凭据撤销 | `internal/auth/auth_test.go`：`TestResetDisableDeleteRevokeOnlyTargetUser`；`internal/httpapi/access_test.go`；WebRTC 租约测试；`data-e2e/revocation-report.json`、`data-e2e/transport-revocation-report.json`、`BROWSER-TEST-REPORT.md` | 本机 TCP 101 在重置 / 禁用 / 退出 / 删除后 10 秒内关闭，下载 / 停滞上传取消，实际 Pion 8 秒租约到期关闭。真实 Bob 浏览器 WebRTC 禁用后 325ms peer 归零、页面回登录，A/B 同容器继续运行。真实部署单登录退出后 PCM / input / noVNC 分别 1455 / 1470 / 1479ms 关闭；另一独立登录及其三条连接不受影响、B 后台继续运行。已覆盖全部传输的真实撤销路径，无需为了重复计时再连接 A；未把每种凭据变更与每种传输的笛卡尔积都声明成部署实测。 |
| A-16 双用户实际操作 | 部署脚本；输入 / 文件自动化；`BROWSER-TEST-REPORT.md`；`data-e2e/audio-report-sess_8685794da70b396a.json` | Alice/Bob真实邮箱登录、独立工作区、WebRTC画面、中文文本、浏览器保存/下载及重连已通过。Bob另通过真实选择器上传/下载和非零PCM捕获（peak2622、RMS230.33）。**剩A的音频信号须等soak完成后补，不能用健康检查代替。** 此证据不冒充主观听感、WebRTC音频或所有输入法组合穷举；原Bob/B在后续确认测试中已实际删除，不影响此前测试证据但不能继续复用。 |
| A-17 用户列表计数 | `internal/store/store_test.go`：`TestQuotaCountsAllStatesAndLoweringRetainsInstances`；`internal/httpapi/coverage_test.go`：`TestUserListCountsAllStatesAndRefreshesReservations`；部署脚本管理员计数检查 | 精确构造 running / stopped / error 三条记录，HTTP 用户列表显示 3；完成记录删除后显示 2，创建占位后回到 3，配额一致。此 HTTP 用例中的删除由 store 表示清理完成，物理清理由生命周期测试负责。部署脚本验证真实 running/stopped=2，尚未故意制造真实异常容器再核对页面计数。 |
| A-18 管理员改密 | `internal/auth/auth_test.go`：`TestResetDisableDeleteRevokeOnlyTargetUser`、`TestMailFailureKeepsSingleUserAndResetDoesNotAffectOthers`；`internal/auth/mail_integration_test.go`：`TestEmailInvitationLoginRetryAndNoPlaintextStorage`；`internal/httpapi/accounts_test.go`：`TestAccountHTTPUserManagementAndTemplates`；部署脚本 Bob 重置 | 覆盖无原密码管理员重置、旧密码 / token 无效、新密码可用、其他用户不受影响；邮件重发通过同一重置服务产生新密码；显式手工回退。部署脚本验证重置不停止真实容器。浏览器中的提示、确认、收取 provider 邮件及已有媒体连接关闭仍需独立证据。 |
| A-19 删除空账号 | `internal/httpapi/accounts_test.go`：`TestAccountHTTPUserManagementAndTemplates`；`internal/store/store_test.go`：`TestUserDeletionFreezesBeforeResourcesAndPreservesAudit`、`TestUserUniquenessFilteringAndAdminProtection`；部署脚本复用邮箱后删除零配额账号 | 验证缺确认不能删除、确认后账号移除、旧权限撤销、邮箱复用新 ID、不继承资源、管理员保护。普通用户管理权限由 HTTP 管理中间件用例覆盖。浏览器点击取消二次确认本身未由 Go 测试执行。 |
| A-20 删除有资源账号 | `internal/httpapi/cascade_test.go`：`TestHTTPMixedStateUserDeletionRequiresConfirmationAndCleansAllResources`；生命周期 / store / audit 用例；`scripts/e2e-resource-cleanup.mjs` 及报告 | HTTP 组合覆盖 running / stopped / error 三状态、确认影响、权限、错误确认零变更及完整清理。真实专用用户的 3 个实例包含运行、手动停止、外部停止；确认删除后 3 容器、6 个真实挂载目录、父目录、3 专网及账号清除，旧凭据拒绝，A/B 的容器身份 / 网络 / profile inode / 下载哈希保持不变。外部停止不假称平台已处于 error；error 分支由 HTTP 组合测试覆盖。 |
| A-21 删除失败 / 并发 / 重启 | `internal/httpapi/cascade_test.go`：`TestHTTPMixedStateDeletionFailureFreezesAccessCountsAndRetries`；`internal/session/service_test.go`：`TestDeleteUserDrainsInflightCreation`、`TestDeleteUserFailureResumesAfterRestartAndPreservesOtherUser`、`TestDirectoryCleanupCannotFollowSymlinkIntoOtherInstance`；`internal/store/store_test.go`：`TestAccountDeletionRacingReservationsRetainsAllAcceptedOwnership`；`internal/httpapi/access_test.go`：`TestStalledRequestBodyIsInterruptedOnRevocation` | HTTP 组合分别注入容器移除失败、Docker 不可达、目录防护失败：账号 delete_failed、剩余数 1 / 3 / 1，旧 / 新登录及创建、启动、上传、改密均拒绝；重建 Service 后重试完成，其他用户及审计保留。生命周期恢复用例另真实关闭并重开 SQLite，清除进程内锁 / 连接状态，再从持久记录自动继续删除；不是仅沿用内存状态。还有冻结与 40 个预约请求竞争、创建在途和停滞请求释放测试。没有实际杀进程的部署报告，不能声称该实测；PRD 的模拟重启要求已有持久化恢复证据，无须将所有故障交叉组合升级为真实 daemon 破坏测试。 |

## 横向质量检查

- 前端提交确认：生产交互已改为共用HTML dialog（`persistent-v3-dialog`），`node --test scripts/tests/app-confirmation.test.cjs` 8项通过，覆盖取消/Escape、一次提交、失败关闭、来源移除、并发及HTMX。实际CUA在使用生产资产/模板的隔离预览验证：表单和HTMX取消/Escape零请求，明确确认各一次，默认取消焦点直接回车不提交。证据 `data-e2e/confirmation-preview-requests.json`；不是仅靠事件模拟，也不冒充最终部署。账号模板及级联确认HTTP定向测试1.818s、JS语法及diff-check通过；新manager已构建，为保护24h计时尚未部署。用户已说明Bob删除时点击了“确定”，属于正常确认删除。
- 凭据：v2 签名加随机 nonce，数据库登录记录和 `AuthVersion` 即时校验；改密使用版本比较更新，避免覆盖并发管理员重置。测试见 `TestLogoutRevokesOneLoginAndTokensRejectTampering`、`TestLoginRevocationCASAndExpiration`。
- CSRF：匿名登录和所有变更入口的签名 token；WebSocket 同源 Origin；模板含隐藏 token、JS 同源变更请求带头。测试见 `TestCSRFAcrossFormsJSONAndWebSockets`、`TestHTTPLoginAndCSRF`。模板渲染不是浏览器点击验证。
- 审计：`internal/httpapi/audit_test.go` 的两项测试证明实例 / 用户删除后仍保留 actor、target、结果；不记录名称、邮箱、密码和原始运行时错误。审计写入失败仅输出脱敏错误日志，当前不是事务型审计系统。
- 文件：`TestProxyDownloadUsesBrowserSafeContentDisposition`、`TestSanitizeFilenameRejectsPathTraversal`、`TestFileListingStopsWhenRequestIsCancelled`，以及 HTTP 的下载 / 停滞 body 撤销测试。
- SQLite：`TestCorruptDatabaseRejectedWithoutResetAndLockReleased` 验证损坏数据库拒绝启动而不清空；`TestRollbackJournalPreservesDataAcrossReopens` 验证重开持久性；这些不等于断电或 Docker Desktop 文件共享故障测试。
- 邮件秘密：假 SMTP 错误只留阶段 / 数字状态码；消息正文只在发送期间存在。`TestEmailInvitationLoginRetryAndNoPlaintextStorage` 不把完整邮件或密码写入持久交付历史。

## 历史收尾清单与验收边界（完成状态见顶部最新记录）

仍须完成，不能以模拟或健康状态替代：

1. A-09：保留正在运行的 A 基线，完成真实至少 24h 无连接及同 ID/profile 浏览器重连核验，记录开始 / 结束时间及数据标记。修改时间字段不算通过。
2. A-16：06:23～06:26 UTC的Bob中文、浏览器与平台文件传输、重连及实际PCM信号已通过（详见浏览器报告），剩A音频须在soak完成后测试。非零PCM捕获不自动等于主观听感或WebRTC音频验证。Bob随后在确认测试中被实际删除，保留已有历史证据，不再使用旧账号/实例补测。
3. 管理界面列表 / 数量 / 详情 / 搜索、配额与状态表单、重置密码说明、无邮件新增提示及资源概览已实际检查。用户已确认Bob删除时选了“确定”；新版页面内确认框取消已通过真实CUA隔离预览，仍须在soak后上线v3，用新建可丢弃账号补最终后台取消核验。受工具安全限制而未输入新凭据的入口如实标记，密码流程由已有实际部署API与集成测试覆盖，不绕过。
4. 收尾同步报告，核对新增代码是否已经由对应测试覆盖；此前全量 Go / 嵌套模块 / 语法 / 构建门禁已有通过记录，没有变化时不因文档更新重复昂贵整套流程。已通过的 WebRTC / PCM / input / noVNC 撤销、15 入口越权、跨容器网络、完整物理清理、manager 重启、A 持久化无需重复。

没有新增发现的实施缺陷，仅凭“尚未做某项更强实测”不能推断功能缺失。外部真实邮箱投递、所有输入法穷举、每类故障与真实 daemon 交叉组合，不是本期额外硬门禁。迁移已有真实旧 SQLite schema / 身份及归属测试、真实目录标记和状态核对测试；本机清空授权不取消这组兼容要求，也不额外要求为验收还原旧测试部署。Docker daemon / 宿主重启尚未实做，交付时应披露为覆盖边界；已有实际 manager 重启、容器替换、期望状态恢复及 restart-policy 契约证据不能写成“整机重启已实测”。

本矩阵只同步已记录证据，不自行连接实例或执行部署操作；最终完成状态仍以主任务补齐上述项目为准。
