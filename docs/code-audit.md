# QRT Pro 代码审计报告

范围：`txqr/`（Go 桌面端）与 `txqr-reader/android/`（Kotlin 接收端）。
目标：逻辑错误、边界条件遗漏、空指针风险、异常处理不完整、资源泄漏、协程/线程使用不当、线程安全。

本报告按「严重程度 → 位置 → 问题 → 处置」记录。第一至第三节共 33 项，全部为 ✅（已修复）；第四节的 3 项收尾工作同样已完成。

> **补记（编码方案与工作模式专项）**：针对五种喷泉码与两种工作模式做的专项验证又发现
> 3 个跨端/静默损坏缺陷，均已修复，详见 **`docs/mode-and-codec-analysis.md` §4**：
> - **Binary**：Go 端是「随机半数源块异或」，Kotlin 端却写成「按序重复源块」，
>   两端不一致 → Android 解码**报成功但数据错误**。已重写 Kotlin 侧并复用 `LubyDecoder`。
> - **Online**：ε/quality 不在帧头里，发送端改参数后接收端仍报完成但数据错误。
>   已把参数固化为包级常量并移除发送页输入框。
> - **Color Matrix**：默认 `split=2048` 超过矩阵容量 1587 B，每帧渲染失败、无任何输出。
>   已加 `clampColorSplit()` 并把实际值回传 UI。
>
> 另有一条重要事实：**桌面接收端不支持 Color Matrix 模式**（`qr.ColorMatrixDecode`
> 无调用方，桌面端只走 zxing QR），该模式只有 Android 端能收。

---

## 一、严重（会导致崩溃、死锁或明显错误行为）

### A1 ✅ GDI 对象生命周期错误：空心形状继承了别人的画刷

位置：`txqr/internal/w32/w32.go`（`DC.Select` / `RoundRect` / `Ellipse`）

两个缺陷叠加：

1. `RoundRect`/`Ellipse` 用 `Select()` 选中画笔后，在函数返回时 `defer Close()` 删除它——**但此时对象仍被选中在 DC 中**。GDI 禁止删除仍被选中的对象，`DeleteObject` 会失败并留下一个悬空的选中句柄。
2. `Select()` 里有一个「已见过就不重复选」的短路（`if _, seen := d.old[obj]; seen`）。`NULL_BRUSH`/`NULL_PEN` 是**进程级共享的常量句柄**，因此每个 DC 只会被真正选中一次，之后所有 `Select(NULL_BRUSH)` 全部空转。

后果：`NoFill`（空心）矩形根本没有切回空画刷，而是沿用了 DC 里残留的那个画刷。实测表现是**接收端「扫描区域」整块面板被涂成 `RGB(239,68,68)` 纯红**——那正是「失败帧」开关里 8px 红色状态圆点的画刷。同时每次绘制都泄漏一个 GDI 句柄，累积到 Windows 的 ~10000 上限后所有绘制静默失败。

处置：
- 新增 `newDC()`，创建时用 `GetCurrentObject` 记录 DC 自带的 `stockBrush`/`stockPen`；
- 新增 `resetObjects()`，每次形状绘制完成后把 DC 还原到这两个对象；
- `RoundRect`/`Ellipse` 改为「显式选中（含 NULL stock）→ 绘制 → `resetObjects()` → 删除自己的句柄」；
- 新增 `NewBrushOrNull` / `NewPenOrNull`，`Brush`/`Pen` 增加 `stock` 标记，`Close()` 对 stock 对象空操作。

### A2 ✅ `w32.RoundRect` 把「无填充」当成黑色

位置：`txqr/internal/w32/w32.go`

调用方用颜色 `0` 表示「不填充」，而 `0` 就是黑色，于是所有 `NoFill` 控件（ghost 按钮、非激活导航项、面板描边）都被涂成黑块。

处置：引入 `NoFill Color = 0xFF000000` 哨兵（COLORREF 只用低 24 位，该值不可能是真实颜色），命中时改用 `GetStockObject(NULL_BRUSH)` / `NULL_PEN`，并在注释里写明原因。

### A3 ✅ `sender.Stats()` 自死锁

位置：`txqr/internal/sender/sender.go`

`Stats()` 持锁后又调用 `Addr()`，`Addr()` 再次请求同一把 `sync.Mutex`。Go 的互斥锁**不可重入**，直接死锁（测试中表现为 60s 超时 panic）。

