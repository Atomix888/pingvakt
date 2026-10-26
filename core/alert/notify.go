package alert

import (
	"encoding/base64"
	"os"
	"os/exec"
	"runtime"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// Notify visar en systemnotis (bästa försök). Texten skickas som argument eller
// miljövariabler – aldrig inbakad i ett skript – så att målnamn inte kan tolkas som kod.
func Notify(title, body string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("osascript",
			"-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
			"-e", "end run", title, body)
	case "windows":
		script := `$ErrorActionPreference='SilentlyContinue';` +
			`[Windows.UI.Notifications.ToastNotificationManager,Windows.UI.Notifications,ContentType=WindowsRuntime]>$null;` +
			`$t=[Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02);` +
			`$x=$t.GetElementsByTagName('text');` +
			`$x.Item(0).AppendChild($t.CreateTextNode($env:PV_TITLE))>$null;` +
			`$x.Item(1).AppendChild($t.CreateTextNode($env:PV_BODY))>$null;` +
			`$id='{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe';` +
			`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($id).Show([Windows.UI.Notifications.ToastNotification]::new($t))`
		cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
		cmd.Env = append(os.Environ(), "PV_TITLE="+title, "PV_BODY="+body)
		hideConsole(cmd)
	default:
		if _, err := exec.LookPath("notify-send"); err != nil {
			return
		}
		cmd = exec.Command("notify-send", "-a", "PingVakt", "--", title, body)
	}
	_ = cmd.Run()
}
