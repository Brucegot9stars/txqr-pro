package com.qrtpro.reader

import android.app.Application
import android.graphics.Bitmap
import android.graphics.Matrix
import android.graphics.Rect
import android.util.Log
import androidx.camera.core.ImageProxy
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.google.mlkit.vision.barcode.BarcodeScanner
import com.google.mlkit.vision.barcode.BarcodeScanning
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.common.InputImage
import com.qrtpro.reader.decoder.ReceiveSession
import com.qrtpro.reader.decoder.ReceiveUpdate
import com.qrtpro.reader.decoder.LatestFrameProcessor
import com.qrtpro.reader.decoder.moduleToPixelAffine
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import com.qrtpro.reader.decoder.TxqrDecoder
import com.qrtpro.reader.decoder.ColorMatrixDecoder
import com.qrtpro.reader.decoder.ColorMatrixDecodeException
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

enum class ScanState {
    SCANNING,
    COMPLETED,
    FAILED,
    SAVING,
    IDLE
}

data class ScanResult(
    val data: ByteArray? = null,
    val fileName: String = "",
    val totalSize: String = "",
    val totalTime: String = "",
    val avgSpeed: String = "",
    val peakSpeed: String = "",
    val speed: String = "",
    val md5: String = ""
)

data class ScannerUiState(
    val scanState: ScanState = ScanState.IDLE,
    val progress: Int = 0,
    val speed: String = "",
    val readInterval: Long = 0,
    val totalSize: String = "",
    val bytesDetail: String = "",
    val totalTime: String = "",
    val avgSpeed: String = "",
    val peakSpeed: String = "",
    val fileName: String = "",
    val suggestedFileName: String = "",
    val md5: String = "",
    val error: String? = null,
    val warning: String? = null,
    val scanResult: ScanResult? = null
)

class ScannerViewModel(application: Application) : AndroidViewModel(application) {
    private var receiveSession = ReceiveSession()
    private var published: ReceiveUpdate? = null
    @Volatile
    private var barcodeScanner: BarcodeScanner? = null

    // Tracking is shared with the analyzer. Decoders belong exclusively to the
    // background processor; the main thread only reads immutable publications.
    @Volatile
    private var trackBox: Rect? = null

    private val scanGeneration = java.util.concurrent.atomic.AtomicLong(0)
    private val decodeInFlight = java.util.concurrent.atomic.AtomicBoolean(false)

    private val _uiState = MutableStateFlow(ScannerUiState())
    val uiState: StateFlow<ScannerUiState> = _uiState.asStateFlow()

    private val _colorMode = MutableStateFlow(false)
    val colorMode: StateFlow<Boolean> = _colorMode.asStateFlow()

    private val _decimenMode = MutableStateFlow(false)
    val decimenMode: StateFlow<Boolean> = _decimenMode.asStateFlow()

    private var uiTicker: Job? = null

    var scanResultData: ByteArray? = null
        private set

    private data class FrameBatch(val generation: Long, val session: ReceiveSession, val frames: List<ByteArray>)
    private val frameProcessor = LatestFrameProcessor<FrameBatch, ReceiveUpdate?>(
        viewModelScope,
        process = { batch -> batch.session.accept(batch.frames) { batch.generation == scanGeneration.get() } },
        publish = { batch, update ->
            if (update != null && batch.generation == scanGeneration.get() &&
                _uiState.value.scanState == ScanState.SCANNING) applyUpdate(update)
        },
    )

    init {
        barcodeScanner = BarcodeScanning.getClient()
        _uiState.value = ScannerUiState(scanState = ScanState.SCANNING)
        startUiTicker()
    }

    private fun startUiTicker() {
        uiTicker?.cancel()
        uiTicker = viewModelScope.launch {
            while (true) {
                refreshUiState()
                // Poll only while waiting for metadata. The session serializes
                // this background check with frame work; it never drops a queued
                // camera batch or acquires the decoder lock on the UI thread.
                if (published?.waitingForName == true && _uiState.value.scanState == ScanState.SCANNING) {
                    val generation = scanGeneration.get()
                    val session = receiveSession
                    val update = withContext(Dispatchers.Default) {
                        session.accept(emptyList()) { generation == scanGeneration.get() }
                    }
                    if (update != null && generation == scanGeneration.get() &&
                        _uiState.value.scanState == ScanState.SCANNING) applyUpdate(update)
                }
                delay(500)
            }
        }
    }

