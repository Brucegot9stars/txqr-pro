package com.txqr.reader.decoder

fun numberOfChunks(length: Int, chunkLen: Int): Int {
    var n = length / chunkLen
    if (length % chunkLen > 0) n++
    return n
}

fun solitonDistribution(n: Int): DoubleArray {
    val cdf = DoubleArray(n + 1)
    cdf[1] = 1.0 / n
    for (i in 2..n) {
        cdf[i] = cdf[i - 1] + 1.0 / (i.toDouble() * (i - 1).toDouble())
    }
    return cdf
}

fun pickDegree(random: Random, degreeCdf: DoubleArray): Int {
    val r = random.nextFloat()
    val d = searchFirstGE(degreeCdf, r)
    return if (degreeCdf[d] > r) {
        d
    } else if (d < degreeCdf.size - 1) {
        d + 1
    } else {
        degreeCdf.size - 1
    }
}

private fun searchFirstGE(cdf: DoubleArray, r: Double): Int {
    var lo = 0
    var hi = cdf.size
    while (lo < hi) {
        val mid = (lo + hi) / 2
        if (cdf[mid] < r) lo = mid + 1 else hi = mid
    }
    return if (lo < cdf.size) lo else cdf.size - 1
}

fun sampleUniform(random: Random, num: Int, max: Int): IntArray {
    if (num >= max) {
        return IntArray(max) { it }
    }
    val picks = IntArray(num)
    val seen = mutableSetOf<Int>()
    for (i in 0 until num) {
        var p = random.nextInt(max)
        while (p in seen) {
            p = random.nextInt(max)
        }
        picks[i] = p
        seen.add(p)
    }
    picks.sort()
    return picks
}

data class Block(
    val data: ByteArray = byteArrayOf(),
    val padding: Int = 0
) {
    val length: Int get() = data.size + padding
    val isEmpty: Boolean get() = length == 0

    fun xor(other: Block): Block {
        val self = data
        val o = other.data
        val n = maxOf(self.size, o.size)
        val newData = ByteArray(n)
        for (i in 0 until n) {
            val a = if (i < self.size) self[i] else 0.toByte()
            val b = if (i < o.size) o[i] else 0.toByte()
            newData[i] = (a.toInt() xor b.toInt()).toByte()
        }
        return Block(newData)
    }

    override fun equals(other: Any?): Boolean {
        if (this === other) return true
        if (other !is Block) return false
        return data.contentEquals(other.data) && padding == other.padding
    }

    override fun hashCode(): Int {
        return data.contentHashCode() * 31 + padding
    }
}

fun partitionBytes(input: ByteArray, p: Int): Pair<List<Block>, List<Block>> {
    val (lenLong, lenShort, numLong, numShort) = partition(input.size, p)

    val sliceIntoBlocks: (ByteArray, Int, Int) -> Pair<List<Block>, ByteArray> = { inp, num, length ->
        val blocks = mutableListOf<Block>()
        var remaining = inp
        for (i in 0 until num) {
            if (remaining.size > length) {
                val (data, rest) = remaining.copyOfRange(0, length) to remaining.copyOfRange(length, remaining.size)
                blocks.add(Block(data))
                remaining = rest
            } else {
                blocks.add(Block(remaining))
                remaining = byteArrayOf()
            }
            val last = blocks.last()
            if (last.data.size < length) {
                blocks[blocks.lastIndex] = Block(last.data, length - last.data.size)
            }
        }
        blocks to remaining
    }

    val (long, remaining) = sliceIntoBlocks(input, numLong, lenLong)
    val (short, _) = sliceIntoBlocks(remaining, numShort, lenShort)
    return long to short
}

fun equalizeBlockLengths(longBlocks: List<Block>, shortBlocks: List<Block>): List<Block> {
    if (longBlocks.isEmpty()) return shortBlocks
    if (shortBlocks.isEmpty()) return longBlocks

    val targetLen = longBlocks[0].length
    return longBlocks + shortBlocks.map { Block(it.data, it.padding + (targetLen - it.length)) }
}

data class PartitionResult(val lenLong: Int, val lenShort: Int, val numLong: Int, val numShort: Int)

fun partition(length: Int, p: Int): PartitionResult {
    val lenLong = (length + p - 1) / p
    val lenShort = length / p
    val numLong = length - (lenShort * p)
    val numShort = p - numLong
    return PartitionResult(lenLong, lenShort, numLong, numShort)
}

fun generateLubyTransformBlock(source: List<Block>, indices: IntArray): Block {
    var symbol: Block? = null
    for (i in indices) {
        if (i < source.size) {
            symbol = if (symbol == null) source[i] else symbol.xor(source[i])
        }
    }
    return symbol ?: Block()
}
