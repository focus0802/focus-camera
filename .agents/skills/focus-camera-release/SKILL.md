---
name: focus-camera-release
description: >-
  Build, verify, package, and publish a new FocusCamera desktop release (macOS .app/binary + Windows x64 .exe + SHA-256 version.json) to local dist/ and GitHub Releases (focus0802/focus-camera). Use when the user asks to package, build executables, bump the version, or publish a new release.
---

# FocusCamera Build & Release Workflow

Follow these steps to build, verify, and publish a new version of FocusCamera (e.g., `v2.0.1`).

## 1. Build & Embed Frontend Assets
Run TypeScript verification and Vite production build (which outputs directly into `web/` for Go `//go:embed`):
```bash
/opt/homebrew/bin/node ./web-ui/node_modules/typescript/bin/tsc -p web-ui/tsconfig.json --noEmit
/opt/homebrew/bin/node ./web-ui/node_modules/vite/bin/vite.js build web-ui
```

## 2. Run Go Unit Tests
Verify the self-updater, autostart, and embedded asset tests pass before packaging (use `-ldflags="-linkmode=external"` on macOS so `LC_UUID` is always emitted on macOS 15.4+/26+):
```bash
/opt/homebrew/bin/go test -mod=vendor -ldflags="-linkmode=external" -v ./internal/updater ./internal/autostart .
```

## 3. Cross-Compile Desktop Executables (`dist/`)
Replace `v2.0.1` with the target semantic version tag:
```bash
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 /opt/homebrew/bin/go build -mod=vendor -ldflags="-s -w -X focus-camera/internal/updater.CurrentVersion=v2.0.1" -o dist/FocusCamera-Mac .
/usr/bin/codesign --force --sign - dist/FocusCamera-Mac
cp dist/FocusCamera-Mac dist/FocusCamera.app/Contents/MacOS/FocusCamera
/usr/bin/codesign --force --deep --sign - dist/FocusCamera.app

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 /opt/homebrew/bin/go build -mod=vendor -ldflags="-H=windowsgui -X focus-camera/internal/updater.CurrentVersion=v2.0.1" -o dist/FocusCamera-Windows-x64.exe .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 /opt/homebrew/bin/go build -mod=vendor -ldflags="-X focus-camera/internal/updater.CurrentVersion=v2.0.1" -o dist/FocusCamera-Windows-Console.exe .
```

## 4. Generate `dist/version.json` with SHA-256 Checksums
Compute SHA-256 hashes and exact byte sizes for `dist/FocusCamera-Mac` and `dist/FocusCamera-Windows-x64.exe`:
```bash
/usr/bin/shasum -a 256 dist/FocusCamera-Mac dist/FocusCamera-Windows-x64.exe
/usr/bin/stat -f "%N: %z bytes" dist/FocusCamera-Mac dist/FocusCamera-Windows-x64.exe
```
Update `dist/version.json` with the new `version`, `published_at`, `notes`, and `assets` (`darwin_arm64` and `windows_amd64`).

## 5. Publish to GitHub Releases & Watch CI Pipeline
1. Commit source changes (ensuring `dist/`, `cameras.json`, and `recordings/` remain ignored) and push `main` first.
2. Create and push the version tag (`git tag -f v2.3.0 && git push origin main --force-with-lease && git push origin -f v2.3.0`) so `.github/workflows/release.yml` triggers.
3. **Mandatory CI Watch & Verification**: Never end the release task immediately after `git push`. You MUST watch the `.github/workflows/release.yml` run until it finishes (`status == "completed"` and `conclusion == "success"`), and verify the published release assets (`version.json`, `FocusCamera-Windows-x64.exe`, `FocusCamera-Windows-Console.exe`, `FocusCamera-Mac`) using the `github` MCP tool (`get_release_by_tag` / `get_latest_release`). If the workflow fails, inspect the step logs, fix the root cause, re-tag, and watch again until green.
