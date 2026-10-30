package com.qrtpro.reader.decoder

import org.junit.Assert.*
import org.junit.Test
import java.util.Base64

class DecodeResourcesTest {
    private fun frames(path:String)=requireNotNull(javaClass.getResourceAsStream(path)).bufferedReader()
        .use{it.readLines().filter(String::isNotBlank).map{line->Base64.getDecoder().decode(line)}}

    @Test fun fileBudgetRejectsBeforeDecoderAllocation() {
        val input=frames("/regression/single.txt")
        val decoder=TxqrDecoder(memoryBudget=16)
        decoder.decodeFrame(input[1])
        assertNotNull(decoder.lastError);assertFalse(decoder.isCompleted)
        assertEquals(0L,decoder.sourceBytesCount);assertEquals(0L,decoder.totalBytesCount)
    }
    @Test fun raptorQRepairBudgetFailsButSystematicPathCanRecover() {
        val input=frames("/codecmatrix/raptorq.txt")
        val expected=frames("/codecmatrix/expected.txt")[0]
        val decoder=TxqrDecoder(memoryBudget=65536)
        for(frame in input.drop(16)) decoder.decodeFrame(frame)
        assertNotNull(decoder.lastError);assertFalse(decoder.isCompleted);assertNull(decoder.dataBytes)
        decoder.reset()
        for(frame in input.take(16)) decoder.decodeFrame(frame)
        assertNull(decoder.lastError);assertArrayEquals(expected,decoder.dataBytes)
    }
    @Test(expected=DecodeResourceLimit::class) fun sparseRowsStopAtRetainedMemoryBudget() {
        val matrix=SparseMatrix(3,memoryBudget=64)
        matrix.addEquation(intArrayOf(0,1,2),Block(ByteArray(32)))
        matrix.addEquation(intArrayOf(1,2),Block(ByteArray(32)))
    }
    @Test fun decimenBudgetRejectsBeforeAccumulatingFrames() {
        val encoder=TestEncoder(ByteArray(100),1445,1)
        val decoder=DecimenDecoder(memoryBudget=64)
        assertFalse(decoder.addFrame(encoder.frameAt(0)))
        assertNotNull(decoder.failure);assertEquals(0,decoder.framesNew);assertNull(decoder.assemble())
    }
    @Test(expected=DecodeResourceLimit::class) fun compressionCannotBypassFileMemoryBudget() {
        val container=DecimenContainer.pack("zeros.bin","application/octet-stream",ByteArray(4096))
        DecimenDecoder(memoryBudget=4096).unpackContainer(container)
    }
}
