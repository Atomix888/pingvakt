package se.pingvakt.app

import android.content.Context
import java.net.HttpURLConnection
import java.net.URL

/** Anrop till agenten på 127.0.0.1 (med appens nyckel). */
object Local {
    fun url(path: String) = "http://127.0.0.1:${AgentService.PORT}$path"

    fun post(ctx: Context, path: String, json: String): String? = request(ctx, "POST", path, json)

    fun get(ctx: Context, path: String): String? = request(ctx, "GET", path, null)

    private fun request(ctx: Context, method: String, path: String, json: String?): String? {
        val c = URL(url(path)).openConnection() as HttpURLConnection
        return try {
            c.requestMethod = method
            c.connectTimeout = 2000
            c.readTimeout = 25_000
            c.setRequestProperty("X-PV-Key", AgentService.key(ctx))
            if (json != null) {
                c.doOutput = true
                c.setRequestProperty("Content-Type", "application/json")
                c.outputStream.use { it.write(json.toByteArray()) }
            }
            if (c.responseCode in 200..299) c.inputStream.bufferedReader().use { it.readText() } else null
        } catch (e: Exception) {
            null
        } finally {
            c.disconnect()
        }
    }
}
