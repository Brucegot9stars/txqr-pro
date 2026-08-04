package com.txqr.reader.decoder

internal fun rqRandom(y: Long, i: Long, m: Long): Long {
    val x0 = ((y + i) % 256).toInt()
    val x1 = ((y / 256 + i) % 256).toInt()
    val x2 = ((y / 65536 + i) % 256).toInt()
    val x3 = ((y / 16777216 + i) % 256).toInt()
    var res = randV0[x0] xor randV1[x1] xor randV2[x2] xor randV3[x3]
    if (m != 0L) {
        res %= m
    }
    return res
}

private fun octMul(x: Int, y: Int): Int {
    if (x == 0 || y == 0) {
        return 0
    }
    return ExpPreCalc[(LogPreCalc[x] + LogPreCalc[y]) % 255]
}

private fun octExp(x: Int): Int {
    return ExpPreCalc[x % 255]
}

private fun octInverse(x: Int): Int {
    return octExp(255 - LogPreCalc[x])
}

private fun octVecAdd(dst: ByteArray, offset: Int, src: ByteArray) {
    for (k in src.indices) {
        dst[offset + k] = (dst[offset + k].toInt() xor (src[k].toInt() and 0xFF)).toByte()
    }
}

private fun isPrime(n: Int): Boolean {
    if (n <= 3) {
        return true
    }
    if (n % 2 == 0 || n % 3 == 0) {
        return false
    }
    var i = 5
    var w = 2
    while (i.toLong() * i.toLong() <= n.toLong()) {
        if (n % i == 0) {
            return false
        }
        i += w
        w = 6 - w
    }
    return true
}

private val degreeDistribution = intArrayOf(
    0, 5243, 529531, 704294, 791675, 844104, 879057, 904023, 922747, 937311, 948962,
    958494, 966438, 973160, 978921, 983914, 988283, 992138, 995565, 998631, 1001391, 1003887,
    1006157, 1008229, 1010129, 1011876, 1013490, 1014983, 1016370, 1017662, 1048576
)

