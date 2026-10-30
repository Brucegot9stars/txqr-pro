# QRT2 传输协议

本轮修复采用用户确认的范围：只要求更新后的两端互通。经典 QR / Color Matrix 的旧文本帧不再接收；请同时更新 Go 发送端、Windows 接收端和 Android 接收端。喷泉码的索引、随机数、分片方式和 Online 参数保持原有定义，未替换算法。decimen 仍使用 D1 0D + DCF2，其线格式未改变。

## 经典帧

```
QRT2/session/sha256/code/chunk/total/codec/kind/crc32|payload
```

| 字段 | 定义 |
|---|---|
| session | 每次 Encode 重新生成的 128 位随机值，32 个小写十六进制字符 |
| sha256 | 原始文件的 SHA-256，64 个小写十六进制字符 |
| code | 十进制编码块编号；文件名帧为 -1 |
| chunk | 用户指定的分片参数，不等同于所有算法的实际源块长度 |
| total | 原始文件字节数 |
| codec | 显式 `lt`、`binary`、`raptor`、`raptorq`、`online` |
| kind | `data` 或 `name` |
| crc32 | CRC-32/IEEE，8 位十六进制字符 |
| payload | 原始二进制；name 帧为 UTF-8 文件名 |

CRC 输入为 `QRT2/.../kind` 的 ASCII 字节、一个 `|` 字节、完整 payload；不包含 `/crc32` 本身。所有字段、长度及 CRC 必须先验证，再更新会话、去重缓存和方程状态。CRC 用于检出帧错误，SHA-256 用于核对恢复的原始文件；它们不提供发送者身份认证。

会话身份为 `(session, sha256, chunk, total, codec)`。不同身份的方程绝不混用。Go Decoder / Android TxqrDecoder 由显式 reset 更换身份；Windows 管理层在连续 15 帧来自同一新身份后，清空旧方程再切换。文件名是独立元数据，不增加数据块进度；如果没有拍到文件名帧，使用接收端生成的名称。

令 `K=ceil(total/chunk)`。RaptorQ 的数据帧长度为 chunk，最后一个源符号补零；其他模式的非空数据帧长度为 `ceil(total/K)`，发送端将 gofountain 的虚拟尾部零填充显式写入帧。Binary 的空组合允许空 payload。小文件 `total<=chunk` 的 gofountain 模式使用显式 LT 单块帧；Raptor 原有小 K / 本地自检失败回退 LT 的规则保留，元数据使用实际生效的 codec。

接收端必须执行：CRC / 参数验证 → 会话匹配 → 去重 → 加入符号 → 尝试实际求解 → 原始长度及 SHA-256 验证 → 发布完成数据。RaptorQ 的 ready 仅代表可以尝试求解；秩不足时继续接收。长度或 SHA 不匹配进入失败状态，只有重扫才重新使用空白状态。

## 资源边界

Go 与 Kotlin 的经典解析器共用：total ≤ 64 MiB、chunk ≤ 2933、K ≤ 8192、code ≤ 0xffffff；Raptor 的 code ≤ 65535 且 K ≥ 4。独立限制同时生效，例如 split=2048 时最多 16 MiB。文件名最多 360 个 UTF-8 字节；头部最多 160 字节；唯一数据帧缓存最多 `4K+256` 项。这些是协议参数上限，不是各算法的理论极限；接收端还会检查工作内存预算，因此合法帧也可能因设备资源不足而被明确拒绝。Windows/Go 经典解码在建矩阵前按算法、K 和数据量估算，预算为 256 MiB。Android 的工作预算为 `min(256 MiB, JVM 最大堆/3)`，检查文件拷贝、稀疏矩阵保留行、RaptorQ 修复矩阵/符号及解压后大小；超限进入可见失败状态，不能当作成功文件保存。该预算不代表整个应用的 RSS 硬上限。

Color Matrix 的总载荷仍为 1587 字节，预留 161 字节给 QRT2 头及分隔符，数据 split 上限为 1426。普通 QR 在 ECC M 下仍受二维码容量限制。发送端根据所有帧的最大长度选择固定 version，保留静区和整数模块尺寸。

发送端默认上传上限为 64 MiB，最多保留 4 个会话；编码请求全局单并发，PNG 渲染最多 8 个 worker；单次 PNG 缓存上限 128 MiB，会话帧及 PNG 总缓存上限 256 MiB。编码和 PNG 生成由同一可执行文件启动的短期 worker 执行，仅使用标准输入/输出管道，不增加网络通信。Windows Job Object 限制 worker 私有提交内存为 512 MiB，并在父进程退出时终止 worker；Linux 使用 RLIMIT_DATA 限制私有可写映射，配合父进程退出信号。Linux 的映射限制与 RSS 不等价；已有更低的系统硬限制不会被提升。请求取消会终止并等待 worker，5 分钟任务期限也会触发终止，即使第三方库正在不可中断的调用中。worker 的临时数据受上述进程限制约束；主进程仍保留上传缓冲和有界缓存，不声称整个 UI 进程被限制为 512 MiB。上传读取期限为 30 秒。活跃取帧刷新空闲过期时间，连续空闲 30 分钟后回收。Windows/Linux 以外的 worker 平台明确报不支持，不静默回退成无限制编码。

## 调度与测量边界

Android 由一个后台处理器顺序接收帧批次，只保留一个最新的待处理批次。重扫创建新会话对象；旧任务只能完成自己的计算，不能向新会话回写。解码、解压和校验不在主线程执行，主线程只读取不可变结果快照。

浏览器每格最多预取一帧，完成 `Image.decode()` 后才在刷新回调中替换显示。慢加载或失败时继续显示上一帧；暂停/取消会终止加载并释放临时 URL。decimen 每次刷新最多更新一个格子并轮流更新，避免同一刷新同时切换多个格子。因此设置的 FPS 是上限，在 60 Hz 屏幕上不能把 2 格 × 60 FPS 当作 120 次实际交错显示。HUD 显示实际替换帧数及观测平均显示率，不能称为文件 Goodput。

接收界面的完成平均速度是原始文件字节数除以从首个有效接收帧到校验恢复的时间；不包含发送编码或首次识别前的等待。完整端到端 Goodput 仍需要受控光学测试，不能从这些局部指标直接推导。

## decimen

解析时验证 `0 < totalLen ≤ K*blockLen` 且 `(K-1)*blockLen < totalLen`、blockLen ≤ 2933，以及容器最大长度。只有成功验证整个 DCF2 容器的 FNV-1a、解包后文件长度及 SHA-256，才对外报告成功。FNV 还覆盖文件名及 MIME 等容器内容，但不提供密码学身份认证。失败后停止重复组装/解压，并要求重扫。Android 显式重扫可采用其他会话；Windows 连续 15 个相同新身份的帧可以清空旧状态后切换。

## 共享向量

`txqr/cmd/goldengen` 生成五种 codec 的帧、K=1536 Online 错误恢复反例、K=8192 索引指纹、RaptorQ 欠秩反例、非整除分片和 Color RGBA/PNG fixture。接收端 JVM 测试从仓库 resources 读取，不依赖临时目录文件。

```
cd txqr
go run ./cmd/goldengen ../txqr-reader/android/app/src/test/resources/codecmatrix
```

每次生成会得到新的随机会话，文件内容、编码组合与期望恢复数据保持可验证。该命令仅生成测试数据。
