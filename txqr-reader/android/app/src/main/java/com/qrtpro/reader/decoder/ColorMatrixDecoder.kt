package com.qrtpro.reader.decoder

/**
 * Color matrix optical transfer decoder (Kotlin mirror of Go color.go).
 *
 * Layout (MxM modules, M = 96, 0-based row/col from top-left):
 *  - rows/cols 0..1: black border (2 modules)
 *  - rows/cols 2: quiet zone (white, 1 module)
 *  - rows/cols 3..9: 7×7 QR-style finder patterns at TL, TR, BL
 *  - rows 11..14: calibration strip (black|red|green|blue 4 bars)
 *  - rows 15..16: header block (16 cols, 8 bytes MSB-first)
 *  - rows 17..85: payload cells (2 bit/module, row-major)
 */
class ColorMatrixDecodeException(message: String) : Exception(message)

object ColorMatrixDecoder {

    const val MATRIX_SIZE = 96
    private const val FRAME_W = 2

    const val FINDER_SZ = 7
    const val FINDER_OFF = FRAME_W + 1 // 3 — 1-mod quiet zone

    private const val CAL_TOP = FINDER_OFF + FINDER_SZ + 1 // 11
    private const val CAL_H = 4
    private const val HDR_TOP = CAL_TOP + CAL_H              // 15
    private const val HDR_WID = 16
    private const val PAY_TOP = HDR_TOP + 2                  // 17
    private const val PAY_END = MATRIX_SIZE - FINDER_OFF - FINDER_SZ // 86

    val NOM: List<IntArray> = listOf(
        intArrayOf(0, 0, 0),      // 00 black
        intArrayOf(255, 0, 0),    // 01 red
        intArrayOf(0, 255, 0),    // 10 green
        intArrayOf(0, 0, 255),    // 11 blue
    )
    private val MAGIC = byteArrayOf('Q'.code.toByte(), 'F'.code.toByte())
    private const val HEADER_SIZE = 8

    val MAX_PAYLOAD_BYTES: Int =
        (PAY_END - PAY_TOP) * (MATRIX_SIZE - 2 * FRAME_W) * 2 / 8

    private fun payloadCols(): Pair<Int, Int> = FRAME_W to MATRIX_SIZE - FRAME_W

    private fun barWidth(): Int {
        val (lo, hi) = payloadCols()
        return (hi - lo) / 4
    }

    /** Decode with an externally-computed affine sampler: (moduleX, moduleY) → 0xRRGGBB. */
    fun decodeWithAffine(sample: (col: Int, row: Int) -> Int): ByteArray {
        val pick = { r: Int, c: Int ->
            val v = sample(c, r)
            intArrayOf((v shr 16) and 0xFF, (v shr 8) and 0xFF, v and 0xFF)
        }

        val (lo, hi) = payloadCols()
        val barW = barWidth()
        val refs = MutableList(4) { i -> NOM[i].copyOf() }
        for (i in 0 until 4) {
            val acc = intArrayOf(0, 0, 0); var n = 0
            for (dy in 1 until CAL_H - 1) {
                for (dx in 1 until barW - 1) {
                    val p = pick(CAL_TOP + dy, lo + i * barW + dx)
                    acc[0] += p[0]; acc[1] += p[1]; acc[2] += p[2]; n++
                }
            }
            if (n > 0) refs[i] = intArrayOf(acc[0] / n, acc[1] / n, acc[2] / n)
        }

        val head = readHeader(pick)
        if (head[0] != MAGIC[0] || head[1] != MAGIC[1]) throw ColorMatrixDecodeException("bad magic")
        val crc = (head[6].toInt() and 0xFF shl 8) or (head[7].toInt() and 0xFF)
        if (crc16(head.copyOf(6)) != crc) throw ColorMatrixDecodeException("bad crc")
        val plen = (head[4].toInt() and 0xFF) or (head[5].toInt() and 0xFF shl 8)
        if (plen <= 0 || plen > MAX_PAYLOAD_BYTES) throw ColorMatrixDecodeException("bad length $plen")

        val out = ByteArray(plen)
        var bitPos = 0
        for (r in PAY_TOP until PAY_END) {
            for (c in lo until hi) {
                if (bitPos >= plen * 8) return out
                val idx = nearest(refs, pick(r, c))
                val byteIdx = bitPos / 8
                out[byteIdx] = (out[byteIdx].toInt() or (idx shl (8 - 2 - (bitPos % 8)))).toByte()
                bitPos += 2
            }
        }
        return out
    }

