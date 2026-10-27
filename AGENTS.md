# FocusCamera Workspace Rules

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