    private fun refreshUiState() {
        if (_uiState.value.scanState != ScanState.SCANNING) {
            uiTicker?.cancel()
            uiTicker = null
            return
        }
        val update = published ?: return
        val now = System.nanoTime() / 1_000_000
        _uiState.update { it.copy(
            speed = if (now - update.observedAt > 2000) "0B/s" else update.speed,
            avgSpeed = update.avgSpeed(now), totalTime = update.duration(now),
        ) }
    }

    private fun applyUpdate(update: ReceiveUpdate) {
        published = update
        _decimenMode.value = update.decimen
        val now = System.nanoTime() / 1_000_000
        if (update.data != null) scanResultData = update.data
        _uiState.update { it.copy(
            scanState = when {
                update.error != null -> ScanState.FAILED
                update.data != null -> ScanState.COMPLETED
                else -> ScanState.SCANNING
            },
            progress = update.progress, speed = update.speed,
            avgSpeed = update.avgSpeed(now), peakSpeed = update.peakSpeed,
            readInterval = update.readInterval, totalSize = TxqrDecoder.formatSize(update.totalBytes),
            bytesDetail = update.bytesDetail, totalTime = update.duration(now),
            suggestedFileName = update.fileName, md5 = update.md5, error = update.error, warning = update.warning,
        ) }
    }

    // The bounded queue favors fresh camera frames under load. It preserves
    // order within each batch, and only one batch can mutate a decoder at once.
    private fun enqueueFrames(frames: List<ByteArray>, generation: Long) {
        if (generation != scanGeneration.get() || _uiState.value.scanState != ScanState.SCANNING) return
        val bounded = frames.asSequence().filter { it.size <= 4096 }.take(16).map { it.copyOf() }.toList()
        if (bounded.isNotEmpty()) frameProcessor.submit(FrameBatch(generation,receiveSession,bounded))
    }

    fun onQrCodeScanned(imageProxy: ImageProxy) {
        // Runs on the analyzer thread; decoder state belongs to the background queue.
        val generation = scanGeneration.get()
        val scanning = _uiState.value.scanState == ScanState.SCANNING
        if (!scanning) {
            imageProxy.close()
            return
        }

        if (_colorMode.value) {
            decodeColorMatrix(imageProxy, generation)
            return
        }

        // Tracked decode: convert the camera frame to an upright bitmap and
        // close the ImageProxy right away, then run ML Kit on a crop around
        // the last known barcode position (a "zoom" that raises the effective
        // module resolution for dense V40 codes). A fresh or lost lock falls
        // back to the full frame. Frames are dropped while a decode is in
        // flight — a stale frame is worth less than the next one.
        if (!decodeInFlight.compareAndSet(false, true)) {
            imageProxy.close()
            return
        }
        val scanner = barcodeScanner
        if (scanner == null) {
            // Without this the guard below would never be released and every
            // later frame would be dropped silently.
            decodeInFlight.set(false)
            imageProxy.close()
            return
        }
        val bitmap = try {
            imageProxyToBitmap(imageProxy)
        } catch (t: Throwable) {
            Log.w("QRT", "frame conversion failed: ${t.message}")
            null
        } finally {
            imageProxy.close()
        }
        if (bitmap == null) {
            decodeInFlight.set(false)
            return
        }

        val track = trackBox?.let { Rect(it) }
        // The final task owns the full bitmap and guard, including a crop retry.
        val released = java.util.concurrent.atomic.AtomicBoolean(false)
        val release = {
            if (released.compareAndSet(false,true)) {
                if (!bitmap.isRecycled) bitmap.recycle()
                decodeInFlight.set(false)
            }
        }
        var cropBitmap: Bitmap? = null
        try {
            val crop = track?.let { expandRect(it,bitmap.width,bitmap.height) }
            val cropped = crop?.let { safeCrop(bitmap,it) }
            cropBitmap = cropped
            if (cropped == null) {
                submit(scanner,bitmap,null,generation,release)
            } else {
                scanner.process(InputImage.fromBitmap(cropped,0))
                    .addOnCompleteListener { task ->
                        val hits = if (task.isSuccessful) task.result else emptyList()
                        if (cropped !== bitmap && !cropped.isRecycled) cropped.recycle()
                        if (generation != scanGeneration.get() || _uiState.value.scanState != ScanState.SCANNING) {
                            release()
                        } else if (hits.isNotEmpty()) {
                            try { consumeBarcodes(hits,crop,generation) } finally { release() }
                        } else {
                            // Crop task has finished; keep the full bitmap alive for its retry.
                            submit(scanner,bitmap,null,generation,release)
                        }
                    }
            }
        } catch (e: Exception) {
            Log.w("QRT","barcode submit failed: ${e.message}")
            cropBitmap?.let { if(it !== bitmap && !it.isRecycled) it.recycle() }
            release()
        }
    }

