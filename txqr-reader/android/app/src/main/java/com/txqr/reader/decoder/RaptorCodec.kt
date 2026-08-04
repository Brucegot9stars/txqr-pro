package com.txqr.reader.decoder

import kotlin.math.*


object RaptorConstants {
    fun raptorRand(x: Long, i: Long, m: Long): Long {
        val v0 = v0table[((((x + i) % 256) + 256) % 256).toInt()]
        val v1 = v1table[(((((x / 256) + i) % 256) + 256) % 256).toInt()]
        return (v0 xor v1) % m
    }

    fun deg(v: Long): Int {
        val f = longArrayOf(0, 10241, 491582, 712794, 831695, 948446, 1032189, 1048576)
        val d = intArrayOf(0, 1, 2, 3, 4, 10, 11, 40)
        for (j in 1 until f.size - 1) {
            if (v < f[j]) return d[j]
        }
        return d[d.size - 1]
    }

    fun isPrime(x: Int): Boolean {
        for (p in smallPrimes) {
            if (p * p > x) return true
            if (x % p == 0) return false
        }
        return true
    }

    fun smallestPrimeGreaterOrEqual(x: Int): Int {
        if (x <= smallPrimes[smallPrimes.size - 1]) {
            var lo = 0
            var hi = smallPrimes.size
            while (lo < hi) {
                val mid = (lo + hi) / 2
                if (smallPrimes[mid] < x) lo = mid + 1 else hi = mid
            }
            return smallPrimes[lo]
        }
        var n = x
        while (!isPrime(n)) n++
        return n
    }

    fun intermediateSymbols(k: Int): Triple<Int, Int, Int> {
        var x = kotlin.math.floor(kotlin.math.sqrt(2.0 * k)).toInt()
        if (x < 1) x = 1
        while (x * (x - 1) < 2 * k) x++
        var s = kotlin.math.ceil(0.01 * k).toInt() + x
        s = smallestPrimeGreaterOrEqual(s)
        var h = kotlin.math.floor(kotlin.math.ln((s + k).toDouble()) / kotlin.math.ln(4.0)).toInt()
        while (centerBinomial(h) < k + s) h++
        return Triple(k + s + h, s, h)
    }

    fun centerBinomial(x: Int): Int = choose(x, x / 2)

    fun choose(n: Int, k: Int): Int {
        var kk = if (k > n / 2) n - k else k
        val size = n - kk
        val numerator = IntArray(size)
        val denominator = IntArray(size)
        var idx = 0
        for (i in kk + 1..n) {
            numerator[idx] = i
            denominator[idx] = idx + 1
            idx++
        }
        var z = 0
        while (z < numerator.size && numerator[z] < denominator[denominator.size - 1]) z++
        if (z > 0) {
            val newNum = numerator.copyOfRange(z + 1, numerator.size)
            val newDen = denominator.copyOfRange(0, denominator.size - z - 1)
            for (j in newDen.size - 1 downTo 1) {
                if (newDen[j] == 1) continue
                for (i in newNum.size - 1 downTo 0) {
                    if (newNum[i] % newDen[j] == 0) {
                        newNum[i] /= newDen[j]
                        newDen[j] = 1
                        break
                    }
                }
            }
            var f = 1
            for (v in newNum) f *= v
            return f
        }
        for (j in denominator.size - 1 downTo 1) {
            if (denominator[j] == 1) continue
            for (i in numerator.size - 1 downTo 0) {
                if (numerator[i] % denominator[j] == 0) {
                    numerator[i] /= denominator[j]
                    denominator[j] = 1
                    break
                }
            }
        }
        var f = 1
        for (v in numerator) f *= v
        return f
    }

    fun tripleGenerator(k: Int, x: Int): Triple<Int, Long, Long> {
        val (l, _, _) = intermediateSymbols(k)
        val lprime = smallestPrimeGreaterOrEqual(l)
        val q = 65521L
        val jk = systematicIndextable[k].toLong()
        var a = (53591L + (jk * 997L)) % q
        val b = (10267L * (jk + 1L)) % q
        val y = (b + (x.toLong() * a)) % q
        val v = raptorRand(y, 0, 1048576)
        val d = deg(v)
        a = 1 + raptorRand(y, 1, (lprime - 1).toLong())
        val bb = raptorRand(y, 2, lprime.toLong())
        return Triple(d, a, bb)
    }

