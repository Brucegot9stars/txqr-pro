# 目录清理分析报告

> 分析对象：`D:/Workspace/AiDev/008-qrt-pro-opti`
> 分析日期：2026-10-26
> 验证方式：真实裁剪副本 + 全量构建（Go 三目标 + Android APK）+ 测试套件 + 体积/依赖闭包比对
> **状态：已执行完毕**（见文末 §11 执行记录）

---

## 1. 结论速览

| 分类 | 内容 | 体积影响 |
|---|---|---|
| **必须保留** | `txqr/`（裁剪后）、`txqr-reader/android/`、`docs/`、`.github/`、根文件 | — |
| **强烈建议移除** | `reference/`、`txqr-reader/` 的 iOS 三件套 | **≈19.3 MB** |
| **可选移除** | `txqr/cmd/txqr-{tester,ascii,gif}`、`txqr/mobile/`、`txqr/docs/` | **≈16.5 MB** |
| **非源码（可随时重建）** | `txqr-reader/android/{.gradle,.kotlin,build}`、`dist/` | **≈457 MB / 46 MB** |

> 裁剪后仓库（含 `.git`）约 **537 MB → 354 MB**；源码部分（不含 `.git` 与构建缓存）约 **260 MB → 约 4 MB**。

---

## 2. dist 产物 ↔ 源码映射

`dist/` 中 5 个文件，逐一追溯到编译入口：

| dist 文件 | 体积 | 编译入口 | 源码路径 |
|---|---|---|---|
| `序列化工具.exe` | 6.8 MB | `txqr/cmd/txqr-web` | `main.go` + 内嵌 `index.html` |
| `qrt-pro-receiver-desktop-windows-amd64-v1.3.0.exe` | 3.8 MB | `txqr/cmd/txqr-desktop` | `main.go` `decode.go` `win32.go` |
| `qrt-pro-launcher.exe` | 2.4 MB | `txqr/cmd/txqr-launcher` | `main.go` |
| `qrt-pro-reader-v1.3.0.apk` | 34 MB | `txqr-reader/android` | Gradle `:app:assembleRelease` |
| `txqr-web.log` | 55 B | — | 运行时日志，非编译产物（已 gitignore） |

**验证**：用 `-trimpath -ldflags "-s -w -H windowsgui"` 在裁剪后的副本中重新编译，产物体积与 `dist/` 中的 exe **逐字节对齐**：

```
6770688  序列化工具.exe          ← 裁剪后重建同为 6770688
3827712  qrt-pro-receiver-...exe ← 裁剪后重建同为 3827712
2411520  qrt-pro-launcher.exe    ← 裁剪后重建同为 2411520
```

---

## 3. 必须保留（编译 dist 必需）

### 3.1 Go 发送端 / 接收端 / 启动器 — `txqr/`

编译闭包（由 `go list -deps` 实测得出）：

```
txqr/                        根包：codec.go decode.go encode.go fountain.go doc.go
├── decimen/                 decimen 协议（发送端 + 桌面接收端共用）
├── qr/                      qr.go color.go  + testdata/helloworld_qr.png（测试用）
├── third_party/go-qrcode/   ← go.mod 中 replace 指令指向本地，必须保留
├── cmd/txqr-web/            main.go + index.html（go:embed 内嵌）
├── cmd/txqr-desktop/        main.go decode.go win32.go
├── cmd/txqr-launcher/       main.go
├── go.mod / go.sum
└── Makefile                 （仅 gomobile bind，依赖 mobile/，见 §5）
```

**依赖收敛效果**：`go mod tidy` 后 `require` 从 13 个降到 5 个 —
移除 `gopherjs`、`vecty`、`websocket`、`bindata-assetfs`、`qrterminal`、`byten`（全部只服务于 `txqr-tester` / `txqr-ascii`）。

### 3.2 Android 接收端 — `txqr-reader/android/`

