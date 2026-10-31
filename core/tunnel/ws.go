package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/websocket"
)

// WS gör en WebSocket till ramar för tunneln.
type WS struct{ C *websocket.Conn }

func (w WS) ReadFrame() ([]byte, error) {
	var b []byte
	err := websocket.Message.Receive(w.C, &b)
	return b, err
}

func (w WS) WriteFrame(b []byte) error { return websocket.Message.Send(w.C, b) }
func (w WS) Close() error              { return w.C.Close() }

// URL bygger tunnelns adress från serverns basadress (https → wss).
func URL(server string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(server), "/"))
	if err != nil || u.Host == "" {
		return "", errors.New("invalid server address")
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", errors.New("invalid server address")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/agent/tunnel"
	return u.String(), nil
}

// Dial kopplar upp agentens tunnel mot servern.
func Dial(ctx context.Context, server, token, userAgent string, insecure bool) (*websocket.Conn, error) {
	wsURL, err := URL(server)
	if err != nil {
		return nil, err
	}
	origin := strings.Replace(strings.Replace(wsURL, "wss://", "https://", 1), "ws://", "http://", 1)
	cfg, err := websocket.NewConfig(wsURL, origin)
	if err != nil {
		return nil, err
	}
	cfg.Header = http.Header{"Authorization": {"Bearer " + token}, "User-Agent": {userAgent}}
	if insecure {
		cfg.TlsConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // uttryckligt val av användaren
	}
	return cfg.DialContext(ctx)
}
