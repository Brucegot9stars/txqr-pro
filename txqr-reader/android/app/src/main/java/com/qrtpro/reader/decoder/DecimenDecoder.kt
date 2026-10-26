package com.qrtpro.reader.decoder

import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.security.MessageDigest
import java.util.zip.GZIPInputStream
import java.util.zip.GZIPOutputStream

/**
 * Decimen optical file transfer receiver — mirrors the Go sender in
 * txqr/decimen and the decimen-optical-transfer reference. Wire format:
 * every QR frame is a 20-byte little-endian header + a blockLen-byte fountain
 * block. Sender and receiver derive each frame's block subset independently,
 * so splitmix32, frameSeed and repairIndices are wire format and must stay
 * byte-identical with the Go/JS implementations (integer ops only).
 *
 *  0  u8   magic 0xD1
 *  1  u8   magic 0x0D  (v2: systematic-carousel fountain)
 *  2  u16  sessionId
 *  4  u32  seq
 *  8  u16  k
 * 10  u16  blockLen
 * 12  u32  totalLen   (DCF2 container length)
 * 16  u32  payloadFnv (FNV-1a of the whole container)
 */
class DecimenDecoder {
    data class FrameHeader(
        val sessionId: Int,
        val seq: Long,
        val k: Int,
        val blockLen: Int,
        val totalLen: Long,
        val payloadFnv: Long,
    )

    var framesNew = 0
        private set
    var framesDup = 0
        private set
    var framesRedundant = 0
        private set

    private var header: FrameHeader? = null
    private var k = 0
    private var words = 0
    private var solved: Array<IntArray?>? = null
    private var byBlock = HashMap<Int, MutableSet<PendingFrame>>()
    private val seen = HashSet<Long>()
    private var solvedCount = 0
    private var completed = false
    private var startTime = 0L
    private var endTime = 0L
    private var lastFrameTime = 0L
    private var readInterval = 0L
    private var peakSpeedBps = 0L
    private var sourceBytesRead = 0L
    private val speedWindow = ArrayDeque<LongArray>()

    private class PendingFrame(var idx: MutableList<Int>, val words: IntArray)

    val isComplete: Boolean get() = completed

    fun isDecimenFrame(bytes: ByteArray): Boolean =
        bytes.size > HEADER_LEN &&
            (bytes[0].toInt() and 0xFF) == MAGIC0 &&
            (bytes[1].toInt() and 0xFF) == MAGIC1

    fun parseFrame(bytes: ByteArray): FrameHeader? {
        if (bytes.size <= HEADER_LEN) return null
        if (!isDecimenFrame(bytes)) return null
        val h = FrameHeader(
            sessionId = leU16(bytes, 2),
            seq = leU32(bytes, 4),
            k = leU16(bytes, 8),
            blockLen = leU16(bytes, 10),
            totalLen = leU32(bytes, 12),
            payloadFnv = leU32(bytes, 16),
        )
        if (h.k == 0 || h.blockLen == 0 || h.totalLen == 0L) return null
        if (bytes.size != HEADER_LEN + h.blockLen) return null
        return h
    }

    /** Feed one decoded frame's raw bytes (the whole QR payload). Returns true
     *  when the transfer just completed. */
    fun addFrame(bytes: ByteArray): Boolean {
        val h = parseFrame(bytes) ?: return completed
        if (header == null) {
            initStream(h)
        } else if (!sameStream(header!!, h)) {
            initStream(h) // new session id or a stream-identity mismatch: reset
        }

        if (!seen.add(h.seq)) {
            framesDup++
            return completed
        }
        framesNew++
        if (completed) return true

        val now = System.currentTimeMillis()
        if (startTime == 0L) startTime = now
        if (lastFrameTime != 0L) readInterval = now - lastFrameTime
        lastFrameTime = now

        val idx = frameComposition(h.k.toUShort(), h.sessionId.toUShort(), h.seq.toUInt())
        val blockWords = IntArray(words)
        copyBlockToWords(bytes, blockWords)

        val rem = ArrayList<Int>(idx.size)
        for (b in idx) {
            val s = solved!![b.toInt()]
            if (s != null) xorInto(blockWords, s) else rem.add(b.toInt())
        }
        if (rem.isEmpty()) {
            framesRedundant++
            return completed
        }
        sourceBytesRead += bytes.size - HEADER_LEN
        recordSpeedSample(now)
        if (rem.size == 1) {
            resolve(rem[0], blockWords)
            return completed
        }
        val pf = PendingFrame(rem, blockWords)
        for (b in rem) {
            byBlock.getOrPut(b) { HashSet() }.add(pf)
        }
        return completed
    }