```
android/
├── app/
│   ├── build.gradle.kts      versionCode=5 / versionName=1.3.0
│   ├── proguard-rules.pro
│   └── src/
│       ├── main/java/com/qrtpro/reader/     FileSaver / MainActivity / ScannerViewModel
│       │                                     decoder/ (17 个 Kotlin 文件)
│       │                                     ui/ (ResultScreen / ScannerScreen)
│       ├── main/res/values/                strings.xml themes.xml
│       └── test/java/com/qrtpro/reader/decoder/  2 个单测
├── build.gradle.kts / settings.gradle.kts / gradle.properties
├── gradle/wrapper/ (jar + properties)
├── gradlew / gradlew.bat
└── local.properties          ← gitignore，本机 SDK 路径，必须保留
```

**验证**：裁剪副本在纯 ASCII 路径下 `assembleDebug` → `BUILD SUCCESSFUL in 1m 17s`。

### 3.3 文档与 CI

| 文件 | 保留理由 |
|---|---|
| `docs/protocol-analysis.md` | **decimen 协议的逐字节规格**，是 Go 发送端与 Kotlin 接收端字节一致性的唯一权威依据（黄金向量、DCF2 容器、splitmix32/FNV-1a）。删掉后续无法维护兼容性 |
| `QRFT_Color_Transfer_Optimization_Design.md` | 彩色二维码方案设计稿。**注意：`txqr/qr/color.go` 的 `ColorMatrixEncode` 已在实装**，此文档是配套设计依据 |
| `.github/workflows/release.yml` | 三个 exe + APK 的发布流水线 |
| `README.md` `LICENSE` `.gitignore` | 根文档 |

---

## 4. 强烈建议移除

### 4.1 `reference/` — 5.2 MB ｜ 纯参考，源码零引用

- 内容：`decimen-optical-transfer_base/`，TypeScript + Vite 的 decimen 实现（39 个 `.ts`）
- **源码零引用**：`grep -rn "reference/" --include=*.go --include=*.kt` 全仓库无命中；协议已由 `docs/protocol-analysis.md` 完整规格化并在 Go / Kotlin 双侧独立实现
- **git 状态**：commit `6ed5d7b` 已解除跟踪，`.gitignore:9` 已忽略 —— 事实上已不在版本库中，仅是工作区残留
- **⚠️ 许可证风险**：该参考项目为 **AGPL-3.0-or-later**（含 CLA.md 双许可条款），而本仓库根 LICENSE 为 **MIT**。把 AGPL 代码留在 MIT 仓库的工作区里，是不必要的合规隐患

> `decimen` 的协议本身（线格式）不受版权保护，MIT 项目的独立实现没有问题；问题仅在于把 AGPL 源码文件直接放在同一目录。

### 4.2 `txqr-reader/` 的 iOS 三件套 — 14.1 MB ｜ 上游遗留，Android 端零引用

| 目录 | 体积 | 内容 |
|---|---|---|
| `txqr-reader/txqr-reader.xcodeproj/` | 28 KB | 上游 iOS Xcode 工程（含 `xcuserdata` 里 divan / simon 的个人 scheme） |
| `txqr-reader/txqr-reader/` | 73 KB | iOS Swift 源码（`QRCodeViewController.swift` 等） |
| `txqr-reader/txqr.framework/` | 14 MB | 上游 gomobile 预编译产物（Mach-O 二进制） |

- `txqr-reader/android/` 对 iOS 目录**零引用**（`grep -rn "xcodeproj|txqr.framework|\.swift"` 无命中）
- 本项目接收端已是 **Android Kotlin + Compose 完整重写**，不是 iOS 移植
- ⚠️ `txqr-reader/README.md` 描述的是 iOS demo app，移除 iOS 源码后应同步改写

---

## 5. 可选移除（上游未改造的备用实现）

这一类**不参与任何 dist 产物**，但属于上游 `divan/txqr` 的原始功能。是否保留取决于你是否还要留着它们做备用发送端。

