package com.qrtpro.reader

import android.app.Application
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.util.Log
import androidx.camera.core.ImageProxy
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.google.mlkit.vision.barcode.BarcodeScanner
import com.google.mlkit.vision.barcode.BarcodeScanning
import com.google.mlkit.vision.common.InputImage
import com.qrtpro.reader.decoder.TxqrDecoder
import com.qrtpro.reader.decoder.ColorMatrixDecoder
import com.qrtpro.reader.decoder.ColorMatrixDecodeException
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import java.security.MessageDigest
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

enum class ScanState {
    SCANNING,
    COMPLETED,
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
    val scanResult: ScanResult? = null
)

class ScannerViewModel(application: Application) : AndroidViewModel(application) {
    private val decoder = TxqrDecoder()
    private var barcodeScanner: BarcodeScanner? = null

    private val _uiState = MutableStateFlow(ScannerUiState())
    val uiState: StateFlow<ScannerUiState> = _uiState.asStateFlow()

    private val _colorMode = MutableStateFlow(false)
    val colorMode: StateFlow<Boolean> = _colorMode.asStateFlow()

    private var uiTicker: Job? = null

    var scanResultData: ByteArray? = null
        private set

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
                delay(500)
            }
        }
    }

    private fun refreshUiState() {
        val current = _uiState.value
        if (current.scanState != ScanState.SCANNING) return
        _uiState.value = current.copy(
            progress = decoder.progress,
            speed = decoder.speedStr,
            avgSpeed = decoder.avgSpeedStr,
            peakSpeed = decoder.peakSpeedStr,
            readInterval = decoder.readIntervalMs,
            totalSize = decoder.totalSizeStr,
            bytesDetail = decoder.bytesDetailStr,
            totalTime = decoder.totalTime
        )
    }

    fun onQrCodeScanned(imageProxy: ImageProxy) {
        if (_uiState.value.scanState != ScanState.SCANNING) {
            imageProxy.close()
            return
        }

        if (_colorMode.value) {
            decodeColorMatrix(imageProxy)
            return
        }

        @Suppress("UnsafeOptInUsageError")
        val mediaImage = imageProxy.image
        if (mediaImage == null) {
            imageProxy.close()
            return
        }

        val inputImage = InputImage.fromMediaImage(mediaImage, imageProxy.imageInfo.rotationDegrees)

        val task = barcodeScanner?.process(inputImage)
            ?: run {
                imageProxy.close()
                return
            }

        task.addOnSuccessListener { barcodes ->
                for (barcode in barcodes) {
                    val frameBytes = barcode.rawBytes ?: barcode.rawValue?.toByteArray()
                    if (frameBytes == null) continue

                    decoder.decodeFrame(frameBytes)

                    val state = _uiState.value
                    _uiState.value = state.copy(
                        progress = decoder.progress,
                        speed = decoder.speedStr,
                        avgSpeed = decoder.avgSpeedStr,
                        peakSpeed = decoder.peakSpeedStr,
                        readInterval = decoder.readIntervalMs,
                        totalSize = decoder.totalSizeStr,
                        bytesDetail = decoder.bytesDetailStr,
                        totalTime = decoder.totalTime
                    )

                    if (decoder.isCompleted) {
                        val dataBytes = decoder.dataBytes ?: continue
                        scanResultData = dataBytes
                        val originalName = decoder.receivedFileName
                        _uiState.value = _uiState.value.copy(
                            scanState = ScanState.COMPLETED,
                            md5 = computeMd5(dataBytes),
                            suggestedFileName = if (originalName.isNotBlank()) originalName else generateFileName(dataBytes)
                        )
                    }
                }
            }
            .addOnCompleteListener {
                imageProxy.close()
            }
    }

    fun setColorMode(enabled: Boolean) {
        _colorMode.value = enabled
    }

    private fun decodeColorMatrix(imageProxy: ImageProxy) {
        try {
            @Suppress("UnsafeOptInUsageError")
            val img = imageProxy.image ?: run { imageProxy.close(); return }
            val planes = img.planes
            if (planes.size < 3) { imageProxy.close(); return }

            val yPlane = planes[0]; val uPlane = planes[1]; val vPlane = planes[2]
            val yBuf = yPlane.buffer; val yRS = yPlane.rowStride; val yPS = yPlane.pixelStride
            val uBuf = uPlane.buffer; val uRS = uPlane.rowStride; val uPS = uPlane.pixelStride
            val vBuf = vPlane.buffer; val vRS = vPlane.rowStride; val vPS = vPlane.pixelStride

            val w = imageProxy.width; val h = imageProxy.height
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
                val coeffs = computeAffine(centers, known) ?: continue
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
                    val frameBytes = ColorMatrixDecoder.decodeWithAffine(sampler)
                    decoder.decodeFrame(frameBytes)
                    if (decoder.isCompleted) {
                        val dataBytes = decoder.dataBytes ?: return
                        scanResultData = dataBytes
                        _uiState.value = _uiState.value.copy(
                            scanState = ScanState.COMPLETED,
                            md5 = computeMd5(dataBytes),
                            suggestedFileName = decoder.receivedFileName.ifBlank { generateFileName(dataBytes) }
                        )
                    }
                    return
                } catch (_: ColorMatrixDecodeException) {}
            }
            _uiState.value = _uiState.value.copy(bytesDetail = "Color: no finders")
        } catch (e: Exception) {
            Log.e("QRT", "Color err: ${e.message}", e)
            _uiState.value = _uiState.value.copy(bytesDetail = "Color: ${e.message}")
        } finally {
            imageProxy.close()
        }
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
        if (result.size >= 2 && result[1].second > result[2].second) {
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

    /** Compute affine transform from 3 point pairs. Returns 6 coefficients or null. */
    private fun computeAffine(
        pixelPts: List<Pair<Int, Int>>, modulePts: List<Pair<Int, Int>>
    ): DoubleArray? {
        if (pixelPts.size < 3 || modulePts.size < 3) return null
        val x1 = pixelPts[0].first.toDouble(); val y1 = pixelPts[0].second.toDouble()
        val x2 = pixelPts[1].first.toDouble(); val y2 = pixelPts[1].second.toDouble()
        val x3 = pixelPts[2].first.toDouble(); val y3 = pixelPts[2].second.toDouble()
        val u1 = modulePts[0].first.toDouble(); val v1 = modulePts[0].second.toDouble()
        val u2 = modulePts[1].first.toDouble(); val v2 = modulePts[1].second.toDouble()
        val u3 = modulePts[2].first.toDouble(); val v3 = modulePts[2].second.toDouble()
        val det = (x1 - x3) * (y2 - y3) - (x2 - x3) * (y1 - y3)
        if (kotlin.math.abs(det) < 1e-6) return null
        val inv = 1.0 / det
        return doubleArrayOf(
            ((u1 - u3) * (y2 - y3) - (u2 - u3) * (y1 - y3)) * inv,  // axx
            ((u1 - u3) * (x3 - x2) + (u2 - u3) * (x1 - x3)) * inv,  // axy
            u1 - ((u1 - u3) * (y2 - y3) - (u2 - u3) * (y1 - y3)) * inv * x1 - ((u1 - u3) * (x3 - x2) + (u2 - u3) * (x1 - x3)) * inv * y1,
            ((v1 - v3) * (y2 - y3) - (v2 - v3) * (y1 - y3)) * inv,  // ayx
            ((v1 - v3) * (x3 - x2) + (v2 - v3) * (x1 - x3)) * inv,  // ayy
            v1 - ((v1 - v3) * (y2 - y3) - (v2 - v3) * (y1 - y3)) * inv * x1 - ((v1 - v3) * (x3 - x2) + (v2 - v3) * (x1 - x3)) * inv * y1,
        )
    }

    fun startScan() {
        decoder.reset()
        scanResultData = null
        _uiState.value = ScannerUiState(scanState = ScanState.SCANNING)
    }

    fun saveDataToFile(fileName: String) {
        val data = scanResultData ?: return
        _uiState.value = _uiState.value.copy(scanState = ScanState.SAVING)
        viewModelScope.launch {
            FileSaver.saveData(getApplication(), data, fileName)
            _uiState.value = _uiState.value.copy(
                scanState = ScanState.IDLE,
                scanResult = ScanResult(
                    data = data,
                    fileName = fileName,
                    totalSize = _uiState.value.totalSize,
                    totalTime = _uiState.value.totalTime,
                    avgSpeed = _uiState.value.avgSpeed,
                    peakSpeed = _uiState.value.peakSpeed,
                    speed = _uiState.value.speed,
                    md5 = _uiState.value.md5
                )
            )
        }
    }

    private fun generateFileName(data: ByteArray): String {
        val ts = SimpleDateFormat("yyyyMMdd_HHmmss", Locale.US).format(Date())
        val hash = sha1Hex(data)
        return "received_${ts}_${hash}.bin"
    }

    private fun computeMd5(data: ByteArray): String {
        val digest = MessageDigest.getInstance("MD5").digest(data)
        return digest.joinToString("") { "%02x".format(it.toInt() and 0xFF) }
    }

    private fun sha1Hex(data: ByteArray): String {
        val digest = MessageDigest.getInstance("SHA-1").digest(data)
        val sb = StringBuilder()
        for (i in 0 until 8) {
            sb.append("%02x".format(digest[i].toInt() and 0xFF))
        }
        return sb.toString()
    }

    override fun onCleared() {
        super.onCleared()
        uiTicker?.cancel()
        barcodeScanner?.close()
    }
}
