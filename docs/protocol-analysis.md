# decimen 协议逐字节分析（开发需求文档）

> 摘要：仿 decimen：20字节帧头、systematic-carousel喷泉码、DCF2容器、标准QR网格、高速流水线。

## 1. 目标与范围

在本项目（Go 发送端 txqr-web + Android 接收端）中新增 `decimen` 模式，复刻
[decimen-optical-transfer](https://github.com/bashalarmistalt/decimen-optical-transfer)
的功能与线格式。要点：

- 发送端以**标准 QR 网格**（1/2/4 格）同时发射**多个相互独立的喷泉帧**，每帧为
  V40-L（2953 字节）满容量 QR，60 fps 错峰翻转。
- 采用 **systematic-carousel 喷泉码**（v2）：前 K 帧直发原始块（零喷泉开销），
  后 K 帧为度 4–24 的 repair 帧，循环往复；接收端集齐约 K·1.04 个不同 seq 即可解出。
- 无握手、可中途锁定、丢帧只损失时间不损失正确性。
- 文件容器 DCF2：文件名/类型/SHA-256/可选 gzip，接收端解包并校验。

本文档为 Go 与 Kotlin 双侧的**逐字节（bit-by-bit）实现规格**，双方必须字节一致。

---

## 2. 线格式总览

```
屏幕/相机 光信道
   │ 每码一个 QR（V40-L, ~2953 字节）→ 解码得到一帧
   │
   ▼
QR 负载 = 20字节帧头 + blockLen 字节喷泉块        （≤2953）
喷泉块内容 = 若干源块的 XOR（由 seq 确定性决定）
喷泉恢复出的整段 = DCF2 文件容器（49字节头 + 可选gzip + 数据）
```

发送端无限循环播放；接收端对每个成功解码的码调用帧解析 → 去重 → 喷泉解码 →
满 K 块后组装 → 校验 FNV → 解包容器 → 校验 SHA-256。

---

## 3. 帧头（20 字节，little-endian）

每个 QR 解码负载的前 20 字节，其余为 `blockLen` 字节块体。

| 偏移 | 长度 | 类型 | 字段 | 说明 |
|----|----|----|----|----|
| 0 | 1 | u8 | magic0 | 固定 `0xD1` |
| 1 | 1 | u8 | magic1 | 固定 `0x0D`（v2 格式标识） |
| 2 | 2 | u16 LE | sessionId | 每次发送端启动随机（1..0xFFFF） |
| 4 | 4 | u32 LE | seq | 驱动喷泉 PRNG 的序列号 |
| 8 | 2 | u16 LE | k | 源块数 |
| 10 | 2 | u16 LE | blockLen | 块体字节数 |
| 12 | 4 | u32 LE | totalLen | 容器总长（解出的文件容器字节数） |
| 16 | 4 | u32 LE | payloadFnv | 容器整体 FNV-1a，完成后校验 |

**黄金向量**（与参考一致，`parseFrame`/`packFrame` 必须复现）：

```
输入: sessionId=0xbeef seq=0x01020304 k=0x0111 blockLen=6
      totalLen=0x00fedcba payloadFnv=0x89abcdef  block=[1,2,3,4,5,6]
输出: d1 0d ef be 04 03 02 01 11 01 06 00 ba dc fe 00 ef cd ab 89
      01 02 03 04 05 06
```

**帧合法判定**（`parseFrame` 返回 nil 即丢弃）：
- `bytes.length <= 20` 丢弃；
- `bytes[0]!=0xD1 || bytes[1]!=0x0D` 丢弃（识别外来源）；
- `k==0 || blockLen==0 || totalLen==0` 丢弃；
- `bytes.length != 20+blockLen` 丢弃（截断/多块）。

**流身份 `streamIdentity`**：`sessionId:k:blockLen:totalLen:payloadFnv` 字符串，
任何字段变化（seq 除外）都要求接收端重置解码器。防 `k/blockLen` 连字符歧义用冒号分隔。

---

## 4. FNV-1a（32 位）

标准 FNV-1a，用于 `payloadFnv` 与测试指纹：

```
h = 0x811c9dc5
for each byte b: h ^= b; h = (h * 0x01000193) mod 2^32
返回 h
```

**确定性要求**：Go 用 `uint32` 自然回绕；Kotlin 用 `Int` + `toUInt()` 处理，乘法用
`Math.multiplyHigh` 或 `(h.toLong()*0x01000193L).toInt()`。

---

## 5. 喷泉码：systematic-carousel（v2）

### 5.1 splitmix32（确定性 PRNG）

```
state = seed | 0  (int32)
next():
  state = (state + 0x9e3779b9) & 0xFFFFFFFF   // 回绕
  t = state
  t = t ^ (t >>> 16)
  t = t * 0x21f0aaad & 0xFFFFFFFF
  t = t ^ (t >>> 15)
  t = t * 0x735a2d97 & 0xFFFFFFFF
  t = t ^ (t >>> 15)
  return t  // 无符号 32 位
```

Go：全程 `uint32`（`*` 自然回绕，`>>>` 即 `>>`）；Kotlin：`Long` 掩码或 `Int`+`toUInt`。

### 5.2 frameSeed(sessionId, seq) → 32 位种子

```
h = ((sessionId + 1) * 0x9e3779b1) ^ ((seq + 0x85ebca6b))  // 均按 32 位回绕
h = h ^ (h >>> 13)
h = h * 0xc2b2ae35 & 0xFFFFFFFF
h = h ^ (h >>> 16)
return h
```

注意 JS 参考中 `seq + 0x85ebca6b` 可能超过 32 位，异或前按 ToInt32 截断——Go/Kotlin
用 32 位算术回绕即等价。**会话不同则同一 seq 产生不同子集**（防跨会话混流）。

### 5.3 frameComposition(k, sessionId, seq) → 块索引列表

```
cycle = 2 * k
pos = seq % cycle
if pos < k: return [pos]                    // 系统直发（sweep）
else:       return repairIndices(...)       // repair 帧
```

`sweep` 在任何 cycle 都重复（`seq + m*cycle` 仍落在 `[pos]`）。

### 5.4 repairIndices(k, sessionId, seq) → 度 d∈[4,24] 随机子集

```
rnd = splitmix32(frameSeed(sessionId, seq))
d   = min(k, 4 + rnd() % (24-4+1))          // 度 4..24（k<4 时 d=k）
set = {}  （保持插入顺序）
while set.size < d: set.add(rnd() % k)
return set 按插入顺序转数组
```

`rnd() % k` 中的 rnd 为无符号 32 位。**集合顺序即 XOR 顺序**，双方必须一致
（Go：slice 去重保序；Kotlin：LinkedHashSet）。

### 5.5 编码 LTEncoder

- `k = max(1, ceil(payloadLen / blockLen))`
- `words = ceil(blockLen / 4)`
- `blocks = Uint32Array(k * words)`，把 payload 逐块拷入（尾块零填充）
- `encode(seq)`：取 `idx = frameComposition(...)`，对每个源块索引 b，将 `blocks[b*words..]`
  逐词 XOR 到输出；返回前 `blockLen` 字节（不足 4 字节的尾词仅截断）。

### 5.6 解码 LTDecoder（Kotlin 接收端）

状态：`solved[k]`（Uint32Array|null）、`byBlock: Map<块, Set<挂起帧>>`、
`seen: Set<seq>`；计数 `framesNew/framesDup/framesRedundant/solvedCount`。

`addFrame(seq, block)`：
1. `seen` 已含该 seq → `framesDup++` 返回（重复读取同一屏不算新帧）。
2. 插入 `seen`，`framesNew++`；`isComplete` 则不再累积。
3. 用与编码器相同的 `frameComposition` 求块集；将块体拷入 `words` 字数组。
4. 对块集里已解出的块：`words ^= solved[b]` 并从集合移除（归零）。
5. 归零后若集合空 → `framesRedundant++` 返回（对该接收端无新信息）。
6. 若集合只剩 1 块 → `resolve` 该块。
7. 否则挂起：`byBlock[b]` 追加此帧。

`resolve(b, w)`（剥皮级联）：
```
queue = [(b, w)]
while queue:
  (b, w) = pop; 若 solved[b] 已存在 continue
  solved[b] = w; solvedCount++
  for 挂起帧 pf in byBlock[b]:
     pf.words ^= w; 从 pf 块集移除 b
     若 pf 块集只剩 1 块 r 且 r 未解: 从 byBlock[r] 摘除 pf 并入队 (r, pf.words)
  byBlock.delete(b)
```

`assemble()`：`isComplete` 时按块序拼回 `totalLen` 字节（尾块截断）。

**进度语义**：UI 进度用 `framesNew - framesRedundant`（信息帧），不用 solvedCount
（剥皮后段突发）。

### 5.7 黄金向量（编码流指纹）

`testPayload(len)`：`b[i] = (i*37 + (i>>8)*11) & 0xFF`。
对 `LTEncoder(testPayload(k*blockLen - 7), blockLen, sessionId)` 把 `encode(0..63)`
首尾相接成 `64*blockLen` 字节流，取 FNV-1a。Go 编码器必须复现：

| k | blockLen | sessionId | 期望 |
|----|----|----|----|
| 1 | 64 | 1 | `0xf6a115c5` |
| 23 | 64 | 7 | `0x4a5d3eaa` |
| 179 | 2933 | 4242 | `0x54f78d05` |
| 716 | 1445 | 65535 | `0x75b73b85` |

**行为约定**（Go 往返测试/Kotlin 单测都要覆盖）：
- 完整 sweep 恰好完成，零开销（`framesNew == k`）。
- 丢 30% 帧仍完成，`wallClock < 2.8`、`overhead < 1.8`。
- 乱序、重复帧、中途接入均正确。
- k=1 首帧即完成；未完成时 `assemble()` 返回 null。
- 同一块在下一 cycle 重复 sweep 计 `framesRedundant`，不算进度。

---

## 6. DCF2 文件容器（49 字节头 + 数据）

容器是喷泉**载荷整体**（即被切块的原始字节流），接收端解出全部 K 块后得到。

| 偏移 | 长度 | 类型 | 字段 | 说明 |
|----|----|----|----|----|
| 0 | 4 | bytes | magic | `"DCF2"` |
| 4 | 1 | u8 | compression | 0=none, 1=gzip |
| 5 | 2 | u16 LE | nameLen | 文件名 UTF-8 字节数 |
| 7 | 2 | u16 LE | typeLen | MIME 类型字节数 |
| 9 | 4 | u32 LE | fileLength | 解压后原始字节数 |
| 13 | 4 | u32 LE | transmittedLength | 容器内数据段长度 |
| 17 | 32 | bytes | sha256 | 原始文件字节的 SHA-256 |
| 49 | nameLen | bytes | name | 文件名 |
| 49+nameLen | typeLen | bytes | type | MIME |
| 49+nameLen+typeLen | transmittedLength | bytes | data | 原始或 gzip |

**打包规则**：
- 文件名必须安全化（去路径、去控制字符；空则 `transfer.bin`）。
- gzip 仅当文件 ≥768 字节且 `transmitted+64 < original` 时采用；已压缩类型
  （jpeg/png/mp4/zip/…）直接跳过（见 `isPrecompressedType` 白名单/黑名单）。
- 传输长度 = data 段长。

**解包规则**（接收端）：
- `container.length < 49` 或 magic 不符 → 拒绝。
- `fileLength==0 || fileLength>64MB || transmittedLength==0 ||
  dataOffset+transmittedLength != container.length` → 拒绝。
- gzip：data 段需 ≥18 字节且尾部 4 字节 gzip ISIZE == fileLength，解压后长度必须等于
  fileLength（解压有上限，防解压炸弹）。
- 文件名仍要二次安全化（信任来自光信道的内容）。
- 最后用容器内 SHA-256 校验解出字节。

---

## 7. 发送端参数与网格

| 参数 | 默认 | 可选 | 说明 |
|----|----|----|----|
| frameBytes | 2953 | 500/1000/1465/1850/2331/2953 | 对应 QR V40-L 上限 |
| blockLen | 2933 | frameBytes-20 | |
| txFps | 60 | 10/15/20/24/30/55/60 | ≥2 次刷新/帧较稳 |
| 网格 | 1 | 1/2/4（2×2） | 每格独立 seq 流 |
| ECC | L | 固定 | 喷泉管擦除，ECC 管损坏 |
| mask | 4 | 固定 | 跳过 8 路掩码评估 ~4× 提速 |

**网格语义**：每格是一个**独立**喷泉帧（不同 seq），不是同一流的不同偏移。
格 j 在 `t` 时刻翻转下一帧；各格翻转相位错开 `interval/N`，使曝光跨越翻转时最多
一个格处于过渡中。发送端帧序号连续分配（每格轮流取下一个 seq）。

---

## 8. 接收端解码流水线（Android）

- 相机 1280 宽、请求 60fps（`exact` 优先，回退 `ideal`）。
- 每帧对所有检测到的码逐个：`parseFrame` → `streamIdentity` 切换解码器 → 去重 →
  `addFrame` → 完成即组装校验。
- 位置跟踪（可选增强）：对已解码码缓存 quad+模块数，下一帧直接裁剪该区域小图解码
  而非全帧扫描（对应参考 tracked 快路径），多个区域并行喂 worker 池。
- 池满丢帧（喷泉兜底），不排队。

---

## 9. 互操作与兼容性

- **双方字节一致性由黄金向量保证**：帧头字节向量、编码流 FNV 指纹（§5.7）、
  repairIndices 子集，Go 与 Kotlin 各自持有同一组常数。
- 帧头 magic `0xD1 0x0D` 与 v1 的 `0x0C` 不同：旧实现会干净地拒绝 v2 流。
- `splitmix32/frameSeed` 只用整数运算，无浮点，跨引擎/跨语言无 Math.log 之类的偏差。
