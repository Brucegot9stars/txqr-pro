package com.qrtpro.reader.decoder

import java.nio.charset.StandardCharsets
import java.security.MessageDigest

class TxqrDecoder {
    private var activeCodec: CodecType = CodecType.LT
    private var lubyCodec: LubyCodec? = null
    private var lubyDecoder: LubyDecoder? = null
    private var binaryDecoder: LubyDecoder? = null
    private var raptorDecoder: RaptorDecoder? = null
    private var raptorQDecoder: RaptorQDecoder? = null
    private var onlineDecoder: OnlineDecoder? = null
    private var completed = false
    private var totalSize = 0
    private var chunkLen = 0
    private var numChunks = 0
    private var neededBlocks = 0
    private var manifest: TransferFrame? = null
    private var recovered: ByteArray? = null
    var lastError: String? = null
        private set
    private val seenBlockCodes = HashSet<Long>()

    private var sourceBytesRead = 0L
    private var startTime = 0L
    private var endTime = 0L
    private var lastChunkTime = 0L
    private var readInterval = 0L
    private var peakSpeedBps = 0L
    private var storedFileName = ""
    private val speedWindow = ArrayDeque<LongArray>()
    private val lock = Any()

    fun decode(chunk: String) = decodeFrame(chunk.toByteArray(StandardCharsets.ISO_8859_1))

    fun decodeFrame(frameBytes: ByteArray) {
        val frame = TransferFrame.parse(frameBytes) ?: return
        synchronized(lock) {
            if (lastError != null) return
            if (manifest?.sameTransfer(frame) == false) return
            if (manifest == null) {
                manifest = frame
                totalSize = frame.total
                chunkLen = frame.chunkLen
                activeCodec = frame.codec
                numChunks = numberOfChunks(totalSize, chunkLen)
                neededBlocks = numChunks
            }
            if (frame.isName) {
                if (storedFileName.isEmpty()) storedFileName = String(frame.payload, Charsets.UTF_8)
                return
            }
            if (completed) return
            if (frame.blockCode in seenBlockCodes) return
            if (lubyDecoder == null && binaryDecoder == null && raptorDecoder == null && raptorQDecoder == null && onlineDecoder == null) {
                when (activeCodec) {
                    CodecType.LT -> lubyDecoder = LubyCodec(numChunks).newDecoder(totalSize)
                    CodecType.BINARY -> binaryDecoder = BinaryCodec(numChunks).newDecoder(totalSize)
                    CodecType.RAPTOR -> raptorDecoder = RaptorCodec(numChunks, 4).newDecoder(totalSize)
                    CodecType.RAPTORQ -> raptorQDecoder = RaptorQCodec(chunkLen, totalSize).newDecoder()
                    CodecType.ONLINE -> onlineDecoder = OnlineCodec(numChunks, 0.01, 3, 42).newDecoder(totalSize)
                }
            }
            val now = System.nanoTime() / 1_000_000
            if (startTime == 0L) startTime = now
            if (lastChunkTime != 0L) readInterval = now - lastChunkTime
            lastChunkTime = now
            val block = listOf(LTBlock(frame.blockCode, frame.payload))
            val ready = when (activeCodec) {
                CodecType.LT -> lubyDecoder!!.addBlocks(block)
                CodecType.BINARY -> binaryDecoder!!.addBlocks(block)
                CodecType.RAPTOR -> raptorDecoder!!.addBlocks(block)
                CodecType.RAPTORQ -> raptorQDecoder!!.addBlocks(block)
                CodecType.ONLINE -> onlineDecoder!!.addBlocks(block)
            }
            seenBlockCodes.add(frame.blockCode)
            sourceBytesRead += frame.payload.size
            recordSpeedSample(now)
            if (!ready) return
            val bytes = when (activeCodec) {
                CodecType.LT -> lubyDecoder!!.decode()
                CodecType.BINARY -> binaryDecoder!!.decode()
                CodecType.RAPTOR -> raptorDecoder!!.decode()
                CodecType.RAPTORQ -> raptorQDecoder!!.decode()
                CodecType.ONLINE -> onlineDecoder!!.decode()
            } ?: return // RaptorQ may still be rank deficient; keep accepting symbols.
            val digest = MessageDigest.getInstance("SHA-256").digest(bytes)
                .joinToString("") { "%02x".format(it.toInt() and 0xff) }
            if (bytes.size != totalSize || digest != manifest!!.sha256) {
                lastError = "SHA-256 校验失败，请重新扫描"
                return
            }
            recovered = bytes
            completed = true
            endTime = System.nanoTime() / 1_000_000
        }
    }

    private fun recordSpeedSample(now: Long) {
        speedWindow.addLast(longArrayOf(now, sourceBytesRead))
        while (speedWindow.size > 1 && now - speedWindow.first()[0] > SPEED_WINDOW_MS) {
            speedWindow.removeFirst()
        }
    }

    private fun currentSpeedBps(): Long {
        if (speedWindow.size < 2) return 0L
        val now = System.nanoTime() / 1_000_000
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

    val isCompleted: Boolean get() = synchronized(lock) { completed }

    val data: String get() = dataBytes?.decodeToString() ?: ""

    val dataBytes: ByteArray?
        get() = synchronized(lock) { recovered?.copyOf() }

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
            formatSpeed((if (completed) totalSize.toLong() else sourceBytesRead) * 1000L / elapsed)
        }

    val peakSpeedStr: String get() = synchronized(lock) { formatSpeed(peakSpeedBps) }

    val totalTime: String
        get() = synchronized(lock) {
            formatDuration(elapsedMs)
        }

    private val elapsedMs: Long
        get() {
            if (endTime != 0L) return endTime - startTime
            return if (startTime == 0L) 0L else System.nanoTime() / 1_000_000 - startTime
        }

    val totalSizeStr: String get() = synchronized(lock) { formatSize(totalSize.toLong()) }

    val readIntervalMs: Long get() = synchronized(lock) { readInterval }

    val receivedFileName: String get() = synchronized(lock) { storedFileName }

    // Unique encoded payload bytes; repair symbols are not source-file goodput.
    val sourceBytesCount: Long get() = synchronized(lock) { sourceBytesRead }
    val totalBytesCount: Long get() = synchronized(lock) { totalSize.toLong() }
    val sourceBytesStr: String
        get() = synchronized(lock) {
            formatSize(sourceBytesRead) + "/" + formatSize(totalSize.toLong())
        }

    // Valid source bytes detail for fine-grained judgement while scanning.
    val bytesDetailStr: String
        get() = synchronized(lock) {
            val total = totalSize.toLong()
            "已收编码Bytes ${sourceBytesRead}/${total}"
        }

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
            manifest = null
            recovered = null
            lastError = null
            seenBlockCodes.clear()
            sourceBytesRead = 0L
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