| 路径 | 体积 | 说明 | 依赖情况 |
|---|---|---|---|
| `txqr/cmd/txqr-tester/` | **14 MB** | 上游 gopherjs/vecty 自动化测试 app | 独占 `gopherjs` `vecty` `websocket` `bindata-assetfs` 四个依赖 |
| `txqr/cmd/txqr-ascii/` | 6 KB | 终端 ASCII 发送端 | 独占 `qrterminal` `byten` |
| `txqr/cmd/txqr-gif/` | 6 KB | GIF 动画发送端 | 仅依赖 `raptorq` + `qr` |
| `txqr/mobile/` | 8 KB | gomobile 绑定目标（`Makefile` 的 `gomobile bind`） | **仅被 `txqr-tester/mobile/` 引用**，Android 端不引用 |
| `txqr/docs/demo.gif` | 2.4 MB | 上游 README 配图 | 被 `txqr/README.md` 引用 |
| `txqr/qr/testdata/15k.jpg` | 11 KB | 无任何引用（`helloworld_qr.png` 有引用，必须留） | — |

**建议**：若目标是"干净但不遗漏功能"，可保留 `txqr-ascii` / `txqr-gif`（各 6 KB，代价几乎为零），移除 `txqr-tester`（14 MB，拖 4 个依赖）、`txqr/mobile`、`txqr/docs`、`15k.jpg`。

**连带清理**：移除 `txqr/mobile/` 后，`txqr/Makefile` 的 `gomobile` 目标失效、`txqr/README.md` 中的 demo 图片与 tester 说明需同步调整。

---

## 6. 非源码（随时可重建，不属于"仓库功能"）

| 路径 | 体积 | 说明 |
|---|---|---|
| `txqr-reader/android/.gradle/` `.kotlin/` `app/build/` `build/` | **≈457 MB** | Gradle 缓存与构建产物，已被 `.gitignore` 忽略 |
| `txqr/bin/` | — | 已被 `.gitignore` 忽略 |
| `dist/` | 46 MB | 发布产物，已被 `.gitignore` 忽略（但它是本次分析的**基准**，建议保留一个基线） |

> `txqr-reader/android/` 的 457 MB 几乎全是 Gradle 缓存，与源码体积（≈200 KB）相差三个数量级。

---

## 7. 验证记录

| 验证项 | 命令 | 结果 |
|---|---|---|
| 三个 exe 构建（原始仓库） | `go build -trimpath -ldflags "-s -w -H windowsgui"` | ✅ 全部成功 |
| 三个 exe 构建（裁剪副本） | 同上 | ✅ 全部成功，**体积与 dist 逐字节一致** |
| `go mod tidy`（裁剪副本） | `GOFLAGS=-mod=mod go mod tidy` | ✅ require 13 → 5 |
| Go 测试（裁剪副本） | `go test ./...` | ✅ root / desktop / decimen / qr 全通过；`txqr-launcher` 2 项失败见 §8 |
| Android 构建（原始仓库） | `gradlew :app:assembleDebug` | ✅ `BUILD SUCCESSFUL in 8m 55s` |
| Android 构建（裁剪副本） | 同上，纯 ASCII 路径 | ✅ `BUILD SUCCESSFUL in 1m 17s` |

---

## 8. 顺带发现的两个既有问题（与裁剪无关）

### 8.1 launcher 找不到发送端 —— 命名三方不一致

- `txqr/cmd/txqr-launcher/main.go:311` 期望：`qrt-pro-sender-windows-amd64-v1.3.0.exe`
- `.github/workflows/release.yml:31` 产出：`qrt-pro-sender-windows-amd64.exe`（**无版本号后缀**）
- `dist/` 实际存在：`序列化工具.exe`（**中文名**）

三者互不匹配，**启动器在当前 `dist/` 下无法拉起发送端**。

### 8.2 `launcher_test.go` 硬编码绝对路径

`txqr/cmd/txqr-launcher/launcher_test.go:13` 写死了 `D:\Workspace\AiDev\008-qrt-pro\dist`，
而当前仓库目录是 `008-qrt-pro-opti`（多一个 `-opti`）→ 该路径不存在 → 必然失败。