internal class RaptorQParams(
    val K: Int,
    val KPadded: Int,
    val J: Int,
    val S: Int,
    val H: Int,
    val W: Int,
    val L: Int,
    val P: Int,
    val P1: Int,
    val U: Int,
    val B: Int
) {
    fun getDegree(v: Int): Int {
        for (i in degreeDistribution.indices) {
            if (v < degreeDistribution[i]) {
                val x = W - 2
                if (x < i) {
                    return x
                }
                return i
            }
        }
        throw IllegalStateException("unreachable")
    }

    fun calcEncodingRow(x: Int): EncodingRow {
        var ja = 53591 + J * 997
        if (ja % 2 == 0) {
            ja++
        }
        val bLocal = 10267 * (J + 1)
        val y = bLocal.toLong() + x.toLong() * ja.toLong()
        val v = rqRandom(y, 0, 1048576L)
        val d = getDegree(v.toInt())
        val a = 1 + rqRandom(y, 1, (W - 1).toLong()).toInt()
        val b = rqRandom(y, 2, W.toLong()).toInt()

        var d1: Int
        if (d < 4) {
            d1 = 2 + rqRandom(x.toLong(), 3, 2L).toInt()
        } else {
            d1 = 2
        }

        val a1 = 1 + rqRandom(x.toLong(), 4, (P1 - 1).toLong()).toInt()
        val b1 = rqRandom(x.toLong(), 5, P1.toLong()).toInt()

        return EncodingRow(d, a, b, d1, a1, b1)
    }

    fun hdpcMultiply(v: MatrixGF256): MatrixGF256 {
        val alpha = octExp(1)
        for (i in 1 until v.rows) {
            v.rowAddMul(i, v.getRow(i - 1), alpha)
        }

        val u = MatrixGF256(H, v.cols)
        for (i in 0 until H) {
            u.rowAddMul(i, v.getRow(v.rows - 1), octExp(i % 255))
        }

        for (col in 0 until v.rows - 1) {
            val a = rqRandom((col + 1).toLong(), 6, H.toLong()).toInt()
            val b = (a + rqRandom((col + 1).toLong(), 7, (H - 1).toLong()).toInt() + 1) % H
            u.rowAdd(a, v.getRow(col))
            u.rowAdd(b, v.getRow(col))
        }
        return u
    }

    fun solve(symbols: List<Symbol>): MatrixGF256? {
        val symSz = symbols[0].data.size
        val rows = S + symbols.size
        val aUpperRows = rows

        val d = MatrixGF256(S + H + symbols.size, symSz)
        var offset = S
        for (symbol in symbols) {
            d.rowSet(offset, symbol.data)
            offset++
        }

        val eRows = ArrayList<EncodingRow>(symbols.size)
        for (symbol in symbols) {
            eRows.add(calcEncodingRow(symbol.id))
        }

        val maxUpperNonZero = 3 * B + 3 * S + 33 * symbols.size
        val aUpper = MatrixGF256(aUpperRows, L)
        val upperBuilder = UpperMatrixBuilder(aUpper, maxUpperNonZero)

        for (i in 0 until B) {
            val a = 1 + i / S
            var b = i % S
            upperBuilder.set(b, i)
            b = (b + a) % S
            upperBuilder.set(b, i)
            b = (b + a) % S
            upperBuilder.set(b, i)
        }

        for (i in 0 until S) {
            upperBuilder.set(i, i + B)
        }

        for (i in 0 until S) {
            upperBuilder.set(i, (i % P) + W)
            upperBuilder.set(i, ((i + 1) % P) + W)
        }

        for (ri in eRows.indices) {
            eRows[ri].encode(upperBuilder, ri, this)
        }

        val dec = inactivateDecode(aUpper, P, upperBuilder.entries)

        val rowPerm = ArrayList<Int>(dec.rows)
        while (rowPerm.size < d.rows) {
            rowPerm.add(rowPerm.size)
        }

        val dPerm = applyPermutation(d, rowPerm.toIntArray())
        val rPermutation = inversePermutation(rowPerm.toIntArray())
        val cPermutation = inversePermutation(dec.cols.toIntArray())

        val upperRes = applyRCPermutationAndUpperIndex(
            aUpper, upperBuilder.entries, rPermutation, cPermutation,
            dec.uSize, dec.uSize, maxUpperNonZero
        )
        val aUpperPerm = upperRes.first
        val upperIndex = upperRes.second

        val e = toGF2(aUpperPerm, 0, dec.uSize, dec.uSize, L - dec.uSize)
        val c = MatrixGF256(aUpperPerm.cols, dPerm.cols)
        c.setFromBlock(dPerm, 0, 0, dec.uSize, dPerm.cols, 0, 0)

        for (i in 0 until dec.uSize) {
            for (row in upperIndex.colRowsFor(i)) {
                if (row == i) {
                    continue
                }
                e.rowAdd(row, e.getRow(i))
                dPerm.rowAdd(row, dPerm.getRow(i))
            }
        }

        fun hdpcMul(m: MatrixGF256): MatrixGF256 {
            val t = MatrixGF256(KPadded + S, m.cols)
            for (i in 0 until m.rows) {
                t.rowSet(dec.cols[i], m.getRow(i))
            }
            return hdpcMultiply(t)
        }

        val gLeft = getBlock(aUpperPerm, dec.uSize, 0, aUpperPerm.rows - dec.uSize, dec.uSize)

        val smallAUpper = MatrixGF256(aUpperPerm.rows - dec.uSize, aUpperPerm.cols - dec.uSize)
        setBinaryBlock(smallAUpper, aUpperPerm, dec.uSize, dec.uSize, smallAUpper.rows, smallAUpper.cols)
        smallAUpper.addInPlace(plainGF2ToGF256(mulGF2(e, gLeft)))

        val smallALower = MatrixGF256(H, aUpperPerm.cols - dec.uSize)
        for (i in 1..H) {
            smallALower.set(smallALower.rows - i, smallALower.cols - i, 1)
        }

        val tRight = MatrixGF256(KPadded + S, KPadded + S - dec.uSize)
        for (i in 0 until tRight.cols) {
            tRight.set(dec.cols[i + tRight.rows - tRight.cols], i, 1)
        }
        val hdpcRight = hdpcMultiply(tRight)
        smallALower.setFrom(hdpcRight, 0, 0)

        smallALower.addInPlace(hdpcMul(plainGF2ToGF256(e)))

        val dUpper = MatrixGF256(dec.uSize, dPerm.cols)
        dUpper.setFromBlock(dPerm, 0, 0, dUpper.rows, dUpper.cols, 0, 0)

        val smallDUpper = MatrixGF256(aUpperPerm.rows - dec.uSize, dPerm.cols)
        smallDUpper.setFromBlock(dPerm, dec.uSize, 0, smallDUpper.rows, smallDUpper.cols, 0, 0)
        smallDUpper.addInPlace(mulSparse(dUpper, gLeft))

        val smallDLower = MatrixGF256(H, dPerm.cols)
        smallDLower.setFromBlock(dPerm, aUpperPerm.rows, 0, smallDLower.rows, smallDLower.cols, 0, 0)
        smallDLower.addInPlace(hdpcMul(dUpper))

        val smallA = MatrixGF256(smallAUpper.rows + smallALower.rows, smallAUpper.cols)
        smallA.setFrom(smallAUpper, 0, 0)
        smallA.setFrom(smallALower, smallAUpper.rows, 0)

        val smallD = MatrixGF256(smallDUpper.rows + smallDLower.rows, smallDUpper.cols)
        smallD.setFrom(smallDUpper, 0, 0)
        smallD.setFrom(smallDLower, smallDUpper.rows, 0)

        val smallC = gaussianElimination(smallA, smallD) ?: return null

        c.setFromBlock(smallC, 0, 0, c.rows - dec.uSize, c.cols, dec.uSize, 0)
        for (row in 0 until dec.uSize) {
            for (col in upperIndex.rowColsFor(row)) {
                if (col == row) {
                    continue
                }
                c.rowAdd(row, c.getRow(col))
            }
        }

        return applyPermutation(c, inversePermutation(dec.cols.toIntArray()))
    }
}