处置：拆出无锁版本 `addrLocked()`，`Stats()` 调用它；`Addr()` 改为 `defer` 解锁。

### A4 ✅ 除零 panic：`SolvedCount() * 100 / int(hdr.K)`

位置：`txqr/internal/scanner/decode.go`

`K` 来自网络/二维码帧，为 0 时直接 panic，整个接收进程退出。

处置：新增 `kDivisor(k uint16) int`，`K == 0` 时返回 1；补充 `TestProgressWithZeroK` 用例。

### A5 ✅ `locateFinderCenters` 数组越界

位置：`txqr-reader/.../ScannerViewModel.kt`

```kotlin
if (result.size >= 2 && result[1].second > result[2].second)
```

`size == 2` 时访问 `result[2]` → `IndexOutOfBoundsException`，从相机分析线程抛出导致崩溃。

处置：条件改为 `result.size >= 3`。

### A6 ✅ 关闭 scanner 时向已关闭的 channel 发送

位置：`txqr/internal/scanner/scanner.go`

`RemoveRegion`/`Clear`/`Stop` 与 worker 并发时，`decQueue`/`saveQueue` 可能已被关闭 → `send on closed channel` panic。

处置：队列由 `qMu sync.RWMutex` + `closed` 标志保护，发送前先取读锁并检查 `closed`。

### A7 ✅ 双缓冲 Blit 与 Restore 顺序颠倒：真实窗口一片空白

位置：`cmd/qrt-pro/app.go`（`paintMain`）与 `cmd/qrt-pro/overlay.go`（`paintOverlay`）

```go
mem.Restore()                             // 1. 把 DC 还原成创建时的 1×1 占位位图
dc.Blit(0, 0, rc.W(), rc.H(), mem, 0, 0)  // 2. BitBlt 复制的正是"当前选中的位图"
```

`Restore()` 的本意是在 `bmp.Close()` 之前取消选中（GDI 不允许删除仍被选中的对象），但它同时把 DC 还原为 `CreateCompatibleDC` 自带的 **1×1 单色占位位图**。`BitBlt` 复制的是 DC 当前选中的位图，于是窗口上贴的是那张 1×1 白位图 —— 表现出来就是一个空白（白色）窗口。

离屏 `-snapshot` 完全不受影响：`ReadBits` 走 `GetDIBits`，直接对 `bmp` 句柄取像素，不依赖 DC 的选中状态。**这正是"截图正确、真窗口空白"的原因**——本轮所有视觉验证都跑在快照路径上，所以直到真正启动才发现。

处置：`Blit` 与 `Restore` 交换顺序（`Restore` 仍排在 `defer bmp.Close()` 之前，defer 是 LIFO，所以从代码顺序看它在后、执行时仍先于 Close）。

验证手段：新增 `QRT_PAINT_CHECK=<path>` 环境变量，`WM_PAINT` 结束后用 `GetPixel` 采样窗口 DC 上的 5 个点并写入该文件（GUI 程序没有控制台，这是唯一客观手段）。对照实验：

| 顺序 | 采样结果 |
| --- | --- |
| `Restore → Blit`（错误） | 5 个点全为 `rgb(255,255,255)` |
| `Blit → Restore`（修复后） | `rgb(15,17,23)` 背景、`rgb(28,33,45)` 面板 |

同时给 `internal/w32` 的 `DC` 增加了 `Pixel()`（`CLR_INVALID` 视为失败）。该检查已写入 `.github/workflows/test.yml` 的 Windows job：若采样出现白色即判失败；若环境无交互式桌面（`WM_PAINT` 未触发、日志为空）则跳过而非误报。

### A8 ✅ 「停止服务」第二次以后完全失效（`sync.Once` 把停止做成了终身一次）

位置：`internal/sender/sender.go`（`Close`）+ `cmd/qrt-pro/app.go`（`toggleSender` / `stopSender`）

症状：点停止服务后，状态栏写「发送服务已停止」，但端口仍在监听、发送页面照常响应，看起来像「后台进程没退出」。

根因：

```go
type Server struct {
	stopOnce sync.Once   // ← 一个 Server 的生命周期里只会生效一次
	done     chan struct{}
}

func (s *Server) Close() error {
	s.stopOnce.Do(func() { /* 真正关闭 listener/srv */ })
	return err               // 第二次调用：直接返回 nil，什么都没做
}
```

