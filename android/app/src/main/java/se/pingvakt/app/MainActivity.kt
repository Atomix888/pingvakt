package se.pingvakt.app

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import android.provider.Settings
import android.view.Gravity
import android.webkit.JavascriptInterface
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.TextView
import androidx.core.content.FileProvider
import org.json.JSONObject
import java.io.File

/** Visar agentens webbgränssnitt i en WebView och ger det en brygga till Android (window.PV). */
class MainActivity : Activity() {
    private lateinit var web: WebView
    private val ui = Handler(Looper.getMainLooper())
    private var loaded = false

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        AgentService.start(this)
        val wait = TextView(this).apply {
            text = getString(R.string.starting)
            setTextColor(Color.parseColor("#8b95a3"))
            gravity = Gravity.CENTER
            setBackgroundColor(Color.parseColor("#0f1216"))
        }
        setContentView(wait)
        web = WebView(this)
        web.setBackgroundColor(Color.parseColor("#0f1216"))
        web.settings.javaScriptEnabled = true
        web.settings.domStorageEnabled = true
        web.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, url: String): Boolean {
                // länkar utanför agenten öppnas i webbläsaren
                if (!url.startsWith("http://127.0.0.1:")) {
                    startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)))
                    return true
                }
                return false
            }
        }
        web.addJavascriptInterface(Bridge(), "PV")
        if (!hasLocation()) requestBasics()
        waitForAgent(0)
    }

    /** Väntar tills agenten svarar och laddar sedan gränssnittet. */
    private fun waitForAgent(tries: Int) {
        Thread {
            val ok = Local.get(this, "/api/status") != null
            ui.post {
                if (ok) {
                    if (!loaded) {
                        loaded = true
                        setContentView(web)
                        web.loadUrl(Local.url("/?k=" + AgentService.key(this)))
                    }
                } else if (tries < 60) {
                    ui.postDelayed({ waitForAgent(tries + 1) }, 500)
                }
            }
        }.start()
    }

    override fun onResume() {
        super.onResume()
        if (loaded) web.evaluateJavascript("window.poll && poll()", null)
    }

    @Deprecated("Deprecated in Java")
    override fun onBackPressed() {
        // låt agenten fortsätta i bakgrunden
        moveTaskToBack(true)
    }

    private fun granted(p: String) = checkSelfPermission(p) == PackageManager.PERMISSION_GRANTED
    private fun hasLocation() = granted(Manifest.permission.ACCESS_FINE_LOCATION)
    private fun hasBackground() = Build.VERSION.SDK_INT < 29 || granted(Manifest.permission.ACCESS_BACKGROUND_LOCATION)
    private fun hasNotifications() = Build.VERSION.SDK_INT < 33 || granted(Manifest.permission.POST_NOTIFICATIONS)
    private fun batteryOk(): Boolean {
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        return pm.isIgnoringBatteryOptimizations(packageName)
    }

    private fun requestBasics() {
        val want = mutableListOf(Manifest.permission.ACCESS_FINE_LOCATION, Manifest.permission.ACCESS_COARSE_LOCATION)
        if (Build.VERSION.SDK_INT >= 33) {
            want += Manifest.permission.POST_NOTIFICATIONS
            want += Manifest.permission.NEARBY_WIFI_DEVICES
        }
        requestPermissions(want.toTypedArray(), 1)
    }

    inner class Bridge {
        @JavascriptInterface
        fun scanNow() {
            val s = AgentService.instance
            if (s != null) s.requestScan() else AgentService.start(this@MainActivity)
        }

        @JavascriptInterface
        fun setFastScan(on: Boolean) {
            AgentService.fastScan = on
        }

        @JavascriptInterface
        fun permissions(): String = JSONObject()
            .put("location", hasLocation()).put("background", hasBackground())
            .put("notifications", hasNotifications()).put("battery", batteryOk()).toString()

        @JavascriptInterface
        fun requestPermissions() {
            ui.post {
                when {
                    !hasLocation() || !hasNotifications() -> requestBasics()
                    !hasBackground() && Build.VERSION.SDK_INT >= 29 ->
                        // Android 11+ öppnar inställningarna där man väljer "Tillåt alltid"
                        requestPermissions(arrayOf(Manifest.permission.ACCESS_BACKGROUND_LOCATION), 2)
                    !batteryOk() -> openBatterySettings()
                }
            }
        }

        @SuppressLint("BatteryLife")
        @JavascriptInterface
        fun openBatterySettings() {
            ui.post {
                try {
                    startActivity(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:$packageName")))
                } catch (e: Exception) {
                    startActivity(Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS))
                }
            }
        }

        @JavascriptInterface
        fun share(name: String, text: String) {
            val dir = File(cacheDir, "exports").apply { mkdirs() }
            val safe = name.replace(Regex("[^A-Za-z0-9._-]"), "_")
            val f = File(dir, safe)
            f.writeText("﻿" + text) // BOM så att Excel läser åäö rätt
            val uri = FileProvider.getUriForFile(this@MainActivity, "$packageName.files", f)
            val i = Intent(Intent.ACTION_SEND).apply {
                type = "text/csv"
                putExtra(Intent.EXTRA_STREAM, uri)
                putExtra(Intent.EXTRA_SUBJECT, safe)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
            ui.post { startActivity(Intent.createChooser(i, getString(R.string.share_csv))) }
        }
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (loaded) web.evaluateJavascript("window.poll && poll()", null)
    }
}
