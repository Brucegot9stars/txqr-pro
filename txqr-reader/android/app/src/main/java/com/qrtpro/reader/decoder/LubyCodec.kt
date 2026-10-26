package com.qrtpro.reader.decoder

/**
 * Maps a code block id to the source blocks it is composed of. Both the LT and
 * the random-binary fountain share the same decoder — only this mapping
 * differs — so the peeling/Gaussian machinery is written once.
 */
interface BlockPicker {
    val sourceBlocks: Int
    fun pickIndices(codeBlockIndex: Long): IntArray
}

class LubyCodec(override val sourceBlocks: Int) : BlockPicker {
    private val random = MersenneTwister()
    private val degreeCdf = solitonDistribution(sourceBlocks)

    override fun pickIndices(codeBlockIndex: Long): IntArray {
        random.seed(codeBlockIndex)
        val d = pickDegree(random, degreeCdf)
        return sampleUniform(random, d, sourceBlocks)
    }

    fun newDecoder(messageLength: Int): LubyDecoder {
        return LubyDecoder(this, messageLength)
    }
}

class LubyDecoder(private val codec: BlockPicker, private val messageLength: Int) {
    val matrix = SparseMatrix(codec.sourceBlocks)

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
        val pr = partition(messageLength, codec.sourceBlocks)
        return matrix.reconstruct(messageLength, pr)
    }
}

data class LTBlock(
    val blockCode: Long,
    val data: ByteArray
)
