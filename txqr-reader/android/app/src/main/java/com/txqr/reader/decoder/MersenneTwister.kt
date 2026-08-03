package com.txqr.reader.decoder

import kotlin.math.abs

@kotlin.ExperimentalUnsignedTypes
class MersenneTwister : Random {
    private val mt = UIntArray(624)
    private var index = 0
    private var initialized = false

    override fun seed(seed: Long) {
        val s = ((seed shr 32) xor seed).toUInt()
        index = 0
        mt[0] = s
        for (i in 1 until mt.size) {
            mt[i] = (1812433253u * (mt[i - 1] xor (mt[i - 1] shr 30)) + i.toUInt())
        }
        initialized = true
    }

    override fun nextLong(): Long {
        val a = nextUInt()
        val b = nextUInt()
        return (a.toLong() shl 31) xor b.toLong()
    }

    override fun nextInt(n: Int): Int {
        return if (n <= Int.MAX_VALUE) int31n(n) else int63n(n.toLong()).toInt()
    }

    override fun nextFloat(): Double {
        return (nextLong().toDouble()) / abs((1L shl 63).toDouble())
    }

    override fun perm(n: Int): IntArray {
        val m = IntArray(n)
        for (i in 0 until n) {
            val j = nextInt(i + 1)
            m[i] = m[j]
            m[j] = i
        }
        return m
    }

    private fun nextUInt(): UInt {
        if (!initialized) seed(4357)
        if (index == 0) generateUntempered()
        var y = mt[index]
        index++
        if (index >= mt.size) index = 0
        y = y xor (y shr 11)
        y = y xor ((y shl 7) and 0x9d2c5680u)
        y = y xor ((y shl 15) and 0xefc60000u)
        y = y xor (y shr 18)
        return y
    }

    private fun generateUntempered() {
        val mag01 = uintArrayOf(0x0u, 0x9908b0dfu)
        for (i in mt.indices) {
            val y = (mt[i] and 0x80000000u) or (mt[(i + 1) % mt.size] and 0x7fffffffu)
            mt[i] = (mt[(i + 397) % mt.size] xor (y shr 1)) xor mag01[(y and 1u).toInt()]
        }
    }

    private fun nextInt31(): Int {
        return (nextLong() shr 32).toInt()
    }

    private fun int31n(n: Int): Int {
        if (n and (n - 1) == 0) {
            return nextInt31() and (n - 1)
        }
        val max = Int.MAX_VALUE - ((1L shl 31) % n).toInt()
        var v = nextInt31()
        while (v > max) {
            v = nextInt31()
        }
        return v % n
    }

    private fun int63n(n: Long): Long {
        if (n and (n - 1) == 0L) {
            return nextLong() and (n - 1)
        }
        val max = Long.MAX_VALUE + (Long.MIN_VALUE % n)
        var v = nextLong()
        while (v > max) {
            v = nextLong()
        }
        return v % n
    }
}

interface Random {
    fun seed(seed: Long)
    fun nextLong(): Long
    fun nextInt(n: Int): Int
    fun nextFloat(): Double
    fun perm(n: Int): IntArray
}
