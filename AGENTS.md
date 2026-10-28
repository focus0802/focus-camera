# 自动经验沉淀与规范维护协议 (Auto-Learn Protocol)

## 1. 执行权限与并发锁（防子代理踩踏）
- **仅限主代理执行**：只有主会话（Root Agent）有权创建、修改或删除规范与技能文件（`AGENTS.md`、`.agents/rules/*`、`.agents/skills/*`）。
- **子代理禁令**：如果你当前是以子代理身份运行（通过 `invoke_subagent`、`research`、`/boost` 或 `/teamwork` 派生），**严禁**直接修改任何规范文件。请将发现的经验写在返回给主代理的汇报总结中，由主代理统一合并落盘。

## 2. 收尾复盘与三重噪音过滤（防垃圾规则）
在完成任何编码、重构或排错任务并输出最终回复前，静默复盘本次会话。**唯有同时通过以下三重门禁**的经验，才允许写入规范：
1. **高价值触发信号**：
   - 用户在对话中明确纠正了你的写法、技术选型、目录结构或命令；或
   - 遇到了隐蔽的项目级陷阱（类型体操、构建配置怪癖、环境限制），历经 ≥ 2 次失败尝试才排错成功；或
   - 本次任务确立了新的公共模块约定或架构不变量。
2. **泛化性测试（严禁记录一次性 Bug）**：
   - 该经验必须对未来的同类开发具有普遍指导意义。
   - **严禁**将一次性的拼写错误、偶然的网络/缓存抽风、临时 Hack 补丁或仅针对单一函数的局部逻辑写入规范。
3. **零冗余检查**：
   - 该约束尚未被现有的 Linter/TypeScript 配置或已有规范覆盖。

*(若本次任务无经验通过上述三重门禁，请直接正常结束回复，不要触碰任何规范文件，也无需提及本协议。)*

## 3. 路由分级与语法防呆门禁（防膨胀与静默失效）
通过门禁的经验，必须在结束当前回合前按以下路由落盘：

- **路由 A —— 全局高频铁律 ➔ `AGENTS.md`**：
  - 仅存放对全仓库所有任务都生效的核心铁律（确保单文件体积严格控制在 15KB 以内）。
  - **自我保护锁**：严禁修改或删除本《自动经验沉淀与规范维护协议》章节。所有项目规范必须严格维护在文件底部的 `<!-- LEARNED_RULES_ANCHOR -->` 锚点下方。
  - **严禁 Frontmatter**：严禁在 `AGENTS.md` 顶部添加任何 YAML 头。

- **路由 B —— 模块/后缀专属规范 ➔ `.agents/rules/<topic>.md`**：
  - 仅针对特定模块或文件类型的规范（如 `ui-components.md`、`api-routes.md`、`database.md`），必须分流至 `.agents/rules/` 下，防止 `AGENTS.md` 膨胀占用常驻 Token。
  - **扁平目录限制**：文件必须直接放在 `.agents/rules/` 根目录下（严禁嵌套子文件夹，否则系统会忽略）。
  - **强制 YAML 门禁**：`.agents/rules/*.md` 顶部**必须**包含合法的 YAML 头，且注意以下防坑细节：
    ```yaml
    ---
    trigger: glob # 严格使用下划线命名：always_on | model_decision | glob | manual（严禁写驼峰如 alwaysOn）
    description: "一句话简述该规则何时生效"
    globs: "*.tsx, apps/web/**/*.ts" # 当 trigger 为 glob 时必填。凡包含 * 的通配符必须用双引号包裹！
    ---
    ```

- **路由 C —— 多步固定工作流 ➔ `.agents/skills/<skill-name>/SKILL.md`**：
  - 涉及固定步骤的脚本执行、发布检查或代码生成流程，写入 `.agents/skills/<skill-name>/SKILL.md`（顶部包含 `name` 和 `description` 的 YAML 头）。

