# Repository Guidelines

## 项目结构与模块组织

RemoteBrowser 是一个用 Go 编写的多用户远程浏览器管理服务，用于创建和管理长期保留的浏览器实例。主服务入口位于 `cmd/manager/main.go`。内部包统一放在 `internal/` 下，并按职责划分：`auth`、`config`、`docker`、`files`、`httpapi`、`proxy`、`session`、`store` 和 `view`。HTML 模板在 `web/templates/`，静态资源在 `web/static/`，第三方前端库 vendored 到 `web/static/vendor/`。数据库迁移文件在 `migrations/`。Session 容器相关文件在 `docker/session/`，部署示例在 `deploy/`。

## 构建、测试与开发命令

- `./scripts/dev.sh`：启动开发 manager；首次初始化必须显式设置至少 12 个字符的 `RB_ADMIN_PASSWORD`，已有管理员密码不会被环境变量覆盖。
- `go run ./cmd/manager`：直接运行 manager；初始化配置见 `.env.example`。
- `./scripts/migrate.sh`：执行数据库迁移后退出。
- `go test ./...`：运行所有 Go 包测试；鉴权、SMTP、配额、迁移、资源生命周期和 HTTP 有定向测试。
- `go build ./cmd/manager`：构建 manager 二进制文件。
- `cd docker/session && docker build -t remotebrowser/session:latest .`：构建 session 浏览器镜像。

应用运行时需要访问 Docker，用于管理浏览器会话生命周期。本地运行数据默认写入 `./data`。

## 编码风格与命名约定

Go 代码使用标准格式化，提交前对改动过的 `.go` 文件运行 `gofmt`。包名保持简短、小写，并与目录职责一致。涉及请求范围或生命周期控制的逻辑应传递 `context.Context`。配置优先使用 `RB_*` 环境变量，命名应延续现有风格，例如 `RB_HTTP_ADDR`、`RB_MAX_SESSIONS`。

前端变更应遵循现有目录分工：页面模板放在 `web/templates/pages`，可复用片段放在 `web/templates/partials`，页面交互脚本放在 `web/static/js`，样式放在 `web/static/css/app.css`。

## 测试指南

Go 测试文件应与被测代码放在同一包内，并使用 `*_test.go` 命名。服务层和存储层逻辑优先使用表驱动测试。涉及数据库行为时，使用临时数据目录或隔离的 SQLite 文件。提交 PR 前运行 `go test ./...`；修改 session 管理、鉴权、文件处理、迁移或 HTTP handler 时，应补充有针对性的测试。

## 提交与 Pull Request 规范

当前仓库尚无可参考的提交历史，因此提交信息使用简短的祈使句，例如 `add session file listing` 或 `fix idle reaper cleanup`。无关改动应拆分到不同提交。

PR 应包含简要说明、测试结果、关联 issue 或任务背景。涉及可见 UI 变化时附截图。若改动了环境变量、数据库迁移、Docker 镜像要求或部署步骤，需要在 PR 中明确说明。

## 安全与配置提示

不要提交真实管理员密码、本地数据、浏览器 profile 或生成的 session 文件。首次初始化（包括本地开发）应显式设置 `RB_ADMIN_PASSWORD`。凡涉及挂载 `/var/run/docker.sock`、代理 session 流量或调整上传限制的改动，都需要重点审查。