internal class Symbol(val id: Int, val data: ByteArray)

internal class EncodingRow(
    var d: Int,
    var a: Int,
    var b: Int,
    var d1: Int,
    var a1: Int,
    var b1: Int
) {
    fun encode(aUpper: UpperMatrixBuilder, ri: Int, p: RaptorQParams) {
        aUpper.set(ri + p.S, b)

        for (j in 1 until d) {
            b = (b + a) % p.W
            aUpper.set(ri + p.S, b)
        }

        while (b1 >= p.P) {
            b1 = (b1 + a1) % p.P1
        }

        aUpper.set(ri + p.S, p.W + b1)
        for (j in 1 until d1) {
            b1 = (b1 + a1) % p.P1
            while (b1 >= p.P) {
                b1 = (b1 + a1) % p.P1
            }
            aUpper.set(ri + p.S, p.W + b1)
        }
    }

    fun encodeGen(dst: ByteArray, offset: Int, relaxed: MatrixGF256, symSz: Int, p: RaptorQParams) {
        for (k in 0 until symSz) {
            dst[offset + k] = 0
        }
        octVecAdd(dst, offset, relaxed.getRow(b))

        for (j in 1 until d) {
            b = (b + a) % p.W
            octVecAdd(dst, offset, relaxed.getRow(b))
        }

        while (b1 >= p.P) {
            b1 = (b1 + a1) % p.P1
        }

        octVecAdd(dst, offset, relaxed.getRow(p.W + b1))
        for (j in 1 until d1) {
            b1 = (b1 + a1) % p.P1
            while (b1 >= p.P) {
                b1 = (b1 + a1) % p.P1
            }
            octVecAdd(dst, offset, relaxed.getRow(p.W + b1))
        }
    }
}

internal class MatrixGF256(val rows: Int, val cols: Int, val data: ByteArray = ByteArray(rows * cols)) {
    fun get(row: Int, col: Int): Byte = data[row * cols + col]

    fun set(row: Int, col: Int, value: Byte) {
        data[row * cols + col] = value
    }

    fun getRow(row: Int): ByteArray {
        val start = row * cols
        return data.copyOfRange(start, start + cols)
    }

    fun rowSet(row: Int, r: ByteArray) {
        System.arraycopy(r, 0, data, row * cols, cols)
    }

    fun rowAdd(row: Int, g2: ByteArray) {
        var off = row * cols
        for (k in 0 until cols) {
            data[off] = (data[off].toInt() xor (g2[k].toInt() and 0xFF)).toByte()
            off++
        }
    }