    /** Run ML Kit on a bitmap, feeding hits to the pipeline and freeing it. */
    private fun submit(
        scanner: BarcodeScanner,
        bitmap: Bitmap,
        crop: Rect?,
        generation: Long,
        onDone: () -> Unit
    ) {
        try {
            scanner.process(InputImage.fromBitmap(bitmap, 0))
                .addOnCompleteListener { task ->
                    try { if(task.isSuccessful) consumeBarcodes(task.result,crop,generation) }
                    finally { onDone() }
                }
        } catch (t: Throwable) {
            Log.w("QRT", "barcode submit failed: ${t.message}")
            onDone()
        }
    }

    /** Crop [bitmap] to [crop], or null when the rect is empty or outside it. */
    private fun safeCrop(bitmap: Bitmap, crop: Rect): Bitmap? {
        val l = crop.left.coerceIn(0, bitmap.width - 1)
        val t = crop.top.coerceIn(0, bitmap.height - 1)
        val r = crop.right.coerceIn(l + 1, bitmap.width)
        val b = crop.bottom.coerceIn(t + 1, bitmap.height)
        return try {
            Bitmap.createBitmap(bitmap, l, t, r - l, b - t)
        } catch (t: Throwable) {
            Log.w("QRT", "crop failed: ${t.message}")
            null
        }
    }

    /** Feed decoded barcodes into the pipeline and refresh the tracking box to
     *  the union of all found codes (so a grid stays covered in one crop). */
    private fun consumeBarcodes(barcodes: List<Barcode>, crop: Rect?, generation: Long) {
        if (generation != scanGeneration.get() || _uiState.value.scanState != ScanState.SCANNING) return
        var union: Rect? = null
        for (b in barcodes) {
            val bb = b.boundingBox ?: continue
            val mapped = if (crop != null) {
                Rect(bb.left + crop.left, bb.top + crop.top, bb.right + crop.left, bb.bottom + crop.top)
            } else {
                bb
            }
            union = if (union == null) Rect(mapped) else Rect(
                minOf(union.left, mapped.left), minOf(union.top, mapped.top),
                maxOf(union.right, mapped.right), maxOf(union.bottom, mapped.bottom)
            )
        }
        if (union != null) trackBox = union

        enqueueFrames(barcodes.mapNotNull { barcode ->
            barcode.rawBytes ?: barcode.rawValue?.toByteArray(Charsets.ISO_8859_1)
        }, generation)
    }

    private fun expandRect(r: Rect, w: Int, h: Int): Rect {
        val dw = (r.width() * 0.5f).toInt().coerceAtLeast(8)
        val dh = (r.height() * 0.5f).toInt().coerceAtLeast(8)
        return Rect(
            (r.left - dw).coerceAtLeast(0),
            (r.top - dh).coerceAtLeast(0),
            (r.right + dw).coerceAtMost(w),
            (r.bottom + dh).coerceAtMost(h)
        )
    }

