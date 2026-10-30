package com.qrtpro.reader.decoder

import org.junit.Assert.*
import org.junit.Test
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.security.MessageDigest
import java.util.zip.CRC32

class ReceiverCapacityTest {
    private fun packet(id:Long,split:Int,total:Int,codec:String,payload:ByteArray,source:ByteArray=byteArrayOf()):ByteArray {
        val sha=MessageDigest.getInstance("SHA-256").digest(source).joinToString(""){"%02x".format(it.toInt() and 255)}
        val header="QRT2/${"0".repeat(32)}/$sha/$id/$split/$total/$codec/data"
        val crc=CRC32().apply{update(header.toByteArray());update('|'.code);update(payload)}
        return "$header/${crc.value.toString(16).padStart(8,'0')}|".toByteArray()+payload
    }
    @Test fun largeRaptorQSessionIsNotRejectedByAnEstimate() {
        val decoder=TxqrDecoder()
        decoder.decodeFrame(packet(0,2048,8 shl 20,"raptorq",ByteArray(2048)))
        assertNull(decoder.lastError);assertEquals(8L shl 20,decoder.totalBytesCount)
        assertEquals(2048L,decoder.sourceBytesCount);assertFalse(decoder.isCompleted)
    }
    @Test fun redundantSymbolsDoNotExhaustAFrameBudget() {
        val source="AB".toByteArray();val decoder=TxqrDecoder();val codec=LubyCodec(2)
        var id=0L;var other=0L;var count=0
        while(count<300) {
            val picks=codec.pickIndices(id)
            if(picks.size==1) {
                if(picks[0]==0) {
                    decoder.decodeFrame(packet(id,1,2,"lt",source.copyOfRange(0,1),source));count++
                } else other=id
            }
            id++
        }
        assertNull(decoder.lastError);assertFalse(decoder.isCompleted)
        decoder.decodeFrame(packet(other,1,2,"lt",source.copyOfRange(1,2),source))
        assertArrayEquals(source,decoder.dataBytes)
    }
    @Test fun decimenAcceptsAValidLargeContainerWithoutAnEstimate() {
        val total=64*1024*1024+1;val block=2933;val k=(total+block-1)/block
        val frame=ByteBuffer.allocate(20+block).order(ByteOrder.LITTLE_ENDIAN)
            .put(0xd1.toByte()).put(0x0d.toByte()).putShort(1).putInt(0).putShort(k.toShort())
            .putShort(block.toShort()).putInt(total).putInt(0).array()
        val decoder=DecimenDecoder();assertFalse(decoder.addFrame(frame))
        assertNull(decoder.failure);assertEquals(1,decoder.framesNew)
    }
}
