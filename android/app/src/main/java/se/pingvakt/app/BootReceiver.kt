package se.pingvakt.app

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** Startar agenten när telefonen startar eller appen har uppdaterats (om den varit igång). */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        val prefs = ctx.getSharedPreferences("pv", Context.MODE_PRIVATE)
        if (prefs.getBoolean("autostart", true)) {
            try {
                AgentService.start(ctx)
            } catch (_: Exception) {
                // Android kan neka start i bakgrunden – agenten startar nästa gång appen öppnas
            }
        }
    }
}
