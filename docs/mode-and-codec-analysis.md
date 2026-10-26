# 编码方案与工作模式：原理、实测与结论

针对「编码方案有 LT / Binary / Raptor(RFC 5053) / RaptorQ(RFC 6330) / Online 五种，哪些真实可用」
以及「Color Matrix / Decimen 两种工作模式到底有没有用」两个问题，本文给出原理说明、
可复现的实测数据和结论。所有数字都来自仓库内可重复运行的测量，不是估计值。

---

## 0. 结论速览

### 编码方案

| 方案 | 往返正确 | 跨端（Go 发 → Android 收） | 实测开销 | 判决 |
| --- | --- | --- | --- | --- |
| **RaptorQ (RFC 6330)** | ✅ | ✅ | **最低**（1.66×） | ✅ **推荐，保持默认** |
| LT（经典） | ✅ | ✅ | 最高（2.24×） | ✅ 可用，作兼容基线 |
| Online | ✅ | ✅ | 1.82× | ✅ 可用（参数已固定，见 §4.2） |
| Binary | ✅ | ✅（本轮修复后） | 1.71× | ⚠️ 可用但无优势 |
| Raptor (RFC 5053) | ✅ | ✅ | 1.73× | ⚠️ **多数文件会静默退化成 LT** |

「开销」= 成功收完一个文件实际需要在屏幕上播出的字节 ÷ 文件字节，越低越快（§2.2）。

### 工作模式

| 模式 | 每帧有效字节 | 模块/边 | 512px 下 px/模块 | 字节/模块² | 有用吗 |
| --- | --- | --- | --- | --- | --- |
| 不选（经典 QR，ECC M） | 2048（默认）／上限 2316 | 169／177 | 3.03／2.89 | 0.072 | 基线 |
| **Decimen** | **2933** | 177 | 2.89 | **0.094** | ✅ **值得开，约 3.9×（默认档对比）** |
| Color Matrix | 1587（上限），钳制后 1539 | **96** | **5.33** | **0.172** | ⚠️ **仅 Android 端可收**；桌面端解码不了 |

* 默认档对比：经典（30 fps、1 码、RaptorQ）≈ **36 KB/s**，Decimen（60 fps、2 码）≈ **140 KB/s**。
* Decimen 的收益**主要来自它锁死的 60 fps × 2 码 + ECC L 大帧**，不是喷泉码本身：
  它的喷泉开销（2.46×）其实比 RaptorQ（1.66×）**差 1.5 倍**。
* Color Matrix 每屏幕面积的信息密度是 Decimen 的 **1.83 倍**、经典默认的 **2.40 倍**，
  但它有两个硬伤：**桌面接收端完全不支持**，且**没有 ECC**（只有 CRC，错一个模块整帧作废）。

---

## 1. 五个编码方案分别是什么

五种都是**喷泉码（fountain code）**：把文件切成 K 个源块，然后源源不断生成编码块，
接收方只要凑够任意约 K 个（有冗余就行）就能还原，不必关心收到的是哪几个、也不必重传。
这正是二维码流传输需要的性质——摄像头随时会漏帧。

它们在代码里的位置：

| 方案 | 发送端（Go） | 接收端（Go 桌面） | 接收端（Kotlin/Android） |
| --- | --- | --- | --- |
| LT | `txqr/codec.go` → `fountain.NewLubyCodec` | 同左（`gofountainCodec`） | `LubyCodec.kt` |
| Binary | `fountain.NewBinaryCodec` | 同左 | `BinaryCodec.kt` |
| Raptor | `fountain.NewRaptorCodec(k,4)` | 同左 | `RaptorCodec.kt` |
| RaptorQ | `raptorq.NewRaptorQ`（xssnick/raptorq） | 同左 | `RaptorQCodec.kt` + 三张查表 |
| Online | `fountain.NewOnlineCodec` | 同左 | `OnlineCodec.kt` |

### LT（Luby Transform，经典）
最早的实用喷泉码。每个编码块按 **soliton 分布**随机选 d 个源块异或而成，解码靠「剥离」：
度为 1 的块直接解出一个源块，把它代回其他方程，连锁反应下去。
**特点**：实现简单，但度分布随机导致总有少量源块最后解不出来，需要多收一些块——
这就是它开销最高的原因（实测 2.24×）。

### Binary（Random Binary Fountain）
度不是按分布抽的，而是「对每个源块抛一次硬币，正面就加入」——因此每个编码块约含一半源块。
**特点**：块度大（≈K/2），几乎不能剥离解码，必须做高斯消元；开销略低于 LT（1.71×）但解码更贵。
`txqr/codec.go` 中的实现用的是 gofountain 的 `binaryCodec`（§4.1 会讲到它和 Kotlin 端曾不一致）。

