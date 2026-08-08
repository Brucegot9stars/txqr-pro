package com.qrtpro.reader.decoder

import org.junit.Assert.assertArrayEquals
import org.junit.Before
import org.junit.Test
import java.io.File
import java.nio.file.Files

class ColorMatrixDecoderTest {

    private lateinit var rgba: ByteArray
    private var width = 0
    private var height = 0
    private lateinit var payload: ByteArray

    @Before
    fun setup() {
        val tmp = File(System.getProperty("java.io.tmpdir"))
        val bin = tmp.listFiles { f -> f.name == "rgba.bin" }?.firstOrNull()
            ?: throw IllegalStateException("rgba.bin not found in ${tmp.absolutePath}")
        val pay = tmp.listFiles { f -> f.name == "payload.bin" }?.firstOrNull()
            ?: throw IllegalStateException("payload.bin not found")
        rgba = Files.readAllBytes(bin.toPath())
        payload = Files.readAllBytes(pay.toPath())
        val side = kotlin.math.sqrt((rgba.size / 4).toDouble()).toInt()
        width = side
        height = side
    }

    private fun sampler() = { x: Int, y: Int ->
        val ofs = (y * width + x) * 4
        ((rgba[ofs].toInt() and 0xFF) shl 16) or
            ((rgba[ofs + 1].toInt() and 0xFF) shl 8) or
            (rgba[ofs + 2].toInt() and 0xFF)
    }

    @Test
    fun decodesGoEncodedMatrix() {
        val result = ColorMatrixDecoder.decode(sampler(), width, height)
        assertArrayEquals(payload, result)
    }

    @Test
    fun maximalPayloadSize() {
        val capacity = ColorMatrixDecoder.MAX_PAYLOAD_BYTES
        assert(capacity == 1932) { "expected 1932, got $capacity" }
    }

    @Test
    fun rejectsTinyImage() {
        val threw = try {
            ColorMatrixDecoder.decode(sampler(), 10, 10)
            false
        } catch (e: ColorMatrixDecodeException) {
            true
        }
        assert(threw) { "expected ColorMatrixDecodeException" }
    }
}