这解释了 `go test ./cmd/txqr-launcher` 的 2 个失败用例（`TestLauncherProcessInfo`、`TestListenPortsReportsIPv6`）。
**裁剪前后失败现象完全一致**，确认与裁剪无关，属既有问题。

---

## 9. 建议的清理执行清单

### 第一批：零风险，立即执行

```bash
# 纯参考 + 许可证风险（工作区残留，本就不在 git 中）
rm -rf reference/

# 上游 iOS 遗留，Android 端零引用
rm -rf txqr-reader/txqr-reader.xcodeproj/
rm -rf txqr-reader/txqr-reader/
rm -rf txqr-reader/txqr.framework/
# 同时改写 txqr-reader/README.md（当前描述的是 iOS demo app）

# 构建缓存（可重建）
rm -rf txqr-reader/android/.gradle/ txqr-reader/android/.kotlin/
rm -rf txqr-reader/android/app/build/ txqr-reader/android/build/
```

### 第二批：上游备用实现（按需）

```bash
rm -rf txqr/cmd/txqr-tester/ txqr/mobile/ txqr/docs/
rm -f txqr/qr/testdata/15k.jpg
# 保留 txqr/cmd/txqr-ascii/ txqr/cmd/txqr-gif/（各 6 KB）
# 同步调整 txqr/README.md 与 txqr/Makefile
```

### 第三批：依赖收敛 + 验证

```bash
cd txqr
go mod tidy          # require 13 → 5
go build ./cmd/txqr-web ./cmd/txqr-desktop ./cmd/txqr-launcher
go test ./...
```

### 第四批：同步修正 CI 命名（可选）

修改 `.github/workflows/release.yml`，让产出名与 launcher 期望一致：

```yaml
go build ... -o ../../../dist/qrt-pro-sender-windows-amd64-v1.3.0.exe .
go build ... -o ../../../dist/qrt-pro-receiver-desktop-windows-amd64-v1.3.0.exe .
```

并修正 `launcher_test.go:13` 的硬编码路径。

---

## 10. 裁剪后的目标结构

```
008-qrt-pro-opti/
├── txqr/                          # Go：发送端 + 桌面接收端 + 启动器
│   ├── cmd/
│   │   ├── txqr-web/              #  → 序列化工具.exe
│   │   ├── txqr-desktop/          #  → qrt-pro-receiver-desktop-*.exe
│   │   └── txqr-launcher/         #  → qrt-pro-launcher.exe
│   ├── decimen/                   # decimen 协议
│   ├── qr/                        # QR 编解码 + 彩色矩阵
│   ├── third_party/go-qrcode/     # replace 目标，必留
│   ├── codec.go decode.go encode.go fountain.go doc.go
│   └── go.mod go.sum
├── txqr-reader/
│   └── android/                   # → qrt-pro-reader-v*.apk
├── docs/
│   ├── protocol-analysis.md       # decimen 协议规格（权威）
│   └── cleanup-analysis.md        # 本报告
├── .github/workflows/release.yml
├── dist/                          # 打包好的 portable 产物
└── README.md  LICENSE  .gitignore
```

---

## 11. 执行记录（2026-10-26 已完成）

### 11.1 实际执行内容

