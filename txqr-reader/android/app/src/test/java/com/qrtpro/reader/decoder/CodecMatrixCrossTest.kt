package com.qrtpro.reader.decoder

import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.Base64

/**
 * Cross-language golden vectors: the frames were produced by the Go sender
 * (txqr/.workbuddy/gen) for every codec in the dropdown. If one of these
 * fails, the Kotlin receiver can no longer decode what the Go sender emits
 * for that scheme — the two implementations derive block subsets
 * independently and never compare notes at runtime.
 */
class CodecMatrixCrossTest {

    private fun lines(name: String): List<String> {
        val s = requireNotNull(javaClass.classLoader.getResourceAsStream("codecmatrix/$name")) {
            "missing resource codecmatrix/$name"
        }
        return s.bufferedReader().readLines().filter { it.isNotBlank() }
    }

    private fun runCase(codec: String) {
        val expected = Base64.getDecoder().decode(lines("expected.txt")[0])
        val frames = lines("$codec.txt").map { Base64.getDecoder().decode(it) }

        val dec = TxqrDecoder()
        var used = 0
        for (f in frames) {
            dec.decodeFrame(f)
            used++
            if (dec.isCompleted) break
        }
        val got = dec.dataBytes
        println("$codec: 用了 $used/${frames.size} 帧, 完成=${dec.isCompleted}")
        assertNotNull("$codec 未解出", got)
        assertTrue("$codec 解出的数据与发送端不一致", got!!.contentEquals(expected))
    }

    @Test fun lt() = runCase("lt")
    @Test fun binary() = runCase("binary")
    @Test fun raptor() = runCase("raptor")
    @Test fun raptorq() = runCase("raptorq")
    @Test fun online() = runCase("online")
}