    /** Convert a YUV_420_888 ImageProxy to an upright ARGB_8888 bitmap. */
    private fun imageProxyToBitmap(imageProxy: ImageProxy): Bitmap? {
        val img = imageProxy.image ?: return null
        val planes = img.planes
        if (planes.size < 3) return null
        val yPlane = planes[0]; val uPlane = planes[1]; val vPlane = planes[2]
        val yBuf = yPlane.buffer; val yRS = yPlane.rowStride; val yPS = yPlane.pixelStride
        val uBuf = uPlane.buffer; val uRS = uPlane.rowStride; val uPS = uPlane.pixelStride
        val vBuf = vPlane.buffer; val vRS = vPlane.rowStride; val vPS = vPlane.pixelStride
        val w = imageProxy.width; val h = imageProxy.height
        val pixels = IntArray(w * h)
        for (row in 0 until h) {
            val yOff = row * yRS
            val uvRow = row shr 1
            for (col in 0 until w) {
                val yi = yBuf.get(yOff + col * yPS).toInt() and 0xFF
                val uo = uvRow * uRS + (col shr 1) * uPS
                val vo = uvRow * vRS + (col shr 1) * vPS
                val ui = (uBuf.get(uo).toInt() and 0xFF) - 128
                val vi = (vBuf.get(vo).toInt() and 0xFF) - 128
                val r = (yi + (1.402f * vi).toInt()).coerceIn(0, 255)
                val g = (yi - (0.344f * ui + 0.714f * vi).toInt()).coerceIn(0, 255)
                val b = (yi + (1.772f * ui).toInt()).coerceIn(0, 255)
                pixels[row * w + col] = (0xFF shl 24) or (r shl 16) or (g shl 8) or b
            }
        }
        val bmp = Bitmap.createBitmap(pixels, w, h, Bitmap.Config.ARGB_8888)
        val rot = imageProxy.imageInfo.rotationDegrees
        if (rot == 0) return bmp
        val m = Matrix()
        m.postRotate(rot.toFloat())
        // Recycle the pre-rotation bitmap: it is a full 1920x1080 ARGB_8888
        // allocation (~8 MB) and nothing else references it after this point.
        val rotated = Bitmap.createBitmap(bmp, 0, 0, w, h, m, true)
        bmp.recycle()
        return rotated
    }

    fun setColorMode(enabled: Boolean) {
        if (_colorMode.value != enabled) { _colorMode.value = enabled; startScan() }
    }

    /**
     * Color-matrix path. The sampling itself is expensive and stays on the
     * camera analyzer thread, but it returns plain bytes: the equation solving uses the
     * background queue, and publishing [_uiState] is dispatched
     * back to [viewModelScope]. Doing both on the analyzer thread (as before)
     * raced the UI ticker over the same decoder instance.
     */
    private fun decodeColorMatrix(imageProxy: ImageProxy, generation: Long) {
        var frame: ByteArray? = null
        var note: String? = null
        try {
            @Suppress("UnsafeOptInUsageError")
            val img = imageProxy.image
            if (img == null) {
                note = "Color: no image"
            } else {
                frame = sampleColorMatrix(img, imageProxy.width, imageProxy.height)
                if (frame == null) note = "Color: no finders"
            }
        } catch (e: Exception) {
            Log.e("QRT", "Color err: ${e.message}", e)
            note = "Color: ${e.message}"
        } finally {
            imageProxy.close()
        }
        val bytes = frame
        val message = note
        viewModelScope.launch {
            if (generation != scanGeneration.get() || _uiState.value.scanState != ScanState.SCANNING) return@launch
            if (bytes != null) {
                enqueueFrames(listOf(bytes),generation)
            } else if (message != null && _uiState.value.scanState == ScanState.SCANNING) {
                _uiState.update { it.copy(bytesDetail = message) }
            }
        }
    }