    /** Direct decode: assumes the matrix fills the entire image at the given width. */
    fun decode(rgb: (x: Int, y: Int) -> Int, width: Int, height: Int): ByteArray {
        if (width < MATRIX_SIZE || height < MATRIX_SIZE) {
            throw ColorMatrixDecodeException("image ${width}x$height too small")
        }
        val scale = width / MATRIX_SIZE
        val pick = { r: Int, c: Int ->
            val v = rgb(c * scale + scale / 2, r * scale + scale / 2)
            intArrayOf((v shr 16) and 0xFF, (v shr 8) and 0xFF, v and 0xFF)
        }

        val (lo, hi) = payloadCols()
        val barW = barWidth()
        val refs = MutableList(4) { i -> NOM[i].copyOf() }
        for (i in 0 until 4) {
            val acc = intArrayOf(0, 0, 0); var n = 0
            for (dy in 1 until CAL_H - 1) {
                for (dx in 1 until barW - 1) {
                    val p = pick(CAL_TOP + dy, lo + i * barW + dx)
                    acc[0] += p[0]; acc[1] += p[1]; acc[2] += p[2]; n++
                }
            }
            if (n > 0) refs[i] = intArrayOf(acc[0] / n, acc[1] / n, acc[2] / n)
        }

        // Header.
        val head = readHeader(pick)
        if (head[0] != MAGIC[0] || head[1] != MAGIC[1]) throw ColorMatrixDecodeException("bad magic")
        val crc = (head[6].toInt() and 0xFF shl 8) or (head[7].toInt() and 0xFF)
        if (crc16(head.copyOf(6)) != crc) throw ColorMatrixDecodeException("bad crc")
        val plen = (head[4].toInt() and 0xFF) or (head[5].toInt() and 0xFF shl 8)
        if (plen <= 0 || plen > MAX_PAYLOAD_BYTES) throw ColorMatrixDecodeException("bad length $plen")

        val out = ByteArray(plen)
        var bitPos = 0
        for (r in PAY_TOP until PAY_END) {
            for (c in lo until hi) {
                if (bitPos >= plen * 8) return out
                val idx = nearest(refs, pick(r, c))
                val byteIdx = bitPos / 8
                out[byteIdx] = (out[byteIdx].toInt() or (idx shl (8 - 2 - (bitPos % 8)))).toByte()
                bitPos += 2
            }
        }
        return out
    }

    private fun readHeader(pick: (Int, Int) -> IntArray): ByteArray {
        val out = ByteArray(HEADER_SIZE)
        var pos = 0
        for (r in 0 until 2) {
            for (c in 0 until HDR_WID) {
                if (pos >= HEADER_SIZE * 8) return out
                val idx = nearest(NOM, pick(HDR_TOP + r, FRAME_W + c))
                val byteIdx = pos / 8
                out[byteIdx] = (out[byteIdx].toInt() or (idx shl (8 - 2 - (pos % 8)))).toByte()
                pos += 2
            }
        }
        return out
    }

    private fun nearest(refs: List<IntArray>, p: IntArray): Int {
        var best = 0; var bd = Int.MAX_VALUE
        for (i in refs.indices) {
            val d0 = refs[i][0] - p[0]
            val d1 = refs[i][1] - p[1]
            val d2 = refs[i][2] - p[2]
            val d = d0 * d0 + d1 * d1 + d2 * d2
            if (d < bd) { bd = d; best = i }
        }
        return best
    }

    private fun crc16(b: ByteArray, len: Int = b.size): Int {
        var crc = 0xFFFF
        for (i in 0 until len) {
            crc = crc xor (b[i].toInt() and 0xFF shl 8)
            for (j in 0 until 8) {
                crc = if (crc and 0x8000 != 0) (crc shl 1) xor 0x1021 else crc shl 1
            }
        }
        return crc and 0xFFFF
    }
}