`Start` 在停止后可以正常重启（`listener == nil`），于是 **启动 → 停止 → 启动 → 停止** 的第二次停止是空操作：`listener` 保持非 nil（`Stats().Listening` 仍为 true），端口继续服务，而 `Close()` 返回 nil 让调用方以为成功了。

连带缺陷：

1. `done` 在 `Start` 的 goroutine 里用 `defer close(s.done)`，而它是 `New()` 里建的单例 channel —— 第二次 `Serve` 返回时会对已关闭的 channel 再 `close`，直接 panic。
2. `cleanup()` 的退出条件是同一个 `done`，重启后它立刻返回，会话 GC 实际不再运行。
3. `toggleSender` 在 UI 线程上同步调用 `Start` / `Close`。实测本机绑定通配地址 `:9000` 在冷启动时可耗时 **3–30 秒**（网卡枚举 / 防火墙），期间整个消息循环被阻塞：界面不重绘、点击排队，按钮看起来完全没反应。

处置：

- 去掉 `stopOnce`，`Close()` 改为「锁内取出 srv/listener 并置 nil → 锁外关闭」，重复调用天然幂等；关闭后 `select` 等 `Serve` 返回（上限 3 s），保证重启时端口真的已释放。`srv.Shutdown` 的宽限期从 2 s 收紧到 1 s，因为它跑在按钮回调里。
- `done` 改为**每次 Start 新建**，由该次运行的 goroutine 关闭；`Done()` 加锁读取。未启动时 `done` 是已关闭的 channel，`select` 不会挂住。
- `cleanup(done)` 改为接收本次运行的 channel。
- `toggleSender` 把启动/停止放到后台 goroutine，`senderBusy` 标志丢弃重入点击，完成后 `PostMessage(wmAppRefresh)` 回 UI 线程重绘；状态栏先显示「发送服务处理中…」。
- `stopSender` 关闭后再查一次 `Stats().Listening`，仍在监听就报错而不是谎称已停止。

验证：新增 `internal/sender/lifecycle_test.go`（4 个用例，全部按 **OS 层能否 connect** 判定，不看服务自己的记账）：

| 用例 | 覆盖内容 |
| --- | --- |
| `TestStopAfterRestartReleasesPort` | 启动→停止→启动→停止 后端口必须关闭（修复前该用例失败：`listening=true`、HTTP 仍返回 200） |
| `TestRepeatedCyclesKeepWorking` | 连续 5 个启停周期都正常，无已关闭 channel 的 panic |
| `TestCloseWithOpenBrowserConnection` | 浏览器页面开着（keep-alive 连接）时停止必须立即成功，不等连接 |
| `TestDoneNeverBlocks` | `Done()` 在未启动/运行中/已停止三种状态都不挂住 |

真机验证（临时注入自检、跑完即删）：真实 exe 连点 3 轮「启动服务 / 停止服务」，每轮启动后 `listening=true`、端口可连，停止后 `listening=false`、端口拒绝连接；`toggleSender()` 本身耗时 0 ms（不再阻塞界面）。

---

## 二、中等（逻辑错误、数据竞争、资源泄漏）

### B1 ✅ 跨线程共享可变状态（Android）

位置：`ScannerViewModel.kt`

`onQrCodeScanned()` 运行在相机分析线程，`consumeBarcodes()`（ML Kit 回调）与 UI ticker 运行在主线程，但 `decodeInFlight`、`trackBox`、`streamMode` 都是普通字段：

- `decodeInFlight` 的读-改-写不是原子的，两个线程可同时判定为 false；
- `trackBox` 是 `Rect`，**可变对象**，即使引用是 volatile 也仍然存在数据竞争；
- `decodeColorMatrix()` 直接在分析线程里调用 `decoder.decodeFrame()` 并写 `_uiState`，与主线程 ticker 操作同一个 decoder 实例。

处置：`decodeInFlight` 改 `AtomicBoolean`；`streamMode`/`trackBox` 加 `@Volatile` 且读取时 `Rect(it)` 防御性拷贝；`decodeColorMatrix` 拆成「分析线程只做像素采样、返回 `ByteArray`」+「`viewModelScope.launch` 回主线程应用结果」。

### B2 ✅ `_uiState.value = value.copy(...)` 非原子读改写

位置：`ScannerViewModel.kt` 多处

