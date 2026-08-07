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

    if (showResult && uiState.scanResult != null) {
        ResultScreen(
            scanResult = uiState.scanResult!!,
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
