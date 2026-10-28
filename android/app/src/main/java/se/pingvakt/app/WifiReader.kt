package se.pingvakt.app

import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.net.wifi.ScanResult
import android.net.wifi.WifiInfo
import android.net.wifi.WifiManager
import android.os.Build
import org.json.JSONArray
import org.json.JSONObject
import java.net.Inet4Address

/** Läser Wi-Fi-anslutningen, nätet och avsökningar och gör JSON i agentens format. */
class WifiReader(private val ctx: Context) {
    private val wm = ctx.applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
    private val cm = ctx.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager

    private fun cleanSsid(s: String?): String {
        if (s == null || s == WifiManager.UNKNOWN_SSID || s == "<unknown ssid>") return ""
        return s.removePrefix("\"").removeSuffix("\"")
    }

    private fun cleanBssid(s: String?): String =
        if (s == null || s == "02:00:00:00:00:00") "" else s.lowercase()

    @Suppress("DEPRECATION")
    @android.annotation.SuppressLint("MissingPermission")
    fun connectionJson(): String {
        val out = JSONObject()
        val info: WifiInfo? = try { wm.connectionInfo } catch (e: Exception) { null }
        val net = cm.activeNetwork
        val caps = net?.let { cm.getNetworkCapabilities(it) }
        val onWifi = caps?.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) == true
        if (info != null && onWifi && info.networkId != -1 && info.rssi < 0 && info.rssi > -127) {
            val w = JSONObject()
            w.put("ssid", cleanSsid(info.ssid))
            val bssid = cleanBssid(info.bssid)
            w.put("bssid", bssid)
            w.put("rssi", info.rssi)
            w.put("freq", info.frequency)
            if (Build.VERSION.SDK_INT >= 29) {
                if (info.txLinkSpeedMbps > 0) w.put("tx", info.txLinkSpeedMbps)
                if (info.rxLinkSpeedMbps > 0) w.put("rx", info.rxLinkSpeedMbps)
            } else if (info.linkSpeed > 0) {
                w.put("tx", info.linkSpeed)
            }
            if (Build.VERSION.SDK_INT >= 30) std(info.wifiStandard)?.let { w.put("std", it) }
            // kanalbredden finns bara i avsökningen
            if (bssid.isNotEmpty()) {
                try {
                    wm.scanResults.firstOrNull { it.BSSID.equals(bssid, true) }?.let { w.put("width", width(it.channelWidth)) }
                } catch (_: SecurityException) {}
            }
            w.put("if", "wlan0")
            out.put("wifi", w)
        }
        // nät, gateway och DNS (Go kan inte läsa dem själv på Android)
        val nets = JSONArray()
        val dns = JSONArray()
        val lp = net?.let { cm.getLinkProperties(it) }
        if (lp != null) {
            var gw = ""
            for (r in lp.routes) {
                if (r.isDefaultRoute && r.gateway is Inet4Address) gw = r.gateway!!.hostAddress ?: ""
            }
            for (la in lp.linkAddresses) {
                val a = la.address
                if (a !is Inet4Address || a.isLoopbackAddress || a.isLinkLocalAddress) continue
                val p = la.prefixLength
                if (p < 8 || p > 30) continue
                val ip = a.address.fold(0L) { acc, b -> (acc shl 8) or (b.toLong() and 0xff) }
                val mask = (0xffffffffL shl (32 - p)) and 0xffffffffL
                val nw = ip and mask
                val cidr = "${(nw shr 24) and 255}.${(nw shr 16) and 255}.${(nw shr 8) and 255}.${nw and 255}/$p"
                nets.put(JSONObject().put("iface", lp.interfaceName ?: "wlan0").put("addr", a.hostAddress)
                    .put("cidr", cidr).put("hosts", (1L shl (32 - p)) - 2).put("gateway", gw))
            }
            for (d in lp.dnsServers) if (d is Inet4Address) dns.put(d.hostAddress)
        }
        out.put("nets", nets)
        out.put("dns", dns)
        return out.toString()
    }

    @Suppress("DEPRECATION")
    fun startScan(): Boolean = try { wm.startScan() } catch (e: Exception) { false }

    @Suppress("DEPRECATION")
    @android.annotation.SuppressLint("MissingPermission")
    fun scanJson(): String {
        val out = JSONObject()
        val list = JSONArray()
        try {
            val cur = try { cleanBssid(wm.connectionInfo?.bssid) } catch (_: Exception) { "" }
            for (r in wm.scanResults) list.put(bss(r, cur))
        } catch (e: SecurityException) {
            out.put("err", "Allow location access in PingVakt to see Wi-Fi networks")
        }
        out.put("bss", list)
        return out.toString()
    }

    @Suppress("DEPRECATION")
    private fun bss(r: ScanResult, cur: String): JSONObject {
        val o = JSONObject()
        val ssid = if (Build.VERSION.SDK_INT >= 33) cleanSsid(r.wifiSsid?.toString()) else (r.SSID ?: "")
        o.put("ssid", ssid)
        o.put("bssid", r.BSSID.lowercase())
        o.put("rssi", r.level)
        o.put("freq", r.frequency)
        o.put("width", width(r.channelWidth))
        o.put("sec", security(r.capabilities ?: ""))
        if (Build.VERSION.SDK_INT >= 30) std(r.wifiStandard)?.let { o.put("std", it) }
        if (cur.isNotEmpty() && r.BSSID.equals(cur, true)) o.put("conn", true)
        return o
    }

    private fun width(w: Int): Int = when (w) {
        ScanResult.CHANNEL_WIDTH_40MHZ -> 40
        ScanResult.CHANNEL_WIDTH_80MHZ -> 80
        ScanResult.CHANNEL_WIDTH_160MHZ, ScanResult.CHANNEL_WIDTH_80MHZ_PLUS_MHZ -> 160
        5 -> 320 // CHANNEL_WIDTH_320MHZ (Android 13+)
        else -> 20
    }

    private fun std(s: Int): String? = when (s) {
        4 -> "802.11n"
        5 -> "802.11ac"
        6 -> "802.11ax"
        7 -> "802.11ad"
        8 -> "802.11be"
        1 -> "802.11a/b/g"
        else -> null
    }

    private fun security(c: String): String {
        val u = c.uppercase()
        val ent = u.contains("EAP")
        val sae = u.contains("SAE")
        val psk = u.contains("PSK")
        val base = when {
            sae && psk -> "WPA2/WPA3"
            sae || u.contains("OWE") -> "WPA3"
            u.contains("RSN") || u.contains("WPA2") -> "WPA2"
            u.contains("WPA") -> "WPA"
            u.contains("WEP") -> "WEP"
            else -> "Open"
        }
        return if (ent && base != "Open" && base != "WEP") "$base Enterprise" else base
    }
}