先读 `value` 再赋值 `copy` 的结果，两次操作之间另一个协程的更新会被覆盖。

处置：统一改用 `_uiState.update { ... }`（单次原子 CAS 循环）。

### B3 ✅ UI ticker 在非扫描态空转

位置：`ScannerViewModel.kt`

`while (true) { refreshUiState(); delay(500) }`，非扫描态时 `refreshUiState` 早期返回但仍执行 `_uiState.value = 同值`。`MutableStateFlow` 每次赋值都会发射，于是**每 500ms 触发一次全量重组，直到进程结束**。

处置：非扫描态时 `uiTicker?.cancel()` 并置空，`startScan()` 重新起 ticker。

### B4 ✅ 相机与线程池泄漏（Android）

位置：`ui/ScannerScreen.kt`

`imageAnalysis.setAnalyzer(Executors.newSingleThreadExecutor())` 每次绑定都新建一个单线程池且**从不 shutdown**；`ProcessCameraProvider` 也从不 `unbindAll()`。反复进入扫描页会累积非守护线程并一直占用相机。

处置：用 `remember` 持有 executor，用 `DisposableEffect { onDispose { unbindAll(); shutdown() } }` 释放。

### B5 ✅ `cameraProviderFuture.get()` 未捕获异常

位置：`ui/ScannerScreen.kt`

`get()` 在 provider 初始化失败时抛 `ExecutionException`，listener 直接死掉，界面只剩黑屏且无任何提示。

处置：try/catch 并打日志后返回。

### B6 ✅ Bitmap 未回收

位置：`ScannerViewModel.kt`

`imageProxyToBitmap()` 每帧分配两张全分辨率 `ARGB_8888` 位图（1920×1080 约 8MB，旋转时甚至三张），裁剪图也未回收，全部依赖 GC——在高帧率扫描下会造成明显的内存压力与 GC 抖动。

处置：旋转用的中间位图用完即 `recycle()`；裁剪图在 `onComplete` 回收；用 `isRecycled` 保证重复释放安全（裁剪失败回退全帧的路径上有两个分支都可能释放它）。

### B7 ✅ `decodeInFlight` 可能永久卡住

位置：`ScannerViewModel.kt`

`barcodeScanner?.process(...)` 在 scanner 已关闭时为 `null`，整条 `?.addOnCompleteListener` 链被跳过，**`decodeInFlight` 永远保持 true，之后每一帧都被静默丢弃**。同理，`process()` 同步抛异常时也不会复位。

处置：scanner 为 null 时立即复位并 `close()`；所有 `process()` 调用包 try/catch，异常路径同样复位并回收位图。

### B8 ✅ 裁剪矩形越界

位置：`ScannerViewModel.kt`

`Bitmap.createBitmap(bitmap, l, t, w, h)` 在矩形越界或宽高 ≤ 0 时抛 `IllegalArgumentException`。

处置：新增 `safeCrop()`，先 `coerceIn` 到合法范围，再 try/catch 兜底返回 null。

### B9 ✅ 保存失败被当成成功

位置：`ScannerViewModel.kt` + `FileSaver.kt` + `MainActivity.kt`

`FileSaver.saveData()` 返回 `Unit` 且内部 catch 后只弹 Toast，调用方**无法区分成功与失败**；`saveDataToFile` 也没有异常处理，无论结果如何都会把状态置为 `IDLE` 并生成 `ScanResult`。而 `MainActivity` 一调用就把 `showResult = true`，保存失败时 `scanResult` 永远为 null，用户被卡在扫描页且看不到错误。

处置：
- `FileSaver.saveData()` 改为返回 `Boolean`，去掉吞异常的宽 catch 之外保留 Toast；
- `saveDataToFile` 用 `runCatching` 捕获，失败时写入 `error` 状态并回到 `COMPLETED`；空文件名直接拒绝；
- `MainActivity` 用 `LaunchedEffect(uiState.error)` 复位 `showResult`，并把 `!!` 换成安全取值。

### B10 ✅ 文件名未净化 / 路径穿越

位置：`FileSaver.kt`

`MediaStore.Downloads.DISPLAY_NAME` 与旧版 `File(downloadsDir, fileName)` 都直接使用用户输入，包含 `/`、`..` 或控制字符时行为不可控（旧版可写出 Downloads 目录之外）。

