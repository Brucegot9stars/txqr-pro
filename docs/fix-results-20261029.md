# 按严重程度修复记录（更新至 2026-10-30）

本轮按此前审查编号处理数据正确性、核心功能、生命周期与资源问题。用户已授权修复，并确认只要求更新后的两端互通；没有升级依赖、修改发布配置、出包、提交、推送或发布。协议及新增运行上限见 [QRT2 协议说明](protocol-v2.md)。

2026-10-30 已补齐上一轮未完成的 F18 进程取消/内存约束、Android 主线程解码，以及浏览器加载/呈现调度。F01–F24 均已有对应代码修改；下面区分已执行的回归与仍需设备验证的行为，不把真机未验证项目宣称为已验收。

## 修复范围

| 审查编号 | 优先级 | 本轮处理 | 证据与限制 |
|---|---|---|---|
| F01、F03 | P0 | QRT2 会话身份、全帧 CRC、最终长度及 SHA-256；拒绝旧经典帧及跨会话方程 | Go/Kotlin 混流、损坏重试、有效 CRC 但错误方程回归；成功输出必须与源一致 |
| F02 | P0 | Online CDF 分母在乘法前转 Double，保持 Go 运算顺序 | K=1536、id2748 优先到达完整文件反例；K=8192 索引指纹；CDF 单调性 |
| F04 | P1 | RaptorQ 仅在实际求解成功后完成，缓存恢复结果 | ID 序列 10,5,13,3,11,1,15,9 不误完成，再加 8 恢复；Go/Kotlin 双端 |
| F05 | P1 | 保存返回 false 正确传播为失败，成功前不产生 ScanResult | Android 源码已修；需设备端模拟保存失败、拒绝权限及重试 |
| F06 | P1 | `total==split` 进入单块 LT 路径，保留 Raptor 原有回退机制 | Go 全 codec 单块测试及 Go→Kotlin 单块向量 |
| F07 | P1 | Color affine 改为 module→pixel | 纯函数比例/平移及退化三点测试；Go PNG→Kotlin 恢复；真实相机待验证 |
| F08 | P1 | 两端严格参数、块长度及帧数量上限；decimen 总量与分片关系验证 | 畸形参数、零分片、短符号、超限及 parser fuzz；追加 Go 建矩阵前预算、Android 行/修复符号/解压预算测试；最大参数设备内存仍未实测 |
| F09 | P1 | 截断过长扩展名后再截断主文件名 | 长 Unicode 扩展名不 panic，已有文件名测试通过 |
| F10 | P1 | GDI 截图在 GetDIBits 和释放 bitmap 前恢复选择；检查 BitBlt 和完整行数 | 已修源码；本轮不做 Windows 构建/设备验证，句柄稳定性待验证 |
| F11 | P1 | crop/full fallback 最后一个 ML Kit task 才释放完整 bitmap 和 in-flight 标志 | 源码生命周期修复；需 Android 延迟 task、后台/重扫实测 |
| F12、F13 | P2 | Windows 在提交处锁定 generation；Android 回调及颜色任务核对 generation；Snapshot/SaveDir 锁一致；采集 goroutine 纳入关闭等待 | Go stale commit / Close 等待及 race 通过；Android 改为单后台处理器、最新一批待处理帧、会话隔离和不可变快照，JVM 验证 UI 执行器不阻塞/旧任务不发布；ML Kit 原生 task 待设备验证 |
| F14、F21 | P2 | decimen 整个容器 FNV + 文件 SHA；失败进入终止状态，重扫清空，避免重复组装与解压 | Go/Kotlin 元数据损坏、失败后忽略新帧、干净重扫成功 |
| F15 | P2 | 每次有效取帧续租，按最后访问而非创建时间过期 | 活跃取帧刷新过期时间的单元测试；未实际等待 30 分钟 |
| F16 | P2 | 所有帧选最大 QR version 后固定渲染，包含元数据和 ID 位数变化 | 多个帧尺寸一致，PNG 再识别与完整帧相同 |
| F17 | P2 | 单 RAF 调度，图片解码后呈现，每格单帧预取，慢加载保留旧帧；取消释放请求/URL，generation 拒绝迟到结果 | Node 覆盖慢加载、解码等待、最短显示时长、交错公平性、重试/取消；真实 Chrome 验证解码后插入、慢加载、暂停、失败保留及 URL 清理；光学效果未实测 |
| F18 | P2 | 编码单并发、会话/缓存/worker 数上限；编码与 PNG 生成移入同一可执行文件的受限子进程，5 分钟任务期限、30 秒上传读取期限 | Linux 下真实 worker 三模式互通、库调用中取消、低内存失败/主进程存活及父进程退出清理通过；Windows Job 代码已实现，需 Windows 设备验证；进程限制不等同于整个应用 RSS 上限 |
| F19 | P2 | Decode benchmark 每轮新建 decoder 并核对恢复结果；基准输入使用实际字节数并 SetBytes；完成平均速度用已验证原始文件字节数 | 单次 Go benchmark 冒烟通过；浏览器改为实际呈现帧率；Kotlin 去除索引/字节装箱并通过随机满秩系统及共享向量，分配对照见下；旧错误基准不能直接比较提速比 |
| F20 | P2 | 网页带入文件名，Classic encoder 使用 SetFileName 生成关联元数据 | 名称与有效 codec / session 一致；未拍到元数据仍使用生成名称 |
| F22、F23 | P2 | Android 旧版保存权限、MediaStore pending/失败删除、IO dispatcher、临时文件及重名不覆盖；Windows 临时文件写完同步后独占发布 | Go 文件不覆盖、临时文件清理及端到端保存测试；Android MediaStore/外置存储及 Windows MoveFile 待平台验证 |
| F24 | P3 | Color 测试改用仓库 fixture，容量断言改为实际 1587 | 空临时目录不再缺 fixture；JUnit 断言实际执行 |