### Raptor (RFC 5053，R10)
在 LT 前面加一层 **预编码（LDPC + Half 码）**：先把 K 个源块变换成 L > K 个中间块，
再对中间块做 LT 编码。预编码保证剥离过程几乎不会卡住。
**特点**：开销优于 LT，但 RFC 5053 规定 **K ≥ 4**，且 gofountain 的实现只在
「消息长度正好是块长整数倍」时才正确——见 §2.4，这条限制让它在真实文件上基本失效。

### RaptorQ (RFC 6330)
Raptor 的增强版，RFC 6330。主要改进：更大的 K 上限（56403）、
**系统性**（前 K 个编码符号就是源块本身，凑齐 K 个就完成，几乎零开销）、
以及 **inactivation decoding**（高斯消元 + 惰性求逆），任意 K 个符号都能解。
**特点**：开销最低（实测 1.66×），且无「长度必须是整数倍」这类退化条件。
发送端用 `github.com/xssnick/raptorq`，Android 端是手写实现（含三张 RFC 查表）。

### Online（Online Codes，Maymounkov & Mazières）
先在数据块后面追加约 `0.55·q·ε·K` 个**辅助块**（辅助块本身也是若干源块的异或），
然后编码块只从「源块 + 辅助块」里按特定分布选少量块异或。解码时先剥离出源块，
剥不动时用辅助块补足。
**特点**：编解码复杂度低（O(log) 级），代价是辅助块带来的固定冗余——开销 1.82×。
参数 ε 与 q **必须两端一致**（§4.2）。

---

## 2. 实测

### 2.1 往返正确性（无丢帧）

`go test -run TestCodecMatrixRoundTrip -v .`（chunk=2048，redundancy=2.0）：

| 方案 | 16 KiB（K=8） | 128 KiB（K=64） |
| --- | --- | --- |
| LT | 9 帧（1.125×） | 86 帧（1.344×） |
| Binary | 9 帧（1.125×） | 64 帧（1.000×） |
| Raptor | 8 帧（1.000×） | 64 帧（1.000×） |
| RaptorQ | 8 帧（1.000×） | 64 帧（1.000×） |
| Online | 12 帧（1.500×） | 67 帧（1.047×） |

五种都能还原出与发送端逐字节一致的数据。**注意**：1 KiB 那档 `total < chunkLen`，
编码器走单帧直通路径，根本不启用喷泉码，所以不能用来判断方案好坏（表里已排除）。

### 2.2 循环播放 + 丢帧：这才是真实链路效率

固定 fps 下，**「要播出多少帧才能收完」就是传输时间**。模型：发送端把帧列表循环播放，
每个被播出的帧以概率 p 被摄像头成功读到（p 越低代表拍摄条件越差）。

**(a) 64 KiB（K=32），10 次试验平均，解出所需帧数：**

| 方案 | p=0.9 | p=0.6 | p=0.35 |
| --- | --- | --- | --- |
| LT | 44（1.38×） | 64（2.02×） | 152（4.74×） |
| Binary | 39（1.22×） | 60（1.89×） | 127（3.96×） |
| Raptor | 38（1.18×） | 61（1.92×） | 122（3.83×） |
| **RaptorQ** | **36（1.12×）** | **58（1.80×）** | **112（3.51×）** |
| Online | 40（1.26×） | 68（2.11×） | 139（4.34×） |

**(b) 128 KiB、p=0.6，按「播出字节 ÷ 文件字节」归一化**（各模式每帧大小不同，
只比帧数会得出错误结论）：

| 方案 | K | 帧长 | 解出帧 | 帧数/K | **传输字节/文件** |
| --- | --- | --- | --- | --- | --- |
| LT | 64 | 2062 | 143 | 2.23× | **2.24×** |
| Binary | 64 | 2062 | 109 | 1.70× | 1.71× |
| Raptor | 64 | 2062 | 110 | 1.72× | 1.73× |
| **RaptorQ** | 64 | 2062 | 106 | 1.65× | **1.66×** |
| Online | 64 | 2062 | 116 | 1.81× | 1.82× |
| **Decimen** | 45 | 2953 | 109 | 2.42× | **2.46×** |

结论：

