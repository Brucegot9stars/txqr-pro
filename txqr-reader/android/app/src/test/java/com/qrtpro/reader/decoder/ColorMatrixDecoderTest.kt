package com.qrtpro.reader.decoder

import org.junit.Assert.assertArrayEquals
import org.junit.Before
import org.junit.Test
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue

class ColorMatrixDecoderTest {

    private lateinit var rgba: ByteArray
    private var width = 0
    private var height = 0
    private lateinit var payload: ByteArray

    @Before
    fun setup() {
        width = ColorMatrixDecoder.MATRIX_SIZE
        height = ColorMatrixDecoder.MATRIX_SIZE
        rgba = requireNotNull(javaClass.getResourceAsStream("/regression/color.rgba")).use { it.readBytes() }
        assertEquals(width * height * 4, rgba.size)
        payload=requireNotNull(javaClass.getResourceAsStream("/regression/color-payload.bin")).use{it.readBytes()}
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
        assertEquals(1587,capacity)
    }

    @Test
    fun rejectsTinyImage() {
        val threw = try {
            ColorMatrixDecoder.decode(sampler(), 10, 10)
            false
        } catch (e: ColorMatrixDecodeException) {
            true
        }
        assertTrue("expected ColorMatrixDecodeException",threw)
    }
}