package com.qrtpro.reader.decoder

/** Map module coordinates to observed finder pixels; the sampler uses this direction. */
fun moduleToPixelAffine(modulePts: List<Pair<Int,Int>>, pixelPts: List<Pair<Int,Int>>): DoubleArray? {
    if (modulePts.size != 3 || pixelPts.size != 3) return null
    val x1=modulePts[0].first.toDouble(); val y1=modulePts[0].second.toDouble()
    val x2=modulePts[1].first.toDouble(); val y2=modulePts[1].second.toDouble()
    val x3=modulePts[2].first.toDouble(); val y3=modulePts[2].second.toDouble()
    val det=(x1-x3)*(y2-y3)-(x2-x3)*(y1-y3)
    if (kotlin.math.abs(det)<1e-6) return null
    fun axis(values: List<Double>): DoubleArray {
        val a=((values[0]-values[2])*(y2-y3)-(values[1]-values[2])*(y1-y3))/det
        val b=((values[0]-values[2])*(x3-x2)+(values[1]-values[2])*(x1-x3))/det
        return doubleArrayOf(a,b,values[0]-a*x1-b*y1)
    }
    return axis(pixelPts.map{it.first.toDouble()}) + axis(pixelPts.map{it.second.toDouble()})
}