    private fun sameStream(a: FrameHeader, b: FrameHeader): Boolean =
        a.sessionId == b.sessionId && a.k == b.k && a.blockLen == b.blockLen &&
            a.totalLen == b.totalLen && a.payloadFnv == b.payloadFnv

    private fun initStream(h: FrameHeader) {
        header = h
        k = h.k
        words = (h.blockLen + 3) / 4
        solved = arrayOfNulls(k)
        byBlock = HashMap()
        seen.clear()
        solvedCount = 0
        completed = false
        framesNew = 0
        framesDup = 0
        framesRedundant = 0
        sourceBytesRead = 0L
        startTime = System.currentTimeMillis()
        endTime = 0L
        lastFrameTime = 0L
        readInterval = 0L
        speedWindow.clear()
    }

    private fun copyBlockToWords(bytes: ByteArray, words: IntArray) {
        val n = (bytes.size - HEADER_LEN).coerceAtMost(words.size * 4)
        for (i in 0 until words.size) {
            var v = 0
            val base = HEADER_LEN + i * 4
            for (j in 0 until 4) {
                val idx = base + j
                if (idx < HEADER_LEN + n) {
                    v = v or ((bytes[idx].toInt() and 0xFF) shl (j * 8))
                }
            }
            words[i] = v
        }
    }

    private fun xorInto(dst: IntArray, src: IntArray) {
        for (i in dst.indices) dst[i] = dst[i] xor src[i]
    }

    private fun resolve(b0: Int, w0: IntArray) {
        val queue = ArrayDeque<Pair<Int, IntArray>>()
        queue.addLast(b0 to w0)
        while (queue.isNotEmpty()) {
            val (b, w) = queue.removeLast()
            if (solved!![b] != null) continue
            solved!![b] = w
            solvedCount++
            if (solvedCount >= k) {
                completed = true
                endTime = System.currentTimeMillis()
                return
            }
            val waiting = byBlock.remove(b) ?: continue
            for (pf in waiting) {
                xorInto(pf.words, w)
                pf.idx.remove(b)
                if (pf.idx.size == 1) {
                    val r = pf.idx[0]
                    byBlock[r]?.remove(pf)
                    if (solved!![r] == null) queue.addLast(r to pf.words)
                }
            }
        }
    }

    /** Recovered DCF2 container bytes, or null while incomplete. */
    fun assemble(): ByteArray? {
        if (!completed) return null
        val h = header!!
        val out = ByteArray(h.totalLen.toInt())
        for (b in 0 until k) {
            val start = b * h.blockLen
            val length = (h.blockLen).coerceAtMost(h.totalLen.toInt() - start)
            if (length <= 0) continue
            val block = solved!![b] ?: return null
            for (i in 0 until length) {
                val shift = (i % 4) * 8
                out[start + i] = ((block[i / 4] shr shift) and 0xFF).toByte()
            }
        }
        return out
    }

    /** Unpack a DCF2 container back into the original file bytes. */
    fun unpackContainer(container: ByteArray): UnpackedFile {
        if (container.size < 49) throw IllegalStateException("container too short")
        val magic = byteArrayOf(0x44, 0x43, 0x46, 0x32)
        for (i in magic.indices) {
            if (container[i] != magic[i]) throw IllegalStateException("bad container magic")
        }
        val compression = container[4].toInt() and 0xFF
        if (compression > 1) throw IllegalStateException("unsupported compression")
        val nameLen = leU16(container, 5)
        val typeLen = leU16(container, 7)
        val fileLen = leU32(container, 9)
        val transLen = leU32(container, 13)
        val dataOffset = 49 + nameLen + typeLen
        if (fileLen == 0L || fileLen > MAX_FILE_BYTES || transLen == 0L || transLen > MAX_FILE_BYTES ||
            dataOffset + transLen.toInt() != container.size
        ) {
            throw IllegalStateException("container length mismatch")
        }
        val name = safeFileName(String(container, 49, nameLen, Charsets.UTF_8))
        val type = String(container, 49 + nameLen, typeLen, Charsets.UTF_8)
        val transmitted = container.copyOfRange(dataOffset, container.size)
        val bytes = if (compression == 1) {
            if (transmitted.size < 18) throw IllegalStateException("gzip payload too short")
            if (leU32(transmitted, transmitted.size - 4) != fileLen) {
                throw IllegalStateException("gzip length mismatch")
            }
            gunzip(transmitted, fileLen.toInt())
        } else {
            transmitted
        }
        if (bytes.size != fileLen.toInt()) throw IllegalStateException("file length mismatch")
        val sha = container.copyOfRange(17, 49)
        return UnpackedFile(name, type, bytes, sha)
    }

