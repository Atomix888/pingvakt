package se.pingvakt.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.ServiceInfo
import android.net.wifi.WifiManager
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import android.util.Log
import java.io.File
import java.security.SecureRandom
import java.util.Locale

/**
 * Förgrundstjänsten som håller agenten igång: startar agentprogrammet (libpvagent.so),
 * startar om det om det stannar och skickar Wi-Fi-data och avsökningar till det.
 */
class AgentService : Service() {
    companion object {
        const val PORT = 47811
        private const val CHANNEL = "agent"
        private const val TAG = "PingVakt"

        /** Nyckeln som skyddar agentens lokala port (samma för appen och webbvyn). */
        fun key(ctx: Context): String {
            val prefs = ctx.getSharedPreferences("pv", Context.MODE_PRIVATE)
            var k = prefs.getString("key", null)
            if (k == null || k.length < 16) {
                val b = ByteArray(24)
                SecureRandom().nextBytes(b)
                k = b.joinToString("") { "%02x".format(it) }
                prefs.edit().putString("key", k).apply()
            }
            return k
        }

        fun start(ctx: Context) {
            val i = Intent(ctx, AgentService::class.java)
            if (Build.VERSION.SDK_INT >= 26) ctx.startForegroundService(i) else ctx.startService(i)
        }

        @Volatile var fastScan = false
        @Volatile var instance: AgentService? = null
    }

    @Volatile private var running = false
    private var proc: Process? = null
    private lateinit var wifi: WifiReader
    private var wakeLock: PowerManager.WakeLock? = null
    private var lastScanRequest = 0L
    private var lastCachedPost = 0L

    private val scanReceiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) {
            Thread { postScan() }.start()
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        instance = this
        wifi = WifiReader(this)
        createChannel()
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val n: Notification = Notification.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_notif)
            .setContentTitle(getString(R.string.notif_running))
            .setContentText(getString(R.string.notif_text))
            .setContentIntent(open)
            .setOngoing(true)
            .build()
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(1, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(1, n)
        }
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "PingVakt:agent").apply { setReferenceCounted(false); acquire() }
        registerReceiver(scanReceiver, IntentFilter(WifiManager.SCAN_RESULTS_AVAILABLE_ACTION))
        running = true
        Thread { superviseAgent() }.start()
        Thread { wifiLoop() }.start()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int = START_STICKY

    override fun onDestroy() {
        running = false
        instance = null
        try { unregisterReceiver(scanReceiver) } catch (_: Exception) {}
        proc?.destroy()
        wakeLock?.release()
        super.onDestroy()
    }

    private fun createChannel() {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        val ch = NotificationChannel(CHANNEL, getString(R.string.channel_name), NotificationManager.IMPORTANCE_LOW)
        ch.setShowBadge(false)
        nm.createNotificationChannel(ch)
    }

    /** Startar agentprogrammet och startar om det om det avslutas. */
    private fun superviseAgent() {
        val exe = File(applicationInfo.nativeLibraryDir, "libpvagent.so")
        val dir = File(filesDir, "agent").apply { mkdirs() }
        val log = File(dir, "agent.log")
        while (running) {
            try {
                if (log.length() > 2_000_000) log.delete()
                val device = (Build.MANUFACTURER.replaceFirstChar { it.uppercase() } + " " + Build.MODEL).trim()
                val pb = ProcessBuilder(exe.absolutePath, "mobile", "--dir", dir.absolutePath, "--listen", "127.0.0.1:$PORT",
                    "--device", device, "--lang", Locale.getDefault().language)
                pb.environment()["PV_KEY"] = key(this)
                pb.environment()["HOME"] = dir.absolutePath
                pb.environment()["TMPDIR"] = cacheDir.absolutePath
                pb.redirectErrorStream(true)
                pb.redirectOutput(ProcessBuilder.Redirect.appendTo(log))
                val p = pb.start()
                proc = p
                val code = p.waitFor()
                Log.w(TAG, "agent exited with $code")
            } catch (e: Exception) {
                Log.e(TAG, "could not start the agent", e)
            }
            if (running) Thread.sleep(3000)
        }
    }

    /** Skickar Wi-Fi-anslutningen till agenten var 10:e sekund och söker av när det behövs. */
    private fun wifiLoop() {
        Thread.sleep(1500)
        while (running) {
            try {
                val body = wifi.connectionJson()
                val resp = Local.post(this, "/api/wifi", body)
                val now = System.currentTimeMillis()
                val wanted = resp?.contains("\"scan\":true") == true
                if ((wanted || (fastScan && now - lastScanRequest > 30_000)) && now - lastScanRequest > 15_000) {
                    requestScan()
                } else if (now - lastCachedPost > 60_000) {
                    // Androids senaste resultat (från systemet eller andra appar) – kostar ingen avsökning
                    lastCachedPost = now
                    postScan()
                }
            } catch (e: Exception) {
                Log.w(TAG, "wifi loop: ${e.message}")
            }
            Thread.sleep(if (fastScan) 5000 else 10_000)
        }
    }

    fun requestScan() {
        lastScanRequest = System.currentTimeMillis()
        val ok = wifi.startScan()
        if (!ok) Thread { postScan() }.start() // strypt av Android – skicka senaste resultaten
    }

    private fun postScan() {
        try {
            Local.post(this, "/api/survey", wifi.scanJson())
        } catch (e: Exception) {
            Log.w(TAG, "scan: ${e.message}")
        }
    }
}
