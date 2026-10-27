# QRT Pro

跨越二维码传输文件的端到端实现。本工程是在两个开源项目基础上改造而来，两个项目均来源于 **Ivan Daniluk (divan)**：

- **发送端** —— 基于 [divan/txqr](https://github.com/divan/txqr)（Go），源码仓库 <https://github.com/divan/txqr>（本目录下 `txqr/`）
- **接收端** —— 基于 [divan/txqr-reader](https://github.com/divan/txqr-reader)，源码仓库 <https://github.com/divan/txqr-reader>（本目录下 `txqr-reader/`）

采用 Luby 喷泉码纠错，通过动态二维码把任意二进制文件从一台屏幕发送到手机端接收 App。

## 目录结构

```
008-qrt-pro-opti/
├── txqr/                      # 桌面端（Go），fork 自 divan/txqr
│   ├── cmd/qrt-pro/           #   统一桌面应用：发送端 + 接收端 + 已收文件（唯一入口）
│   ├── cmd/txqr-web/          #   无界面命令行发送端（薄封装 internal/sender）
│   ├── cmd/txqr-ascii/        #   终端 ASCII 发送端
│   ├── cmd/txqr-gif/          #   GIF 发送端
│   ├── internal/w32/          #   Win32/GDI 底层：资源所有权、DPI、COLORREF
│   ├── internal/sender/       #   可嵌入的发送端 HTTP 服务（页面已 go:embed）
│   ├── internal/scanner/      #   接收端管线：区域管理、解码 worker 池、落盘
│   ├── internal/uikit/        #   立即模式绘制层：主题、按钮、卡片、进度条
│   ├── decimen/               #   decimen 协议（systematic-carousel + DCF2）
│   ├── qr/                    #   QR 编解码 + 彩色矩阵
│   └── third_party/go-qrcode/ #   go.mod replace 目标，本地副本
├── txqr-reader/
│   └── android/               # 接收端（Android, Kotlin/Compose）
├── docs/
│   ├── protocol-analysis.md   # decimen 协议逐字节规格（权威依据）
│   ├── cleanup-analysis.md    # 目录清理分析报告
│   ├── code-audit.md          # 代码审计报告（逻辑/并发/资源/线程安全）
│   └── mode-and-codec-analysis.md  # 编码方案与工作模式的原理、实测与结论
├── .github/workflows/         # 发布流水线（qrt-pro + sender + APK）与 CI 测试（含 -race）
├── dist/                      # 打包好的 portable 产物
└── README.md
```

## 统一桌面应用（cmd/qrt-pro）

以前需要三个 exe：`序列化工具.exe`（发送端）、`qrt-pro-receiver-desktop-*.exe`（接收端）、
`qrt-pro-launcher.exe`（管理前两者的外进程启动器）。三者只通过进程名互相识别，命名不一致的
bug 正源于此。现在它们合并为**一个进程、一个窗口**：

- 左侧导航栏切换「发送端 / 接收端 / 已收文件」，发送端与接收端可同时工作；
- 停止扫描从「遍历进程树」退化为一次方法调用；
- 接收端支持多区域（多屏各扫一路）、实时预览、逐区域进度与统计。

```sh
cd txqr
go build -ldflags "-s -w -H windowsgui" -o qrt-pro.exe ./cmd/qrt-pro
```

离屏出图（用于文档截图与布局回归，锁屏环境下同样可用）：

```sh
qrt-pro.exe -snapshot shot.png -tab 1 -demo -w 1080 -h 720   # -tab: 0 发送端 1 接收端 2 已收文件
```

启动后若窗口是一片空白，可以用绘制自检定位（GUI 程序没有控制台，这是唯一客观手段）：

```sh
set QRT_PAINT_CHECK=%CD%\paint.log
qrt-pro.exe          # 跑几秒后关掉，paint.log 记录 WM_PAINT 后在窗口 DC 上采样的像素
```

采样点应出现主题色 `rgb(15,17,23)`（背景）与 `rgb(28,33,45)`（面板）；若全是 `rgb(255,255,255)`，
说明双缓冲的内容根本没贴到窗口上（CI 里就靠这条断言防回归）。

> 旧的 `cmd/txqr-desktop` 与 `cmd/txqr-launcher` 已随本次合并删除（仍在 Git 历史中，
> 需要时 `git checkout <旧提交> -- txqr/cmd/txqr-desktop` 即可取回）。
> `dist/` 下 `qrt-pro-receiver-desktop-*.exe`、`qrt-pro-launcher.exe`、`序列化工具.exe`
> 是这三个旧入口的构建产物，功能全部在 `qrt-pro.exe` 里，已移入 `dist/_legacy/` 待清理。

## 协议要点

- 帧格式：`blockCode/chunkLen/total|payload`
- 每帧携带喷泉码编码后的原始二进制（**已去掉 base64**，payload 即文件字节）
- 喷泉码参数：Mersenne–Twister 随机种子固定为 `200`，solitonDistribution，按 RFC5053 划分 chunk
- 发送端与接收端必须配套使用（旧 base64 版与新 raw 版互不兼容）

## 发送端（txqr）

基于 Go 的 `txqr`，新增/改造：

- **网页发送端 `txqr-web`**：全屏循环播放二维码，支持 N×N 网格（1–4）、暂停/继续/停止/取消、重选文件自动取消旧会话，浏览器直接操作。
- 默认参数：每帧字节数 `1024`、帧率 `15`、网格 `1`（可在页面调整）。
- **去掉 base64**：payload 直接为文件原始字节，消除其.de编码开销。
- **Portable 打包**：静态单文件 exe（`index.html` 已内嵌，无运行时依赖），可在隔离（未联网）Win10 上双击运行，用浏览器打开 `localhost:9000` 即可发送。
- 日志写入 exe 同目录 `txqr-web.log`，避免无控制台启动时因写 stderr 阻塞导致服务起不来。

### 构建与运行发送端

```sh
cd txqr/cmd/txqr-web
go build -ldflags "-s -w" -o txqr-web.exe .     # 或使用 bin/ / dist/ 内已编译产物
./txqr-web.exe                                   # 默认监听 :9000
# 打开 http://localhost:9000，选文件，点击发送，手机端对屏扫描
```

默认端口 `:9000`，可用 `-addr :18888` 指定其他端口。

## 接收端（`txqr-reader`）

基于原 iOS 参考代码，改造为 **Android** 应用（Kotlin + Jetpack Compose）：

- CameraX 摄像头预览 + ML Kit 二维码识别（默认不限制帧率，摄像头最高可用帧率；只保留最新帧）。
- **完整 Kotlin 解码器**（与 Go 端逐字节一致）：
  - MersenneTwister（Int63/Float64/Perm）、RFC5053 分区、soliton CDF、PickIndices。
  - 已去除 base64 还原逻辑，`dataBytes` 直接返回原始字节。
- **原始二进制保存**：通过 MediaStore 写入 Downloads，按内容自动推断后缀。
- 实时速度：2 秒滑动窗口瞬时速率 + 全程平均速率，每 500ms 刷新。
- 支持扫码完成后重扫（「Scan Another」）。

### 构建接收端 APK

```bash
cd txqr-reader/android
# 设置本机 Gradle 用户目录（含完整发行版，避免 wrapper 重新下载/锁冲突）
set GRADLE_USER_HOME=C:\gradle-home
gradlew.bat :app:assembleDebug --no-daemon
# 产物：app\build\outputs\apk\debug\app-debug.apk
```

> ⚠️ **工程路径必须为纯 ASCII**。AGP 会硬性检查并直接失败：
> `Your project path contains non-ASCII characters`。
> `local.properties` 记录本机 SDK 路径（`sdk.dir`），已被 `.gitignore` 忽略，换机器需自行生成。

## 使用流程

1. 接收端手机安装 APK。
2. 隔离机器上双击运行 portable 发送端 → 浏览器打开 `localhost:9000` → 选择文件 → 开始发送。
3. 手机端 App 对准屏幕持续扫描，完成进度后文件自动保存到 Downloads。

## 版本

当前版本：`v1.4.2`（见 Git tag）。Android 端 `versionCode=8` / `versionName=1.4.2`。

v1.4.0 的主要变化：桌面端三个 exe 合并为统一的 `qrt-pro.exe`，接收端界面重做，并修复
了代码审计发现的 30 项缺陷（详见 `docs/code-audit.md`）。

v1.4.1 是缺陷修复版本：

- **「停止服务」第二次以后失效**：`Close()` 被 `sync.Once` 包着，只能生效一次，
  端口继续服务而界面显示已停止；同时把发送服务启停移出 UI 线程（本机绑定通配地址
  实测耗时 3–30 秒，会把窗口冻住）。见 `docs/code-audit.md` A8。
- **Binary 编码跨端不一致**：Go 端是「随机半数源块异或」，Android 端写成「按序重复源块」，
  导致解码报成功但数据错误。已重写 Kotlin 侧。
- **Online 编码参数不在线上格式里**：发送端改 ε/quality 会让接收端解出错误数据，
  已将参数固化为两端共用的常量。
- **Color Matrix 模式默认发不出去**：默认 `split=2048` 超过矩阵容量 1587 B，
  每帧渲染失败；已自动钳制到 1539 并回传有效值。
- 新增 `docs/mode-and-codec-analysis.md`：五种编码方案与两种工作模式的原理、实测与结论。

v1.4.2 是缺陷修复版本：

- **接收端列表不能滚动**：「扫描区域」超过 3 个、或「已收文件」超过一屏时，多出来的行
  被直接丢弃且没有任何滚动手段。现在支持滚轮、拖动和右侧滚动条，新增区域会自动滚到可见处。
  见 `docs/code-audit.md` B20。
- 顺带修掉两个被它暴露的底层缺陷：`RoundRectGradient` 用 `SelectClipRgn`（替换语义）
  会顶掉外层裁剪，改为 `ExtSelectClipRgn(RGN_AND)` 求交；裁剪区外的控件不再注册，
  滚出半个身位的按钮点不到。

## License

[MIT](./LICENSE)（与上游 divan/txqr、divan/txqr-reader 一致）。