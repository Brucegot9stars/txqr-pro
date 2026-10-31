package com.qrtpro.reader.decoder

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancel
import org.junit.Assert.*
import org.junit.Test
import java.util.Base64
import java.util.Collections
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

class ReceivePipelineTest {
    @Test fun decoderWorkDoesNotBlockPublisherAndQueueKeepsLatest() {
        val uiExecutor=Executors.newSingleThreadExecutor { Thread(it,"test-ui") }
        val decodeExecutor=Executors.newSingleThreadExecutor { Thread(it,"test-decode") }
        val ui=uiExecutor.asCoroutineDispatcher(); val worker=decodeExecutor.asCoroutineDispatcher()
        val scope=CoroutineScope(SupervisorJob()+ui)
        val uiThread=uiExecutor.submit<Thread>{Thread.currentThread()}.get()
        val workerThread=decodeExecutor.submit<Thread>{Thread.currentThread()}.get()
        val entered=CountDownLatch(1);val release=CountDownLatch(1);val done=CountDownLatch(2)
        val results=Collections.synchronizedList(mutableListOf<Int>())
        val active=AtomicInteger();val maximum=AtomicInteger()
        val processor=LatestFrameProcessor<Int,Int>(scope,process={ value ->
            assertSame(workerThread,Thread.currentThread())
            maximum.accumulateAndGet(active.incrementAndGet(),::maxOf)
            try { if(value==1) {entered.countDown();check(release.await(5,TimeUnit.SECONDS))};value }
            finally {active.decrementAndGet()}
        },publish={ _,result ->
            assertSame(uiThread,Thread.currentThread())
            results.add(result);done.countDown()
        },worker=worker)
        try {
            assertTrue(processor.submit(1));assertTrue(entered.await(5,TimeUnit.SECONDS))
            // The publication/UI thread remains available while solving is blocked.
            assertEquals(42,uiExecutor.submit<Int>{42}.get(2,TimeUnit.SECONDS).toInt())
            for(i in 2..1000) assertTrue(processor.submit(i))
            release.countDown();assertTrue(done.await(5,TimeUnit.SECONDS))
            assertEquals(listOf(1,1000),results.toList());assertEquals(1,maximum.get())
            processor.close();assertFalse(processor.submit(1001))
        } finally {release.countDown();processor.close();scope.cancel();ui.close();worker.close()}
    }

    @Test fun closedProcessorCannotPublishALateResult() {
        val uiExecutor=Executors.newSingleThreadExecutor()
        val decodeExecutor=Executors.newSingleThreadExecutor()
        val ui=uiExecutor.asCoroutineDispatcher();val worker=decodeExecutor.asCoroutineDispatcher()
        val scope=CoroutineScope(SupervisorJob()+ui)
        val entered=CountDownLatch(1);val release=CountDownLatch(1);val publications=AtomicInteger()
        val processor=LatestFrameProcessor<Int,Int>(scope,process={entered.countDown();release.await();it},
            publish={_,_->publications.incrementAndGet()},worker=worker)
        try {
            processor.submit(1);assertTrue(entered.await(5,TimeUnit.SECONDS));processor.close()
            release.countDown()
            decodeExecutor.submit{}.get(5,TimeUnit.SECONDS)
            uiExecutor.submit{}.get(5,TimeUnit.SECONDS)
            assertEquals(0,publications.get())
        } finally {release.countDown();processor.close();scope.cancel();ui.close();worker.close()}
    }

    private fun fixture(name:String)=requireNotNull(javaClass.getResourceAsStream("/regression/$name"))
        .bufferedReader().use{it.readLines().filter(String::isNotBlank).map{line->Base64.getDecoder().decode(line)}}

    @Test fun sessionVerifiesAndPublishesOnlyOneCompletedFile() {
        for(name in listOf("single.txt","rq-rank.txt","online-large.txt","nonmultiple.txt")) {
            val all=fixture(name);var now=1000L;val session=ReceiveSession {now}
            var result:ReceiveUpdate?=null
            for(frame in all.drop(1)) {val update=session.accept(listOf(frame));if(update?.data!=null) {result=update;break}}
            if(result==null) {now+=ReceiveSession.FILE_NAME_WAIT_MS;result=session.accept(emptyList())}
            assertNotNull(name,result);assertNull(result!!.error);assertArrayEquals(all[0],result.data)
            assertEquals(all[0].size.toLong(),result.totalBytes);assertEquals(32,result.md5.length)
            assertNotNull(result.finishedAt);assertNull(session.accept(listOf(all[1])))
        }
    }

    @Test fun rescanCreatesIndependentStateAndDiscardsStaleWork() {
        val rank=fixture("rq-rank.txt");val single=fixture("single.txt")
        val old=ReceiveSession();old.accept(listOf(rank[1]))
        assertNull(old.accept(rank.drop(2)) {false})
        var now=1000L;val current=ReceiveSession {now};current.accept(single.drop(1))
        now+=ReceiveSession.FILE_NAME_WAIT_MS
        val fresh=current.accept(emptyList())
        assertArrayEquals(single[0],fresh!!.data)
    }
}
