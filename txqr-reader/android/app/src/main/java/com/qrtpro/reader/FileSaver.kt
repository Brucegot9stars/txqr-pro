package com.qrtpro.reader

import android.content.ContentValues
import android.content.Context
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import android.widget.Toast

object FileSaver {

    /**
     * Write [data] to the public Downloads directory.
     *
     * Returns a result instead of only toasting: the caller owns the UI state
     * and needs to know whether the save succeeded, which the old Unit return
     * made impossible (a failure and a success looked identical to it).
     */
    fun saveData(context: Context, data: ByteArray, rawFileName: String): Boolean {
        val fileName = sanitizeFileName(rawFileName)
        if (fileName.isEmpty()) return false
        return try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                val contentValues = ContentValues().apply {
                    put(MediaStore.Downloads.DISPLAY_NAME, fileName)
                    put(MediaStore.Downloads.MIME_TYPE, detectMimeType(fileName))
                    put(MediaStore.Downloads.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS)
                }

                val resolver = context.contentResolver
                val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, contentValues)
                    ?: throw Exception("Failed to create MediaStore entry")

                val stream = resolver.openOutputStream(uri)
                    ?: throw Exception("Failed to open output stream")
                stream.use { it.write(data) }
            } else {
                val downloadsDir = Environment.getExternalStoragePublicDirectory(
                    Environment.DIRECTORY_DOWNLOADS
                )
                if (!downloadsDir.exists() && !downloadsDir.mkdirs()) {
                    throw Exception("Failed to create Downloads directory")
                }
                val file = java.io.File(downloadsDir, fileName)
                // Canonical-path check: a name like "../../x" must not escape
                // the Downloads directory.
                val expected = java.io.File(downloadsDir.canonicalPath)
                val target = java.io.File(file.canonicalPath)
                if (!target.path.startsWith(expected.path + java.io.File.separator)) {
                    throw Exception("Invalid file name")
                }
                file.writeBytes(data)
            }
            Toast.makeText(context, "Saved: $fileName", Toast.LENGTH_LONG).show()
            true
        } catch (e: Exception) {
            Toast.makeText(context, "Save failed: ${e.message}", Toast.LENGTH_LONG).show()
            false
        }
    }

    /**
     * Reduce a user-typed name to something safe as a single file name: no path
     * separators (so it cannot escape the target directory), no control
     * characters, no leading/trailing dots or spaces, and a sane length cap.
     */
    fun sanitizeFileName(name: String): String {
        val cleaned = name
            .replace(Regex("[\\u0000-\\u001f/\\\\:*?\"<>|]"), "_")
            .trim()
            .trimStart('.')
            .take(120)
        return if (cleaned.isBlank()) "" else cleaned
    }

    private fun detectMimeType(fileName: String): String {
        val ext = fileName.substringAfterLast('.', "").lowercase()
        return when (ext) {
            "jpg", "jpeg" -> "image/jpeg"
            "png" -> "image/png"
            "gif" -> "image/gif"
            "pdf" -> "application/pdf"
            "txt" -> "text/plain"
            "json" -> "application/json"
            "zip" -> "application/zip"
            "mp4" -> "video/mp4"
            "mp3" -> "audio/mpeg"
            else -> "application/octet-stream"
        }
    }
}
