package com.qrtpro.reader.ui

import android.Manifest
import android.content.pm.PackageManager
import android.util.Size
import android.view.ViewGroup
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.Preview
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.compose.foundation.background
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.content.ContextCompat
import androidx.lifecycle.compose.LocalLifecycleOwner
import com.qrtpro.reader.ScannerUiState
import com.qrtpro.reader.ScannerViewModel
import com.qrtpro.reader.ScanState
import com.qrtpro.reader.decoder.TransferFrame
import java.util.concurrent.Executors

@Composable
fun ScannerScreen(
    viewModel: ScannerViewModel,
    onSaveRequest: (String) -> Unit
) {
    val uiState by viewModel.uiState.collectAsState()
    val context = LocalContext.current
    val lifecycleOwner = LocalLifecycleOwner.current

    var hasCameraPermission by remember {
        mutableStateOf(
            ContextCompat.checkSelfPermission(
                context, Manifest.permission.CAMERA
            ) == PackageManager.PERMISSION_GRANTED
        )
    }

    val permissionLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.RequestPermission()
    ) { granted ->
        hasCameraPermission = granted
    }

    LaunchedEffect(Unit) {
        if (!hasCameraPermission) {
            permissionLauncher.launch(Manifest.permission.CAMERA)
        }
    }

    Box(modifier = Modifier.fillMaxSize()) {
        if (hasCameraPermission) {
            CameraPreview(
                viewModel = viewModel,
                lifecycleOwner = lifecycleOwner,
                modifier = Modifier.fillMaxSize()
            )

            val colorMode by viewModel.colorMode.collectAsState()
            val decimenMode by viewModel.decimenMode.collectAsState()

            ScannerOverlay(
                uiState = uiState,
                colorMode = colorMode,
                decimenMode = decimenMode,
                onColorModeChange = { viewModel.setColorMode(it) },
                onSaveRequest = onSaveRequest,
                onRestart = { viewModel.startScan() }
            )
        } else {
            Column(
                modifier = Modifier
                    .fillMaxSize()
                    .padding(32.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.Center
            ) {
                VersionAndLimits(textColor = MaterialTheme.colorScheme.onBackground)
                Spacer(modifier = Modifier.height(16.dp))
                Text("Camera permission is required to scan QR codes")
                Spacer(modifier = Modifier.height(16.dp))
                Button(onClick = { permissionLauncher.launch(Manifest.permission.CAMERA) }) {
                    Text("Grant Permission")
                }
            }
        }
    }
}

@Composable
private fun CameraPreview(
    viewModel: ScannerViewModel,
    lifecycleOwner: androidx.lifecycle.LifecycleOwner,
    modifier: Modifier = Modifier
) {
    val context = LocalContext.current
    // One executor for the lifetime of this composable. The old code created it
    // inline per binding and never shut it down, so every re-entry into the
    // screen leaked a non-daemon thread that also kept the camera alive.
    val analyzerExecutor = remember { Executors.newSingleThreadExecutor() }
    var cameraProvider by remember { mutableStateOf<ProcessCameraProvider?>(null) }

    AndroidView(
        modifier = modifier,
        factory = { ctx ->
            val previewView = PreviewView(ctx).apply {
                layoutParams = ViewGroup.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT,
                    ViewGroup.LayoutParams.MATCH_PARENT
                )
                scaleType = PreviewView.ScaleType.FILL_CENTER
            }

            val cameraProviderFuture = ProcessCameraProvider.getInstance(ctx)
            cameraProviderFuture.addListener({
                // get() throws if the provider failed to initialise; without
                // this the listener dies and the screen shows a black preview.
                val provider = try {
                    cameraProviderFuture.get()
                } catch (t: Throwable) {
                    android.util.Log.w("QRT", "camera provider unavailable: ${t.message}")
                    return@addListener
                }
                cameraProvider = provider

                val preview = Preview.Builder().build().also {
                    it.surfaceProvider = previewView.surfaceProvider
                }

                val imageAnalysis = ImageAnalysis.Builder()
                    .setTargetResolution(Size(1920, 1080))
                    .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                    .build()

                imageAnalysis.setAnalyzer(analyzerExecutor) { imageProxy ->
                    viewModel.onQrCodeScanned(imageProxy)
                }

                val cameraSelector = CameraSelector.DEFAULT_BACK_CAMERA

                try {
                    provider.unbindAll()
                    provider.bindToLifecycle(
                        lifecycleOwner,
                        cameraSelector,
                        preview,
                        imageAnalysis
                    )
                } catch (e: Exception) {
                    android.util.Log.w("QRT", "bindToLifecycle failed: ${e.message}")
                }
            }, ContextCompat.getMainExecutor(ctx))

            previewView
        }
    )

    DisposableEffect(Unit) {
        onDispose {
            try {
                cameraProvider?.unbindAll()
            } catch (t: Throwable) {
                android.util.Log.w("QRT", "unbindAll failed: ${t.message}")
            }
            cameraProvider = null
            analyzerExecutor.shutdown()
        }
    }
}