    fun rowAddMul(row: Int, g2: ByteArray, x: Int) {
        if (x == 0) {
            return
        }
        if (x == 1) {
            rowAdd(row, g2)
            return
        }
        var off = row * cols
        for (k in 0 until cols) {
            val mul = octMul(g2[k].toInt() and 0xFF, x)
            data[off] = (data[off].toInt() xor mul).toByte()
            off++
        }
    }

    fun rowMul(row: Int, x: Int) {
        var off = row * cols
        for (k in 0 until cols) {
            data[off] = octMul(data[off].toInt() and 0xFF, x).toByte()
            off++
        }
    }

    fun addInPlace(s: MatrixGF256) {
        for (r in 0 until s.rows) {
            rowAdd(r, s.getRow(r))
        }
    }

    fun setFrom(g: MatrixGF256, rowOffset: Int, colOffset: Int) {
        for (r in 0 until g.rows) {
            System.arraycopy(g.data, r * g.cols, data, (r + rowOffset) * cols + colOffset, g.cols)
        }
    }

    fun setFromBlock(
        blockFrom: MatrixGF256,
        blockRowOffset: Int,
        blockColOffset: Int,
        blockRowSize: Int,
        blockColSize: Int,
        setRowOffset: Int,
        setColOffset: Int
    ) {
        for (row in 0 until blockRowSize) {
            System.arraycopy(
                blockFrom.data,
                (row + blockRowOffset) * blockFrom.cols + blockColOffset,
                data,
                (row + setRowOffset) * cols + setColOffset,
                blockColSize
            )
        }
    }

    fun applyPermutationInPlace(permutation: IntArray) {
        if (permutation.size != rows || rows <= 1) {
            val res = MatrixGF256(rows, cols)
            for (row in 0 until rows) {
                res.rowSet(row, getRow(permutation[row]))
            }
            System.arraycopy(res.data, 0, data, 0, data.size)
            return
        }
        val visited = BooleanArray(rows)
        val tmp = ByteArray(cols)
        for (start in 0 until rows) {
            if (visited[start] || permutation[start] == start) {
                visited[start] = true
                continue
            }
            System.arraycopy(data, start * cols, tmp, 0, cols)
            var pos = start
            while (true) {
                visited[pos] = true
                val next = permutation[pos]
                if (next == start) {
                    break
                }
                System.arraycopy(data, next * cols, data, pos * cols, cols)
                pos = next
            }
            System.arraycopy(tmp, 0, data, pos * cols, cols)
        }
    }
}

internal class PlainMatrixGF2(val rows: Int, val cols: Int, val data: ByteArray = ByteArray(rows * ((cols + 7) / 8))) {
    val rowSize: Int = (cols + 7) / 8

    fun set(row: Int, col: Int) {
        val el = row * rowSize + col / 8
        data[el] = (data[el].toInt() or (1 shl (col % 8))).toByte()
    }

    fun get(row: Int, col: Int): Int {
        val el = row * rowSize + col / 8
        return (data[el].toInt() shr (col % 8)) and 1
    }

    fun getRow(row: Int): ByteArray {
        val first = row * rowSize
        return data.copyOfRange(first, first + rowSize)
    }

    fun rowAdd(row: Int, what: ByteArray) {
        val first = row * rowSize
        for (i in what.indices) {
            data[first + i] = (data[first + i].toInt() xor (what[i].toInt() and 0xFF)).toByte()
        }
    }

    fun mulTo(s: MatrixGF256, mg: PlainMatrixGF2): PlainMatrixGF2 {
        mg.data.fill(0)
        var i = 0
        while (i < s.data.size) {
            if (s.data[i] != 0.toByte()) {
                val row = i / s.cols
                val col = i % s.cols
                mg.rowAdd(row, getRow(col))
            }
            i++
        }
        return mg
    }

    fun rowToGF256(row: Int, dst: ByteArray, dstOffset: Int) {
        val first = row * rowSize
        var col = 0
        for (i in 0 until rowSize) {
            val b = data[first + i].toInt() and 0xFF
            var bit = 0
            while (bit < 8 && col < cols) {
                dst[dstOffset + col] = ((b shr bit) and 1).toByte()
                bit++
                col++
            }
        }
    }
}

