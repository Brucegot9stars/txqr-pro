package com.qrtpro.reader.decoder

import java.security.MessageDigest
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/** Frame work and the metadata deadline poll serialize off the UI thread. */
class ReceiveSession(private val clock: () -> Long = { System.nanoTime() / 1_000_000 }) {
    private val classic = TxqrDecoder()
    private val decimen = DecimenDecoder()
    private var mode = 0
    private var start = 0L
    private var terminal = false
    private var verifiedBytes: ByteArray? = null
    private var verifiedAt: Long? = null
    private var verifiedName = ""

    companion object { const val FILE_NAME_WAIT_MS = 3000L }

    @Synchronized
    fun accept(frames: List<ByteArray>, isCurrent: () -> Boolean = { true }): ReceiveUpdate? {
        if (terminal || !isCurrent()) return null
        try {
            for (frame in frames) {
                if (!isCurrent()) return null
                if (mode == 0) {
                    mode = when {
                        decimen.parseFrame(frame) != null -> 2
                        TransferFrame.parse(frame) != null -> 1
                        else -> continue
                    }
                    start = clock()
                }
                if (mode == 2) decimen.addFrame(frame) else classic.decodeFrame(frame)
                val error = if (mode == 2) decimen.failure else classic.lastError
                if (error != null) return failed(error)
                val contentReady = if (mode == 2) decimen.isComplete else classic.isCompleted
                if (contentReady && verifiedBytes == null) {
                    val file = if (mode == 2) decimen.verifiedResult() else null
                    val bytes = if (mode == 2) file?.bytes else classic.dataBytes
                    if (bytes == null) return failed(decimen.failure ?: "恢复数据无效，请重新扫描")
                    verifiedBytes = bytes
                    verifiedAt = clock()
                    verifiedName = file?.name ?: ""
                }
                if (!isCurrent()) return null
                if (verifiedBytes != null && (mode == 2 || classic.receivedFileName.isNotBlank())) return complete()
            }
            if (verifiedBytes != null && isCurrent() && clock() - verifiedAt!! >= FILE_NAME_WAIT_MS) return complete()
            return if (mode == 0 || !isCurrent()) null else snapshot()
        } catch (e: Exception) {
            return if (isCurrent()) failed("解码失败：${e.message ?: "未知错误"}，请重新扫描") else null
        }
    }

    private fun complete(): ReceiveUpdate {
        val bytes = verifiedBytes!!
        val name = if (mode == 2) verifiedName else classic.receivedFileName
        val missing = name.isBlank()
        val fallback = if (missing) {
            val time = SimpleDateFormat("yyyyMMdd_HHmmss", Locale.US).format(Date())
            val hash = MessageDigest.getInstance("SHA-1").digest(bytes).take(8).toByteArray().hex()
            "received_${time}_${hash}.bin"
        } else name
        val md5 = MessageDigest.getInstance("MD5").digest(bytes).hex()
        terminal = true
        return snapshot().copy(progress = 100, data = bytes, fileName = fallback,
            md5 = md5, finishedAt = verifiedAt, totalBytes = bytes.size.toLong(), waitingForName = false,
            warning = if (missing) "未收到原始文件名，已使用默认名称，可手动修改。" else null)
    }

    private fun failed(message: String): ReceiveUpdate {
        terminal = true
        return snapshot().copy(error = message, finishedAt = clock())
    }

    private fun snapshot(): ReceiveUpdate = ReceiveUpdate(
        decimen = mode == 2, progress = if (verifiedBytes != null) 99 else if (mode == 2) decimen.progress else classic.progress,
        speed = if (mode == 2) TxqrDecoder.formatSize(decimen.speedBps) + "/s" else classic.speedStr,
        peakSpeed = if (mode == 2) TxqrDecoder.formatSize(decimen.peakBps) + "/s" else classic.peakSpeedStr,
        readInterval = if (mode == 2) decimen.readIntervalMs else classic.readIntervalMs,
        totalBytes = if (mode == 2) decimen.totalByteCount else classic.totalBytesCount,
        encodedBytes = if (mode == 2) decimen.sourceByteCount else classic.sourceBytesCount,
        bytesDetail = if (verifiedBytes != null && mode == 1 && !terminal) "内容已校验，正在补收文件名…" else if (mode == 2) decimen.bytesDetailStr else classic.bytesDetailStr,
        startedAt = start, observedAt = clock(), finishedAt = verifiedAt,
        waitingForName = verifiedBytes != null && !terminal && mode == 1,
    )

    private fun ByteArray.hex() = joinToString("") { "%02x".format(it.toInt() and 255) }
}

/** An immutable publication: the main thread never reads or locks live decoders. */
data class ReceiveUpdate(
    val decimen: Boolean,
    val progress: Int,
    val speed: String,
    val peakSpeed: String,
    val readInterval: Long,
    val totalBytes: Long,
    val encodedBytes: Long,
    val bytesDetail: String,
    val startedAt: Long,
    val observedAt: Long,
    val finishedAt: Long? = null,
    val data: ByteArray? = null,
    val fileName: String = "",
    val md5: String = "",
    val error: String? = null,
    val waitingForName: Boolean = false,
    val warning: String? = null,
) {
    fun elapsed(now: Long): Long = ((finishedAt ?: now) - startedAt).coerceAtLeast(0)
    fun avgSpeed(now: Long): String {
        val span = elapsed(now)
        val bytes = if (data != null || waitingForName) totalBytes else encodedBytes
        return TxqrDecoder.formatSize(if (span == 0L) 0 else bytes * 1000 / span) + "/s"
    }
    fun duration(now: Long): String {
        val span = elapsed(now)
        return if (span < 1000) "${span}ms" else "%.1fs".format(span / 1000.0)
    }
}
