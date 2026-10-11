// Copyright (C) Ubaldo Porcheddu <ubaldo@eja.it>

package it.eja.cronotopia

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.os.ParcelFileDescriptor
import android.os.Process
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import java.io.BufferedReader
import java.io.FileInputStream
import java.io.InputStreamReader

class MainActivity : Activity() {

    private lateinit var webView: WebView
    private var pfd: ParcelFileDescriptor? = null
    private var ptmPfd: ParcelFileDescriptor? = null
    private var ptmFd = -1
    private var childPid = -1
    private var currentUriStr: String? = null
    private var currentHostStr: String? = null
    private var currentPortStr: String? = null

    private external fun createSubprocess(
        cmd: String,
        cwd: String?,
        args: Array<String>?,
        envVars: Array<String>?,
        processId: IntArray?,
        dbFd: Int
    ): Int

    companion object {
        private const val REQUEST_PICK_DB = 1001

        init {
            System.loadLibrary("launcher")
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        webView = WebView(this).apply {
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            webViewClient = object : WebViewClient() {
                override fun onReceivedError(
                    view: WebView?,
                    request: WebResourceRequest?,
                    error: WebResourceError?
                ) {
                    if (request?.isForMainFrame == true) {
                        val targetUrl = request.url.toString()
                        val host = currentHostStr ?: "127.0.0.1"
                        if (targetUrl.startsWith("http://$host:")) {
                            view?.postDelayed({ view.loadUrl(targetUrl) }, 500)
                        }
                    }
                }
            }
        }
        setContentView(webView)
    }

    override fun onResume() {
        super.onResume()

        val prefs = getSharedPreferences("prefs", MODE_PRIVATE)
        val savedUri = prefs.getString("db_uri", null)
        val savedHost = prefs.getString("web_host", "127.0.0.1") ?: "127.0.0.1"
        val savedPort = prefs.getString("web_port", "35248") ?: "35248"

        if (savedUri == null) {
            pickDatabase()
            return
        }

        if (savedUri != currentUriStr || savedHost != currentHostStr || savedPort != currentPortStr) {
            currentUriStr = savedUri
            currentHostStr = savedHost
            currentPortStr = savedPort
            launchSubprocess(Uri.parse(savedUri), savedHost, savedPort)
        }
    }

    private fun pickDatabase() {
        val intent = Intent(Intent.ACTION_OPEN_DOCUMENT).apply {
            addCategory(Intent.CATEGORY_OPENABLE)
            type = "*/*"
        }
        startActivityForResult(intent, REQUEST_PICK_DB)
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == REQUEST_PICK_DB && resultCode == RESULT_OK) {
            data?.data?.let { uri ->
                contentResolver.takePersistableUriPermission(
                    uri,
                    Intent.FLAG_GRANT_READ_URI_PERMISSION
                )
                getSharedPreferences("prefs", MODE_PRIVATE)
                    .edit()
                    .putString("db_uri", uri.toString())
                    .apply()
            }
        }
    }

    private fun launchSubprocess(uri: Uri, host: String, port: String) {
        if (childPid > 0) {
            Process.killProcess(childPid)
            childPid = -1
        }
        ptmPfd?.close()
        pfd?.close()

        try {
            pfd = contentResolver.openFileDescriptor(uri, "r")
            val fd = pfd?.fd ?: return

            val binaryPath = applicationInfo.nativeLibraryDir + "/libcronotopia.so"
            val args = arrayOf(
                "-db", "/proc/self/fd/3",
                "-web-host", host,
                "-web-port", port
            )

            val pids = IntArray(1)
            ptmFd = createSubprocess(binaryPath, filesDir.absolutePath, args, null, pids, fd)
            childPid = pids[0]

            if (ptmFd >= 0) {
                val adoptedPtm = ParcelFileDescriptor.adoptFd(ptmFd)
                ptmPfd = adoptedPtm

                Thread {
                    try {
                        val reader = BufferedReader(InputStreamReader(FileInputStream(adoptedPtm.fileDescriptor)))
                        while (reader.readLine() != null) {
                        }
                    } catch (_: Exception) {
                    }
                }.start()

                webView.postDelayed({
                    webView.loadUrl("http://$host:$port")
                }, 1000)
            }
        } catch (_: Exception) {
            pickDatabase()
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        if (childPid > 0) {
            Process.killProcess(childPid)
        }
        ptmPfd?.close()
        pfd?.close()
    }
}