package com.qrtpro.reader

import android.app.Application
import android.graphics.Bitmap
import android.graphics.BitmapFactory
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
            val planes = imageProxy.planes
            if (planes.isEmpty()) { imageProxy.close(); return }
            val yPlane = planes[0]
            val yBuf = yPlane.buffer
            val pStride = yPlane.pixelStride
            val rStride = yPlane.rowStride

            val w = imageProxy.width
            val h = imageProxy.height
            val side = minOf(w, h)
            val ox = (w - side) / 2
            val oy = (h - side) / 2
            if (side < ColorMatrixDecoder.MATRIX_SIZE) { imageProxy.close(); return }

            val sampler: (Int, Int) -> Int = { x, y ->
                val yi = y + oy
                val xi = x + ox
                val idx = yi * rStride + xi * pStride
                val yVal = yBuf.get(idx).toInt() and 0xFF
                (yVal shl 16) or (yVal shl 8) or yVal
            }

            val frameBytes = ColorMatrixDecoder.decode(sampler, side, side)
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
        } catch (_: ColorMatrixDecodeException) {
        } finally {
            imageProxy.close()
        }
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