internal class UpperMatrixEntries(val rows: IntArray, val cols: IntArray, var n: Int = 0, var overflow: Boolean = false) {
    fun valid(): Boolean = !overflow && n <= rows.size && n <= cols.size
}

internal class UpperMatrixBuilder(val m: MatrixGF256, maxEntries: Int) {
    val entries = UpperMatrixEntries(IntArray(maxEntries), IntArray(maxEntries))

    fun set(row: Int, col: Int) {
        if (m.get(row, col) != 0.toByte()) {
            return
        }
        m.set(row, col, 1)
        if (entries.overflow) {
            return
        }
        if (entries.n >= entries.rows.size) {
            entries.overflow = true
            return
        }
        entries.rows[entries.n] = row
        entries.cols[entries.n] = col
        entries.n++
    }
}

private class UpperSparseIndex(
    val rowStarts: IntArray,
    val rowCols: IntArray,
    val colStarts: IntArray,
    val colRows: IntArray
) {
    fun rowColsFor(row: Int): IntArray {
        return rowCols.copyOfRange(rowStarts[row], rowStarts[row + 1])
    }

    fun colRowsFor(col: Int): IntArray {
        return colRows.copyOfRange(colStarts[col], colStarts[col + 1])
    }
}

private class InactivateResult(val uSize: Int, val rows: ArrayList<Int>, val cols: ArrayList<Int>)

private fun inactivateDecode(l: MatrixGF256, pi: Int, entries: UpperMatrixEntries): InactivateResult {
    val cols = l.cols - pi
    val rows = l.rows
    val dec = InactivateDecoder(cols, rows)

    dec.indexFromEntries(entries)
    dec.sort()
    dec.loop()

    for (row in 0 until rows) {
        if (!dec.wasRow[row]) {
            dec.pRows.add(row)
        }
    }

    val uSize = dec.pCols.size
    dec.inactiveCols.reverse()

    for (col in dec.inactiveCols) {
        dec.pCols.add(col)
    }

    for (i in 0 until pi) {
        dec.pCols.add(cols + i)
    }

    return InactivateResult(uSize, dec.pRows, dec.pCols)
}

private class InactivateDecoder(val cols: Int, val rows: Int) {
    val wasRow = BooleanArray(rows)
    val wasCol = BooleanArray(cols)
    val colCnt = IntArray(cols)
    val rowCnt = IntArray(rows)
    val rowXor = IntArray(rows)
    var rowCntOffset = IntArray(0)
    var sortedRows = IntArray(0)
    var rowPos = IntArray(0)
    var rowStarts = IntArray(0)
    var rowCols = IntArray(0)
    var colStarts = IntArray(0)
    var colRows = IntArray(0)
    val pRows = ArrayList<Int>()
    val pCols = ArrayList<Int>()
    val inactiveCols = ArrayList<Int>()

    fun indexFromEntries(entries: UpperMatrixEntries) {
        var nonZero = 0
        for (i in 0 until entries.n) {
            val row = entries.rows[i]
            val col = entries.cols[i]
            if (row >= rows || col >= cols) {
                continue
            }
            colCnt[col]++
            rowCnt[row]++
            rowXor[row] = rowXor[row] xor col
            nonZero++
        }

        rowStarts = IntArray(rows + 1)
        colStarts = IntArray(cols + 1)

        var offset = 0
        for (row in 0 until rows) {
            rowStarts[row] = offset
            offset += rowCnt[row]
        }
        rowStarts[rows] = offset

        offset = 0
        for (col in 0 until cols) {
            colStarts[col] = offset
            offset += colCnt[col]
        }
        colStarts[cols] = offset

        rowCols = IntArray(nonZero)
        colRows = IntArray(nonZero)

        val rowCursor = rowStarts.copyOf(rows)
        val colCursor = colStarts.copyOf(cols)

        for (i in 0 until entries.n) {
            val row = entries.rows[i]
            val col = entries.cols[i]
            if (row >= rows || col >= cols) {
                continue
            }
            val rowPos = rowCursor[row]
            rowCols[rowPos] = col
            rowCursor[row]++

            val colPos = colCursor[col]
            colRows[colPos] = row
            colCursor[col]++
        }
    }

