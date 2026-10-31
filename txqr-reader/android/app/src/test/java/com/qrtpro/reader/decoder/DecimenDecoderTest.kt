package com.qrtpro.reader.decoder

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Golden vectors shared with txqr/decimen/decimen_test.go and the
 * decimen-optical-transfer reference (tests/fountain.test.ts). If one of
 * these fails the Kotlin receiver has diverged from the Go sender — senders
 * and receivers derive frame subsets independently and never compare notes,
 * so a one-bit difference here silently never-completes a transfer.
 */
class DecimenDecoderTest {

    private fun testPayload(byteLength: Int): ByteArray {
        val b = ByteArray(byteLength)
        for (i in 0 until byteLength) {
            b[i] = ((i * 37 + (i ushr 8) * 11) and 0xFF).toByte()
        }
        return b
    }

    @Test
    fun streamFingerprint() {
        data class Golden(val k: Int, val blockLen: Int, val sessionId: Long, val want: String)
        val golden = listOf(
            Golden(1, 64, 1, "k=1 fnv=0xf6a115c5"),
            Golden(23, 64, 7, "k=23 fnv=0x4a5d3eaa"),
            Golden(179, 2933, 4242, "k=179 fnv=0x54f78d05"),
            Golden(716, 1445, 65535, "k=716 fnv=0x75b73b85"),
        )
        for (g in golden) {
            val payload = testPayload(g.k * g.blockLen - 7)
            val encoder = TestEncoder(payload, g.blockLen, g.sessionId)
            assertEquals(g.k, encoder.k)
            val stream = ByteArray(64 * g.blockLen)
            for (seq in 0 until 64) {
                val block = encoder.encode(seq)
                assertEquals(g.blockLen, block.size)
                block.copyInto(stream, seq * g.blockLen)
            }
            val actual = "k=${g.k} fnv=0x" + fnvHex(DecimenDecoder.fnv1a(stream))
            assertEquals(g.want, actual)
        }
    }

    @Test
    fun frameCompositionIsSystematicThenRepair() {
        for (k in intArrayOf(1, 17, 179, 4096)) {
            val cycle = DecimenDecoder.cycleLength(k.toUShort())
            for (pos in setOf(0, k / 2, k - 1)) {
                val sweep = DecimenDecoder.frameComposition(k.toUShort(), 9.toUShort(), pos.toUInt())
                assertEquals(listOf(pos.toUShort()), sweep)
                val later = DecimenDecoder.frameComposition(
                    k.toUShort(), 9.toUShort(),
                    (pos + 6 * cycle).toUInt()
                )
                assertEquals(listOf(pos.toUShort()), later)
            }
            for (seq in intArrayOf(k, k + 1, 2 * k - 1)) {
                val idx = DecimenDecoder.frameComposition(k.toUShort(), 9.toUShort(), seq.toUInt())
                val lo = minOf(k, 4)
                val hi = minOf(k, 24)
                assertTrue("k=$k seq=$seq degree ${idx.size}", idx.size in lo..hi)
                assertEquals("k=$k seq=$seq duplicate", idx.size, idx.toSet().size)
            }
        }
    }

    @Test
    fun singleBlockCompletesOnFirstFrame() {
        val payload = testPayload(900)
        val enc = TestEncoder(payload, 2933, 5)
        assertEquals(1, enc.k)
        val frame = enc.frameAt(0)
        val dec = DecimenDecoder()
        assertTrue(dec.isDecimenFrame(frame))
        assertTrue(dec.addFrame(frame))
        assertNotNull(dec.assemble())
    }

    @Test
    fun fullRoundTripWithLoss() {
        val file = testPayload(50000)
        val container = DecimenContainer.pack("hello.bin", "application/octet-stream", file)
        val enc = TestEncoder(container, 1445, 11)
        val dec = DecimenDecoder()
        var processed = 0
        val ceiling = enc.k * 6 + 1000
        var seq = 0
        while (!dec.isComplete && seq < ceiling) {
            if (seq % 7 != 0) { // ~14% deterministic loss
                dec.addFrame(enc.frameAt(seq))
                processed++
            }
            seq++
        }
        assertTrue("did not complete in $processed frames", dec.isComplete)
        val recovered = dec.assemble()
        assertEquals(container.size, recovered!!.size)
        assertTrue(container.contentEquals(recovered))
        val unpacked = dec.unpackContainer(recovered)
        assertEquals("hello.bin", unpacked.name)
        assertTrue(file.contentEquals(unpacked.bytes))
        assertTrue(dec.verifySha256(recovered, unpacked.bytes))
    }

