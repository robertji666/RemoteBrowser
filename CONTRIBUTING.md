# Contributing / 贡献指南

感谢参与 RemoteBrowser。提交改动即表示你同意按本仓库的 MIT License 提供贡献。

## 开始之前

- 安全漏洞请按 [SECURITY.md](SECURITY.md) 私下报告，不要创建公开 issue。
- 功能或行为变化建议先用 issue 说明问题、使用场景和兼容性影响。
- 一个 Pull Request 只处理一个主题；不要混入无关格式化或生成文件。
- 不得提交真实密码、`.env`、数据库、profile、下载文件、备份或预编译本地二进制。

## 开发与验证

Go 代码使用 `gofmt`，包名和 `RB_*` 配置命名应延续现有风格。前端页面、组件、脚本和样式分别放在现有 `web/templates/pages`、`web/templates/partials`、`web/static/js` 和 `web/static/css` 目录。

```bash
go test ./...
go vet ./...
go build ./cmd/manager
(cd docker/session/input-service && go test ./... && go vet ./...)
(cd docker/session/webrtc-service && go test ./... && go vet ./...)
```

修改会话镜像后还应构建 `docker/session`。涉及画面、音频、输入、剪贴板、文件或权限时，请用真实浏览器走完整用户路径；静态检查和健康状态不能替代端到端验证。

Pull Request 请说明动机、关键变化、测试结果、配置或迁移影响。可见 UI 变化请附截图。

---

Thank you for contributing to RemoteBrowser. By submitting a contribution, you agree to license it under this repository's MIT License.

- Report vulnerabilities privately through [SECURITY.md](SECURITY.md).
- Keep each pull request focused and avoid unrelated formatting or generated artifacts.
- Never commit real secrets, `.env`, databases, profiles, downloads, backups, or local compiled binaries.
- Run the Go tests, vet checks, and build commands above. Build the Session image when it changes.
- Exercise real browser workflows for video, audio, input, clipboard, file handling, and authorization changes.
- Describe motivation, behavior, test results, configuration or migration impact, and include screenshots for visible UI changes.
