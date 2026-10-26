package com.qrtpro.reader

import android.os.Bundle
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
                viewModel.saveDataToFile(fileName)
                showResult = true
            }
        )
    }
}