    fun rowColumns(row: Int): IntArray {
        return rowCols.copyOfRange(rowStarts[row], rowStarts[row + 1])
    }

    fun columnRows(col: Int): IntArray {
        return colRows.copyOfRange(colStarts[col], colStarts[col + 1])
    }

    fun sort() {
        val offset = IntArray(cols + 2)
        for (i in 0 until rows) {
            offset[rowCnt[i] + 1]++
        }
        for (i in 1..cols + 1) {
            offset[i] += offset[i - 1]
        }
        rowCntOffset = offset.copyOf(rows)

        sortedRows = IntArray(rows)
        rowPos = IntArray(rows)
        for (i in 0 until rows) {
            val pos = offset[rowCnt[i]]
            offset[rowCnt[i]]++

            sortedRows[pos] = i
            rowPos[i] = pos
        }
    }

    fun loop() {
        while (rowCntOffset[1] != rows) {
            val row = sortedRows[rowCntOffset[1]]
            val col = chooseCol(row)

            val cnt = rowCnt[row]
            pCols.add(col)
            pRows.add(row)

            if (cnt == 1) {
                inactivate(col)
            } else {
                for (x in rowColumns(row)) {
                    if (wasCol[x]) {
                        continue
                    }
                    if (x != col) {
                        inactiveCols.add(x)
                    }
                    inactivate(x)
                }
            }
            wasRow[row] = true
        }
    }

    fun chooseCol(row: Int): Int {
        val cnt = rowCnt[row]
        if (cnt == 1) {
            return rowXor[row]
        }
        var bestCol = -1
        for (col in rowColumns(row)) {
            if (wasCol[col]) {
                continue
            }
            if (bestCol == -1 || colCnt[col] < colCnt[bestCol]) {
                bestCol = col
            }
        }
        return bestCol
    }

    fun inactivate(col: Int) {
        wasCol[col] = true
        for (row in columnRows(col)) {
            if (wasRow[row]) {
                continue
            }

            val pos = rowPos[row]
            val cnt = rowCnt[row]
            val offset = rowCntOffset[cnt]
            val tmp = sortedRows[pos]
            sortedRows[pos] = sortedRows[offset]
            sortedRows[offset] = tmp

            rowPos[sortedRows[pos]] = pos
            rowPos[sortedRows[offset]] = offset
            rowCntOffset[cnt]++
            rowCnt[row]--
            rowXor[row] = rowXor[row] xor col
        }
    }
}

private fun applyRCPermutationAndUpperIndex(
    m: MatrixGF256,
    entries: UpperMatrixEntries,
    rPerm: IntArray,
    cPerm: IntArray,
    rowsLimit: Int,
    colIndexLimit: Int,
    maxUpperNonZero: Int
): Pair<MatrixGF256, UpperSparseIndex> {
    val res = MatrixGF256(m.rows, m.cols)
    val rowCounts = IntArray(rowsLimit)
    val colCounts = IntArray(colIndexLimit)
    val upperRows = IntArray(maxUpperNonZero)
    val upperCols = IntArray(maxUpperNonZero)

    var rowNNZ = 0
    var colNNZ = 0
    for (i in 0 until entries.n) {
        val row = entries.rows[i]
        val col = entries.cols[i]
        val dstRow = rPerm[row]
        val dstCol = cPerm[col]
        res.data[dstRow * res.cols + dstCol] = 1
        if (dstRow < rowsLimit) {
            upperRows[rowNNZ] = dstRow
            upperCols[rowNNZ] = dstCol
            rowCounts[dstRow]++
            rowNNZ++
            if (dstCol < colIndexLimit) {
                colCounts[dstCol]++
                colNNZ++
            }
        }
    }

    val idx = newUpperSparseIndexFromCounts(rowCounts, colCounts, rowNNZ, colNNZ, rowsLimit, colIndexLimit)

    val rowCursor = idx.rowStarts.copyOf(rowsLimit)
    val colCursor = idx.colStarts.copyOf(colIndexLimit)

    for (i in 0 until rowNNZ) {
        val dstRow = upperRows[i]
        val dstCol = upperCols[i]

        val rowPos = rowCursor[dstRow]
        idx.rowCols[rowPos] = dstCol
        rowCursor[dstRow]++

        if (dstCol < colIndexLimit) {
            val colPos = colCursor[dstCol]
            idx.colRows[colPos] = dstRow
            colCursor[dstCol]++
        }
    }

    return Pair(res, idx)
}

