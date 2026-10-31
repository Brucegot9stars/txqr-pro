package com.qrtpro.reader.decoder

import org.junit.Assert.*
import org.junit.Test
import java.util.Base64
import java.util.zip.CRC32

class VerifiedTransferTest {
    private fun lines(name:String)=requireNotNull(javaClass.getResourceAsStream("/regression/$name")).bufferedReader().use{it.readLines().filter(String::isNotBlank)}
    private fun fixture(name:String)=lines(name).map{Base64.getDecoder().decode(it)}
    private fun runFixture(name:String) {
        val all=fixture(name); val decoder=TxqrDecoder()
        for(frame in all.drop(1)) {decoder.decodeFrame(frame);if(decoder.isCompleted) break}
        assertNull(decoder.lastError)
        assertTrue("$name did not complete",decoder.isCompleted)
        assertArrayEquals(all[0],decoder.dataBytes)
    }
    private fun repack(bytes:ByteArray, change:(MutableList<String>,ByteArray)->Unit):ByteArray {
        val sep=bytes.indexOf('|'.code.toByte())
        val fields=String(bytes,0,sep,Charsets.ISO_8859_1).split('/').toMutableList()
        val payload=bytes.copyOfRange(sep+1,bytes.size)
        change(fields,payload)
        val prefix=fields.take(8).joinToString("/")
        val crc=CRC32().apply{update(prefix.toByteArray());update('|'.code);update(payload)}.value
        return "$prefix/${crc.toString(16).padStart(8,'0')}|".toByteArray()+payload
    }
    @Test fun onlineOverflowCounterexample()=runFixture("online-large.txt")
    @Test fun exactSingleBlock()=runFixture("single.txt")
    @Test fun paddedNonMultiple()=runFixture("nonmultiple.txt")
    @Test fun rankDeficiencyNeedsAnotherSymbol() {
        val all=fixture("rq-rank.txt");val d=TxqrDecoder()
        for(f in all.drop(1).take(8)) d.decodeFrame(f)
        assertFalse(d.isCompleted);assertNull(d.dataBytes)
        d.decodeFrame(all.last())
        assertTrue(d.isCompleted);assertArrayEquals(all[0],d.dataBytes)
    }
    @Test fun corruptionRejectedBeforeSessionAndDedup() {
        val all=fixture("single.txt");val good=all[1];val bad=good.copyOf()
        bad[bad.lastIndex]=(bad.last().toInt() xor 1).toByte()
        val d=TxqrDecoder();d.decodeFrame(bad)
        assertEquals(0L,d.totalBytesCount);assertEquals(0L,d.sourceBytesCount)
        d.decodeFrame(good);assertArrayEquals(all[0],d.dataBytes)
    }
    @Test fun finalHashRejectsIncorrectEquationWithValidCRC() {
        val f=repack(fixture("single.txt")[1]){_,p->p[0]=(p[0].toInt() xor 1).toByte()}
        val d=TxqrDecoder();d.decodeFrame(f)
        assertFalse(d.isCompleted);assertNull(d.dataBytes);assertNotNull(d.lastError)
        d.reset();d.decodeFrame(fixture("single.txt")[1]);assertTrue(d.isCompleted)
    }
    @Test fun foreignSessionCannotContribute() {
        val all=fixture("rq-rank.txt");val d=TxqrDecoder();d.decodeFrame(all[1])
        val before=d.sourceBytesCount
        d.decodeFrame(repack(all[2]){f,_->f[1]=if(f[1][0]=='0') "1"+f[1].drop(1) else "0"+f[1].drop(1)})
        assertEquals(before,d.sourceBytesCount);assertFalse(d.isCompleted)
        for(f in all.drop(2)) d.decodeFrame(f)
        assertArrayEquals(all[0],d.dataBytes)
    }
    @Test fun invalidParametersAndLegacyAreRejected() {
        val f=fixture("single.txt")[1]
        for((index,value) in listOf(3 to "-2",4 to "0",5 to "67108865",6 to "unknown")) {
            assertNull(TransferFrame.parse(repack(f){fields,_->fields[index]=value}))
        }
        assertNull(TransferFrame.parse("0/8/8|12345678".toByteArray()))
    }
    @Test fun onlineIndicesMatchGoAtOverflowBoundary() {
        for(line in lines("online-indices.txt")) {
            val p=line.split(' ');val indices=OnlineCodec(p[0].toInt(),0.01,3,42).pickIndices(p[1].toLong())
            var h=2166136261u;for(i in indices){h=(h xor i.toUInt())*16777619u}
            assertEquals(line,p[2].toInt(),indices.size);assertEquals(line,p[3].toLong(),h.toLong())
        }
        val cdf=OnlineCodec(8192,0.01,3,42).cdf
        for(i in 1 until cdf.size) assertTrue(cdf[i]>=cdf[i-1])
    }
    @Test fun affineMapsModulesToPixels() {
        val modules=listOf(6 to 6,89 to 6,6 to 89)
        val pixels=listOf(26 to 26,358 to 26,26 to 358)
        val a=moduleToPixelAffine(modules,pixels)!!
        assertEquals(26.0,a[0]*6+a[1]*6+a[2],1e-9)
        assertEquals(26.0,a[3]*6+a[4]*6+a[5],1e-9)
        assertEquals(202.0,a[0]*50+a[1]*50+a[2],1e-9)
        assertNull(moduleToPixelAffine(listOf(1 to 1,2 to 2,3 to 3),pixels))
    }
}
