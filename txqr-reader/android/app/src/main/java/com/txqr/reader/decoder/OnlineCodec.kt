package com.txqr.reader.decoder

import kotlin.math.ceil
import kotlin.math.ln
import kotlin.math.max

class OnlineCodec(
    val numSourceBlocks: Int,
    val epsilon: Double,
    val quality: Int,
    val randomSeed: Long
) {
    val cdf: DoubleArray = onlineSolitonDistribution(epsilon)

    fun numAuxBlocks(): Int = ceil(0.55 * quality * epsilon * numSourceBlocks).toInt()

    fun pickIndices(codeBlockIndex: Long): IntArray {
        val random = MersenneTwister()
        random.seed(codeBlockIndex)
        val degree = pickDegree(random, cdf)
        return sampleUniform(random, degree, numSourceBlocks + numAuxBlocks())
    }

    fun newDecoder(messageLength: Int): OnlineDecoder {
        return OnlineDecoder(this, messageLength)
    }

    companion object {
        fun onlineSolitonDistribution(eps: Double): DoubleArray {
            val f = ceil(ln(eps * eps / 4) / ln(1 - eps / 2)).toInt()
            val cdf = DoubleArray(f + 1)
            val rho = 1 - (1 + 1.0 / f) / (1 + eps)
            cdf[1] = rho
            for (i in 2..f) {
                val rhoI = (1 - rho) * f / ((f - 1) * i * (i - 1))
                cdf[i] = cdf[i - 1] + rhoI
            }
            return cdf
        }
    }
}

class OnlineDecoder(private val codec: OnlineCodec, private val messageLength: Int) {
    val totalBlocks = codec.numSourceBlocks + codec.numAuxBlocks()
    val matrix = SparseMatrix(totalBlocks)

    init {
        val auxBlockComposition = Array(codec.numAuxBlocks()) { mutableListOf<Int>() }
        val random = MersenneTwister()
        random.seed(codec.randomSeed)
        for (i in 0 until codec.numSourceBlocks) {
            val touchAuxBlocks = sampleUniform(random, codec.quality, codec.numAuxBlocks())
            for (j in touchAuxBlocks) {
                auxBlockComposition[j].add(i)
            }
        }
        for (i in auxBlockComposition.indices) {
            auxBlockComposition[i].add(i + codec.numSourceBlocks)
            matrix.addEquation(auxBlockComposition[i].toIntArray(), Block())
        }
    }

    fun addBlocks(blocks: List<LTBlock>): Boolean {
        for (block in blocks) {
            val indices = codec.pickIndices(block.blockCode)
            matrix.addEquation(indices, Block(block.data))
        }
        return matrix.determined()
    }

    fun decode(): ByteArray? {
        if (!matrix.determined()) return null
        matrix.reduce()
        return matrix.reconstruct(messageLength, codec.numSourceBlocks)
    }
}
