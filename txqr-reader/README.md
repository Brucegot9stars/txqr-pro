# QRT Pro Reader — Android

接收端（Android，Kotlin + Jetpack Compose），扫描屏幕上的动态二维码帧并还原文件。

> 本目录原本还包含上游 `divan/txqr-reader` 的 iOS 原始实现（Swift + Xcode 工程 +
> gomobile 预编译的 `txqr.framework`）。项目已改为 Android 完整重写，iOS 侧与
> Android 构建零耦合，故在仓库清理中移除（见 `../docs/cleanup-analysis.md`）。

## 功能

- CameraX 摄像头预览 + ML Kit 二维码识别（不限制帧率，只保留最新帧）
- 完整 Kotlin 解码器，与 Go 发送端逐字节一致：
  - MersenneTwister（Int63/Float64/Perm）、RFC5053 分区、soliton CDF、PickIndices
  - 已去除 base64 还原逻辑，`dataBytes` 直接返回原始字节
- 三种解码模式：`txqr`（喷泉码）、`decimen`（systematic-carousel + DCF2 容器）、`color`（四色矩阵）
- 原始二进制保存：通过 MediaStore 写入 Downloads，按内容自动推断后缀
- 实时速度：2 秒滑动窗口瞬时速率 + 全程平均速率，每 500ms 刷新
- 扫码完成后可继续重扫（"Scan Another"）

## 构建

```bash
cd txqr-reader/android
# 设置本机 Gradle 用户目录（含完整发行版，避免 wrapper 重新下载/锁冲突）
set GRADLE_USER_HOME=C:\gradle-home
gradlew.bat :app:assembleDebug --no-daemon
# 产物：app\build\outputs\apk\debug\app-debug.apk
```

> ⚠️ **构建路径必须为纯 ASCII**。AGP 会硬性检查并报
> `Your project path contains non-ASCII characters`。
> 仓库位于 `D:\Workspace\AiDev\008-qrt-pro-opti`（纯 ASCII）时正常。
> `local.properties` 记录本机 SDK 路径（`sdk.dir`），已被 `.gitignore` 忽略，
> 换机器需自行生成。

## 协议规格

decimen 协议的逐字节规格见 `../docs/protocol-analysis.md`，是 Go 发送端与本接收端
字节一致性的唯一权威依据。

### Licence

MIT
