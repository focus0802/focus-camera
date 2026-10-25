# FocusCamera v2.0

**FocusCamera** 是一款专为琴房、录音室及多媒体教室打造的轻量级跨平台桌面监控、双向语音对讲与 1080P 音视频同步录制客户端（支持 Windows 10/11 与 macOS）。

## 核心特性

- **多宫格低延迟实时监控墙**：基于原生 RTSP/TCP 连接复用（`StreamHub`）与 WebCodecs 硬解，支持全部视图、4/9/16 宫格、按分组筛选、拖拽排序及单击 1080P 高清放大。
- **双向语音实时对讲（`:8800`）**：支持一键持续对讲与长按空格（PTT）对讲，内置 G.711 PCMA 8kHz 硬件编解码与麦克风实时音量包络反馈。
- **1080P + 音频 MP4 录制与回放**：内置纯 Go fMP4 实时封装引擎（H.264 + AAC），支持定时计划录制、分片落盘、磁盘配额自动循环覆盖与历史录像检索下载。
- **局域网一键扫描与 IP 漂移自动对齐**：并发扫描局域网段内的 TP-LINK IPC 摄像头，自动读取 OSD 房号名称，并在路由器重启导致 DHCP IP 漂移时按房号自动对齐更新。
- **受控一键安全热升级**：默认绑定 GitHub Releases（`focus0802/focus-camera`），内置国内加速镜像自动回退、SHA-256 哈希与 PE/Mach-O 文件头双重校验，升级前自动安全封存所有正在录制的 MP4 分片。

## 快速构建

```bash
# 1. 构建前端资源
cd web-ui && npm install && npm run build && cd ..

# 2. 编译 macOS 原生客户端
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -mod=vendor -o dist/FocusCamera-Mac .

# 3. 编译 Windows 64位桌面客户端
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod=vendor -ldflags="-s -w -H=windowsgui" -o dist/FocusCamera-Windows-x64.exe .
```

## 发布新版本（GitHub Releases 自动更新）

只需打上语义化版本标签并推送到 GitHub，GitHub Actions 将自动编译 Windows 与 macOS 安装包、计算 SHA-256 签名并发布到 GitHub Releases：

```bash
git tag v2.0.1
git push origin v2.0.1
```

客户端启动时或点击顶部状态栏的 **版本胶囊按钮** 即可检测到新版本并一键安全升级。
