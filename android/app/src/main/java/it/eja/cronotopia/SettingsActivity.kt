// Copyright (C) Ubaldo Porcheddu <ubaldo@eja.it>

package it.eja.cronotopia

import android.app.Activity
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.text.InputType
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView

class SettingsActivity : Activity() {

    private val pickDbRequest = 1002

    override fun onCreate(savedInstanceState: Bundle?) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            setTheme(android.R.style.Theme_DeviceDefault_DayNight)
        } else {
            setTheme(android.R.style.Theme_DeviceDefault)
        }

        super.onCreate(savedInstanceState)

        val prefs = getSharedPreferences("prefs", MODE_PRIVATE)
        val density = resources.displayMetrics.density
        val padH = (24 * density).toInt()
        val padT = (80 * density).toInt()
        val padB = (24 * density).toInt()

        val layout = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            fitsSystemWindows = true
            setPadding(padH, padT, padH, padB)
        }

        val hostLabel = TextView(this).apply {
            text = "Listening Host"
            textSize = 16f
        }

        val hostInput = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_TEXT
            setText(prefs.getString("web_host", "127.0.0.1"))
        }

        val portLabel = TextView(this).apply {
            text = "Listening Port"
            textSize = 16f
        }

        val portInput = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_NUMBER
            setText(prefs.getString("web_port", "35248"))
        }

        val btnPick = Button(this).apply {
            text = "Select Database File"
            setOnClickListener {
                val intent = Intent(Intent.ACTION_OPEN_DOCUMENT).apply {
                    addCategory(Intent.CATEGORY_OPENABLE)
                    type = "*/*"
                }
                startActivityForResult(intent, pickDbRequest)
            }
        }

        val btnSave = Button(this).apply {
            text = "Save"
            setOnClickListener {
                val host = hostInput.text.toString().trim()
                val port = portInput.text.toString().trim()
                val editor = prefs.edit()
                if (host.isNotEmpty()) {
                    editor.putString("web_host", host)
                }
                if (port.isNotEmpty()) {
                    editor.putString("web_port", port)
                }
                editor.apply()
                finish()
            }
        }

        layout.addView(hostLabel)
        layout.addView(hostInput)
        layout.addView(portLabel)
        layout.addView(portInput)
        layout.addView(btnPick)
        layout.addView(btnSave)
        setContentView(layout)
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == pickDbRequest && resultCode == RESULT_OK) {
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
}