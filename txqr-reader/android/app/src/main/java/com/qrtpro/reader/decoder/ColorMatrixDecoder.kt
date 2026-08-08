package com.qrtpro.reader.decoder

class ColorMatrixDecodeException(message: String) : Exception(message)

/**
 * Color matrix optical transfer decoder (Kotlin mirror of the Go
 * qr.ColorMatrix implementation in txqr/qr/color.go).
 *
 * A matrix replaces the black/white QR module grid with four maximally
 * distinguishable colors (Black/Red/Green/Blue) carrying 2 bit/module.
 *
 * Layout (MxM modules, M = 96, coordinates row/col 0-based from top-left):
 *  - rows/cols 0..1: black border
 *  - corner markers at 3 corners (red TL, green TR, blue BL) for rotation
 *  - rows 4..7: calibration strip (black|red|green|blue 4 equal bars)
 *  - rows 8..9, cols 2..17: header cells (8 bytes MSB-first)
 *  - rows 10..93, cols 2..93: payload cells (2 bit/module, row-major)
 */
class ColorMatrixDecoder private constructor() {

    companion object {
        const val MATRIX_SIZE = 96
        private const val FRAME_W = 2
        private const val CAL_TOP = 4
        private const val CAL_H = 4
        private const val HDR_TOP = 8
        private const val HDR_WID = 16
        private const val PAY_TOP = 10

        val NOM: List<IntArray> = listOf(
            intArrayOf(0, 0, 0),      // 00 black
            intArrayOf(255, 0, 0),    // 01 red
            intArrayOf(0, 255, 0),    // 10 green
            intArrayOf(0, 0, 255),    // 11 blue
        )
        private val MAGIC = byteArrayOf('Q'.code.toByte(), 'F'.code.toByte())
        private const val HEADER_SIZE = 8

        val MAX_PAYLOAD_BYTES: Int =
            (MATRIX_SIZE - FRAME_W - PAY_TOP) * (MATRIX_SIZE - 2 * FRAME_W) * 2 / 8

        private fun payloadCols(): Pair<Int, Int> = FRAME_W to MATRIX_SIZE - FRAME_W

        private fun barWidth(): Int {
            val (lo, hi) = payloadCols()
            return (hi - lo) / 4
        }

        /**
         * Decodes a color matrix from an RGB pixel sampler (x, y) -> 0xRRGGBB.
         * The matrix is assumed to occupy the whole image: module scale =
         * width / MATRIX_SIZE.
         *
         * @throws DecodeException on format or CRC errors.
         */
        fun decode(sampler: (x: Int, y: Int) -> Int, width: Int, height: Int): ByteArray {
            if (width < MATRIX_SIZE || height < MATRIX_SIZE) {
                throw ColorMatrixDecodeException("image ${width}x$height too small")
            }
            val scale = width / MATRIX_SIZE
            val pick = { r: Int, c: Int ->
                val x = c * scale + scale / 2
                val y = r * scale + scale / 2
                rgb(sampler(x, y))
            }

            val rot = estimateRotation(pick)

            // Calibrated reference colors from the strip.
            val (lo, hi) = payloadCols()
            val barW = barWidth()
            val refs = MutableList(4) { i -> NOM[i].copyOf() }
            for (i in 0 until 4) {
                val acc = intArrayOf(0, 0, 0)
                var n = 0
                for (dy in 1 until CAL_H - 1) {
                    for (dx in 1 until barW - 1) {
                        val p = pickRel(pick, lo + i * barW + dx, CAL_TOP + dy, rot)
                        acc[0] += p[0]; acc[1] += p[1]; acc[2] += p[2]
                        n++
                    }
                }
                if (n > 0) {
                    refs[i] = intArrayOf(acc[0] / n, acc[1] / n, acc[2] / n)
                }
            }

            // Header bytes.
            val head = readHeader(pick, rot)
            if (head[0] != MAGIC[0] || head[1] != MAGIC[1]) throw ColorMatrixDecodeException("bad magic")
            if (crc16(head.copyOf(6)) != (head[6].toInt() and 0xFF shl 8) or (head[7].toInt() and 0xFF)) {
                throw ColorMatrixDecodeException("bad crc")
            }
            val plen = (head[4].toInt() and 0xFF) or (head[5].toInt() and 0xFF shl 8)
            if (plen <= 0 || plen > MAX_PAYLOAD_BYTES) throw ColorMatrixDecodeException("bad length $plen")

            // Payload stream.
            val out = ByteArray(plen)
            var bitPos = 0
            for (r in PAY_TOP until MATRIX_SIZE - FRAME_W) {
                for (c in lo until hi) {
                    if (bitPos >= plen * 8) return out
                    val p = pickRel(pick, c, r, rot)
                    val idx = nearest(refs, p)
                    val byteIdx = bitPos / 8
                    val shift = 8 - 2 - (bitPos % 8)
                    out[byteIdx] = (out[byteIdx].toInt() or (idx shl shift)).toByte()
                    bitPos += 2
                }
            }
            return out
        }

        private fun rgb(v: Int): IntArray = intArrayOf((v shr 16) and 0xFF, (v shr 8) and 0xFF, v and 0xFF)

        /** Reads the 2x16 header block back into bytes. */
        private fun readHeader(pick: (Int, Int) -> IntArray, rot: Int): ByteArray {
            val out = ByteArray(HEADER_SIZE)
            var pos = 0
            for (r in 0 until 2) {
                for (c in 0 until HDR_WID) {
                    if (pos >= HEADER_SIZE * 8) return out
                    val p = pickRel(pick, FRAME_W + c, HDR_TOP + r, rot)
                    val idx = nearest(NOM, p)
                    val byteIdx = pos / 8
                    val shift = 8 - 2 - (pos % 8)
                    out[byteIdx] = (out[byteIdx].toInt() or (idx shl shift)).toByte()
                    pos += 2
                }
            }
            return out
        }

        /** Resolves an absolute module column/row under rotation. */
        private fun pickRel(pick: (Int, Int) -> IntArray, c: Int, r: Int, rot: Int): IntArray {
            val last = MATRIX_SIZE - 1
            return when (rot) {
                90 -> pick(c, last - r)
                180 -> pick(last - r, last - c)
                270 -> pick(last - c, r)
                else -> pick(r, c)
            }
        }

        private fun estimateRotation(pick: (Int, Int) -> IntArray): Int {
            data class Corner(val r: Int, val c: Int)
            val corners = listOf(
                Corner(FRAME_W, FRAME_W),                                 // TL -> red
                Corner(FRAME_W, MATRIX_SIZE - FRAME_W - 2),               // TR -> green
                Corner(MATRIX_SIZE - FRAME_W - 2, FRAME_W),               // BL -> blue
            )
            var best = 0
            var bestScore = Int.MAX_VALUE
            for (rot in listOf(0, 90, 180, 270)) {
                var score = 0
                for (i in corners.indices) {
                    val p = pickRel(pick, corners[i].c, corners[i].r, rot)
                    score += distSq(NOM[i + 1], p)
                }
                if (score < bestScore) {
                    bestScore = score
                    best = rot
                }
            }
            return best
        }

        private fun distSq(a: IntArray, b: IntArray): Int {
            val d0 = a[0] - b[0]; val d1 = a[1] - b[1]; val d2 = a[2] - b[2]
            return d0 * d0 + d1 * d1 + d2 * d2
        }

        /** Returns the reference index closest (Euclidean) to p. */
        private fun nearest(refs: List<IntArray>, p: IntArray): Int {
            var best = 0
            var bd = Int.MAX_VALUE
            for (i in refs.indices) {
                val d = distSq(refs[i], p)
                if (d < bd) {
                    bd = d
                    best = i
                }
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
}
