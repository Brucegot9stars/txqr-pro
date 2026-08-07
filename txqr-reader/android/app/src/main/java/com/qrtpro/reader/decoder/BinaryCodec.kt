package com.qrtpro.reader.decoder

class BinaryCodec(val sourceBlocks: Int) {
    fun pickIndices(codeBlockIndex: Long): IntArray {
        val idx = (codeBlockIndex % sourceBlocks).toInt()
        return intArrayOf(idx)
    }

    fun newDecoder(messageLength: Int): BinaryDecoder {
        return BinaryDecoder(this, messageLength)
    }
}

class BinaryDecoder(private val codec: BinaryCodec, private val messageLength: Int) {
    val blocks = arrayOfNulls<ByteArray>(codec.sourceBlocks)
    private var received = 0
    private var completed = false

    fun addBlocks(blkList: List<LTBlock>): Boolean {
        for (block in blkList) {
            val idx = codec.pickIndices(block.blockCode)
            val i = idx[0]
            if (blocks[i] == null) {
                blocks[i] = block.data.copyOf()
                received++
            }
            if (received == codec.sourceBlocks) {
                completed = true
                return true
            }
        }
        return false
    }

    fun decode(): ByteArray? {
        if (!completed) return null
        val (lenLong, lenShort, numLong, numShort) = partition(messageLength, codec.sourceBlocks)
        val result = ByteArray(messageLength)
        var pos = 0
        for (i in 0 until codec.sourceBlocks) {
            val block = blocks[i] ?: return null
            val len = if (i < numLong) lenLong else lenShort
            val copyLen = minOf(len, block.size, messageLength - pos)
            System.arraycopy(block, 0, result, pos, copyLen)
            pos += len
        }
        return result
    }
}
