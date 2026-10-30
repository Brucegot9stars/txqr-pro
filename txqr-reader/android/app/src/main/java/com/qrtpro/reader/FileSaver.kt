package com.qrtpro.reader

import android.content.ContentValues
import android.content.Context
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import java.io.File
import java.io.FileOutputStream

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
                    put(MediaStore.Downloads.IS_PENDING,1)
                }

                val resolver = context.contentResolver
                val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, contentValues)
                    ?: throw Exception("Failed to create MediaStore entry")

                try {
                    val stream = resolver.openOutputStream(uri)
                        ?: throw Exception("Failed to open output stream")
                    stream.use { it.write(data) }
                    check(resolver.update(uri,ContentValues().apply { put(MediaStore.Downloads.IS_PENDING,0) },null,null)==1) {
                        "Failed to publish download"
                    }
                } catch (e: Exception) {
                    resolver.delete(uri,null,null)
                    throw e
                }
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
                // Write completely before publishing; move without REPLACE_EXISTING
                // preserves previous downloads and works on emulated storage.
                val temp = File.createTempFile(".qrt-", ".part",downloadsDir)
                try {
                    FileOutputStream(temp).use { it.write(data); it.fd.sync() }
                    var published = false
                    for (index in 1..1000) {
                        val candidate = if (index==1) file else File(downloadsDir,
                            file.nameWithoutExtension + " ($index)" + file.extension.let { if(it.isBlank()) "" else ".$it" })
                        try {
                            java.nio.file.Files.move(temp.toPath(),candidate.toPath())
                            published = true
                            break
                        } catch(_: java.nio.file.FileAlreadyExistsException) {
                            // Try the next available name.
                        }
                    }
                    check(published) { "Too many files with the same name" }
                } finally { temp.delete() }
            }
            true
        } catch (e: Exception) {
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
