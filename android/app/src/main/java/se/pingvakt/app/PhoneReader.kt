package se.pingvakt.app

import android.annotation.SuppressLint
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.location.Location
import android.location.LocationManager
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.os.BatteryManager
import android.os.Build
import android.os.SystemClock
import android.telephony.CellInfo
import android.telephony.CellInfoGsm
import android.telephony.CellInfoLte
import android.telephony.CellInfoNr
import android.telephony.CellInfoWcdma
import android.telephony.CellSignalStrengthNr
import android.telephony.TelephonyManager
import org.json.JSONObject

/** Läser telefonens batteri, mobilnät, nätverksstatus och position. */
class PhoneReader(private val ctx: Context) {
    private val cm = ctx.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
    private val tm = ctx.getSystemService(Context.TELEPHONY_SERVICE) as TelephonyManager?
    private val lm = ctx.getSystemService(Context.LOCATION_SERVICE) as LocationManager?

    fun json(): JSONObject {
        val o = JSONObject()
        battery(o)
        o.put("model", (Build.MANUFACTURER.replaceFirstChar { it.uppercase() } + " " + Build.MODEL).trim())
        o.put("android", Build.VERSION.RELEASE)
        o.put("uptime", SystemClock.elapsedRealtime() / 1000)
        try { cell()?.let { o.put("cell", it) } } catch (_: Exception) {}
        try { net()?.let { o.put("net", it) } } catch (_: Exception) {}
        return o
    }

