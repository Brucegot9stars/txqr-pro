package com.qrtpro.reader.decoder

import org.junit.Assert.*
import org.junit.Test
import kotlin.random.Random

class SparseMatrixTest {
    @Test fun primitiveRowsRecoverRandomInvertibleSystems() {
        val random=Random(1543)
        repeat(100) {
            val k=random.nextInt(1,33);val source=ByteArray(k*3){random.nextInt(256).toByte()}
            val rows=Array(k){i->BooleanArray(k){it==i}}
            if(k>1) repeat(k*4) {
                val a=random.nextInt(k);var b=random.nextInt(k-1);if(b>=a)b++
                for(i in 0 until k) rows[a][i]=rows[a][i] xor rows[b][i]
            }
            val matrix=SparseMatrix(k)
            for(row in rows.toList().shuffled(random)) {
                val indices=(0 until k).filter{row[it]}.toIntArray()
                val value=ByteArray(3)
                for(index in indices) for(i in 0..2) value[i]=(value[i].toInt() xor source[index*3+i].toInt()).toByte()
                matrix.addEquation(indices,Block(value))
            }
            assertTrue(matrix.determined());matrix.reduce();assertArrayEquals(source,matrix.reconstruct(source.size,k))
        }
    }
    @Test fun reconstructPreservesSemiEqualPartitionAndVirtualZeros() {
        val matrix=SparseMatrix(3)
        matrix.addEquation(intArrayOf(0),Block(byteArrayOf(1,2,3,4)))
        matrix.addEquation(intArrayOf(1),Block(byteArrayOf(5,6),padding=2))
        matrix.addEquation(intArrayOf(2),Block(byteArrayOf(7,8,9)))
        assertArrayEquals(byteArrayOf(1,2,3,4,5,6,0,7,8,9),matrix.reconstruct(10,3))
    }
}