处置：新增 `sanitizeFileName()`（去控制字符与非法字符、去首尾点与空格、限长 120）；旧版分支增加 `canonicalPath` 前缀校验；`openOutputStream` 返回 null 时视为失败而非成功。

### B11 ✅ 接收端写入失败被静默吞掉

位置：`txqr/internal/scanner/scanner.go`

`writeToDisk` 的错误没有回传，`saver()` 仍然弹出「保存成功」。

处置：`SavedFile` 增加 `Errored`/`Err`，通过 `OnSaved` 回调上报，UI 在「已收文件」页以红色标出失败原因。

### B12 ✅ 同名文件被静默覆盖

位置：`txqr/internal/scanner/scanner.go`

连续接收同名文件会直接覆盖上一次的结果。

处置：新增 `uniquePath()`，同名追加 `(2)`、`(3)` 序号。

### B13 ✅ 解码 worker 重复保存

位置：`txqr/internal/scanner/scanner.go`

多个 worker 同时判定完成时，同一区域会被保存多次。

处置：`doneOnce.CompareAndSwap(0, 1)` 保证 exactly-once。

### B14 ✅ `SaveFails` 数据竞争

位置：`txqr/internal/scanner/decode.go`

UI 线程写、worker 线程读的 `bool` 字段。

处置：改为 `atomic.Int32`（`SaveFailFlag`）。

### B15 ✅ `time.Tick` 泄漏

位置：`txqr/internal/sender/sender.go`

`cleanup()` 使用 `time.Tick`，即使服务关闭定时器也不会被回收。

处置：改为 `time.NewTicker` + `done` channel，退出时 `Stop()`。

### B16 ✅ 上传限流与错误码混淆

位置：`txqr/internal/sender/sender.go`

- `handleMD5` 没有处理 `MaxBytesError`，超限请求返回 400 而不是 413；
- `handleFrame` 把「帧下标越界」和「渲染失败」都返回 500；旧版本越界时返回 **200 + 空 body**，播放器会一直卡在等待帧。

处置：抽出统一 `readBody(w, r)` 供两个 handler 共用；`handleFrame` 增加 `badIndex` 标志，越界返回 400，渲染失败返回 500。

### B17 ✅ `pushSpeedSample` 持有底层数组

位置：`txqr/internal/scanner/decode.go`

用 reslice 丢弃旧样本，底层数组不会被回收，长时间扫描会稳定占用一大块内存。

处置：改为 `copy` 左移。

### B18 ✅ HTTP 服务缺少超时与隔离

位置：`txqr/internal/sender/sender.go`

使用 `http.DefaultServeMux`（会被任意依赖包污染）、`ListenAndServe`（无超时，慢连接可耗尽资源）。

处置：私有 `http.ServeMux` + 显式 `http.Server`（`ReadHeaderTimeout`/`WriteTimeout`/`IdleTimeout`）+ `MaxBytesReader` 限流 + `stopOnce` 幂等关闭。

### B19 ✅ 进度值越界导致 Compose 崩溃

位置：`ui/ScannerScreen.kt`

`LinearProgressIndicator(progress = { progress / 100f })`，解码器在最后几帧可能短暂上报 >100，Compose 对超出 `0..1` 的 progress 抛异常。

处置：`progress.coerceIn(0, 100) / 100f`。

### B20 ✅ 列表面板无法滚动：超过三行直接看不到

位置：`cmd/qrt-pro/views.go`（`drawRegionList` / `drawFilesView`）

两个列表都用「画到超出面板就 `break`」的方式处理溢出：

```go
for i, s := range snaps {
	rowTop := top + int32(i)*pitch
	row := ...
	if row.B > inner.B {
		break // 第 4 行之后既不画，也没有任何滚动手段
	}
	a.drawRegionRow(row, i, s)
}
```

所以第 4 个及以后的扫描区域（以及较长的已收文件列表）被静默丢弃：计数显示「5 个」，列表里只能看到 3 个，
既不能滚轮也不能拖动，新加的区域落在末尾等于没有反馈。

处置：

- 新增 `cmd/qrt-pro/scroll.go`：`scrollList` 保存 offset / viewport / content，提供 `layout`（每帧重算并
  clamp，窗口变矮或删掉行后不会停在越界位置）、`wheel`、`beginDrag`/`dragTo`/`endDrag`、`scrollToEnd`。
