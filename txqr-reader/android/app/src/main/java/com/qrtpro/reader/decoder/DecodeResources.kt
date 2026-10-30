package com.qrtpro.reader.decoder

/** Leave room for camera bitmaps, UI and GC copies. This is a working-data
 * budget, not a claim that the JVM's total RSS can be capped by this class. */
fun decoderWorkingBudget(): Long = minOf(256L * 1024 * 1024, Runtime.getRuntime().maxMemory() / 3)

class DecodeResourceLimit : IllegalStateException("解码内存预算不足，请减小文件、增大分片或选择 decimen 模式后重扫")