@Composable
private fun ScannerOverlay(
    uiState: ScannerUiState,
    colorMode: Boolean,
    decimenMode: Boolean,
    onColorModeChange: (Boolean) -> Unit,
    onSaveRequest: (String) -> Unit,
    onRestart: () -> Unit
) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.SpaceBetween
    ) {
        TopBar(
            colorMode = colorMode,
            decimenMode = decimenMode,
            onColorModeChange = onColorModeChange,
            onRestart = onRestart
        )

        when (uiState.scanState) {
            ScanState.SCANNING -> {
                ProgressPanel(
                    progress = uiState.progress,
                    speed = uiState.speed,
                    readInterval = uiState.readInterval,
                    totalSize = uiState.totalSize,
                    bytesDetail = uiState.bytesDetail,
                    decimenMode = decimenMode
                )
            }
            ScanState.COMPLETED -> {
                if(uiState.error!=null) Text(uiState.error,color=Color.Red)
                CompletedPanel(
                    totalSize = uiState.totalSize,
                    totalTime = uiState.totalTime,
                    avgSpeed = uiState.avgSpeed,
                    peakSpeed = uiState.peakSpeed,
                    md5 = uiState.md5,
                    suggestedFileName = uiState.suggestedFileName,
                    onSave = { fileName -> onSaveRequest(fileName) },
                    onRescan = onRestart
                )
            }
            ScanState.SAVING -> {
                Box(
                    modifier = Modifier.fillMaxWidth().padding(16.dp),
                    contentAlignment = Alignment.Center
                ) {
                    CircularProgressIndicator()
                }
            }
            ScanState.FAILED -> {
                Surface(color=Color.Black.copy(alpha=0.8f), modifier=Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(16.dp)) {
                        Text(uiState.error ?: "接收失败，请重新扫描",color=Color.White)
                        Button(onClick=onRestart) { Text("重新扫描") }
                    }
                }
            }
            ScanState.IDLE -> {}
        }
    }
}

@Composable
private fun TopBar(
    colorMode: Boolean,
    decimenMode: Boolean,
    onColorModeChange: (Boolean) -> Unit,
    onRestart: () -> Unit
) {
    Surface(
        modifier = Modifier.fillMaxWidth(),
        color = Color.Black.copy(alpha = 0.6f),
        shape = RoundedCornerShape(12.dp)
    ) {
        Row(
            modifier = Modifier.fillMaxWidth().padding(8.dp),
            verticalAlignment = Alignment.CenterVertically
        ) {
            VersionAndLimits(Modifier.weight(1f))
            Column(horizontalAlignment = Alignment.CenterHorizontally) {
                Text("Color", color = Color.White, fontSize = 10.sp)
                Switch(
                    checked = colorMode,
                    onCheckedChange = onColorModeChange,
                    colors = SwitchDefaults.colors(
                        checkedThumbColor = Color.Green,
                        checkedTrackColor = Color.Green.copy(alpha = 0.3f)
                    )
                )
            }
            Spacer(modifier = Modifier.width(8.dp))
            Column(horizontalAlignment = Alignment.CenterHorizontally) {
                Text(
                    text = "Decimen",
                    color = if (decimenMode) Color(0xFF4FC3F7) else Color.White,
                    fontSize = 10.sp
                )
                Switch(
                    checked = decimenMode,
                    onCheckedChange = { /* auto-detected from the stream; read-only */ },
                    enabled = false,
                    colors = SwitchDefaults.colors(
                        checkedThumbColor = Color(0xFF4FC3F7),
                        checkedTrackColor = Color(0xFF4FC3F7).copy(alpha = 0.3f)
                    )
                )
            }
            Spacer(modifier = Modifier.width(8.dp))
            TextButton(
                onClick = onRestart
            ) {
                Text("重选", color = Color.White)
            }
        }
    }
}

@Composable
private fun VersionAndLimits(modifier: Modifier = Modifier, textColor: Color = Color.White) {
    val context = LocalContext.current
    val version = remember(context) {
        context.packageManager.getPackageInfo(context.packageName, 0).versionName ?: "未知"
    }
    var showLimits by remember { mutableStateOf(false) }
    Column(modifier, horizontalAlignment = Alignment.CenterHorizontally) {
        Text("QRT Pro Reader", color = textColor, fontSize = 16.sp)
        TextButton(onClick = { showLimits = true }) {
            Text("v$version · 限制", fontSize = 12.sp)
        }
    }
    if (showLimits) {
        AlertDialog(
            onDismissRequest = { showLimits = false },
            title = { Text("v$version · 传输限制") },
            text = {
                Text(
                    "经典 QR / Color：原始文件最多 ${TransferFrame.MAX_TRANSFER_BYTES / (1024 * 1024)} MiB，源块最多 ${TransferFrame.MAX_SOURCE_BLOCKS} 个，协议分片最多 ${TransferFrame.MAX_CHUNK_BYTES} 字节。\n\n" +
                        "实际文件参数上限还受源块数约束：2048 字节分片时最多 ${TransferFrame.MAX_SOURCE_BLOCKS * 2048 / (1024 * 1024)} MiB。\n\n" +
                        "配套发送端的普通 QR 使用 ECC M，每帧总容量为 2331 字节，包含协议头；Color 的有效分片最多 1426 字节。不能把协议分片上限直接当作二维码可装的数据量。\n\n" +
                        "Decimen 原始文件最多 64 MiB。参数合法仍受设备实际内存和画面识别条件影响；发送端还有编码内存、缓存和任务时限，详见其限制面板。",
                    modifier = Modifier.verticalScroll(rememberScrollState())
                )
            },
            confirmButton = { TextButton(onClick = { showLimits = false }) { Text("关闭") } }
        )
    }
}

