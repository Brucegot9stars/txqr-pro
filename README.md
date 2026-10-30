# QRT Pro

跨越二维码传输文件的端到端实现。本工程是在两个开源项目基础上改造而来，两个项目均来源于 **Ivan Daniluk (divan)**：

- **发送端** —— 基于 [divan/txqr](https://github.com/divan/txqr)（Go），源码仓库 <https://github.com/divan/txqr>（本目录下 `txqr/`）
- **接收端** —— 基于 [divan/txqr-reader](https://github.com/divan/txqr-reader)，源码仓库 <https://github.com/divan/txqr-reader>（本目录下 `txqr-reader/`）

支持 LT、Binary、Raptor、RaptorQ、Online 及 decimen 模式，通过动态二维码把二进制文件从屏幕传到接收端。

当前经典 QR / Color 模式使用 QRT2 校验协议，**发送端和接收端需要同时更新**，不接收旧经典帧。详见 [协议与运行上限](docs/protocol-v2.md) 和 [修复及验证记录](docs/fix-results-20261029.md)。

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
bug 正源于此。现在 UI 与收发服务合并为**一个主进程、一个窗口**；编码及 PNG 生成由同一可执行文件启动短期受限 worker，不需要另装程序或网络通信：

- 左侧导航栏切换「发送端 / 接收端 / 已收文件 / 诊断日志」，发送端与接收端可同时工作；
- 发送端的码画在**白底卡片**上，尺寸按「整数个设备像素/模块」由服务端渲染、页面 1:1 显示，
  不再随窗口缩放（改窗口只会在整数档之间跳，每一档都能读）；
- 停止扫描从「遍历进程树」退化为一次方法调用；
- 接收端支持多区域（多屏各扫一路）、实时预览、逐区域进度与统计；
- **诊断日志**记录"为什么收不下来"：解码失败的原因、帧头异常、长时间收不到新块。

```sh
cd txqr
go build -ldflags "-s -w -H windowsgui" -o qrt-pro.exe ./cmd/qrt-pro
```

离屏出图（用于文档截图与布局回归，锁屏环境下同样可用）：

```sh
qrt-pro.exe -snapshot shot.png -tab 1 -demo -w 1080 -h 720   # -tab: 0 发送端 1 接收端 2 已收文件 3 诊断日志
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

## 诊断日志（收不下来时先看这里）

传输「有时候能成、有时候一直失败」时，失败计数本身说明不了任何问题。桌面接收端把每个
环节的结果写进日志，并同时显示在应用的「诊断日志」页：

| 原因 id | 含义 | 通常是 |
| --- | --- | --- |
| `qr.notfound` | 10 次缩放/裁剪都没检出二维码 | 框选位置不对、屏幕没显示、码太小/太模糊 |
| `qr.found-no-payload` | 检出二维码，但载荷不是本接收端的帧 | 屏幕上是 Color Matrix 帧（仅 Android 支持）或别人的二维码 |
| `frame.header-invalid` | 帧头无法解析，日志里带前 40 字节的可打印形式 | 协议不匹配、帧被截断 |
| `frame.session-mismatch` | 本帧的 chunk/总长与当前会话不一致 | 发送端换了文件；连续 15 帧后会重置并接管新会话 |
| `frame.session-new` | 接手一次传输，记录 chunk/总长/块数/编码 | 正常，用来确认锁到了正确的流 |
| `fountain.stalled` | 帧在来但都是重复的，5 秒没有新增块 | 发送端暂停/换文件，或区域拍到了静止画面 |
| `region.too-small` | 框比码还小，像素密度不够 | 见下面「框要多大」 |
| `region.dense` | 框勉强够，解码会时好时坏 | 把框放大一点 |
| `save.error` | 写文件失败 | 磁盘满、目录无权限 |

- 日志文件：`<接收目录>/logs/receiver.log`（默认 `%USERPROFILE%\Downloads\logs\receiver.log`），
  超过 4 MB 自动轮转，保留一份 `.1`；写不进去时只在内存里留最近 400 条，页面顶部会说明原因。
- 同一条原因每 3 秒最多写一行，行内带 `×N` 折叠计数——30 fps 下失败风暴也不会把日志写爆。
- 判读顺序：先看 `region.too-small`（框太小，读不出任何东西）→ 再看 `qr.notfound`（拍不到）
  → 再看 `found-no-payload`（拍到了但不是我们的帧）
  → 再看 `fountain.stalled`（拍到了也对，但内容重复）。
- 勾选接收端工具栏的「失败帧」还会把解不出的画面存成 PNG 到 `%TEMP%\qrt-debug`，用于核对框选。

### 框要多大（像素密度决定一切）

决定能不能读出来的是**每个模块占几个像素**，不是框多大本身。桌面端是按屏幕像素 1:1 抓图，
所以这个密度 = 编码显示尺寸 ÷ 符号模块数：

| 每帧字节 | 符号模块（含静区，ECC M） | 实测最低可用框 | 建议框（≈3 px/模块） |
| --- | --- | --- | --- |
| 512 | 97 | ≈ 180 px | ≈ 290 px |
| 1024 | 129 | ≈ 320 px | ≈ 390 px |
| 1536 | 157 | ≈ 360 px | ≈ 470 px |
| 2048（默认） | 177 | ≈ 400 px | ≈ 530 px |

「最低可用」是实测值（缩放平滑时，2~3 帧全中），但**贴着这个下限时成败取决于缩放相位**：
框大 20px 就可能从全中掉到全灭，而在相邻的某个尺寸又恰好全中。想稳就按「建议框」留余量。

阈值：**≥ 2.5 px/模块**才稳定，**低于 1.5 基本读不出**（实测边界，之前按 3.5 估得太保守）。
三条实操建议：

1. **框比码大**：在码四周留 40 px 左右空白。紧贴码边画会切掉静区（码外围的空白边框），
   解码器的边缘裁剪又会再切一刀，两边一叠就把定位图案切掉了。
2. **发送端窗口尽量大**：页面把每帧缩放显示到 `min(92vw, 90vh)`，窗口越大码越大。
   全屏（F11）时 1080p 上约 900 px，2048 字节/块也有 5 px/模块。
3. 窗口小又不想放大框时，把「每帧字节」调小（如 1024）：符号变简单，同样的框就够用了，
   代价是帧数翻倍。

接收端在识别到传输时会自动算这个密度，不够就写 `region.too-small` / `region.dense`，
并在区域行上用黄字显示结论。

### 发送端尺寸：三种模式

页面上的「缩放方式」决定浏览器能不能重采样。默认是第一种，也就是不需要再找窗口大小的那种。

| 模式 | 行为 | 结果 |
| --- | --- | --- |
| **固定不缩放**（默认） | 服务端按页面报告的 `fit`（一个码可用的设备像素）渲染成 `模块数 × 整数 ppm`，页面以同样尺寸 1:1 显示 | 浏览器零重采样；改窗口只在整数档之间跳，每档都能读 |
| 允许缩放（铺满窗口） | 页面把图铺满格位，由浏览器缩放 | 尺寸最灵活，但模块宽度变小数，成功率随窗口大小跳变（就是以前的行为） |
| 手动（指定 px/模块） | 你给 2~8 的数字，仍按该值渲染并 1:1 显示 | 想固定成某个大小、或复现某次配置 |

- 1:1 实测：ppm 2~8 × 留白 0/10/40 共 **21 组全部解码**；同一张图交给浏览器缩放到视口时
  只有约 60%，且相邻宽度会从 0/3 跳到 3/3（见 `docs/code-audit.md` A9）。
- 状态栏显示当前状态：`4px/模块` / `铺满窗口（允许缩放）` / `点「序列化」应用 6px/模块`
  （手动改了数字但还没重新序列化时会这样提示，避免悄悄缩放）。
- 选择记在 localStorage，下次打开沿用。
- 只有窗口小于渲染尺寸时才会退回缩放，并在状态栏注明「窗口偏小，码已缩放」。

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

当前版本：`v2.0.2`（见 Git tag）。Android 端 `versionCode=13` / `versionName=2.0.2`。

v2.0.2 为 Windows 接收区域记住已验证的识别路径，失败时完整回退，并复用 QR 检测/回退的只读亮度数据，降低识别 CPU 与分配。详见 [本轮优化证据与 Horizon 验收方案](docs/performance-v2.0.2.md)。

v2.0.1 修复播放帧率回归，取消接收端内存估算/累计帧数拒绝，减少重复截图识别及裁剪复制。性能证据及限制见 [性能修复记录](docs/performance-v2.0.1.md)。

v2.0.0 引入 QRT2 会话与完整性校验，修复已知 F01–F24，并加入受限编码 worker、后台接收及解码后呈现。**经典 QR / Color 收发两端必须同时更新**；不兼容旧经典帧，decimen 线格式保持不变。详见 [修复记录](docs/fix-results-20261029.md) 与 [协议及运行上限](docs/protocol-v2.md)。

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
  每帧渲染失败；该版本自动钳制到 1539。当前 QRT2 预留校验头后为 1426，并回传有效值。
- 新增 `docs/mode-and-codec-analysis.md`：五种编码方案与两种工作模式的原理、实测与结论。

v1.4.2 是缺陷修复版本：

- **接收端列表不能滚动**：「扫描区域」超过 3 个、或「已收文件」超过一屏时，多出来的行
  被直接丢弃且没有任何滚动手段。现在支持滚轮、拖动和右侧滚动条，新增区域会自动滚到可见处。
  见 `docs/code-audit.md` B20。
- 顺带修掉两个被它暴露的底层缺陷：`RoundRectGradient` 用 `SelectClipRgn`（替换语义）
  会顶掉外层裁剪，改为 `ExtSelectClipRgn(RGN_AND)` 求交；裁剪区外的控件不再注册，
  滚出半个身位的按钮点不到。

v1.4.3 新增接收端诊断日志，用来回答「有时候能传成、有时候一直失败」：

- 新增「诊断日志」页：逐条记录解码失败、帧头异常、会话切换与停滞，带 `×N` 折叠计数，
  并写到 `<接收目录>/logs/receiver.log`（4 MB 轮转）。判读方法见
  [诊断日志](#诊断日志收不下来时先看这里)。
- `qr` 包新增 `DecodeRawMultiScaleVerbose`：能区分「一次都没检出二维码」与
  「检出了但载荷不是本接收端的帧」（后者通常是 Color Matrix 帧或别人的二维码）。
- 顺带修掉两个真问题：异会话的帧不再喂给旧解码器（连续 15 帧不一致才判定换文件并重置）；
  文件名帧不再被算作一个数据块（进度曾因此提前一格，且文件名恒为空）。见 `docs/code-audit.md` B21。

v1.4.4 修「传输时好时坏、每次都要重找窗口大小」：

- **发送端不再让浏览器缩放码**：服务端按「整数个设备像素/模块」渲染，页面 1:1 显示。
  1:1 实测 21 组（ppm 2~8 × 留白 0/10/40）全部解码；同一张图交给浏览器缩放到视口只有约 60%，
  且相邻窗口宽度会从 0/3 跳到 3/3。见 `docs/code-audit.md` A9。
- 新增 **缩放方式** 三档开关：固定不缩放（默认）/ 允许缩放（铺满窗口，旧行为）/ 手动（指定 px/模块）。
- 码改画在**白底卡片**上：深色底会让「框留白」变成倒置对比度环，同一张图 1:1 深色底 1/4、白底 4/4。
  见 A10。
- 接收端的密度诊断阈值按实测修正（<1.5 px/模块 报错、<2.5 警告），不再误报实际可用的配置；
  README 补了各 chunk 的实测最小框尺寸。

## License

[MIT](./LICENSE)（与上游 divan/txqr、divan/txqr-reader 一致）。