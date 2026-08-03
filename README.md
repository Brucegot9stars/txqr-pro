# QRT Pro

跨越二维码传输文件的端到端实现。本工程是在两个开源项目基础上改造而来，两个项目均来源于 **Ivan Daniluk (divan)**：

- **发送端** —— 基于 [divan/txqr](https://github.com/divan/txqr)（Go），源码仓库 <https://github.com/divan/txqr>（本目录下 `txqr/`）
- **接收端** —— 基于 [divan/txqr-reader](https://github.com/divan/txqr-reader)，源码仓库 <https://github.com/divan/txqr-reader>（本目录下 `txqr-reader/`）

采用 Luby 喷泉码纠错，通过动态二维码把任意二进制文件从一台屏幕发送到手机端接收 App。

## 目录结构

```
008-qrt-pro/
├── txqr/                 # 发送端（Go），fork 自 divan/txqr
│   ├── cmd/txqr-web/     #   网页发送端（主要使用，"序列化工具"）
│   ├── cmd/txqr-ascii/   #   终端 ASCII 发送端
│   ├── cmd/txqr-gif/     #   GIF 发送端
│   └── bin/              #   编译产物
├── txqr-reader/          # 接收端（Android, Kotlin/Compose）
│   └── android/          #   Google Android 工程
├── dist/                 # 打包好的 portable 发送端 exe
└── README.md
```

## 协议要点

- 帧格式：`blockCode/chunkLen/total|payload`
- 每帧携带喷泉码编码后的原始二进制（**已去掉 base64**，payload 即文件字节）
- 喷泉码参数：Mersenne–Twister 随机种子固定为 `200`，solitonDistribution，按 RFC5053 划分 chunk
- 发送端与接收端必须配套使用（旧 base64 版与新 raw 版互不兼容）

## 发送端（txqr）

基于 Go 的 `txqr`，新增/改造：

- **网页发送端 `txqr-web`**：全屏循环播放二维码，支持 N×N 网格（1–4）、暂停/继续/停止/取消、重选文件自动取消旧会话。
- **界面伪装**：页面标题为「序列化工具」，/发送按钮为「序列化」，不暴露 TXQR 字样。
- 默认参数：每帧字节数 `1024`、帧率 `15`、网格 `1`（可在页面调整）。
- **去掉 base64**：payload 直接为文件原始字节，消除其.de编码开销。
- **Portable 打包**：静态单文件 exe（`index.html` 已内嵌，无运行时依赖），可在隔离（未联网）Win10 上双击运行，用浏览器打开 `localhost:9000` 即可发送。
- 日志写入 exe 同目录 `txqr-web.log`，避免无控制台启动时因写 stderr 阻塞导致服务起不来。

### 构建与运行发送端

```sh
cd txqr/cmd/txqr-web
go build -ldflags "-s -w" -o txqr-web.exe .     # 或使用 bin/ / dist/ 内已编译产物
./txqr-web.exe                                   # 默认监听 :9000
# 打开 http://localhost:9000，选文件，点「序列化」，手机端对屏扫描
```

默认端口 `:9000`，可用 `-addr :18888` 指定其他端口。

## 接收端（`txqr-reader`）

基于原 iOS 参考代码，改造为 **Android** 应用（Kotlin + Jetpack Compose）：

- CameraX 摄像头预览 + ML Kit 二维码识别（默认不限制帧率，摄像头最高可用帧率；只保留最新帧）。
- **完整 Kotlin 解码器**（与 Go 端逐字节一致）：
  - MersenneTwister（Int63/Float64/Perm）、RFC5053 分区、soliton CDF、PickIndices。
  - 已去除 base64 还原逻辑，`dataBytes` 直接返回原始字节。
- **原始二进制保存**：通过 MediaStore 写入 Downloads，按内容自动推断后缀。
- 实时速度：2 秒滑动窗口瞬时速率，每 500ms 刷新。
- 支持扫码完成后重扫（「Scan Another」）。

### 构建接收端 APK

```bash
cd txqr-reader/android
# 设置本机 Gradle 用户目录（含完整发行版，避免 wrapper 重新下载/锁冲突）
set GRADLE_USER_HOME=C:\gradle-home
gradlew.bat :app:assembleDebug --no-daemon
# 产物：app\build\outputs\apk\debug\app-debug.apk
```

## 使用流程

1. 接收端手机安装 APK。
2. 隔离机器上双击运行 portable 发送端 → 浏览器打开 `localhost:9000` → 选择文件 → 点「序列化」。
3. 手机端 App 对准屏幕持续扫描，完成进度后文件自动保存到 Downloads。