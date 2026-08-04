package com.txqr.reader.decoder

import java.nio.charset.StandardCharsets

class TxqrDecoder {
    private var activeCodec: CodecType = CodecType.LT
    private var lubyCodec: LubyCodec? = null
    private var lubyDecoder: LubyDecoder? = null
    private var binaryDecoder: BinaryDecoder? = null
    private var raptorDecoder: RaptorDecoder? = null
    private var raptorQDecoder: RaptorQDecoder? = null
    private var onlineDecoder: OnlineDecoder? = null
    private var completed = false
    private var totalSize = 0
    private var chunkLen = 0
    private val cache = mutableSetOf<String>()

    private var bytesRead = 0
    private var startTime = 0L
    private var lastChunkTime = 0L
    private var readInterval = 0L
    private val speedWindow = ArrayDeque<LongArray>()

    fun decode(chunk: String) {
        val idx = chunk.indexOf('|')
        if (idx == -1) return
        decodeHeaderAndPayload(chunk.substring(0, idx), chunk.substring(idx + 1))
    }

    fun decodeFrame(frameBytes: ByteArray) {
        var idx = -1
        for (i in frameBytes.indices) {
            if (frameBytes[i] == '|'.code.toByte()) {
                idx = i
                break
            }
        }
        if (idx == -1) return
        val header = String(frameBytes, 0, idx, StandardCharsets.ISO_8859_1)
        val payload = frameBytes.copyOfRange(idx + 1, frameBytes.size)
        decodeHeaderAndPayload(header, payload)
    }

    private fun decodeHeaderAndPayload(header: String, payloadString: String) {
        val payload = payloadString.toByteArray(StandardCharsets.ISO_8859_1)
        decodeHeaderAndPayload(header, payload)
    }

    private fun decodeHeaderAndPayload(header: String, payload: ByteArray) {
        if (completed) return
        if (isCached(header)) return

        val parts = header.split("/")
        if (parts.size < 3) return

        val blockCode = parts[0].toLongOrNull() ?: return
        val chunkLen = parts[1].toIntOrNull() ?: return
        val total = parts[2].toIntOrNull() ?: return
        val codecType = if (parts.size >= 4) CodecType.fromString(parts[3]) else CodecType.LT

        if (startTime == 0L) {
            startTime = System.currentTimeMillis()
        }
        if (lastChunkTime != 0L) {
            readInterval = System.currentTimeMillis() - lastChunkTime
        }
        lastChunkTime = System.currentTimeMillis()

        if (lubyDecoder == null && binaryDecoder == null && raptorDecoder == null && raptorQDecoder == null && onlineDecoder == null) {
            this.totalSize = total
            this.chunkLen = chunkLen
            this.activeCodec = codecType
            val numChunks = numberOfChunks(total, chunkLen)

            when (codecType) {
                CodecType.LT -> {
                    lubyCodec = LubyCodec(numChunks)
                    lubyDecoder = lubyCodec!!.newDecoder(total)
                }
                CodecType.BINARY -> {
                    binaryDecoder = BinaryCodec(numChunks).newDecoder(total)
                }
                CodecType.RAPTOR -> {
                    raptorDecoder = RaptorCodec(numChunks, 4).newDecoder(total)
                }
                CodecType.RAPTORQ -> {
                    raptorQDecoder = RaptorQCodec(chunkLen, total).newDecoder()
                }
                CodecType.ONLINE -> {
                    onlineDecoder = OnlineCodec(numChunks, 0.01, 3, 42).newDecoder(total)
                }
            }
        }

        bytesRead += payload.size
        recordSpeedSample()

        val ltBlock = LTBlock(blockCode = blockCode, data = payload)
        when (activeCodec) {
            CodecType.LT -> completed = lubyDecoder!!.addBlocks(listOf(ltBlock))
            CodecType.BINARY -> completed = binaryDecoder!!.addBlocks(listOf(ltBlock))
            CodecType.RAPTOR -> completed = raptorDecoder!!.addBlocks(listOf(ltBlock))
            CodecType.RAPTORQ -> completed = raptorQDecoder!!.addBlocks(listOf(ltBlock))
            CodecType.ONLINE -> completed = onlineDecoder!!.addBlocks(listOf(ltBlock))
        }
    }

    private fun recordSpeedSample() {
        val now = System.currentTimeMillis()
        speedWindow.addLast(longArrayOf(now, bytesRead.toLong()))
        while (speedWindow.size > 1 && now - speedWindow.first()[0] > SPEED_WINDOW_MS) {
            speedWindow.removeFirst()
        }
    }

    private fun currentSpeed(): Int {
        if (speedWindow.size < 2) return 0
        val now = System.currentTimeMillis()
        val last = speedWindow.last()
        if (now - last[0] > SPEED_WINDOW_MS) return 0
        val first = speedWindow.first()
        val span = last[0] - first[0]
        if (span <= 0) return 0
        val delta = last[1] - first[1]
        if (delta <= 0) return 0
        return (delta * 1000 / span).toInt()
    }

    private fun isCached(header: String): Boolean {
        if (header in cache) return true
        cache.add(header)
        return false
    }

    val isCompleted: Boolean get() = completed

    val data: String get() = dataBytes?.decodeToString() ?: ""

    val dataBytes: ByteArray?
        get() {
            if (!completed) return null
            return when (activeCodec) {
                CodecType.LT -> lubyDecoder?.decode()
                CodecType.BINARY -> binaryDecoder?.decode()
                CodecType.RAPTOR -> raptorDecoder?.decode()
                CodecType.RAPTORQ -> raptorQDecoder?.decode()
                CodecType.ONLINE -> onlineDecoder?.decode()
            }
        }

    val progress: Int
        get() {
            if (totalSize == 0) return 0
            val p = 100 * bytesRead / totalSize
            return if (p > 100) 100 else p
        }

    val speedStr: String get() = formatSpeed(currentSpeed())

    val totalTime: String
        get() {
            val dur = System.currentTimeMillis() - startTime
            return formatDuration(dur)
        }

    val totalSizeStr: String get() = formatSize(totalSize)

    val readIntervalMs: Long get() = readInterval

    fun reset() {
        activeCodec = CodecType.LT
        lubyCodec = null
        lubyDecoder = null
        binaryDecoder = null
        raptorDecoder = null
        raptorQDecoder = null
        onlineDecoder = null
        completed = false
        totalSize = 0
        chunkLen = 0
        cache.clear()
        bytesRead = 0
        startTime = 0L
        lastChunkTime = 0L
        readInterval = 0L
        speedWindow.clear()
    }

    companion object {
        private const val SPEED_WINDOW_MS = 2000L

        private fun formatSpeed(bytesPerSec: Int): String {
            return formatSize(bytesPerSec) + "/s"
        }

        private fun formatDuration(ms: Long): String {
            if (ms > 1000) {
                val rounded = ms - (ms % 100)
                return "%.1fs".format(rounded / 1000.0)
            }
            return "${ms}ms"
        }

        fun formatSize(bytes: Int): String {
            return when {
                bytes < 1024 -> "${bytes}B"
                bytes < 1024 * 1024 -> "%.1fKB".format(bytes / 1024.0)
                else -> "%.1fMB".format(bytes / (1024.0 * 1024.0))
            }
        }
    }
}
