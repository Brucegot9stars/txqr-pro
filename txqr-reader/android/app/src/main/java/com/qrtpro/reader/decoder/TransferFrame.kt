package com.qrtpro.reader.decoder

import java.util.zip.CRC32

/** QRT2 header and limits are shared with Go's frame.go. Validate before state changes. */
data class TransferFrame(
    val session: String,
    val sha256: String,
    val blockCode: Long,
    val chunkLen: Int,
    val total: Int,
    val codec: CodecType,
    val isName: Boolean,
    val payload: ByteArray,
) {
    fun sameTransfer(other: TransferFrame): Boolean =
        session == other.session && sha256 == other.sha256 && chunkLen == other.chunkLen &&
            total == other.total && codec == other.codec

    companion object {
        const val MAX_TRANSFER_BYTES = 64 * 1024 * 1024
        const val MAX_SOURCE_BLOCKS = 8192
        const val MAX_CHUNK_BYTES = 2933
        const val MAX_HEADER_BYTES = 160
        const val MAX_BLOCK_CODE = 0xffffffL
        private val sessionPattern = Regex("[0-9a-f]{32}")
        private val digestPattern = Regex("[0-9a-f]{64}")
        private val crcPattern = Regex("[0-9a-fA-F]{8}")

        fun parse(bytes: ByteArray): TransferFrame? {
            if (bytes.size > MAX_HEADER_BYTES + MAX_CHUNK_BYTES + 1) return null
            val sep = bytes.indexOf('|'.code.toByte())
            if (sep !in 1..MAX_HEADER_BYTES) return null
            val header = String(bytes, 0, sep, Charsets.ISO_8859_1)
            val p = header.split('/')
            if (p.size != 9 || p[0] != "QRT2" || !sessionPattern.matches(p[1]) || !digestPattern.matches(p[2])) return null
            val code = p[3].toLongOrNull() ?: return null
            val chunk = p[4].toIntOrNull() ?: return null
            val total = p[5].toIntOrNull() ?: return null
            if (chunk !in 1..MAX_CHUNK_BYTES || total !in 1..MAX_TRANSFER_BYTES) return null
            val codec = CodecType.fromString(p[6])
            if ((if (codec == CodecType.LT) "lt" else codec.id) != p[6]) return null
            val k = numberOfChunks(total, chunk)
            if (k !in 1..MAX_SOURCE_BLOCKS || (codec == CodecType.RAPTOR && k < 4)) return null
            val name = p[7] == "name"
            if ((!name && p[7] != "data") || (name && code != -1L) ||
                (!name && code !in 0L..MAX_BLOCK_CODE) || (codec == CodecType.RAPTOR && code > 65535L)) return null
            val length = bytes.size - sep - 1
            val expected = if (codec == CodecType.RAPTORQ) chunk else numberOfChunks(total, k)
            if (name) {
                if (length !in 1..360) return null
            } else if (length != expected && !(codec == CodecType.BINARY && length == 0)) return null
            if (!crcPattern.matches(p[8])) return null
            val prefixLength = header.lastIndexOf('/')
            val crc = CRC32().apply {
                update(bytes, 0, prefixLength)
                update('|'.code)
                update(bytes, sep + 1, length)
            }
            if (crc.value != p[8].toLong(16)) return null
            return TransferFrame(p[1], p[2], code, chunk, total, codec, name, bytes.copyOfRange(sep + 1, bytes.size))
        }
    }
}