    fun findLTIndices(k: Int, x: Int): IntArray {
        val (l, _, _) = intermediateSymbols(k)
        val lprime = smallestPrimeGreaterOrEqual(l).toLong()
        val (d, a, b) = tripleGenerator(k, x)
        val actualD = if (d > l) l else d
        val indices = mutableListOf<Int>()
        var bb = b
        while (bb >= l.toLong()) {
            bb = (bb + a) % lprime
        }
        indices.add(bb.toInt())
        for (j in 1 until actualD) {
            bb = (bb + a) % lprime
            while (bb >= l.toLong()) {
                bb = (bb + a) % lprime
            }
            indices.add(bb.toInt())
        }
        indices.sort()
        return indices.toIntArray()
    }

    fun grayCode(x: Long): Long = (x shr 1) xor x

    fun bitsSet(x: Long): Int {
        var xx = x
        xx -= (xx shr 1) and 0x5555555555555555L
        xx = (xx and 0x3333333333333333L) + ((xx shr 2) and 0x3333333333333333L)
        xx = (xx + (xx shr 4)) and 0x0f0f0f0f0f0f0f0fL
        return ((xx * 0x0101010101010101L) shr 56).toInt()
    }

    fun buildGraySequence(length: Int, b: Int): IntArray {
        val s = IntArray(length)
        var i = 0
        var x = 0L
        while (i < length) {
            val g = grayCode(x)
            if (bitsSet(g) == b) {
                s[i] = g.toInt()
                i++
            }
            x++
        }
        return s
    }
}

class RaptorCodec(val sourceBlocks: Int, val alignmentSize: Int = 1) {
    fun pickIndices(codeBlockIndex: Int): IntArray {
        return RaptorConstants.findLTIndices(sourceBlocks, codeBlockIndex)
    }

    fun newDecoder(messageLength: Int): RaptorDecoder {
        return RaptorDecoder(this, messageLength)
    }
}

class RaptorDecoder(private val codec: RaptorCodec, private val messageLength: Int) {
    private val l = RaptorConstants.intermediateSymbols(codec.sourceBlocks).first
    val matrix = SparseMatrix(l)

    init {
        val (lTotal, s, h) = RaptorConstants.intermediateSymbols(codec.sourceBlocks)
        val k = codec.sourceBlocks

        // LDPC equations (S blocks)
        val compositions = Array<MutableList<Int>>(s) { mutableListOf() }
        for (i in 0 until k) {
            val a = 1 + (i / s) % (s - 1)
            var b = i % s
            compositions[b].add(i)
            b = (b + a) % s
            compositions[b].add(i)
            b = (b + a) % s
            compositions[b].add(i)
        }
        for (i in 0 until s) {
            compositions[i].add(k + i)
            matrix.addEquation(compositions[i].toIntArray(), Block())
        }

        // HDPC equations (H blocks)
        val hCompositions = Array<MutableList<Int>>(h) { mutableListOf() }
        val hprime = kotlin.math.ceil(h.toDouble() / 2).toInt()
        val m = RaptorConstants.buildGraySequence(k + s, hprime)
        for (i in 0 until h) {
            for (j in 0 until k + s) {
                if ((m[j] and (1 shl i)) != 0) {
                    hCompositions[i].add(j)
                }
            }
            hCompositions[i].add(k + s + i)
            matrix.addEquation(hCompositions[i].toIntArray(), Block())
        }
    }

    fun addBlocks(blocks: List<LTBlock>): Boolean {
        for (block in blocks) {
            val indices = codec.pickIndices(block.blockCode.toInt())
            matrix.addEquation(indices, Block(block.data))
        }
        return matrix.determined()
    }

    fun decode(): ByteArray? {
        if (!matrix.determined()) return null
        matrix.reduce()
        val intermediate = matrix.v
        val source = arrayOfNulls<Block>(codec.sourceBlocks)
        for (i in 0 until codec.sourceBlocks) {
            val indices = codec.pickIndices(i)
            var symbol: Block? = null
            for (idx in indices) {
                if (idx < intermediate.size && intermediate[idx] != null) {
                    symbol = if (symbol == null) intermediate[idx]!! else symbol.xor(intermediate[idx]!!)
                }
            }
            source[i] = symbol ?: Block()
        }
        val (lenLong, lenShort, numLong, numShort) = partition(messageLength, codec.sourceBlocks)
        val out = mutableListOf<Byte>()
        for (i in 0 until numLong) {
            val blockData = source[i]!!.data
            out.addAll(blockData.copyOfRange(0, minOf(lenLong, blockData.size)).toList())
        }
        for (i in numLong until numLong + numShort) {
            val blockData = source[i]!!.data
            out.addAll(blockData.copyOfRange(0, minOf(lenShort, blockData.size)).toList())
        }
        return out.toByteArray()
    }
}