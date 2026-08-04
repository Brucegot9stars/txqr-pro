package com.txqr.reader.decoder

enum class CodecType(val id: String) {
    LT(""),
    BINARY("binary"),
    RAPTOR("raptor"),
    RAPTORQ("raptorq"),
    ONLINE("online");

    companion object {
        fun fromString(s: String?): CodecType {
            return when (s?.lowercase()) {
                "binary" -> BINARY
                "raptor" -> RAPTOR
                "raptorq" -> RAPTORQ
                "online" -> ONLINE
                else -> LT
            }
        }
    }
}