    /** Pure pixel work: returns the decoded frame bytes, or null if no grid. */
    @Suppress("UnsafeOptInUsageError")
    private fun sampleColorMatrix(img: android.media.Image, w: Int, h: Int): ByteArray? {
        val planes = img.planes
        if (planes.size < 3) return null

        val yPlane = planes[0]; val uPlane = planes[1]; val vPlane = planes[2]
        val yBuf = yPlane.buffer; val yRS = yPlane.rowStride; val yPS = yPlane.pixelStride
        val uBuf = uPlane.buffer; val uRS = uPlane.rowStride; val uPS = uPlane.pixelStride
        val vBuf = vPlane.buffer; val vRS = vPlane.rowStride; val vPS = vPlane.pixelStride

        val side = minOf(w, h)
        val ox0 = (w - side) / 2; val oy0 = (h - side) / 2

        // Lum sampler: Y-plane already is luminance.
        val lumAt: (Int, Int) -> Int = { x, y -> (yBuf.get((y + oy0) * yRS + (x + ox0) * yPS).toInt() and 0xFF) }

        // Scan for finder centers at a coarse grid. Try a few module-size guesses.
        for (estModule in listOf(side / 60, side / 80, side / 100)) {
            if (estModule < 2) continue
            val centers = locateFinderCenters(lumAt, side, side, estModule)
            if (centers.size < 3) continue

            // Map finder pixel centers to module positions.
            val known = listOf(
                ColorMatrixDecoder.FINDER_OFF + ColorMatrixDecoder.FINDER_SZ / 2 to
                        ColorMatrixDecoder.FINDER_OFF + ColorMatrixDecoder.FINDER_SZ / 2,
                ColorMatrixDecoder.MATRIX_SIZE - ColorMatrixDecoder.FINDER_OFF - ColorMatrixDecoder.FINDER_SZ / 2 - 1 to
                        ColorMatrixDecoder.FINDER_OFF + ColorMatrixDecoder.FINDER_SZ / 2,
                ColorMatrixDecoder.FINDER_OFF + ColorMatrixDecoder.FINDER_SZ / 2 to
                        ColorMatrixDecoder.MATRIX_SIZE - ColorMatrixDecoder.FINDER_OFF - ColorMatrixDecoder.FINDER_SZ / 2 - 1
            )
            val coeffs = moduleToPixelAffine(known, centers) ?: continue
            val axx = coeffs[0]; val axy = coeffs[1]; val atx = coeffs[2]
            val ayx = coeffs[3]; val ayy = coeffs[4]; val aty = coeffs[5]

            // RGB sampler that maps module (x,y) -> pixel RGB via affine.
            val sampler: (Int, Int) -> Int = { mx, my ->
                val gx = (axx * mx + axy * my + atx).toInt() + ox0
                val gy = (ayx * mx + ayy * my + aty).toInt() + oy0
                if (gx < 0 || gy < 0 || gx >= w || gy >= h) 0 else {
                    val yv = (yBuf.get(gy * yRS + gx * yPS).toInt() and 0xFF)
                    val ui = gx / 2; val vi = gy / 2
                    val uv = (uBuf.get(vi * uRS + ui * uPS).toInt() and 0xFF) - 128
                    val vv = (vBuf.get(vi * vRS + ui * vPS).toInt() and 0xFF) - 128
                    val r = (yv + (1.402f * vv).toInt()).coerceIn(0, 255)
                    val g = (yv - (0.344f * uv + 0.714f * vv).toInt()).coerceIn(0, 255)
                    val b = (yv + (1.772f * uv).toInt()).coerceIn(0, 255)
                    (r shl 16) or (g shl 8) or b
                }
            }

            try {
                return ColorMatrixDecoder.decodeWithAffine(sampler)
            } catch (_: ColorMatrixDecodeException) {
            }
        }
        return null
    }

