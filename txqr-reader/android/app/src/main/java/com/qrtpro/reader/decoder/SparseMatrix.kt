package com.qrtpro.reader.decoder

class SparseMatrix(val size: Int) {
    val coeff = arrayOfNulls<IntArray>(size)
    val v = arrayOfNulls<Block>(size)

    fun addEquation(components: IntArray, b: Block) {
        var comps = components
        var block = b
        while (comps.isNotEmpty()) {
            val pivot = comps[0]
            val existing = coeff[pivot] ?: break
            if (comps.size < existing.size) {
                val old = v[pivot]!!
                coeff[pivot] = comps
                v[pivot] = block
                comps = existing
                block = old
            }
            block = block.xor(v[pivot]!!)
            comps = xorIndices(comps, coeff[pivot]!!)
        }
        if (comps.isNotEmpty()) {
            coeff[comps[0]] = comps
            v[comps[0]] = block
        }
    }

    // Both inputs are sorted sets, exactly as in the original list-based XOR.
    // Primitive arrays avoid boxing every source index at every elimination.
    private fun xorIndices(a: IntArray, b: IntArray): IntArray {
        val result = IntArray(a.size + b.size)
        var i = 0; var j = 0; var n = 0
        while (i < a.size && j < b.size) {
            when {
                a[i] == b[j] -> { i++; j++ }
                a[i] < b[j] -> result[n++] = a[i++]
                else -> result[n++] = b[j++]
            }
        }
        while (i < a.size) result[n++] = a[i++]
        while (j < b.size) result[n++] = b[j++]
        return result.copyOf(n)
    }

    fun determined(): Boolean = coeff.all { it != null && it.isNotEmpty() }

    fun reduce() {
        for (i in coeff.indices.reversed()) {
            val ci = coeff[i] ?: continue
            for (j in 0 until i) {
                val cj = coeff[j] ?: continue
                for (k in 1 until cj.size) {
                    if (cj[k] == ci[0]) {
                        v[j] = v[j]!!.xor(v[i]!!)
                        break
                    }
                }
            }
            coeff[i] = intArrayOf(ci[0])
        }
    }

    fun reconstruct(totalLength: Int, numSourceBlocks: Int): ByteArray =
        reconstruct(totalLength, partition(totalLength, numSourceBlocks))

    fun reconstruct(totalLength: Int, partitionResult: PartitionResult): ByteArray {
        val (lenLong, lenShort, numLong, numShort) = partitionResult
        val result = ByteArray(totalLength)
        var offset = 0
        for (i in 0 until numLong + numShort) {
            val length = if (i < numLong) lenLong else lenShort
            val data = v[i]!!.data
            data.copyInto(result, offset, 0, minOf(length, data.size))
            offset += length // virtual padding remains zero in the output
        }
        check(offset == totalLength)
        return result
    }
}
