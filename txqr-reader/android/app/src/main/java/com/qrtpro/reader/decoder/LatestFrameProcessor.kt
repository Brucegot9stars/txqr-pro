package com.qrtpro.reader.decoder

import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** One in-flight batch and one latest queued batch. Publishing runs on scope's
 * dispatcher; all equation solving, decompression and hashing run on worker. */
class LatestFrameProcessor<I, O>(
    scope: CoroutineScope,
    private val process: (I) -> O,
    private val publish: (I, O) -> Unit,
    worker: CoroutineDispatcher = Dispatchers.Default,
) {
    private val input = Channel<I>(1, BufferOverflow.DROP_OLDEST)
    private val job = scope.launch {
        for (item in input) {
            val result = withContext(worker) { process(item) }
            publish(item, result)
        }
    }
    fun submit(item: I): Boolean = input.trySend(item).isSuccess
    fun close() { input.cancel(); job.cancel() }
}