1. **RaptorQ 在所有丢帧率下都是最优**，且丢帧越严重优势越大（p=0.35 时比 LT 少 26% 的帧）。
2. **LT 最差**，它的随机度分布在 K 变大时退化明显（K=64、无丢帧就已要 86 帧而非 64 帧）。
3. **Decimen 的系统轮播喷泉码开销最高**（2.46×）——它靠大帧和锁死的高 fps 取胜，不靠喷泉码。
4. 注意 RaptorQ 有个实现细节：它**忽略 redundancyFactor**，帧数固定为 `K + 1.05K`（见
   `TestRaptorQFrameCount`），所以它播出的总帧数比其他方案少，这也是它快的一部分原因。

### 2.3 跨端一致性：Go 编码 → Kotlin 解码

这是真正决定「能不能用」的一环。用 `cmd/goldengen` 生成 Go 端帧，
喂给 Android 的 `TxqrDecoder`（`CodecMatrixCrossTest`）：

| 方案 | 8 KiB（K=16） | 37,777 B（K=74） | 修复前 |
| --- | --- | --- | --- |
| LT | 20 帧 ✅ | 88 帧 ✅ | ✅ |
| Binary | 18 帧 ✅ | 75 帧 ✅ | ❌ **MISMATCH（解出错误数据）** |
| Raptor | 16 帧 ✅ | 88 帧 ✅ | ✅ |
| RaptorQ | 16 帧 ✅ | 74 帧 ✅ | ✅ |
| Online | 22 帧 ✅ | 80 帧 ✅ | ✅ |

第二组 K=74 且长度非 512 整数倍：LT 与 Raptor 都是 88 帧、行为完全一致 ——
**直接印证了 Raptor 已退化成 LT**（§2.4）。

### 2.4 逐个方案的「真实可用」判定

**RaptorQ（RFC 6330）— 可用，且是最佳默认**
无退化条件、开销最低、跨端一致。发送端默认选中它是对的。

**LT — 可用，作兼容基线**
帧头不带 codec 字段时（3 段头）接收端就按 LT 初始化，因此它也是所有旧流和降级路径的兜底。
代价是开销最高。

**Online — 可用，但参数必须两端一致**
ε/q/随机种子都是**线上格式的一部分**：帧头只带 `online` 这个标签，接收端必须靠常量重建编解码器。
`txqr/encode.go` 与 `txqr/codec.go` 现在共用 `onlineEpsilon/onlineQuality/onlineSeed` 三个常量，
Android 端 `TxqrDecoder.kt` 里是 `OnlineCodec(numChunks, 0.01, 3, 42)`。
见 §4.2——这里曾经是个静默损坏数据的坑。

**Binary — 可用（本轮修复），但没有优势**
实测开销 1.71×，与 RaptorQ 的 1.66× 基本持平，却不具备 RaptorQ 的「任意 K 个符号即可解」性质，
而且块度≈K/2 意味着解码必须做消元，CPU 更贵。保留它只是为了协议完整。

**Raptor（RFC 5053）— 名义可选，实际基本等于 LT**
`txqr/encode.go` 里有两条降级：

```go
if e.codec == CodecRaptor && numChunks < 4 { effective = CodecLT }             // RFC 5053 要求 K>=4
if effective == CodecRaptor && !raptorRoundTrip(...) { effective = CodecLT }    // 长度非整倍数时往返不通
```

第二条几乎对所有真实文件生效——文件长度正好是 split 整数倍的概率极低。
实测（split=2048）：

| 数据量 | K | 帧头实际标记 |
| --- | --- | --- |
| 4 KiB | 2 | `lt`（K<4 降级） |
| 17 KiB | 9 | **`lt`**（非整倍数降级） |
| 64 KiB | 32 | `raptor`（正好整倍数） |
| 512 KiB | 256 | `raptor` |

所以选它时，**只有碰巧整倍数的文件才真的用上 Raptor**，其余都是 LT 的行为和开销。
UI 下拉框已加注「长度非整倍数时回落 LT」，避免误判。

---

## 3. 两种工作模式有没有用

### 3.1 先量「一帧能装多少」

用渲染→解码的完整链路实测（走的是接收端同一条解码路径 `qr.DecodeRawMultiScale`）：

| 模式 | 每帧有效字节 | 模块/边 | 512px 下 px/模块 | 字节/模块² | 渲染后解码 |
| --- | --- | --- | --- | --- | --- |
| 经典 QR（ECC M，split=512） | 512 | 89 | 5.75 | 0.065 | 5/5 |
| 经典 QR（ECC M，split=2048，默认） | 2048 | 169 | 3.03 | 0.072 | 5/5 |
| 经典 QR（ECC M，上限） | **2316** | 177 | 2.89 | 0.073 | — |
| Decimen（V40-L，mask 4） | **2933** | 177 | 2.89 | **0.094** | 5/5 |
| Color Matrix（2 bit/模块） | **1587**（钳制后 1539） | **96** | **5.33** | **0.172** | 5/5 |

