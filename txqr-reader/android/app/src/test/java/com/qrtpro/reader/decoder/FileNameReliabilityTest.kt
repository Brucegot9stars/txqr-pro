package com.qrtpro.reader.decoder

import org.junit.Assert.*
import org.junit.Test
import java.util.Base64
import java.util.zip.CRC32

class FileNameReliabilityTest {
    private fun fixture(): List<ByteArray> = requireNotNull(javaClass.getResourceAsStream("/regression/filename.txt"))
        .bufferedReader().use { it.readLines().filter(String::isNotBlank).map { line -> Base64.getDecoder().decode(line) } }

    private fun foreign(packet: ByteArray): ByteArray {
        val sep = packet.indexOf('|'.code.toByte())
        val fields = String(packet, 0, sep, Charsets.ISO_8859_1).split('/').toMutableList()
        fields[1] = (if (fields[1][0] == '0') "1" else "0") + fields[1].drop(1)
        val prefix = fields.take(8).joinToString("/")
        val payload = packet.copyOfRange(sep + 1, packet.size)
        val crc = CRC32().apply { update(prefix.toByteArray()); update('|'.code); update(payload) }.value
        return "$prefix/${crc.toString(16).padStart(8,'0')}|".toByteArray() + payload
    }

    @Test fun goSenderRepeatsThreeNamesAndLateNameDoesNotAlterContent() {
        val all = fixture()
        val frames = all.drop(1)
        val names = frames.filter { TransferFrame.parse(it)!!.isName }
        assertEquals(3, names.size)
        assertArrayEquals(names[0], names[2])
        val d = TxqrDecoder()
        frames.filterNot { TransferFrame.parse(it)!!.isName }.forEach(d::decodeFrame)
        assertTrue(d.isCompleted)
        assertEquals("", d.receivedFileName)
        assertArrayEquals(all[0], d.dataBytes)
        val count = d.sourceBytesCount
        val bad = names[0].copyOf().also { it[it.lastIndex] = (it.last().toInt() xor 1).toByte() }
        d.decodeFrame(bad)
        d.decodeFrame(foreign(names[0]))
        assertEquals("", d.receivedFileName)
        d.decodeFrame(names[2]) // The first two filename packets were lost.
        assertEquals("报告 2026.zip", d.receivedFileName)
        assertEquals(count, d.sourceBytesCount)
        assertArrayEquals(all[0], d.dataBytes)
    }

    @Test fun completedContentKeepsReceivingSameSessionNameAndFreezesContentTime() {
        val frames = fixture().drop(1)
        val data = frames.filterNot { TransferFrame.parse(it)!!.isName }
        val name = frames.first { TransferFrame.parse(it)!!.isName }
        var now = 1000L
        val session = ReceiveSession { now }
        session.accept(data.take(1))
        now = 1400L
        val waiting = session.accept(data.drop(1))!!
        assertTrue(waiting.waitingForName)
        assertNull(waiting.data)
        assertEquals(99, waiting.progress)
        now = 1900L
        val stillWaiting = session.accept(List(20) { foreign(name) })!!
        assertTrue(stillWaiting.waitingForName)
        val result = session.accept(listOf(name))!!
        assertEquals("报告 2026.zip", result.fileName)
        assertArrayEquals(fixture()[0], result.data)
        assertNull(result.warning)
        assertEquals(1400L, result.finishedAt)
        assertEquals(400L, result.elapsed(now))
        assertNull(session.accept(listOf(name)))
    }

    @Test fun absentNamesTimeoutWithoutNewFramesAndWarnOnlyOnce() {
        val frames = fixture().drop(1).filterNot { TransferFrame.parse(it)!!.isName }
        var now = 1000L
        val session = ReceiveSession { now }
        assertTrue(session.accept(frames)!!.waitingForName)
        now += ReceiveSession.FILE_NAME_WAIT_MS - 1
        assertNull(session.accept(emptyList())!!.data)
        now++
        val result = session.accept(emptyList())!!
        assertArrayEquals(fixture()[0], result.data)
        assertFalse(result.waitingForName)
        assertNotNull(result.warning)
        assertTrue(result.fileName.startsWith("received_"))
        assertEquals(1000L, result.finishedAt)
        assertNull(session.accept(emptyList()))
    }

    @Test fun knownNameCompletesImmediatelyAndStaleGenerationCannotPublish() {
        val frames = fixture().drop(1)
        val ready = ReceiveSession().accept(frames)!!
        assertArrayEquals(fixture()[0], ready.data)
        assertFalse(ready.waitingForName)
        assertNull(ready.warning)
        val old = ReceiveSession()
        old.accept(frames.filterNot { TransferFrame.parse(it)!!.isName })
        assertNull(old.accept(frames) { false })
        assertArrayEquals(fixture()[0], ReceiveSession().accept(frames)!!.data)
    }
}