private fun newUpperSparseIndexFromCounts(
    rowCounts: IntArray,
    colCounts: IntArray,
    rowNNZ: Int,
    colNNZ: Int,
    rowsLimit: Int,
    colIndexLimit: Int
): UpperSparseIndex {
    val rowStarts = IntArray(rowsLimit + 1)
    val colStarts = IntArray(colIndexLimit + 1)

    var offset = 0
    for (row in 0 until rowsLimit) {
        rowStarts[row] = offset
        offset += rowCounts[row]
    }
    rowStarts[rowsLimit] = offset

    offset = 0
    for (col in 0 until colIndexLimit) {
        colStarts[col] = offset
        offset += colCounts[col]
    }
    colStarts[colIndexLimit] = offset

    return UpperSparseIndex(rowStarts, IntArray(rowNNZ), colStarts, IntArray(colNNZ))
}

private fun inversePermutation(mut: IntArray): IntArray {
    val res = IntArray(mut.size)
    for (i in mut.indices) {
        res[mut[i]] = i
    }
    return res
}

private fun applyPermutation(m: MatrixGF256, permutation: IntArray): MatrixGF256 {
    val res = MatrixGF256(m.rows, m.cols)
    for (row in 0 until m.rows) {
        res.rowSet(row, m.getRow(permutation[row]))
    }
    return res
}

private fun getBlock(m: MatrixGF256, rowOffset: Int, colOffset: Int, rowSize: Int, colSize: Int): MatrixGF256 {
    val res = MatrixGF256(rowSize, colSize)
    res.setFromBlock(m, rowOffset, colOffset, rowSize, colSize, 0, 0)
    return res
}

private fun setBinaryBlock(dst: MatrixGF256, src: MatrixGF256, rowOffset: Int, colOffset: Int, rowSize: Int, colSize: Int) {
    for (row in 0 until rowSize) {
        val srcOff = (row + rowOffset) * src.cols + colOffset
        val dstOff = row * dst.cols
        for (col in 0 until colSize) {
            if (src.data[srcOff + col] != 0.toByte()) {
                dst.data[dstOff + col] = 1
            }
        }
    }
}

private fun toGF2(m: MatrixGF256, rowFrom: Int, colFrom: Int, rowSize: Int, colSize: Int): PlainMatrixGF2 {
    val mGF2 = PlainMatrixGF2(rowSize, colSize)
    for (row in rowFrom until rowFrom + rowSize) {
        val off = row * m.cols + colFrom
        for (col in 0 until colSize) {
            if (m.data[off + col] != 0.toByte()) {
                mGF2.set(row - rowFrom, col)
            }
        }
    }
    return mGF2
}

private fun mulSparse(m: MatrixGF256, s: MatrixGF256): MatrixGF256 {
    val mg = MatrixGF256(s.rows, m.cols)
    for (row in 0 until s.rows) {
        val off = row * s.cols
        for (col in 0 until s.cols) {
            if (s.data[off + col] != 0.toByte()) {
                mg.rowAdd(row, m.getRow(col))
            }
        }
    }
    return mg
}

private fun mulGF2(m: PlainMatrixGF2, s: MatrixGF256): PlainMatrixGF2 {
    return m.mulTo(s, PlainMatrixGF2(s.rows, m.cols))
}

private fun plainGF2ToGF256(m: PlainMatrixGF2): MatrixGF256 {
    val mg = MatrixGF256(m.rows, m.cols)
    for (row in 0 until m.rows) {
        m.rowToGF256(row, mg.data, row * mg.cols)
    }
    return mg
}