    private fun battery(o: JSONObject) {
        val i: Intent? = ctx.registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        if (i == null) {
            o.put("bat", -1)
            return
        }
        val level = i.getIntExtra(BatteryManager.EXTRA_LEVEL, -1)
        val scale = i.getIntExtra(BatteryManager.EXTRA_SCALE, 100)
        o.put("bat", if (level >= 0 && scale > 0) level * 100 / scale else -1)
        val status = i.getIntExtra(BatteryManager.EXTRA_STATUS, -1)
        o.put("chg", status == BatteryManager.BATTERY_STATUS_CHARGING || status == BatteryManager.BATTERY_STATUS_FULL)
        when (i.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0)) {
            BatteryManager.BATTERY_PLUGGED_AC -> o.put("plug", "ac")
            BatteryManager.BATTERY_PLUGGED_USB -> o.put("plug", "usb")
            BatteryManager.BATTERY_PLUGGED_WIRELESS -> o.put("plug", "wireless")
            8 -> o.put("plug", "dock") // BATTERY_PLUGGED_DOCK (Android 14)
        }
        val t = i.getIntExtra(BatteryManager.EXTRA_TEMPERATURE, Int.MIN_VALUE)
        if (t != Int.MIN_VALUE) o.put("temp", t / 10.0)
        o.put("health", when (i.getIntExtra(BatteryManager.EXTRA_HEALTH, 0)) {
            BatteryManager.BATTERY_HEALTH_GOOD -> "good"
            BatteryManager.BATTERY_HEALTH_OVERHEAT -> "overheat"
            BatteryManager.BATTERY_HEALTH_DEAD -> "dead"
            BatteryManager.BATTERY_HEALTH_COLD -> "cold"
            BatteryManager.BATTERY_HEALTH_OVER_VOLTAGE -> "overvoltage"
            else -> "unknown"
        })
    }

    /** Mobilnätet: den registrerade cellen (kräver platsbehörighet, som appen redan har). */
    @SuppressLint("MissingPermission")
    private fun cell(): JSONObject? {
        val t = tm ?: return null
        val cells: List<CellInfo> = t.allCellInfo ?: return null
        val reg = cells.filter { it.isRegistered }
        if (reg.isEmpty()) return null
        val hasNr = Build.VERSION.SDK_INT >= 29 && reg.any { it is CellInfoNr }
        // föredra 5G om både 4G och 5G är registrerade (5G NSA)
        val c = if (hasNr) reg.first { it is CellInfoNr } else reg.first()
        val o = JSONObject()
        val op = t.networkOperatorName
        if (!op.isNullOrBlank()) o.put("op", op)
        if (Build.VERSION.SDK_INT >= 29 && c is CellInfoNr) {
            val s = c.cellSignalStrength as CellSignalStrengthNr
            o.put("tech", "5G")
            if (s.ssRsrp != Int.MAX_VALUE) o.put("rsrp", s.ssRsrp)
            if (s.ssRsrq != Int.MAX_VALUE) o.put("rsrq", s.ssRsrq)
            if (s.ssSinr != Int.MAX_VALUE) o.put("sinr", s.ssSinr)
            o.put("level", s.level)
            if (Build.VERSION.SDK_INT >= 30) {
                val id = c.cellIdentity as android.telephony.CellIdentityNr
                if (id.nci != Long.MAX_VALUE) o.put("ci", id.nci.toString())
                id.bands.firstOrNull()?.let { o.put("band", "n$it") }
            }
            return o
        }
        when (c) {
            is CellInfoLte -> {
                val s = c.cellSignalStrength
                o.put("tech", if (hasNr || nsa()) "5G NSA" else "4G")
                if (s.rsrp != Int.MAX_VALUE) o.put("rsrp", s.rsrp)
                if (s.rsrq != Int.MAX_VALUE) o.put("rsrq", s.rsrq)
                if (s.rssnr != Int.MAX_VALUE) o.put("sinr", s.rssnr)
                o.put("level", s.level)
                val id = c.cellIdentity
                if (id.ci != Int.MAX_VALUE) o.put("ci", id.ci.toString())
                if (Build.VERSION.SDK_INT >= 30) id.bands.firstOrNull()?.let { o.put("band", "B$it") }
            }
            is CellInfoWcdma -> {
                o.put("tech", "3G")
                o.put("rssi", c.cellSignalStrength.dbm)
                o.put("level", c.cellSignalStrength.level)
            }
            is CellInfoGsm -> {
                o.put("tech", "2G")
                o.put("rssi", c.cellSignalStrength.dbm)
                o.put("level", c.cellSignalStrength.level)
            }
            else -> {
                o.put("tech", "?")
                if (Build.VERSION.SDK_INT >= 30) o.put("level", c.cellSignalStrength.level)
            }
        }
        return o
    }

    // NR-cellen syns inte alltid vid 5G NSA; signalstyrkan avslöjar den ibland.
    private fun nsa(): Boolean = Build.VERSION.SDK_INT >= 29 && try {
        tm?.signalStrength?.cellSignalStrengths?.any { it is CellSignalStrengthNr } == true
    } catch (_: Exception) { false }

    private fun net(): JSONObject? {
        val n = cm.activeNetwork ?: return null
        val caps = cm.getNetworkCapabilities(n) ?: return null
        val o = JSONObject()
        o.put("tr", when {
            caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
            else -> "other"
        })
        o.put("valid", caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED))
        o.put("captive", caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_CAPTIVE_PORTAL))
        o.put("metered", !caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED))
        // VPN syns som ett eget nät
        val vpn = cm.allNetworks.any { cm.getNetworkCapabilities(it)?.hasTransport(NetworkCapabilities.TRANSPORT_VPN) == true }
        o.put("vpn", vpn)
        if (Build.VERSION.SDK_INT >= 28) cm.getLinkProperties(n)?.let { o.put("pdns", it.isPrivateDnsActive) }
        if (caps.linkDownstreamBandwidthKbps > 0) o.put("down", caps.linkDownstreamBandwidthKbps)
        if (caps.linkUpstreamBandwidthKbps > 0) o.put("up", caps.linkUpstreamBandwidthKbps)
        return o
    }

    /** Senaste kända position (GPS eller nät), högst 2 minuter gammal. */
    @SuppressLint("MissingPermission")
    fun location(): JSONObject? {
        val m = lm ?: return null
        var best: Location? = null
        try {
            for (p in m.getProviders(true)) {
                val l = m.getLastKnownLocation(p) ?: continue
                if (best == null || l.time > best.time || (l.time == best.time && l.accuracy < best.accuracy)) best = l
            }
        } catch (_: SecurityException) {
            return null
        }
        val l = best ?: return null
        if (System.currentTimeMillis() - l.time > 120_000) return null
        return JSONObject().put("lat", l.latitude).put("lon", l.longitude).put("acc", l.accuracy.toDouble())
    }

    /** Ber om löpande positioner medan en survey spelas in (annars kan senaste positionen vara gammal). */
    @SuppressLint("MissingPermission")
    fun track(on: Boolean, listener: android.location.LocationListener) {
        val m = lm ?: return
        try {
            m.removeUpdates(listener)
            if (!on) return
            for (p in listOf(LocationManager.GPS_PROVIDER, LocationManager.NETWORK_PROVIDER)) {
                if (m.isProviderEnabled(p)) m.requestLocationUpdates(p, 5000L, 2f, listener, android.os.Looper.getMainLooper())
            }
        } catch (_: Exception) {}
    }
}
