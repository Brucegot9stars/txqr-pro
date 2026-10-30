# v2.0.1 性能与接收限制修复

## 问题与结论

用户反馈 Windows 接收同一约 46K 文件、split=2048、30 FPS、单格、RaptorQ RFC 6330 时，v1.4.4 约 50 KB/s，v2.0.0 约 28 KB/s，并遇到“超过接收内存预算”。这些速度来自用户设备，本机没有复现完整 Windows 光学链路。

实际帧检查表明，两版 RaptorQ 数据符号均为 2048 字节。以确定性随机数据 46×1024 字节、文件名 `46KiB.bin` 为样本，旧帧头含分隔符约 21–22 字节，QRT2 为 138–139 字节。QR 使用 ECC M、每模块四像素时，边长从 177 模块/708 像素增加至 181 模块/724 像素；轮播序列从 47 帧增加至 48 帧，其中增加的是文件名元数据。协议头没有挤占分片，但二维码几何和元数据仍会影响光学识别与短文件完成时间。

已确认的回归是播放实际帧率不足：单格 30 FPS 的真实 Chrome 测试，v2.0.0 播放逻辑在 2.5 秒内仅替换 48 帧（19.2 FPS）；改动后为 75 帧（30 FPS）。该测试使用快速模拟取帧，不能直接解释实际设备上全部 50→28 KB/s 差值。

## 修改

| 位置 | 原因 | 修复与边界 |
|---|---|---|
| `txqr/internal/sender/index.html` | 每格只预取一帧；`now+interval` 不断累积刷新量化误差；decimen 单刷新只能更新一格 | 每格预取三帧并等待 Image.decode；按已有时间轴推进；所有到期格子可在同一刷新更新。请求/解码失败按原序号退避重试，迟到回调隔离，暂停/取消释放资源。不会突破显示器刷新率 |
| `txqr/decode.go`、`txqr/decimen/decimen.go`、Android `decoder/` | 保守工作内存估算可拒绝合法会话，累计帧数限制可拒绝仍有机会恢复的传输 | 移除接收端人工内存预算、矩阵/修复符号预算及 `4K+256` 帧数拒绝。保留实际协议字段范围、会话身份、去重、CRC/FNV 和最终 SHA。实际内存需求由算法和设备决定 |
| `txqr/internal/scanner/scanner.go` | 捕获约 60 FPS、显示 30 FPS 时，相邻相同截图重复耗费 QR CPU | 完整像素比较后只提交变化帧；只有成功入队才更新基准，每次重扫重建基准。队列满后相同截图可重试；不使用可能碰撞的图像摘要 |
| `txqr/qr/qr.go` | 多个裁剪候选逐像素复制；通用 RGBA 转亮度产生逐像素装箱分配 | 标准 image 使用只读 SubImage；其他 image 保留复制。RGBA 直接访问缓冲，再以单字节 Gray 进入原识别器；逐字节测试对照全部 alpha 值、非零原点和跨行 stride。保留原候选顺序及探测 fallback |

未改变喷泉码组合、随机数、分片长度、冗余规则或 QRT2 线格式，v2.0.0/v2.0.1 可互通。发送端 worker 和缓存管理仍保留；本次取消的是接收端新增预算。

## 已执行测试

环境：Linux amd64、AMD Ryzen 5 5600U、Go 1.25.12、Kotlin 2.1.0、Chrome 150。没有本地产品出包。

- Go 五包 `-race -short` 全部通过：核心、decimen、QR、scanner、sender。涵盖原有会话/CRC/SHA/欠秩回归，新增大会话不按估算拒绝、300 个冗余符号后恢复、相同截图跳过、队列满/关闭、裁剪非零原点及亮度等价测试。
- Kotlin 纯解码 JUnit 35 项通过；删除旧预算失败断言，增加 8 MiB RaptorQ 会话、长冗余序列、合法大 decimen 容器接收测试。大容器测试只加入首个符号，不代表整份大文件恢复或设备内存验收。
- Node 播放生命周期回归通过；确定性 60 Hz 且带刷新抖动/两次刷新解码延迟的测试中，QR/decimen、单格/双格、30/60 FPS 共八种组合达到期望呈现帧数；阻塞后无追赶突发。
- 真实 Chrome 验证解码后呈现、慢请求有界、暂停/恢复、失败保留旧图和 URL 回收。另测单格 30 FPS 结果如上。
- `git diff --check` 及 JavaScript 语法检查通过。播放测试已加入远程 CI；Windows 和 Android 产品验证以远程工作流结果为准。

## CPU 对照

为隔离协议及图像差异，将同一组已生成的 724×724 RGBA 图像分别交给 v2.0.0 tag 快照和修改后的当前代码，串行执行 QR 多尺度识别、帧解析、RaptorQ 恢复及最终校验。每次新建解码器，均使用 24 帧并核对恢复的 47,104 字节；预热一次、测三次，期间没有并行运行其他测试。图像生成和 PNG 读取不计时。

| 代码 | 三次耗时（ms） | 三次累计分配（bytes） | 三次分配次数 |
|---|---|---|---|
| v2.0.0 | 2398.595 / 2340.816 / 2362.632 | 616351928 / 616346536 / 616346200 | 101194741 / 101194734 / 101194731 |
| v2.0.1 修改后 | 979.179 / 969.837 / 974.146 | 168023160 / 168017944 / 168017352 | 25171 / 25165 / 25159 |

这是同图像串行 CPU 工作的局部测量，累计分配不是堆峰值。截图去重和浏览器调度收益不包含在该 CPU 对照中；不据此推导光学 Goodput 倍数。原始工具及日志位于 `/tmp/txqr-review-20261029/optical-replay.go`、`frames-current.json`、`replay-v200.log`、`replay-v201.log`。

新增可持续运行的 `BenchmarkReceiveRaptorQ46KiB`：固定数据种子、split=2048、包含文件名，计时内每轮重新识别和恢复，检查完整原始数据。它会重新生成会话和 QR，因此耗时可与上面的同图像对照略有差异。

```sh
cd txqr
go test -race -short . ./decimen ./qr ./internal/scanner ./internal/sender -timeout 300s
go test ./qr -run '^$' -bench '^BenchmarkReceiveRaptorQ46KiB$' -benchtime=1x -count=3
node internal/sender/testdata/playback.test.cjs
node internal/sender/testdata/throughput.test.cjs
CHROME=/path/to/chrome node internal/sender/testdata/browser.test.cjs
```

## 设备复测

两版用同一源文件 SHA、2048 分片、30 FPS、单格、RaptorQ、同一窗口/模块像素、显示器与接收区域，至少各测十次。记录实际呈现 FPS、捕获/识别成功/有效唯一帧、从开始显示及从首个有效接收帧起的两个完成时间、原始 SHA-256、CPU 与内存峰值。分别计算两个时间口径的 Goodput，不能直接以浏览器帧率或编码字节率替代。

再覆盖 60 FPS、多格、丢帧/重复、暂停/继续、大文件、校验失败及保存失败。Windows GDI、Android 相机/存储和不同设备的内存峰值尚未实测，本报告不将这些项目标为通过。