    private fun gunzip(data: ByteArray, maxBytes: Int): ByteArray {
        val out = ByteArrayOutputStream()
        GZIPInputStream(ByteArrayInputStream(data)).use { gz ->
            val buf = ByteArray(8192)
            var total = 0
            while (true) {
                val n = gz.read(buf)
                if (n < 0) break
                total += n
                if (total > maxBytes) throw IllegalStateException("inflate overrun")
                out.write(buf, 0, n)
            }
        }
        return out.toByteArray()
    }

    fun verifySha256(container: ByteArray, fileBytes: ByteArray): Boolean {
        if (container.size < 49) return false
        val expected = container.copyOfRange(17, 49)
        val actual = MessageDigest.getInstance("SHA-256").digest(fileBytes)
        return expected.contentEquals(actual)
    }

    // ------------------------------------------------------------ stats

    val progress: Int
        get() {
            if (completed) return 100
            if (k <= 0) return 0
            val p = 100L * solvedCount / k
            return p.coerceIn(0L, 99L).toInt()
        }

    val speedBps: Long
        get() {
            if (speedWindow.size < 2) return 0L
            val now = System.currentTimeMillis()
            val last = speedWindow.last()
            if (now - last[0] > SPEED_WINDOW_MS) return 0L
            val first = speedWindow.first()
            val span = last[0] - first[0]
            if (span <= 0L) return 0L
            val delta = last[1] - first[1]
            if (delta <= 0L) return 0L
            val s = delta * 1000L / span
            if (s > peakSpeedBps) peakSpeedBps = s
            return s
        }

    val avgSpeedBps: Long
        get() {
            val elapsed = elapsedMs
            if (elapsed <= 0L) return 0L
            return (header?.totalLen ?: 0L) * 1000L / elapsed
        }

    val peakBps: Long get() = peakSpeedBps

    val elapsedMs: Long
        get() {
            if (endTime != 0L) return endTime - startTime
            return if (startTime == 0L) 0L else System.currentTimeMillis() - startTime
        }

    val readIntervalMs: Long get() = readInterval

    val totalLen: Long get() = header?.totalLen ?: 0L

    val totalSizeStr: String get() = formatSize(totalLen)

    val sourceBytesStr: String
        get() = formatSize(sourceBytesRead) + "/" + formatSize(totalLen)

    val bytesDetailStr: String
        get() {
            return "已接收有效Bytes ${sourceBytesRead}/${totalLen} · 帧${framesNew}(重${framesRedundant}) · K=$k"
        }

    fun reset() {
        header = null
        k = 0
        words = 0
        solved = null
        byBlock = HashMap()
        seen.clear()
        solvedCount = 0
        completed = false
        framesNew = 0
        framesDup = 0
        framesRedundant = 0
        sourceBytesRead = 0L
        startTime = 0L
        endTime = 0L
        lastFrameTime = 0L
        readInterval = 0L
        peakSpeedBps = 0L
        speedWindow.clear()
    }

    private fun recordSpeedSample(now: Long) {
        speedWindow.addLast(longArrayOf(now, sourceBytesRead))
        while (speedWindow.size > 1 && now - speedWindow.first()[0] > SPEED_WINDOW_MS) {
            speedWindow.removeFirst()
        }
    }

