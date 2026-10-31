# QRT Pro

通过屏幕上的动态二维码，在网络隔离的设备之间传输文件。提供 Windows 统一收发客户端、独立网页发送服务和 Android 接收端，运行时无需互联网或剪贴板。

发送端基于 [divan/txqr](https://github.com/divan/txqr)，接收端参考 [divan/txqr-reader](https://github.com/divan/txqr-reader)，上游作者均为 Ivan Daniluk（divan）。当前实现使用 Go、Kotlin / Jetpack Compose 和内嵌 HTML / JavaScript。

## 下载与版本

当前发布版本为 **[v2.0.6](https://github.com/Brucegot9stars/txqr-pro/releases/tag/v2.0.6)**。Windows 主窗口、发送网页和 Android 接收界面均显示版本；Android 为 `versionCode=17` / `versionName=2.0.6`。

| 下载文件 | 用途 |
| --- | --- |
| [qrt-pro-windows-amd64-v2.0.6.exe](https://github.com/Brucegot9stars/txqr-pro/releases/download/v2.0.6/qrt-pro-windows-amd64-v2.0.6.exe) | Windows 统一客户端：发送服务、屏幕区域接收、已收文件、诊断日志 |
| [qrt-pro-sender-windows-amd64-v2.0.6.exe](https://github.com/Brucegot9stars/txqr-pro/releases/download/v2.0.6/qrt-pro-sender-windows-amd64-v2.0.6.exe) | Windows 独立网页发送服务，无原生收发管理窗口 |
| [qrt-pro-reader-v2.0.6.apk](https://github.com/Brucegot9stars/txqr-pro/releases/download/v2.0.6/qrt-pro-reader-v2.0.6.apk) | Android 8.0 / API 26 及以上接收端 |

升级发送程序后刷新浏览器页面，确认顶部版本。建议收发两端都升级到 v2.0.6，以同时获得文件名重复发送和完成后补收的修复。

普通 QR / Color 使用 **QRT2** 协议，v2.0.x 之间保持互通；v2.0.0 之前的经典帧不兼容，需要同时更新两端。Decimen 使用独立的二进制帧和 DCF2 容器，详见 [协议与运行上限](docs/protocol-v2.md)。

## 快速开始

### Windows 发送

1. 运行统一客户端，在「发送端」页确认服务已启动，点击打开网页；或者直接运行独立发送端。
2. 浏览器打开服务地址，默认是 `http://localhost:9000`。统一客户端遇到端口占用时会改用空闲端口，以窗口显示的地址为准。
3. 选择文件，检查分片、帧率、编码方案和容量提示，选择是否预生成全部二维码。
4. 点击「序列化」。上传、编码和准备期间会显示二维码播放区域的虚框，预生成开启或关闭时均可见。准备完成后在同一区域自动播放。
5. 播放时可暂停、返回设置或取消任务；等待期间也可以返回或取消。快捷键：空格暂停 / 继续，`S` 或 `Esc` 返回，`C` 取消。

发送页采用宽窗口双栏、窄窗口单栏布局，底部保留操作栏，右侧提供限制说明和发送日志。文件参数不合法时会在上传前提示，服务端在编码前再次校验。

### Windows 接收

1. 在接收机器上运行统一客户端，进入「接收端」，框选本机屏幕上可见的二维码区域。
2. 确认保存目录并启动扫描；支持多个独立区域、实时预览和逐区域统计。
3. 内容校验及必要的文件名补收完成后自动保存，可在「已收文件」查看，也可对完成区域执行「另存为」。默认目录为用户 Downloads，实际目录以界面显示为准。

当前 Windows 屏幕接收管线支持普通 QR 和 Decimen；**Color Matrix 的图像接收需使用 Android**。

### Android 接收

1. 安装 APK，授予相机权限，对准发送屏幕持续扫描。
2. 普通 QR 直接扫描；接收 Color Matrix 时打开 `Color` 开关，Decimen 从帧中自动识别。
3. 内容恢复完成后检查或修改建议文件名，再点击保存，写入 Downloads。接收完成不等于已经保存；保存失败会留在完成界面并显示错误，可重试。
4. 需要接收下一个文件时使用重新扫描功能。

## 发送参数与预生成

网页普通 QR 的初始默认值如下；缩放方式和手动像素值会记在浏览器本地存储中。

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| 编码方案 | RaptorQ（RFC 6330） | 也提供 LT、Binary、Raptor、Online |
| 分片大小 | 2048 B | 不包含 QRT2 头；还须满足源块数和整帧容量限制 |
| 目标帧率 | 30 FPS | 目标值，不保证浏览器、远程桌面或接收端达到相同速率 |
| 网格 | 1 × 1 | 普通模式可设 N × N，N 为 1–4 |
| 缩放方式 | 固定不缩放 | 按整数个设备像素 / 模块显示 |
| 预生成全部二维码 | 关闭 | 先准备首批，后续边生成边播放 |

预生成开关影响下次序列化：

- **关闭**：编码完成后先准备最多 64 张二维码，再提前生成后续帧，缩短启动等待。生成吞吐跟不上播放时，缓冲耗尽后实际帧率会下降。
- **开启**：整轮二维码准备完成后播放，启动等待较长；图像缓存充足时，播放阶段的生成开销更低。整轮图像超过缓存预算时会明确报错。

两种方式使用相同的编码数据，均受资源边界约束。编码和初始图像准备在可取消的受限子进程中执行；后续按需图像由主进程的有界渲染池生成和缓存。详见 [发送端准备策略及性能证据](docs/sender-v2.0.3.md)。

Decimen 使用独立固定参数：V40-L、mask 4、ECC L、2933 B 编码负载、双码和目标 60 FPS，部分普通模式控件会禁用。它不会绕过远程桌面的实际刷新上限；在最高 30 FPS 的环境中，应结合实际显示帧率和接收完成时间比较效果。

### 尺寸与识别

| 缩放方式 | 行为 |
| --- | --- |
| 固定不缩放 | 服务端根据可用空间选择整数像素 / 模块，网页按设备像素显示；窗口小于已渲染图像时会退回缩放并提示 |
| 允许缩放（铺满窗口） | 浏览器将图像缩放到格位，模块可能落在非整数像素上，识别效果受缩放比例影响 |
| 手动指定 | 指定 2–8 px / 模块，改值后需重新序列化 |

框选应包含二维码静区及周围白底，不要切到定位图案。窗口变小、网格增多或远程桌面缩放都会减少单码可用像素；可放大发送窗口、减少网格或减小分片，重新序列化后观察识别结果。接收端的 `region.too-small` / `region.dense` 日志可辅助判断。

旧版本测得的尺寸下限和成功率保留在 [历史代码审计](docs/code-audit.md)，不能直接当作当前 QRT2 头、任意文件和 Horizon 环境的保证。

## 发送速率与接收速度

播放工具栏显示 **发送速率、显示 FPS、累计发送量**：

- 发送速率按最近 2 秒实际呈现的编码负载计算，每 250 毫秒刷新；预取完成但尚未显示的图片不计入。
- 不计协议头、文件名帧及 PNG 压缩体积，但计入编码填充、喷泉冗余和轮播重发。
- 显示 FPS 是网格内单码的平均帧率；累计帧数则包含各码的呈现。
- 暂停时速率归零，恢复时重新建立速率窗口；累计量保留，新序列化任务重新计数。

这项指标反映网页显示的编码数据，**不代表接收成功速度**。Horizon 等远程桌面还可能丢弃网页之后的画面更新；不能用设定 FPS 或发送读数代替接收端的文件恢复结果。

端到端有效吞吐量应使用：`成功恢复的原始文件字节数 / 从传输开始到文件完整恢复的时间`。相关统计口径见 [发送界面说明](docs/sender-interface.md)。

## 文件大小、容量与资源边界

上传上限、编码参数上限和二维码容量需要同时满足。网页会显示当前文件的可用分片范围，不能只按上传上限判断是否可传。

| 项目 | 当前限制 / 行为 |
| --- | --- |
| 默认上传上限 | 64 MiB |
| 经典 QR / Color 源块数 | 最多 8192 个，`K = ceil(文件字节数 / 分片字节数)` |
| 协议分片上限 | 2933 B；不代表普通 QR 能装下这么大的分片 |
| 普通 QR | ECC M，整帧最多 2331 B，包含 QRT2 头 |
| 默认 2048 B 分片 | 源块数对应的文件上限为 16 MiB |
| Color Matrix | 整帧容量 1587 B，当前有效分片保守限制为 1426 B，选择该模式时自动收敛 |
| Decimen | 原始文件最多 64 MiB，每帧 20 B 头 + 2933 B 编码负载，仍受实际资源约束 |
| 发送编码 worker | 内存边界 512 MiB，编码任务最长 5 分钟 |
| 发送缓存 | 每会话 PNG 最多 128 MiB，所有会话原始帧 + PNG 合计 256 MiB，最多 4 个会话 |

因此，**24.4 MB 文件在当前普通 QR 模式仍需压缩或分卷**，不能通过任意增大分片解决；分片太大会先触及二维码容量。合法参数也不保证任意设备都有足够资源。

v2.0.1 已移除接收端的估算内存预算和累计帧数拒绝，但保留协议字段、长度、会话身份和完整性校验；设备实际可用内存及算法开销仍会影响可接收规模。

## 协议、完整性与文件名

普通 QR / Color 的 QRT2 帧格式为：

```text
QRT2/session/sha256/code/chunk/total/codec/kind/crc32|payload
```

`payload` 是原始二进制编码符号，不使用 base64；它可能是源符号、异或 / 修复符号或填充后的数据，并非每帧都直接对应一段原始文件。不同编码方案使用各自的分片、随机性和恢复逻辑，不能把所有模式都视为相同的 LT 实现。

每帧校验 CRC 和参数，会话身份包含会话标识、文件摘要、分片、总长及编码方案。恢复结果通过长度与 SHA-256 校验后才进入完成流程。Decimen 使用 DCF2 容器及独立帧头，校验完整容器 FNV-1a、原始文件长度和 SHA-256。

### 为什么有时保存为 received.bin？

经典模式的文件名是独立元数据，不参与喷泉数据恢复。内容可以完整恢复，而文件名帧恰好没有被识别。

v2.0.6 的处理方式：

1. 有文件名时，每轮分散发送相同文件名帧 **3 次**，不增加数据块进度或发送负载计数。
2. 内容已校验但没有文件名时，两端继续补收约 **3 秒**；正常已知文件名不增加等待。
3. 只接受通过校验、属于同一会话的名称，保留第一个名称；补收不重复解码，也不延长已冻结的内容恢复耗时。
4. 超时仍未收到名称时明确提示，Windows 使用 `received.bin` 等可用名称，Android 建议使用带时间和摘要的默认名称，保存前可修改。

三次重复仍可能全部漏识别，不保证任何光学条件下都能保留名称。兜底名称本身不表示内容损坏；文件内容仍须通过独立校验。Decimen 的文件名包含在 DCF2 容器中，流程不依赖这三次名称帧。

详细字段、算法映射与边界见 [当前协议说明](docs/protocol-v2.md)。

## 日志与故障定位

### 发送端

网页「发送日志」可刷新最近记录、查看实际日志路径及写盘错误。日志包含版本、编码失败、编码 / 图像准备耗时、首帧响应和慢渲染等信息，不记录原始文件内容。

Windows 通常位于 `%LOCALAPPDATA%\qrt-pro\logs\sender.log`，不可用时尝试临时目录，以页面显示为准。当前文件和两个备份各约 2 MiB，内存保留最近 200 条。独立发送服务另有 EXE 同目录的 `txqr-web.log`，用于启动、监听和退出等运行日志；它不是完整发送诊断日志的替代品。

### Windows 接收端

「诊断日志」页与 `<接收目录>/logs/receiver.log` 记录区域识别、会话、解码和保存情况。文件约 4 MiB 轮转，保留一份备份；内存保留最近 400 条，同类高频信息按约 3 秒窗口折叠。

| 原因 ID | 含义与排查方向 |
| --- | --- |
| `qr.notfound` | 多次识别尝试未获得有效帧；检查区域、画面清晰度和尺寸 |
| `qr.found-no-payload` | 检出符号但载荷未通过验证；检查模式、校验或是否扫到了其他二维码 |
| `frame.header-invalid` | 帧头、参数或 CRC 无效；检查版本和输入 |
| `frame.session-mismatch` | 拒绝其他会话；工作中的经典传输连续收到同一新身份 15 帧后可切换，等待补收名称时不切换 |
| `fountain.stalled` | 持续收到重复帧而无新增块；检查是否暂停或画面未更新 |
| `region.too-small` / `region.dense` | 区域像素密度不足，调整窗口、分片或网格 |
| `frame.name-wait` | 内容已校验，正在补收名称 |
| `frame.name-missing` | 名称补收超时，将使用默认名称 |
| `save.error` | 文件写入失败，检查目录权限或剩余空间 |

启用「失败帧」可将有限数量的识别失败画面保存到 `%TEMP%\qrt-debug`，辅助检查框选。

## 源码结构

```text
txqr-pro/
├── txqr/                         # Go 模块，模块名沿用 github.com/divan/txqr
│   ├── cmd/qrt-pro/              # Windows 统一桌面入口
│   ├── cmd/txqr-web/             # 独立网页发送服务
│   ├── cmd/txqr-ascii/           # 终端 ASCII 发送入口
│   ├── cmd/txqr-gif/             # GIF 发送入口
│   ├── internal/sender/          # HTTP 服务、内嵌网页、隔离编码、图像缓存与日志
│   ├── internal/scanner/         # 屏幕采集、解码池、接收状态、自动保存与审计
│   ├── internal/w32/             # Win32 / GDI 资源与窗口接口
│   ├── internal/uikit/           # 原生绘制层
│   ├── internal/version/         # Windows / 网页版本号
│   ├── decimen/                  # Decimen 帧、喷泉变体与 DCF2 容器
│   ├── qr/                       # QR 与彩色矩阵编解码
│   └── third_party/go-qrcode/    # go.mod replace 使用的本地实现
├── txqr-reader/android/          # Kotlin / Compose、CameraX、ML Kit 与接收解码器
├── docs/                         # 协议、性能证据、审计与发布说明
└── .github/workflows/            # 检查与发布工作流
```

统一客户端的收发服务运行在同一主进程中；喷泉编码及初始 PNG 准备使用同一 EXE 的短期受限 worker，无需额外安装编码程序。后续 PNG 在主进程中按需生成，受输入尺寸和并发约束，不具有隔离 worker 的独立内存硬上限。网页和图像通过本机 HTTP 服务提供，无需外部网站。

## 构建、测试与发布

直接使用 Release 安装包不需要开发环境。源码构建需要依赖已下载或可获取；离线运行不等于首次构建无需依赖缓存。

### Windows 客户端

Go 版本以 [txqr/go.mod](txqr/go.mod) 为准，当前为 Go 1.25.12。在 Windows PowerShell 中：

```powershell
cd txqr
go build -trimpath -ldflags "-s -w -H windowsgui" -o qrt-pro.exe ./cmd/qrt-pro
go build -trimpath -ldflags "-s -w -H windowsgui" -o qrt-pro-sender.exe ./cmd/txqr-web
.\qrt-pro-sender.exe -addr :9000
```

独立服务可用 `-addr :18888` 修改端口，不会自动弹出浏览器。统一客户端有「打开发送页面」入口。

原生界面离屏截图与窗口绘制诊断：

```powershell
.\qrt-pro.exe -snapshot shot.png -tab 1 -demo -w 1080 -h 720
$env:QRT_PAINT_CHECK = "$PWD\paint.log"
.\qrt-pro.exe
```

`-tab` 为 0 发送、1 接收、2 已收文件、3 诊断日志。绘制日志记录窗口 DC 采样，便于排查空白窗口。

### Android

需要 JDK 17 和 Android SDK，当前 `compileSdk=35`；按本机 SDK 位置配置 `local.properties`。使用项目自带 Gradle Wrapper：

```powershell
cd txqr-reader/android
.\gradlew.bat :app:testReleaseUnitTest :app:assembleRelease --no-daemon
```

Linux / macOS 使用 `./gradlew`。产物为 `app/build/outputs/apk/release/app-release.apk`。当前 release 构建配置使用 debug signingConfig，与仓库发布工作流一致。

### 回归检查

Windows 可在 `txqr/` 下运行 `go test ./... -timeout 300s`。Linux 的共享模块 race 与网页回归：

```sh
cd txqr
go test -race -short . ./decimen ./qr ./internal/scanner ./internal/sender -timeout 300s
node internal/sender/testdata/playback.test.cjs
node internal/sender/testdata/throughput.test.cjs
node internal/sender/testdata/limits.test.cjs
# 已安装 Chrome 时可另外执行真实浏览器回归：
CHROME=/path/to/chrome node internal/sender/testdata/browser.test.cjs
```

测试覆盖完整性、跨语言向量、文件名丢失与迟到、重复 / 其他会话、取消、保存、资源与播放调度。性能记录见文档索引，不将本地 CPU 基准等同于光学传输速度。

普通分支推送运行 [CI 检查](.github/workflows/test.yml)，不会生成发布附件；推送 `v*` 标签才触发 [出包工作流](.github/workflows/release.yml)，检查 Go / Android / tag 版本一致后发布两个 Windows EXE 和 APK。

## 文档与版本记录

| 文档 | 内容 |
| --- | --- |
| [v2.0.6 发布说明](docs/releases/v2.0.6.md) | 文件名三次发送、完成后补收与升级兼容性 |
| [全部 v2 发布记录](docs/releases/) | v2.0.0–v2.0.6 的逐版变化 |
| [发送界面说明](docs/sender-interface.md) | 布局、等待虚框和发送速率口径 |
| [当前协议与运行上限](docs/protocol-v2.md) | QRT2、DCF2、跨端映射及边界 |
| [发送端准备策略及证据](docs/sender-v2.0.3.md) | 首批 / 整轮准备、缓存、日志和受控测试 |
| [v2.0.2 接收优化](docs/performance-v2.0.2.md) | 识别路径缓存、CPU 证据及 Horizon 验收方案 |
| [v2.0.1 性能修复](docs/performance-v2.0.1.md) | 帧率回归、接收预算与截图开销修复 |
| [v2.0.0 修复及验证](docs/fix-results-20261029.md) | 会话、完整性、算法和资源修复记录 |
| [算法与模式历史分析](docs/mode-and-codec-analysis.md) | 五种经典算法及模式设计对照 |
| [历史代码审计](docs/code-audit.md) | 旧版本问题、历史实验与修复依据 |

历史分析文档可能保留当时的帧格式和参数，当前协议以 `docs/protocol-v2.md` 及当前源码为准。

## License

[MIT](LICENSE)。保留上游项目的许可和作者信息。