三个关键事实：

1. **经典模式 ECC M 的每帧上限是 2316 B**；Decimen 用 ECC L 塞到 2953 B（有效 2933），**单帧多 27%**。
2. **Color Matrix 的模块数只有 96**（经典 V40 是 177），同样显示尺寸下每个模块大 **1.84 倍**，
   同时每模块带 2 bit → **每屏幕面积的信息密度是 Decimen 的 1.83 倍、经典默认的 2.40 倍**。
3. 上表的「解码成功」是**渲染出的干净图**直接喂给解码器，只能证明格式与容量正确，
   **不能证明真实摄像头在运动/模糊/白平衡漂移下读得出来**。真实可读性必须实机对比。

### 3.2 名义吞吐

`每帧有效字节 × 屏幕码数 × fps ÷ 开销`：

| 组合 | 原始 | ÷1.66（RaptorQ 实测开销） | ÷2.46（Decimen 实测开销） |
| --- | --- | --- | --- |
| 经典默认：2048 B × 1 码 × 30 fps | 60.0 KB/s | **36.1 KB/s** | — |
| 经典拉满：2048 B × 4 码 × 60 fps | 480 KB/s | 289 KB/s | — |
| **Decimen：2933 B × 2 码 × 60 fps（模式内锁死）** | 343.7 KB/s | — | **139.7 KB/s** |
| Color：1539 B × 1 码 × 30 fps | 45.1 KB/s | 27.2 KB/s | — |
| Color 拉满：1539 B × 4 码 × 60 fps | 360 KB/s | 217 KB/s | — |

### 3.3 Decimen 模式：**有用，实测约 3.9×**

相对默认档（经典 30 fps / 单码 / RaptorQ = 36 KB/s），Decimen 的 140 KB/s 是 **≈3.9 倍**。
把这个倍数拆开看：

| 来源 | 贡献 |
| --- | --- |
| fps 30 → 60（模式内锁死） | ×2 |
| 码数 1 → 2（1×2 网格，模式内锁死） | ×2 |
| 每帧 2048 → 2933 B（ECC L + V40 固定） | ×1.43 |
| 喷泉开销 1.66× → 2.46×（轮播码不如 RaptorQ） | **×0.67** |
| **合计** | **≈3.9×** |

**诚实的补充**：前两项（fps × 码数）用户自己在经典模式里也能拉。若把经典设成
60 fps + 2×2 网格 + RaptorQ，理论 289 KB/s **高于** Decimen。差别在于
4 个 V40（177×177 模块）同屏时每个码只剩屏幕的 1/4，摄像头很可能读不出来；
Decimen 的「2 个 V40 + 60 fps」是它验证过的组合。
所以：**想省事就开 Decimen；愿意调参就试经典 60 fps + 2×2 + RaptorQ，用实测成功率说话。**

### 3.4 Color Matrix 模式：**只在 Android 端有意义，且本轮之前根本发不出去**

两个硬约束：

1. **桌面接收端不解码色彩矩阵。** `internal/scanner/decode.go` 只走 zxing 的 QR 解码
   （`qr.DecodeRawMultiScale`）；`qr.ColorMatrixDecode` 定义了但**没有任何非测试调用方**。
   桌面端开这个模式 = 每帧解码失败、进度永远 0。只有 Android 端有 `ColorMatrixDecoder`。
2. **默认 split 超限，帧直接渲染不出来。** 矩阵容量 1587 B，而 UI 默认 split=2048，
   加上帧头后是 2063 B → `color: payload 2063 exceeds capacity 1587`，
   `renderColorPNG` 返回 nil，发送端拿不到任何图。**这就是「不知道选了有没有用」的原因：
   按默认设置它根本没有输出。** 本轮已在服务端钳制并把有效值回传给 UI（§4.3）。

即便修好，还有两个固有代价：
* **没有 ECC**：只有 CRC-16，一个模块读错整帧作废（QR 的 ECC L/M 能纠正 7%/15% 的错误）。
  密度换来的收益会被噪声下的丢帧率吃掉一部分。
* **依赖颜色判别**：4 色（黑/红/绿/蓝）比黑白的 QR 更容易受白平衡、色偏、屏幕色域影响。

**结论**：桌面端接收 → 别开。Android 端接收 → 值得对比实测：把 split 降到 1539 以下
（现在勾选后 UI 会自动收敛），同一台手机分别测经典和 Color，比「多久收完」而不是比理论值。

---

## 4. 本轮发现并修复的缺陷