## 4. 规范新陈代谢：读取、淘汰与极简格式
1. **先读后写**：修改任何规范文件前，必须先调用 `view_file` 查看完整上下文。
2. **淘汰过时规则（极重要）**：如果本次重构废弃了旧接口或旧写法，必须同步**删除或更新** `AGENTS.md` 及 `.agents/rules/` 中与之冲突的旧规则。腐化的旧规则比没有规则更危险。
3. **祈使句极简格式**：每条规则必须采用“直接给结论 + 简短正反例”的祈使句（如：`使用 X 替代 Y（例：...）`），单条规则不超过 2 行，严禁记录排错心路历程或对话流水账。

## 5. 结尾单行汇报
每当新增、更新或清理了规范文件，在最终回复的最末尾附上一行简短通知：
> 📝 **已自动维护规范**：`[新增 | 更新 | 清理]` `<文件路径>` —— *[一句话简述变更内容]*。

---
<!-- LEARNED_RULES_ANCHOR -->
# 项目沉淀规范与约定 (Project Learned Rules)

<!-- AI Agent 请在下方按模块分类维护项目规则，保持精炼与去重 -->

## 1. UI Component & Feedback Guardrails (`@uiarc` First)
- **Never Use Native Browser Dialogs**: Never call `window.alert()`, `window.confirm()`, or `window.prompt()`. Inside macOS WKWebView and Windows WebView2 desktop containers, native alerts block the UI thread or fail to render. Always use `@uiarc/dialog` (`Dialog`, `DialogContent`) for confirmations and `@uiarc/alert` (`Alert`) for inline status/error banners.
- **Prefer `@uiarc` Over Hand-Rolled Controls**: Always use existing `@uiarc` components in `web-ui/src/components/arc/` (`Tabs`, `MorphSelect`, `SearchField`, `NumberField`, `TimePicker`, `PasswordField`, `Progress`, `Pagination`, `SortableDataTable`, `ReorderableList`, `AnimatedCounter`, `ScrollArea`, `Switch`, `Checkbox`, `Badge`, `Button`, `Input`) instead of raw `<select>`, `<input type="number">`, `<input type="time">`, or custom CSS widgets.

## 2. Public Repository Security & Credential Hygiene
- **Zero Hardcoded Credentials**: This project is hosted in a public GitHub repository (`focus0802/focus-camera`). Never hardcode real camera passwords, studio IPs, or personal tokens in `web-ui/src/`, `internal/`, or `*_test.go`.
- **Runtime & Test Configuration**: Default camera credentials must be loaded dynamically from the local git-ignored `cameras.json` (`/api/cameras` -> `config.default_password`). Live hardware tests in `engine_test.go` must read `os.Getenv("FOCUS_CAMERA_PASSWORD")` and call `t.Skip(...)` when unset.

## 3. Sandbox & Toolchain Execution on macOS
- **Avoid `/usr/bin/env` Node Shims**: Do not invoke `./web-ui/node_modules/.bin/tsc` or `./web-ui/node_modules/.bin/vite` directly, as `#!/usr/bin/env node` is blocked in standard sandbox mode. Instead, invoke `/opt/homebrew/bin/node` directly:
  - Typecheck: `/opt/homebrew/bin/node ./web-ui/node_modules/typescript/bin/tsc -p web-ui/tsconfig.json --noEmit`
  - Build UI: `/opt/homebrew/bin/node ./web-ui/node_modules/vite/bin/vite.js build web-ui`
- **Go Vendor Mode**: Always pass `-mod=vendor` when running `/opt/homebrew/bin/go test` or `/opt/homebrew/bin/go build`.
- **Git Proxy for GitHub**: When pushing to `github.com` on this machine, ensure local git proxy `http.https://github.com.proxy=http://127.0.0.1:7890` is configured.

## 4. Native Desktop & Self-Updater Invariants
- **Loopback Binding on Windows**: Keep the embedded HTTP server bound to `127.0.0.1` (starting at `:18080` with automatic port fallback) so Windows Defender Firewall does not block local WebView2 connections.
- **Graceful Recording Flush Before Exit/Upgrade**: Any shutdown (`/api/shutdown`) or self-upgrade (`updMgr.StartUpgrade`) must invoke `recMgr.StopAll()` before process termination or binary replacement so active 1080P fMP4 segments are finalized cleanly on disk.