- 列表改为按 `offset` 平移绘制，并用 `Ctx.PushClip(viewport)` 裁剪；完全在视口外的行直接 `continue`。
- 输入：新增 `WM_MOUSEWHEEL` 处理（`WM_MOUSEWHEEL` 报的是**屏幕坐标**，先 `ScreenToClient` 再判定在哪个列表上），
  滚轮一格 = 一个行距，`delta` 不足 120 的高精度滚轮/触控板也按一格处理（否则这类设备完全没反应）；
  在列表空白处按下拖动可滚动，按在控件上则控件优先。
- 新增右侧滚动条（滑块 + 轨道），只在内容超出时出现。
- 新增区域时 `scrollToEnd()`：新行在末尾，跳到底部才算给了反馈。

顺带修掉两个被这个功能暴露出来的底层问题：

- `RoundRectGradient` 用 `SelectClipRgn` 设置圆角裁剪，而它是**替换**语义，会把列表视口的裁剪整块顶掉——
  行卡片因此能画到面板外面。改为 `ExtSelectClipRgn(..., RGN_AND)` 求交，并用 `SaveDC/RestoreDC`
  保存/恢复，而不是 `SelectClipRgn(h, 0)` 硬清零。
- `internal/w32` 新增 `DC.PushClip/PopClip`（`SaveDC` + `IntersectClipRect` + `RestoreDC`）与
  `ScreenToClient`、`WheelDelta`；`uikit.Ctx.PushClip` 同时让**控件矩形中心落在裁剪区外时不注册**，
  否则滚出半个身位的行按钮还能从列表外面点到。

验证：`cmd/qrt-pro/scroll_test.go` 6 个用例（clamp、短内容不滚、滚轮方向与高精度增量、拖动、
内容缩短后重新 clamp、pinToEnd）；真机自检驱动真实 handler：滚轮下三格 → 停在 271（max），
上一格 → 135（正好一个行距），拖动上移 120 → 120，删掉一个区域后 → 自动 clamp 到 135。
自检还抓到我写反的 `regionRowMetrics()` 返回值顺序（`_, pitch :=` 拿到的其实是行高，每格少滚 8px）。

### B21 ✅ 文件名帧被当成一个数据块计数

位置：`internal/scanner/decode.go`（`feedTxqrLocked`）

发送端会先发一帧元数据 `-1/{chunk}/{total}/{codec}/name|{文件名}`。旧代码只判断
`len(parts) >= 3` 且数值可解析，于是 blockCode=-1 的元数据帧也被算进 `txqrSeen`/`txqrGot`：
进度条提前一个块、`已收 N/M 块` 多算 1，且文件名根本没被记录（桌面端 txqr 传输的
`fileName` 恒为空）。加诊断日志时顺带发现。

处置：`code < 0` 且 header 以 `/name` 结尾时按元数据帧处理——写入 `fileName`、记一条
`frame.name` 日志、直接返回，不计入进度。`TestNameFrameIsMetadataNotProgress` 覆盖。

---

## 三、结构性整改（需求 1：整合到一个启动器）

### C1 ✅ 三个 exe 合并为一个进程

原先：`序列化工具.exe`（sender）+ `qrt-pro-receiver-desktop-*.exe`（receiver）+ `qrt-pro-launcher.exe`（外进程管理）。三者只通过进程名互相识别，命名不一致类 bug 因此而来。

现在：`cmd/qrt-pro` 单进程同时托管 sender（`internal/sender`）与 receiver（`internal/scanner`）：

- 停止扫描从「遍历进程树」退化为一次方法调用；
- 只有一个消息循环需要保活；
- 发送端页面、接收端页面、已收文件页面在同一窗口内用左侧导航栏切换。

同时把可复用逻辑抽成独立包，避免新旧两处实现各自演化：

| 包 | 职责 |
| --- | --- |
| `internal/w32` | Win32/GDI 底层（资源所有权、DPI、COLORREF、UTF-16 计数） |
| `internal/sender` | 可嵌入的发送端 HTTP 服务（`go:embed` 页面） |
| `internal/scanner` | 接收端管线（区域管理、解码 worker 池、落盘） |
| `internal/uikit` | 立即模式绘制层（主题、按钮、卡片、进度条、预览） |

`cmd/txqr-web` 保留为无界面的命令行发送端，仅剩 62 行薄封装。

### C2 ✅ 离屏渲染快照能力

