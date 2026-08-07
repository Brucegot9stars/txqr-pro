package com.qrtpro.reader.decoder

class LubyCodec(val sourceBlocks: Int) {
    private val random = MersenneTwister()
    private val degreeCdf = solitonDistribution(sourceBlocks)

    fun pickIndices(codeBlockIndex: Long): IntArray {
        random.seed(codeBlockIndex)
        val d = pickDegree(random, degreeCdf)
        return sampleUniform(random, d, sourceBlocks)
    }

    fun newDecoder(messageLength: Int): LubyDecoder {
        return LubyDecoder(this, messageLength)
    }
}

class LubyDecoder(private val codec: LubyCodec, private val messageLength: Int) {
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