    private fun locateFinderCenters(
        lum: (Int, Int) -> Int, w: Int, h: Int, estModule: Int
    ): List<Pair<Int, Int>> {
        val step = maxOf(estModule, 1)
        data class Peak(val x: Int, val y: Int, val score: Int)

        val peaks = mutableListOf<Peak>()
        for (cy in estModule * 4 until h - estModule * 4 step step) {
            for (cx in estModule * 4 until w - estModule * 4 step step) {
                val score = finderScore(lum, cx, cy, estModule)
                if (score > 30) peaks.add(Peak(cx, cy, score))
            }
        }
        if (peaks.size < 3) return emptyList()
        peaks.sortByDescending { it.score }

        // Pick top 3 non-overlapping (min dist = 8 * estModule).
        val minDist = estModule * 8
        val result = mutableListOf<Pair<Int, Int>>()
        for (p in peaks) {
            val overlap = result.any { kotlin.math.hypot((it.first - p.x).toDouble(), (it.second - p.y).toDouble()) < minDist }
            if (!overlap) {
                result.add(p.x to p.y)
                if (result.size >= 3) break
            }
        }
        // Sort TL (top-left), TR (top-right), BL (bottom-left)
        result.sortWith(compareBy<Pair<Int, Int>> { it.first * it.first + it.second * it.second })
        // Guarded on size >= 3: with only two peaks this used to read result[2]
        // and throw IndexOutOfBoundsException from the analyzer thread.
        if (result.size >= 3 && result[1].second > result[2].second) {
            val tmp = result[1]; result[1] = result[2]; result[2] = tmp
        }
        return result
    }

    /** Rate how well a 7×7 finder pattern fits at (cx,cy) with given module size. */
    private fun finderScore(lum: (Int, Int) -> Int, cx: Int, cy: Int, mod: Int): Int {
        // 7×7 finder template: outer ring dark, inner ring light, centre dark
        var darkSum = 0; var darkN = 0
        var lightSum = 0; var lightN = 0
        for (dr in -3..3) {
            for (dc in -3..3) {
                val outer = kotlin.math.abs(dr) == 3 || kotlin.math.abs(dc) == 3
                val inner = kotlin.math.abs(dr) >= 1 && kotlin.math.abs(dc) >= 1 && kotlin.math.abs(dr) <= 2 && kotlin.math.abs(dc) <= 2
                val center = kotlin.math.abs(dr) <= 1 && kotlin.math.abs(dc) <= 1
                val v = lum(cx + dc * mod, cy + dr * mod)
                if (outer || center) { darkSum += v; darkN++ }
                else if (inner) { lightSum += v; lightN++ }
            }
        }
        if (darkN == 0 || lightN == 0) return 0
        val contrast = lightSum / lightN - darkSum / darkN
        return contrast.coerceAtLeast(0)
    }

    fun startScan() {
        scanGeneration.incrementAndGet()
        receiveSession = ReceiveSession()
        published = null
        trackBox = null
        // An older ML Kit task releases the guard only after its bitmap is no longer used.
        _decimenMode.value = false
        scanResultData = null
        _uiState.value = ScannerUiState(scanState = ScanState.SCANNING)
        startUiTicker()
    }

    fun saveDataToFile(fileName: String) {
        val data = scanResultData ?: return
        val trimmed = fileName.trim()
        if (trimmed.isEmpty()) {
            _uiState.update { it.copy(error = "文件名不能为空") }
            return
        }
        val generation = scanGeneration.get()
        _uiState.update { it.copy(scanState = ScanState.SAVING, error = null) }
        viewModelScope.launch {
            val saved = withContext(Dispatchers.IO) { runCatching { FileSaver.saveData(getApplication(), data, trimmed) }
                .onFailure { Log.e("QRT", "save failed: ${it.message}", it) }
                .getOrDefault(false) }
            if (generation != scanGeneration.get()) return@launch
            // Reading the summary fields inside update{} keeps them consistent
            // with the snapshot that is being published.
            _uiState.update { state ->
                if (!saved) {
                    state.copy(scanState = ScanState.COMPLETED, error = "保存失败")
                } else {
                    state.copy(
                        scanState = ScanState.IDLE,
                        scanResult = ScanResult(
                            data = data,
                            fileName = trimmed,
                            totalSize = state.totalSize,
                            totalTime = state.totalTime,
                            avgSpeed = state.avgSpeed,
                            peakSpeed = state.peakSpeed,
                            speed = state.speed,
                            md5 = state.md5
                        )
                    )
                }
            }
        }
    }

    override fun onCleared() {
        super.onCleared()
        scanGeneration.incrementAndGet()
        _uiState.update { it.copy(scanState=ScanState.IDLE) }
        uiTicker?.cancel()
        frameProcessor.close()
        barcodeScanner?.close()
        barcodeScanner=null
    }
}
