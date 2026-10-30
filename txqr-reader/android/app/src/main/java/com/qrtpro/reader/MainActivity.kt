package com.qrtpro.reader

import android.os.Bundle
import android.os.Build
import android.Manifest
import android.content.pm.PackageManager
import androidx.core.content.ContextCompat
import androidx.compose.ui.platform.LocalContext
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.lifecycle.viewmodel.compose.viewModel
import com.qrtpro.reader.ui.ResultScreen
import com.qrtpro.reader.ui.ScannerScreen

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContent {
            MaterialTheme {
                Surface(
                    modifier = Modifier.fillMaxSize(),
                    color = MaterialTheme.colorScheme.background
                ) {
                    TxqrReaderApp()
                }
            }
        }
    }
}

@Composable
fun TxqrReaderApp() {
    val viewModel: ScannerViewModel = viewModel()
    val uiState by viewModel.uiState.collectAsState()
    var showResult by remember { mutableStateOf(false) }
    val context=LocalContext.current
    var pendingName by remember { mutableStateOf<String?>(null) }
    val storagePermission=rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        val name=pendingName
        pendingName=null
        if(granted && name!=null) { viewModel.saveDataToFile(name); showResult=true }
    }

    // A failed save never produces a ScanResult, so the flag has to be dropped
    // again — otherwise the app sits on the scanning screen with the error
    // buried in the state and no visible way back.
    LaunchedEffect(uiState.error) {
        if (uiState.error != null) showResult = false
    }

    val result = uiState.scanResult
    if (showResult && result != null) {
        ResultScreen(
            scanResult = result,
            onBackToScan = {
                showResult = false
                viewModel.startScan()
            }
        )
    } else {
        ScannerScreen(
            viewModel = viewModel,
            onSaveRequest = { fileName ->
                if(Build.VERSION.SDK_INT<=28 && ContextCompat.checkSelfPermission(context,Manifest.permission.WRITE_EXTERNAL_STORAGE)!=PackageManager.PERMISSION_GRANTED) {
                    pendingName=fileName
                    storagePermission.launch(Manifest.permission.WRITE_EXTERNAL_STORAGE)
                } else {
                    viewModel.saveDataToFile(fileName)
                    showResult=true
                }
            }
        )
    }
}