### 4.1 Binary：两端定义不一致，Android 端解出**错误数据**却报成功

| | 实现 |
| --- | --- |
| Go（gofountain `binaryCodec.PickIndices`） | 对每个源块抛硬币：`rand.New(MT(blockCode)).Intn(2)==1` 则加入 → **随机约半数源块的异或** |
| Kotlin（修复前） | `intArrayOf(blockCode % sourceBlocks)` → **度 1，按序重复源块** |

后果不是「解码失败」，而是**解码成功 + 数据错误**：Android 把随机异或组合当成源块拼起来，
`isCompleted` 返回 true，用户拿到一个损坏的文件且界面显示传输成功。
经典模式没有端到端校验（只显示 MD5 供人工比对），所以这个错误不会自动暴露。

修复：`BinaryCodec.kt` 改为与 Go 完全一致的随机半数挑选，并复用 `LubyDecoder`
（新增 `BlockPicker` 接口，LT 与 Binary 共用同一套消元/重构逻辑）；
`BinaryDecoder`（只收集源块的旧实现）已删除。修复后跨端两档数据全部逐字节一致。

### 4.2 Online：ε/quality 不在线上格式里，改参数会静默损坏数据

帧头只带 `online` 标签，接收端用 `OnlineCodec(numChunks, 0.01, 3, 42)` 硬编码重建。
而发送端曾经允许通过 URL 参数改 ε 与 quality：

```
online eps=0.01 q=7 → 解出=true 数据一致=true
online eps=0.05 q=3 → 解出=true 数据一致=false   ← 静默损坏
online eps=0.05 q=7 → 解出=true 数据一致=false   ← 静默损坏
```

修复：`SetOnlineParams` 删除，ε/quality/seed 变成 `encode.go` 里的包级常量，
发送端与接收端工厂 `gofountainCodec()` 共用；发送页的 epsilon/quality 输入框移除。
新增 `TestOnlineParamsAreFixedByWireFormat`，直接比对编码端与解码端工厂对同一 blockCode
选出的下标集合，任何分歧都会立刻失败——**这类 bug 必须这样测，往返测试测不出来
（因为往返用的是同一个错误常量）**。

### 4.3 Color Matrix：默认 split 超过矩阵容量，帧渲染不出来

`split=2048` + 帧头 = 2063 B > 1587 B 容量 → `ColorMatrixEncode` 报错 → PNG 为 nil。
修复：`clampColorSplit()` 在 color 模式下把 split 钳到 `NumColorPayloadBytes()-48`（1539，
48 是帧头预留），会话记录实际值并在 `/encode` 响应里回传 `split`，UI 勾选该模式时自动收敛输入框。
回归测试 `TestColorModeClampsSplitToMatrixCapacity` 会真的发一次 color 请求并检查返回的 PNG。

---

## 5. 复现方法

```sh
# 1) 往返矩阵、丢帧、循环播放效率（含打印的实测表格）
cd txqr && go test -run 'TestCodecMatrix|TestRaptorQFrameCount' -v .
cd txqr && go test -run 'TestCodecCarouselEfficiency' -v .

# 2) 生成跨端 golden 向量（改了任一端编解码器后必须重跑并重新验证）
cd txqr && go run ./cmd/goldengen ../txqr-reader/android/app/src/test/resources/codecmatrix

# 3) Android 端解码验证
cd txqr-reader/android && ./gradlew :app:testDebugUnitTest --tests "*CodecMatrixCrossTest*"
```

> 本机 `gradlew :app:testDebugUnitTest` 起不来（Gradle worker 报
> `ClassNotFoundException: GradleWorkerMain`，仓库既有的 `DecimenDecoderTest` 同样失败，
> 属环境问题）。替代办法：直接用 Gradle 编译出的 class 在 JVM 上跑——
> `javac` 一个调用 `TxqrDecoder.decodeFrame` 的小 Runner，
> `java -cp <app/build/tmp/kotlin-classes/debug>:<kotlin-stdlib.jar> Runner <golden 目录>`。

## 6. 待办/未覆盖

* 本文所有吞吐都是**名义值**：模型假设「播出的每个码都能被读到」。
  真实摄像头下的每帧成功率（尤其 2×2 网格、Color Matrix 的 4 色判别）**必须实机测**，
  建议加一个「同一文件、同一设备、分别用三种模式各传一次，记录总耗时」的手测表。
* 经典模式没有端到端数据校验，只显示 MD5 供人工比对。§4.1 那类静默损坏只有靠
  跨端 golden 测试才能发现，建议后续在帧头或容器层加校验和（decimen 的 DCF2 已有 SHA-256）。
