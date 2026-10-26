package com.qrtpro.reader.decoder

/**
 * Random binary fountain code, mirroring gofountain's binaryCodec.
 *
 * The previous implementation took the block code modulo the number of source
 * blocks — a degree-1 systematic round-robin, i.e. plain repetition of the
 * source blocks. The Go sender (github.com/google/gofountain) instead builds
 * every code block from a *random half* of the source blocks: for each source
 * block b it draws one bit from a Mersenne Twister seeded with the block code
 * and includes b when that bit is 1. The two ends therefore disagreed, the
 * receiver treated random XOR combinations as if they were source blocks, and
 * the user got a silently corrupted file reported as a success.
 *
 * Wire compatibility: this mapping and the RNG draw are wire format. Changing
 * either breaks every stream already in flight.
 */
class BinaryCodec(override val sourceBlocks: Int) : BlockPicker {
    override fun pickIndices(codeBlockIndex: Long): IntArray {
        val random = MersenneTwister()
        random.seed(codeBlockIndex)
        val indices = ArrayList<Int>(sourceBlocks)
        for (b in 0 until sourceBlocks) {
            // Go: rand.New(NewMersenneTwister(codeBlockIndex)).Intn(2) == 1
            if (random.nextInt(2) == 1) {
                indices.add(b)
            }
        }
        return indices.toIntArray()
    }

    fun newDecoder(messageLength: Int): LubyDecoder {
        return LubyDecoder(this, messageLength)
    }
}
