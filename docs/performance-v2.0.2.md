# v2.0.2 接收识别优化

## 范围

以稳定版 v2.0.1 为基线，针对 Windows 接收端识别计算优化。用户发送端位于 Horizon 连接的 Win10 云桌面，最高 30 FPS。本轮保持 QRT2/D1 0D 线格式、喷泉码算法、CRC/FNV/SHA 验证、默认分片与帧率设置，优先减少无效识别工作；未引入文件或剪贴板重定向、在线服务或新增通信通道。

## 实现

- `txqr/qr/qr.go` 新增 `DecodeRawMultiScalePreferred`：优先尝试已成功的裁剪/缩放候选，失败后按原顺序尝试所有其余候选，同次搜索不重复执行优先候选。未知偏好按原完整顺序搜索。旧公开解码入口继续可用。
- `txqr/internal/scanner/decode.go`：每个区域保存独立的候选名称和图像尺寸；尺寸不符时从完整搜索开始。只有通过 QRT2 参数/CRC 或 decimen 帧解析的载荷才能结束候选搜索。帧读取后，原有会话隔离、实际求解和最终完整性验证继续执行。
- `txqr/internal/scanner/scanner.go`：识别偏好与载荷在同一扫描代次检查之后发布，旧任务不能写回已重扫的状态。状态重置清除偏好。图像识别在锁外运行，偏好读写使用已有锁；缓存只保存名称和尺寸，不保留图像。
- 同一候选的常规检测与纯码回退共享只读亮度数据，各自创建新的二值矩阵；二维码库会修改解析矩阵，因此不共享二值矩阵。各个裁剪/缩放候选仍独立处理。
- 扫到错误协议或 CRC 错误的 QR 时，诊断明确说明载荷未通过验证，避免把可读取 QR 直接当作有效传输帧。

优先识别只改变搜索顺序；无法识别时仍保留原搜索候选。多个 QR 同处一个接收区域时，候选顺序可能影响选中的 QR；该区域仍按单个码使用，多码应分别建立接收区域。

## 验证

本机 Linux amd64、AMD Ryzen 5 5600U、Go 1.25.12，未本地构建 Windows/Android 产品。

- 完整 `go test ./qr ./internal/scanner` 通过，包含既有密集 QR、屏幕留白、缩放、decimen 恢复与保存测试。
- `go test -race -short . ./decimen ./qr ./internal/scanner ./internal/sender -timeout 300s` 五包通过。
- 新增：优先路径命中、错误路径完整回退、不重复尝试错误路径、未知偏好、载荷验证失败继续搜索、共享亮度不被修改、尺寸变化后的 RaptorQ 文件恢复、重置清除偏好、旧代次禁止发布偏好、CRC 错误不学习及并发读取/重置。
- Node 播放生命周期与 30/60 FPS 单格/双格回归通过；60 FPS 用例用于已有播放逻辑回归，不作为 Horizon 参数建议。
- `git diff --check` 通过。

发送端、Android 版本仅随发布同步版本号；本轮不对 Android 解码性能作提升声明。实际 Windows/Horizon 的 CPU、有效唯一帧率、内存峰值和文件 Goodput 仍需设备复测。

## 同图像对照

使用同一组 724×724 RGBA 图像，原始文件为固定种子的随机 47,104 字节，split=2048、RaptorQ，含文件名帧。每轮新建解码器，优先偏好为空；24 帧后恢复并校对全部原始字节。图像读取与生成不计时。各方案串行运行，预热一次、测量三次，没有并行跑测试。

| 方案 | 三次耗时（ms） | 累计分配约值 |
|---|---|---|
| v2.0.1 tag 原实现 | 996.523 / 980.697 / 993.459 | 168.0 MB/轮 |
| 当前代码，关闭优先候选，仅复用检测/回退亮度 | 775.437 / 769.361 / 793.404 | 106.4 MB/轮 |
| 当前代码，首次完整搜索，后续采用已验证偏好 | 221.065 / 233.785 / 228.277 | 34.5 MB/轮 |

中位数耗时约降低 77%，累计分配约降低 79%。这里 MB 使用十进制字节；累计分配不等于内存峰值。该对照包含 QR 识别、协议验证、RaptorQ 恢复及 SHA 校验，不包含屏幕采集、并发队列、Horizon 显示、发送准备或磁盘保存，不能推导为端到端四倍提速。

永久 benchmark 新增 `BenchmarkReceiveAdaptiveRaptorQ46KiB`，与完整搜索基准都逐帧验证协议并最终检查文件。其三次耗时分别约 243/238/264 ms，对照完整搜索约 797/757/759 ms；每次生成新的随机会话和二维码，图像可与上述固定图像对照不同。

```sh
cd txqr
go test ./qr ./internal/scanner -timeout 180s
go test -race -short . ./decimen ./qr ./internal/scanner ./internal/sender -timeout 300s
go test ./qr -run '^$' -bench '^BenchmarkReceive(Adaptive)?RaptorQ46KiB$' -benchtime=1x -count=3
```

本地工具和原始证据位于 `/tmp/txqr-review-20261029/`：`frames-current.json`、`adaptive-replay.go`、`adaptive-baseline.log`、`adaptive-fullsearch.log`、`adaptive-preferred.log`、`adaptive-bench.log`、`adaptive-full.log`、`adaptive-race.log`。v2.0.1 对照使用 `git archive v2.0.1 txqr` 导出的临时快照，项目源码未为对照而回退。

## Horizon 验收

先固定原来的 2048 分片、30 FPS、单码、RaptorQ 和相同显示区域，比较 v2.0.1/v2.0.2；用同一文件各传十次，核对 SHA-256，记录实际完成时间、有效唯一帧、识别失败数、CPU 和内存。再测留白变化、窗口缩放、错误区域、暂停/重扫与长时间运行。窗口稳定且接收 CPU 已有充足余量时，实际文件速度可能保持接近原水平；收益主要表现为降低 CPU 和增加识别余量。

后续可独立比较 20/24/25/30 FPS 和不同分片大小的 Goodput；本轮未自动改变这些参数或降低二维码纠错级别。
