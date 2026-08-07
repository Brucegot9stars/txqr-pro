package com.qrtpro.reader.decoder

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
    private var numChunks = 0
    private var neededBlocks = 0
    private val cache = mutableSetOf<String>()
    private val seenBlockCodes = HashSet<Long>()

    private var bytesRead = 0L
    private var startTime = 0L
    private var endTime = 0L
    private var lastChunkTime = 0L
    private var readInterval = 0L
    private var peakSpeedBps = 0L
    private var storedFileName = ""
    private val speedWindow = ArrayDeque<LongArray>()
    private val lock = Any()

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
        synchronized(lock) {
            if (completed) return
            if (isCached(header)) return

            val parts = header.split("/")
            if (parts.size < 3) return

            if (parts.size >= 5 && parts[0] == "-1" && parts[4] == "name") {
                storedFileName = String(payload, StandardCharsets.UTF_8)
                return
            }

            val blockCode = parts[0].toLongOrNull() ?: return
            val chunkLen = parts[1].toIntOrNull() ?: return
            val total = parts[2].toIntOrNull() ?: return
            val codecType = if (parts.size >= 4) CodecType.fromString(parts[3]) else CodecType.LT

            val now = System.currentTimeMillis()
            if (startTime == 0L) {
                startTime = now
            }
            if (lastChunkTime != 0L) {
                readInterval = now - lastChunkTime
            }
            lastChunkTime = now

            if (lubyDecoder == null && binaryDecoder == null && raptorDecoder == null && raptorQDecoder == null && onlineDecoder == null) {
                this.totalSize = total
                this.chunkLen = chunkLen
                this.activeCodec = codecType
                this.numChunks = numberOfChunks(total, chunkLen)
                this.neededBlocks = if (codecType == CodecType.RAPTOR) {
                    RaptorConstants.intermediateSymbols(numChunks).first
                } else {
                    numChunks
                }

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
            seenBlockCodes.add(blockCode)
            recordSpeedSample(now)

            val ltBlock = LTBlock(blockCode = blockCode, data = payload)
            when (activeCodec) {
                CodecType.LT -> completed = lubyDecoder!!.addBlocks(listOf(ltBlock))
                CodecType.BINARY -> completed = binaryDecoder!!.addBlocks(listOf(ltBlock))
                CodecType.RAPTOR -> completed = raptorDecoder!!.addBlocks(listOf(ltBlock))
                CodecType.RAPTORQ -> completed = raptorQDecoder!!.addBlocks(listOf(ltBlock))
                CodecType.ONLINE -> completed = onlineDecoder!!.addBlocks(listOf(ltBlock))
            }
            if (completed) {
                endTime = System.currentTimeMillis()
            }
        }
    }

    private fun recordSpeedSample(now: Long) {
        speedWindow.addLast(longArrayOf(now, bytesRead))
        while (speedWindow.size > 1 && now - speedWindow.first()[0] > SPEED_WINDOW_MS) {
            speedWindow.removeFirst()
        }
    }

    private fun currentSpeedBps(): Long {
        if (speedWindow.size < 2) return 0L
        val now = System.currentTimeMillis()
        val last = speedWindow.last()
        if (now - last[0] > SPEED_WINDOW_MS) return 0L
        val first = speedWindow.first()
        val span = last[0] - first[0]
        if (span <= 0L) return 0L
        val delta = last[1] - first[1]
        if (delta <= 0L) return 0L
        val speed = delta * 1000L / span
        if (speed > peakSpeedBps) {
            peakSpeedBps = speed
        }
        return speed
    }

    private fun isCached(header: String): Boolean {
        if (header in cache) return true
        cache.add(header)
        return false
    }

    val isCompleted: Boolean get() = synchronized(lock) { completed }

    val data: String get() = dataBytes?.decodeToString() ?: ""

    val dataBytes: ByteArray?
        get() = synchronized(lock) {
            if (!completed) return@synchronized null
            when (activeCodec) {
                CodecType.LT -> lubyDecoder?.decode()
                CodecType.BINARY -> binaryDecoder?.decode()
                CodecType.RAPTOR -> raptorDecoder?.decode()
                CodecType.RAPTORQ -> raptorQDecoder?.decode()
                CodecType.ONLINE -> onlineDecoder?.decode()
            }
        }

    val progress: Int
        get() = synchronized(lock) {
            if (completed) return@synchronized 100
            if (neededBlocks <= 0) return@synchronized 0
            val p = 100L * seenBlockCodes.size / neededBlocks
            p.coerceIn(0L, 99L).toInt()
        }

    val speedStr: String get() = synchronized(lock) { formatSpeed(currentSpeedBps()) }

    val avgSpeedStr: String
        get() = synchronized(lock) {
            val elapsed = elapsedMs
            if (elapsed <= 0L) return@synchronized formatSpeed(0L)
            formatSpeed(totalSize.toLong() * 1000L / elapsed)
        }

    val peakSpeedStr: String get() = synchronized(lock) { formatSpeed(peakSpeedBps) }

    val totalTime: String
        get() = synchronized(lock) {
            formatDuration(elapsedMs)
        }

    private val elapsedMs: Long
        get() {
            if (endTime != 0L) return endTime - startTime
            return if (startTime == 0L) 0L else System.currentTimeMillis() - startTime
        }

    val totalSizeStr: String get() = formatSize(totalSize.toLong())

    val readIntervalMs: Long get() = synchronized(lock) { readInterval }

    val receivedFileName: String get() = synchronized(lock) { storedFileName }

    fun reset() {
        synchronized(lock) {
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
            numChunks = 0
            neededBlocks = 0
            cache.clear()
            seenBlockCodes.clear()
            bytesRead = 0L
            startTime = 0L
            endTime = 0L
            lastChunkTime = 0L
            readInterval = 0L
            peakSpeedBps = 0L
            storedFileName = ""
            speedWindow.clear()
        }
    }

    companion object {
        private const val SPEED_WINDOW_MS = 2000L

        private fun formatSpeed(bytesPerSec: Long): String {
            return formatSize(bytesPerSec) + "/s"
        }

        private fun formatDuration(ms: Long): String {
            if (ms > 1000) {
                val rounded = ms - (ms % 100)
                return "%.1fs".format(rounded / 1000.0)
            }
            return "${ms}ms"
        }

        fun formatSize(bytes: Long): String {
            return when {
                bytes < 1024 -> "${bytes}B"
                bytes < 1024 * 1024 -> "%.1fKB".format(bytes / 1024.0)
                else -> "%.1fMB".format(bytes / (1024.0 * 1024.0))
            }
        }
    }
}