private fun gaussianElimination(a: MatrixGF256, d: MatrixGF256): MatrixGF256? {
    val rows = a.rows
    val rowPerm = IntArray(rows) { it }

    for (row in 0 until a.cols) {
        var nonZero = row
        while (nonZero < rows && a.get(rowPerm[nonZero], row) == 0.toByte()) {
            nonZero++
        }
        if (nonZero == rows) {
            return null
        }

        if (nonZero != row) {
            val t = rowPerm[nonZero]
            rowPerm[nonZero] = rowPerm[row]
            rowPerm[row] = t
        }

        val mul = octInverse(a.get(rowPerm[row], row).toInt() and 0xFF)

        a.rowMul(rowPerm[row], mul)
        d.rowMul(rowPerm[row], mul)

        for (zeroRow in 0 until rows) {
            if (zeroRow == row) {
                continue
            }
            val x = a.get(rowPerm[zeroRow], row).toInt() and 0xFF
            if (x != 0) {
                a.rowAddMul(rowPerm[zeroRow], a.getRow(rowPerm[row]), x)
                d.rowAddMul(rowPerm[zeroRow], d.getRow(rowPerm[row]), x)
            }
        }
    }

    d.applyPermutationInPlace(rowPerm)
    return d
}

private fun findRawParams(k: Int): IntArray {
    for (entry in ParamsTable) {
        if (entry[0] >= k) {
            return entry
        }
    }
    throw IllegalArgumentException("k is too big")
}

private fun calcParams(dataSize: Int, symbolSz: Int): RaptorQParams {
    val k = (dataSize + symbolSz - 1) / symbolSz
    val raw = findRawParams(k)
    val l = raw[0] + raw[2] + raw[3]
    val b = raw[4] - raw[2]
    val p = l - raw[4]
    val u = p - raw[3]
    var p1 = p + 1
    while (!isPrime(p1)) {
        p1++
    }
    return RaptorQParams(k, raw[0], raw[1], raw[2], raw[3], raw[4], l, p, p1, u, b)
}

class RaptorQCodec(val symbolSize: Int, val dataSize: Int) {
    internal val params: RaptorQParams = calcParams(dataSize, symbolSize)

    fun newDecoder(): RaptorQDecoder {
        return RaptorQDecoder(this)
    }
}

class RaptorQDecoder(private val codec: RaptorQCodec) {
    private val symbolSz: Int = codec.symbolSize
    private val dataSz: Int = codec.dataSize
    private val params: RaptorQParams = codec.params
    private val fastSeen = BooleanArray(params.K)
    private val fastSymbols = ByteArray(params.K * symbolSz)
    private val slowIDs = ArrayList<Int>()
    private val slowSymbols = ArrayList<ByteArray>()
    private var fastNum = 0
    private var slowNum = 0

    fun addBlocks(blocks: List<LTBlock>): Boolean {
        for (block in blocks) {
            val id = block.blockCode.toInt()
            val data = block.data
            if (id < params.K) {
                if (!fastSeen[id]) {
                    System.arraycopy(data, 0, fastSymbols, id * symbolSz, symbolSz)
                    fastSeen[id] = true
                    fastNum++
                }
            } else {
                val k = id + params.KPadded - params.K
                if (!slowIDs.contains(k)) {
                    slowIDs.add(k)
                    slowSymbols.add(data.copyOf(symbolSz))
                    slowNum++
                }
            }
        }
        return fastNum + slowNum >= params.K
    }

    fun decode(): ByteArray? {
        if (fastNum + slowNum < params.K) {
            return null
        }
        if (fastNum == params.K) {
            return fastSymbols.copyOfRange(0, dataSz)
        }

        var sz = params.K + slowNum
        if (sz < params.KPadded) {
            sz = params.KPadded
        }

        val symbols = ArrayList<Symbol>(sz)
        for (i in 0 until params.K) {
            if (fastSeen[i]) {
                symbols.add(Symbol(i, fastSymbols.copyOfRange(i * symbolSz, (i + 1) * symbolSz)))
            }
        }
        for (i in 0 until slowIDs.size) {
            symbols.add(Symbol(slowIDs[i], slowSymbols[i]))
        }
        while (symbols.size < params.KPadded) {
            symbols.add(Symbol(symbols.size, ByteArray(symbolSz)))
        }

        val relaxed = params.solve(symbols) ?: return null

        val out = ByteArray(params.K * symbolSz)
        for (i in 0 until params.K) {
            if (fastSeen[i]) {
                System.arraycopy(fastSymbols, i * symbolSz, out, i * symbolSz, symbolSz)
            } else {
                val row = params.calcEncodingRow(i)
                row.encodeGen(out, i * symbolSz, relaxed, symbolSz, params)
            }
        }
        return out.copyOfRange(0, dataSz)
    }
}