`cmd/qrt-pro -snapshot <path> -tab <0|1|2> [-demo] [-w] [-h]`：走与 `WM_PAINT` 完全相同的 `drawInto()` 代码路径，自建位图 + `GetDIBits` 回读 + PNG 导出。锁屏/无桌面环境下也能出图，用于文档截图与布局回归检查。

### C3 ✅ 绘制调用跟踪

`internal/w32` 支持 `QRT_DRAW_LOG=<path>`，把每次 `RoundRect`/`Ellipse`/`VerticalGradient` 的矩形与颜色写成一行日志。GUI 程序无法挂控制台，这是定位「这块为什么是红色」最快的手段（A1 就是靠它定位的）。

---

## 四、收尾项

### D1 ✅ 删除已被取代的旧实现副本

`cmd/txqr-desktop`（`main.go` 821 行 + `decode.go` 347 行 + `win32.go` 365 行）与 `cmd/txqr-launcher`（741 行）合计约 2200 行，功能已被 `internal/scanner` + `internal/w32` + `cmd/qrt-pro` 取代，而两份副本里仍保留着 A4/A6/B11/B12/B14/B15 等项的原始缺陷（本轮只修了抽取出来的新包）。

处置：两个目录连同其测试一并 `git rm`（仍在 Git 历史中，可 `git checkout <旧提交> -- txqr/cmd/txqr-desktop` 取回）。

仍未处理：`dist/` 下 `qrt-pro-receiver-desktop-*.exe`、`qrt-pro-launcher.exe`、`序列化工具.exe` 三个旧产物。`dist/` 被 `.gitignore` 忽略、不在版本控制内，删除后不可恢复，因此留给使用者自行清理；README 已注明它们可以安全删除。

### D2 ✅ 导航副标题文案

`cmd/qrt-pro/app.go` 侧栏副标题原为 `"光佃轮·一步到位"`，`光佃轮` 不成词。处置：改为 `"收发一体·一步到位"`，与「发送端 + 接收端合并为一个进程」这一改动相符。若原文案另有含义，改回来只需一行。

### D3 ✅ CI 增加 `-race` job

`CGO_ENABLED=0` 时 `go test -race` 无法运行，本机也没有可用的 C 工具链（只有 clang，缺 Windows SDK 头文件）。

处置：新增 `.github/workflows/test.yml`，两个 job：

- `windows`：`windows-latest`，`CGO_ENABLED=0`，跑 `gofmt -l` 校验 + `go build ./...` + `go vet ./...` + `go test ./...`（全模块）；
- `race`：`ubuntu-latest`，`CGO_ENABLED=1`，跑 `go test -race ./internal/sender/... ./decimen/... ./qr/...`。

`internal/w32`、`internal/scanner`、`internal/uikit`、`cmd/qrt-pro` 通过 `golang.org/x/sys/windows` 只能在 Windows 下编译，因此 `-race` 覆盖的是与平台无关的并发逻辑（sender 会话与清理协程、decimen 喷泉码、qr 编解码）。若要让 scanner 也进 race，需在 Windows runner 上装 MinGW 并开 CGO，成本较高，暂未做。

---

## 五、验证

| 检查项 | 结果 |
| --- | --- |
| `go build ./...` | ✅ |
| `go vet ./...` | ✅ |
| `go test ./...`（含 sender/scanner 新增用例） | ✅ 全部通过 |
| `./gradlew :app:compileDebugKotlin --offline` | ✅ |
| `./gradlew :app:assembleDebug --offline` | ✅ BUILD SUCCESSFUL |
| 三张页面离屏快照配色复核 | ✅ 大面积纯红/纯黑已消除 |
| 真实窗口 DC 采样（`QRT_PAINT_CHECK`） | ✅ 修复前全 `rgb(255,255,255)`，修复后为主题色 |
| 发送服务启停（`internal/sender/lifecycle_test.go`） | ✅ 5 个启停周期端口都真正释放（A8） |
| 列表滚动（`cmd/qrt-pro/scroll_test.go`） | ✅ 6 个用例 + 真机 handler 自检（B20） |
| 诊断日志（`internal/scanner/audit_test.go`） | ✅ 采样/轮转/环形缓冲 + 会话切换/停滞/文件名帧 |
| 真机捕获日志 | ✅ 空白区域 2 秒 → 1 条 `qr.notfound`（折叠 60+ 次），含 gozxing 原始错误 |
