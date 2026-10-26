package alert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// PostWebhook skickar händelsen som JSON. Fältet "text" gör att meddelandet
// visas direkt i Slack och i Teams/Mattermost-kanaler med inkommande webhook.
func PostWebhook(url string, ev Event) error {
	text := fmt.Sprintf("PingVakt %s: %s (%s) – %s. %s", ev.KindText(), ev.TargetName, ev.Host, ev.AlertName, ev.Detail)
	body, _ := json.Marshal(map[string]any{
		"text":   text,
		"event":  ev.Kind,
		"alert":  ev.AlertName,
		"target": ev.TargetName,
		"host":   ev.Host,
		"detail": ev.Detail,
		"time":   time.UnixMilli(ev.Time).Format(time.RFC3339),
	})
	c := &http.Client{Timeout: 15 * time.Second}
	res, err := c.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("mottagaren svarade %d", res.StatusCode)
	}
	return nil
}