    @Test
    fun foreignSessionIgnoredUntilReset() {
        val first=DecimenContainer.pack("a.bin","application/octet-stream",testPayload(20000))
        val second=DecimenContainer.pack("b.bin","application/octet-stream",testPayload(21000))
        val enc1=TestEncoder(first,1445,21);val enc2=TestEncoder(second,1445,22)
        val dec=DecimenDecoder()
        dec.addFrame(enc1.frameAt(0));dec.addFrame(enc2.frameAt(1))
        assertEquals(1,dec.framesNew)
        for(seq in 1 until enc1.k) dec.addFrame(enc1.frameAt(seq))
        assertTrue(first.contentEquals(dec.assemble()))
        dec.reset()
        for(seq in 0 until enc2.k) dec.addFrame(enc2.frameAt(seq))
        assertTrue(second.contentEquals(dec.assemble()))
        assertEquals("b.bin",dec.verifiedResult()!!.name)
    }

    @Test
    fun corruptContainerFailsOnceAndRequiresRescan() {
        val file=testPayload(500)
        val container=DecimenContainer.pack("a.bin","application/octet-stream",file)
        val enc=TestEncoder(container,1445,21)
        val bad=enc.frameAt(0);bad[20+49]=(bad[20+49].toInt() xor 1).toByte()
        val dec=DecimenDecoder();dec.addFrame(bad)
        assertNull(dec.verifiedResult());assertNotNull(dec.failure)
        val frames=dec.framesNew
        dec.addFrame(enc.frameAt(2));assertEquals(frames,dec.framesNew)
        assertNull(dec.verifiedResult())
        dec.reset();dec.addFrame(enc.frameAt(0));assertTrue(file.contentEquals(dec.verifiedResult()!!.bytes))
    }

    @Test
    fun rejectsInconsistentHeaderBeforeAllocation() {
        val enc=TestEncoder(byteArrayOf(1),1,1)
        val frame=enc.frameAt(0)
        frame[12]=0x40;frame[13]=0x42;frame[14]=0x0f // total 1,000,000 vs K=1 B=1
        val dec=DecimenDecoder();assertNull(dec.parseFrame(frame));assertFalse(dec.addFrame(frame));assertNull(dec.assemble())
    }

    @Test
    fun rejectsNonDecimenFrames() {
        val dec = DecimenDecoder()
        assertFalse(dec.isDecimenFrame(byteArrayOf(0x01, 0x02)))
        assertFalse(dec.isDecimenFrame(ByteArray(19)))
    }

    private fun assertFalse(v: Boolean) = assertTrue(!v)

    private fun fnvHex(v: Long): String {
        return "%08x".format(v and 0xFFFFFFFFL)
    }
}

/** Minimal encoder mirroring the reference LTEncoder, used to drive the
 *  decoder tests without depending on the Go package. */
class TestEncoder(payload: ByteArray, val blockLen: Int, val sessionId: Long) {
    val k: Int = maxOf(1, (payload.size + blockLen - 1) / blockLen)
    private val words = (blockLen + 3) / 4
    private val blocks = IntArray(k * words)
    private val payloadLen = payload.size
    private val payloadFnv = DecimenDecoder.fnv1a(payload)

    init {
        val buf = ByteArray(k * words * 4)
        for (b in 0 until k) {
            val src = payload.copyOfRange(
                b * blockLen,
                minOf((b + 1) * blockLen, payload.size)
            )
            src.copyInto(buf, b * words * 4)
        }
        for (i in blocks.indices) {
            var v = 0
            for (j in 0 until 4) {
                v = v or ((buf[i * 4 + j].toInt() and 0xFF) shl (j * 8))
            }
            blocks[i] = v
        }
    }

    fun frameAt(seq: Int): ByteArray {
        val frame = ByteArray(DecimenDecoder.HEADER_LEN + blockLen)
        frame[0] = DecimenDecoder.MAGIC0.toByte()
        frame[1] = DecimenDecoder.MAGIC1.toByte()
        writeU16(frame, 2, sessionId.toInt() and 0xFFFF)
        writeU32(frame, 4, seq.toLong())
        writeU16(frame, 8, k)
        writeU16(frame, 10, blockLen)
        writeU32(frame, 12, payloadLen.toLong())
        writeU32(frame, 16, payloadFnv)
        encode(seq).copyInto(frame, DecimenDecoder.HEADER_LEN)
        return frame
    }

    fun encode(seq: Int): ByteArray {
        val idx = DecimenDecoder.frameComposition(k.toUShort(), sessionId.toUShort(), seq.toUInt())
        val out = IntArray(words)
        for (b in idx) {
            val off = b.toInt() * words
            for (w in 0 until words) out[w] = out[w] xor blocks[off + w]
        }
        val buf = ByteArray(words * 4)
        for (i in out.indices) {
            for (j in 0 until 4) buf[i * 4 + j] = ((out[i] shr (j * 8)) and 0xFF).toByte()
        }
        return buf.copyOf(blockLen)
    }

    private fun writeU16(b: ByteArray, off: Int, v: Int) {
        b[off] = (v and 0xFF).toByte()
        b[off + 1] = ((v shr 8) and 0xFF).toByte()
    }

    private fun writeU32(b: ByteArray, off: Int, v: Long) {
        for (i in 0 until 4) b[off + i] = ((v shr (i * 8)) and 0xFF).toByte()
    }
}