    companion object {
        const val HEADER_LEN = 20
        const val MAGIC0 = 0xD1
        const val MAGIC1 = 0x0D
        const val MAX_FILE_BYTES = 64 * 1024 * 1024
        private const val REPAIR_DEGREE_MIN = 4
        private const val REPAIR_DEGREE_MAX = 24
        private const val SPEED_WINDOW_MS = 2000L

        fun leU16(b: ByteArray, off: Int): Int =
            (b[off].toInt() and 0xFF) or ((b[off + 1].toInt() and 0xFF) shl 8)

        fun leU32(b: ByteArray, off: Int): Long =
            (b[off].toLong() and 0xFF) or ((b[off + 1].toLong() and 0xFF) shl 8) or
                ((b[off + 2].toLong() and 0xFF) shl 16) or ((b[off + 3].toLong() and 0xFF) shl 24)

        fun fnv1a(bytes: ByteArray): Long {
            var h = 0x811c9dc5L
            for (b in bytes) {
                h = ((h xor (b.toLong() and 0xFF)) * 0x01000193L) and 0xFFFFFFFFL
            }
            return h
        }

        // splitmix32 — integer ops only, so it is bit-identical with the Go
        // (uint32) and JS (`|0`/`>>>0`) implementations.
        class SplitMix32(seed: UInt) {
            private var s = seed
            fun next(): UInt {
                s += 0x9e3779b9u
                var t = s xor (s shr 16)
                t *= 0x21f0aaadu
                t = t xor (t shr 15)
                t *= 0x735a2d97u
                t = t xor (t shr 15)
                return t
            }
        }

        private fun frameSeed(sessionId: UShort, seq: UInt): UInt {
            var h = (sessionId.toUInt() + 1u) * 0x9e3779b1u xor (seq + 0x85ebca6bu)
            h = (h xor (h shr 13)) * 0xc2b2ae35u
            return h xor (h shr 16)
        }

        fun cycleLength(k: UShort): Int = 2 * k.toInt()

        fun frameComposition(k: UShort, sessionId: UShort, seq: UInt): List<UShort> {
            val pos = seq % cycleLength(k).toUInt()
            return if (pos < k.toUInt()) listOf(pos.toUShort()) else repairIndices(k, sessionId, seq)
        }

        private fun repairIndices(k: UShort, sessionId: UShort, seq: UInt): List<UShort> {
            val rnd = SplitMix32(frameSeed(sessionId, seq))
            var d = k.toInt()
            val ld = REPAIR_DEGREE_MIN + (rnd.next() % (REPAIR_DEGREE_MAX - REPAIR_DEGREE_MIN + 1).toUInt()).toInt()
            if (ld < d) d = ld
            val idx = ArrayList<UShort>(d)
            val seen = HashSet<UShort>()
            while (seen.size < d) {
                val b = (rnd.next() % k.toUInt()).toUShort()
                if (seen.add(b)) idx.add(b)
            }
            return idx
        }

        private fun safeFileName(name: String): String {
            var base = name.substringAfterLast('/', name).substringAfterLast('\\', name)
            base = base.filter { it.code >= 0x20 && it.code != 0x7f }.trim()
            if (base.isEmpty() || base == "." || base == "..") return "transfer.bin"
            return base
        }

        fun formatSize(bytes: Long): String = when {
            bytes < 1024 -> "${bytes}B"
            bytes < 1024 * 1024 -> "%.1fKB".format(bytes / 1024.0)
            else -> "%.1fMB".format(bytes / (1024.0 * 1024.0))
        }
    }
}

data class UnpackedFile(
    val name: String,
    val type: String,
    val bytes: ByteArray,
    val sha256: ByteArray,
)

/** DCF2 container packer — used by tests and for parity with the Go sender. */
object DecimenContainer {
    const val HEADER_LEN = 49
    private val magic = byteArrayOf(0x44, 0x43, 0x46, 0x32)

    fun pack(name: String, type: String, data: ByteArray): ByteArray {
        val nameBytes = name.toByteArray(Charsets.UTF_8)
        val typeBytes = type.toByteArray(Charsets.UTF_8)
        val useGzip = data.size >= 768
        val transmitted = if (useGzip) gzip(data) else data
        val out = ByteArray(HEADER_LEN + nameBytes.size + typeBytes.size + transmitted.size)
        magic.copyInto(out, 0)
        out[4] = if (useGzip) 1 else 0
        writeU16(out, 5, nameBytes.size)
        writeU16(out, 7, typeBytes.size)
        writeU32(out, 9, data.size.toLong())
        writeU32(out, 13, transmitted.size.toLong())
        val sha = MessageDigest.getInstance("SHA-256").digest(data)
        sha.copyInto(out, 17)
        nameBytes.copyInto(out, HEADER_LEN)
        typeBytes.copyInto(out, HEADER_LEN + nameBytes.size)
        transmitted.copyInto(out, HEADER_LEN + nameBytes.size + typeBytes.size)
        return out
    }

    private fun gzip(data: ByteArray): ByteArray {
        val out = ByteArrayOutputStream()
        GZIPOutputStream(out).use { it.write(data) }
        return out.toByteArray()
    }

    private fun writeU16(b: ByteArray, off: Int, v: Int) {
        b[off] = (v and 0xFF).toByte()
        b[off + 1] = ((v shr 8) and 0xFF).toByte()
    }

    private fun writeU32(b: ByteArray, off: Int, v: Long) {
        for (i in 0 until 4) b[off + i] = ((v shr (i * 8)) and 0xFF).toByte()
    }
}