## 已执行验证

- Go：`go test -mod=readonly -race -short . ./decimen ./qr ./internal/scanner ./internal/sender -timeout 300s`。核心库、decimen、QR、扫描管线及发送服务五个包通过。扫描管线将 Win32 捕获边界按平台拆出，Linux 测试使用捕获替身，未构建 Windows 应用。
- JVM：直接用 Kotlin 编译器运行纯 decoder JUnit，37 项通过，包含五 codec Go→Kotlin 向量、Online 原始反例、RaptorQ 欠秩、CRC/SHA、会话隔离、decimen 失败重扫及 Color fixture、后台队列/线程/关闭，以及资源预算失败与重扫恢复。
- Go parser fuzz：10 秒配置，实际约 11 秒，92,787 次执行，无失败。只证明本次样本未发现 parser 崩溃，不代表全部解码状态已穷尽。
- `BenchmarkTXQRDecode`：每个参数 1 次的冒烟测试通过，验证新基准真的创建并恢复文件；没有据此宣称端到端性能提升。
- Node：播放调度、慢加载、解码等待、呈现时长、过期回调、重试及资源释放测试与脚本语法检查通过。运行 `node txqr/internal/sender/testdata/playback.test.cjs`。真实无界面 Chrome 150 回归通过，运行 `CHROME=/path/to/chrome node txqr/internal/sender/testdata/browser.test.cjs`；测试使用 DevTools 管道和模拟慢请求，不访问外网。
- `git diff --check` 通过。Go 工具链 1.25.12、Kotlin 编译器 2.1.0；本机 Linux amd64，AMD Ryzen 5 5600U。测试工具及依赖缓存位于 `/tmp/txqr-review-20261029`。

## 分配优化证据

在 Linux JVM、`-Xms256m -Xmx512m` 下，用同一套当前解码器与输入，仅替换修复前后的 SparseMatrix，预热 2 次、测量 5 次。每轮校对恢复字节，分配量由 JDK ThreadMXBean 记录。旧矩阵仅补了相同的构造器签名以便链接，保留原有列表实现。测试源码及日志在 `/tmp/txqr-review-20261029/AllocationProbe.kt`、`matrix-allocation-before.log`、`matrix-allocation-after.log`。

| 场景 | 修改前每轮分配 | 修改后每轮分配 | 观测减少 |
|---|---:|---:|---:|
| 1 MiB 文件重构，K=512 | 24,847,320 B | 1,048,624 B | 95.8% |
| Go→Kotlin Online，K=1536 | 247,425,000 B | 90,754,241 B | 63.3% |

最终数据见同目录日志；以上是 JVM 累计分配，不是 Android 堆峰值，也不是端到端吞吐量。基础数组取代 MutableList<Int>/MutableList<Byte>，GF(2) 行操作、随机数和符号组合保持一致。随机可逆矩阵、半等长分区/零填充及五 codec 共享向量均通过。

## 验收边界

已知 F01–F24 的代码措施已全部写入，当前环境可执行的逻辑、并发、资源限制及浏览器回归已完成。没有升级依赖或构建配置，没有 APK/Windows 产品出包，也没有提交、推送或发布。

Windows GDI/Job Object/文件系统，以及 Android CameraX/ML Kit/权限/MediaStore 的真机验证仍未执行。对应“已修”表示实现已写入，不表示平台验收完成。还需在两端更新后验证长时间截图句柄数、延迟 crop/full task、后台/重扫、拒绝权限、满盘及保存失败重试。真实相机丢帧率与完整光学 Goodput 仍需设备测量；本轮不宣称任何端到端提速倍数。

公开 Go Encoder.EncodeContext 本身仍是协作取消；产品 HTTP 发送路径通过可终止 worker 解决不可中断库调用问题。各接收内存预算是工作数据保护，不能替代全应用内存监控。保守预算可能拒绝协议参数合法但资源开销过高的传输，界面会报告失败；它不会绕过校验、静默改变文件或替换编码算法。

测试日志：`/tmp/txqr-review-20261029/fix-race.log`、`remaining-kotlin-tests.log`、`fix-fuzz.log`、`fix-benchmark.log`。此前完整审查报告仍在同目录 `txqr-pro-review.md`，其行号对应修复前基线。