| 批次 | 操作 | 结果 |
|---|---|---|
| 备份 | `reference/` 打包到仓库外 `D:/Workspace/AiDev/qrt-reference-backup-20261026.tar.gz`（113 项 / 4.5 MB） | ✅ 该目录 **0 个文件受 git 跟踪**，删除后无法用 git 恢复，故先备份 |
| 第一批 | `rm -rf reference/` | ✅ |
| 第一批 | `git rm -r txqr-reader/txqr-reader.xcodeproj/ txqr-reader/txqr-reader/ txqr-reader/txqr.framework/` | ✅ 35 个文件 |
| 第一批 | `rm -rf txqr-reader/android/{.gradle,.kotlin,build,app/build}` | ✅ 457 MB → 357 KB |
| 第二批 | `git rm -r txqr/cmd/txqr-tester/ txqr/mobile/ txqr/docs/ txqr/qr/testdata/15k.jpg txqr/Makefile` | ✅ 40 个文件 |
| 第二批 | **保留** `txqr/cmd/txqr-ascii/` `txqr/cmd/txqr-gif/`（各 6 KB） | ✅ 按建议保留 |
| 第三批 | `go mod tidy` | ✅ require 13 → 7 |
| 第三批 | `git rm txqr/Makefile`（唯一 gomobile 目标，依赖已删的 `mobile/`） | ✅ |

**累计：75 个文件删除，70728 行代码移除。**

### 11.2 同步修正的文档与配置

| 文件 | 修正内容 |
|---|---|
| `README.md` | 目录结构更新为实际布局；版本 `v1.0.0` → `v1.3.0`；补充 Android 构建的 ASCII 路径警告 |
| `txqr/README.md` | 删除已移除的 `docs/demo.gif` 引用与 tester 说明 |
| `txqr-reader/README.md` | 原内容描述的是已删除的 iOS demo app → 重写为 Android 接收端说明 |
| `.github/workflows/release.yml` | 产物名改为带版本号（`...-${{ github.ref_name }}.exe`），与 launcher 期望一致 |

### 11.3 顺带修复的两个功能性 Bug

**Bug 1 —— launcher 找不到发送端（§8.1）**

原实现硬编码 `qrt-pro-sender-windows-amd64-v1.3.0.exe`，但 CI 产出 `qrt-pro-sender-windows-amd64.exe`，
`dist/` 中实际是 `序列化工具.exe`，三者互不匹配 → **启动器在当前 `dist/` 下完全无法拉起发送端**。

修复（`txqr/cmd/txqr-launcher/main.go`）：
- 新增 `resolveExe(dir, candidates, globs)`：按候选名依次探测 → 再用 glob 兜底匹配任意版本号 → 最后回退到首选名
- 新增 `procPrefix(exe)`：由解析出的实际文件名推导进程名前缀，供 `exeRunning` / `stopProcess` 使用
- 原本硬编码的 `"qrt-pro-sender"` / `"qrt-pro-receiver"` 前缀全部改为动态推导 —— 否则即便找到文件，
  进程检测与停止也会因文件名不匹配而失效

**Bug 2 —— 测试写死旧目录名（§8.2）**

`launcher_test.go` 硬编码 `D:\Workspace\AiDev\008-qrt-pro\dist`（旧目录名，缺 `-opti`）→ 必然失败。

修复：改用 `runtime.Caller(0)` 从源文件位置推导 `dist/` 路径；产物未构建时 `t.Skip` 而非 `t.Fatal`。

### 11.4 最终验证

| 验证项 | 结果 |
|---|---|
| `go build ./...`（全部 5 个 cmd） | ✅ |
| `go vet ./...` | ✅ 无告警 |
| `go test -count=1 ./...` | ✅ **全部通过**（含 launcher 2 项此前必失败的用例） |
| launcher 真实拉起发送端 | ✅ `sender pid=43412 ports=[9000]` |
| 三 exe 重建体积 | ✅ 与 `dist/` 基线一致：`6770688` / `3827712`；launcher 因修复增大至 `2448896` |
| `gradlew :app:assembleDebug` | ✅ `BUILD SUCCESSFUL` |
| 残留进程检查 | ✅ `:9000` 正常释放，无进程泄漏 |

> **遗留提示**：`gofmt -l` 仍报告 16 个文件不合规 —— 与清理**无关**，是仓库既有状态（改动前 HEAD 基线为 17 个，其中 1 个位于已删除的 `txqr-tester/`）。差异全部是 `300*time.Millisecond` 这类 `*` 两侧空格问题，未做批量格式化以免污染本次变更。

