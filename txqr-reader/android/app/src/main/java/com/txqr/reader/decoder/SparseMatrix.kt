package com.txqr.reader.decoder

class SparseMatrix(val size: Int) {
    val coeff = Array<MutableList<Int>?>(size) { null }
    val v = arrayOfNulls<Block>(size)

    fun addEquation(components: IntArray, b: Block) {
        var comps = components.toMutableList()
        var block = b

        while (comps.isNotEmpty()) {
            val s = comps[0]
            val existingCoeff = coeff[s]
            if (existingCoeff != null) {
                if (comps.size >= existingCoeff.size) {
                    val result = xorRow(s, comps, block)
                    comps = result.first
                    block = result.second
                } else {
                    val oldCoeff = coeff[s]!!
                    val oldV = v[s]!!
                    coeff[s] = comps
                    v[s] = block
                    val result = xorRow(s, oldCoeff, oldV)
                    comps = result.first
                    block = result.second
                }
            } else {
                break
            }
        }

        if (comps.isNotEmpty()) {
            coeff[comps[0]] = comps
            v[comps[0]] = block
        }
    }

    private fun xorRow(s: Int, indices: MutableList<Int>, b: Block): Pair<MutableList<Int>, Block> {
        var newBlock = b.xor(v[s]!!)
        val existingCoeff = coeff[s]!!

        val newIndices = mutableListOf<Int>()
        var i = 0
        var j = 0
        while (i < existingCoeff.size && j < indices.size) {
            val index = indices[j]
            if (existingCoeff[i] == index) {
                i++
                j++
            } else if (existingCoeff[i] < index) {
                newIndices.add(existingCoeff[i])
                i++
            } else {
                newIndices.add(index)
                j++
            }
        }
        while (i < existingCoeff.size) {
            newIndices.add(existingCoeff[i])
            i++
        }
        while (j < indices.size) {
            newIndices.add(indices[j])
            j++
        }

        return newIndices to newBlock
    }

    fun determined(): Boolean {
        for (r in coeff) {
            if (r == null || r.isEmpty()) return false
        }
        return true
    }

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
            coeff[i] = mutableListOf(ci[0])
        }
    }

    fun reconstruct(totalLength: Int, partitionResult: PartitionResult): ByteArray {
        val (lenLong, lenShort, numLong, numShort) = partitionResult
        val out = mutableListOf<Byte>()
        for (i in 0 until numLong) {
            val blockData = v[i]!!.data
            out.addAll(blockData.copyOfRange(0, minOf(lenLong, blockData.size)).toList())
        }
        for (i in numLong until numLong + numShort) {
            val blockData = v[i]!!.data
            out.addAll(blockData.copyOfRange(0, minOf(lenShort, blockData.size)).toList())
        }
        return out.toByteArray()
    }
}