@Composable
private fun ProgressPanel(
    progress: Int,
    speed: String,
    readInterval: Long,
    totalSize: String,
    bytesDetail: String,
    decimenMode: Boolean
) {
    Surface(
        modifier = Modifier.fillMaxWidth(),
        color = Color.Black.copy(alpha = 0.6f),
        shape = RoundedCornerShape(12.dp)
    ) {
        Column(
            modifier = Modifier.padding(16.dp),
            horizontalAlignment = Alignment.CenterHorizontally
        ) {
            Text(
                text = if (decimenMode) "Receiving (Decimen fountain)..." else "Receiving...",
                color = Color.White,
                fontSize = 18.sp
            )
            Spacer(modifier = Modifier.height(8.dp))

            // coerceIn: the decoder can briefly report >100 while the last
            // frames land, and Compose throws on a progress outside 0..1.
            LinearProgressIndicator(
                progress = { progress.coerceIn(0, 100) / 100f },
                modifier = Modifier.fillMaxWidth().height(8.dp),
                color = Color.Green,
                trackColor = Color.DarkGray,
            )

            Spacer(modifier = Modifier.height(8.dp))
            Text(
                text = "$progress% | $speed | ${totalSize}total",
                color = Color.White,
                fontSize = 14.sp,
                fontFamily = FontFamily.Monospace
            )
            Spacer(modifier = Modifier.height(4.dp))
            Text(
                text = bytesDetail,
                color = Color.White,
                fontSize = 12.sp,
                fontFamily = FontFamily.Monospace
            )
        }
    }
}

@Composable
private fun CompletedPanel(
    totalSize: String,
    totalTime: String,
    avgSpeed: String,
    peakSpeed: String,
    md5: String,
    suggestedFileName: String,
    onSave: (String) -> Unit,
    onRescan: () -> Unit
) {
    var fileName by remember(suggestedFileName) { mutableStateOf(suggestedFileName) }

    Surface(
        modifier = Modifier.fillMaxWidth(),
        color = Color.Black.copy(alpha = 0.7f),
        shape = RoundedCornerShape(12.dp)
    ) {
        Column(
            modifier = Modifier.padding(16.dp),
            horizontalAlignment = Alignment.CenterHorizontally
        ) {
            Text(
                text = "Received Successfully!",
                color = Color.Green,
                fontSize = 20.sp
            )
            Spacer(modifier = Modifier.height(8.dp))
            Text(
                text = "Size: $totalSize  Time: $totalTime",
                color = Color.White,
                fontSize = 14.sp,
                fontFamily = FontFamily.Monospace
            )
            Spacer(modifier = Modifier.height(4.dp))
            Text(
                text = "Average Speed: $avgSpeed  Peak Speed: $peakSpeed",
                color = Color.White,
                fontSize = 14.sp,
                fontFamily = FontFamily.Monospace
            )
            Spacer(modifier = Modifier.height(4.dp))
            Text(
                text = "MD5: $md5",
                color = Color.White,
                fontSize = 13.sp,
                fontFamily = FontFamily.Monospace
            )

            Spacer(modifier = Modifier.height(12.dp))

            OutlinedTextField(
                value = fileName,
                onValueChange = { fileName = it },
                label = { Text("File name") },
                singleLine = true,
                textStyle = LocalTextStyle.current.copy(color = Color.White),
                colors = OutlinedTextFieldDefaults.colors(
                    focusedBorderColor = Color.Green,
                    unfocusedBorderColor = Color.Gray
                )
            )

            Spacer(modifier = Modifier.height(12.dp))

            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceEvenly
            ) {
                Button(
                    onClick = { onSave(fileName) },
                    colors = ButtonDefaults.buttonColors(containerColor = Color(0xFF4CAF50))
                ) {
                    Text("Save")
                }
                OutlinedButton(
                    onClick = onRescan,
                    colors = ButtonDefaults.outlinedButtonColors(contentColor = Color.White)
                ) {
                    Text("Rescan")
                }
            }
        }
    }
}
