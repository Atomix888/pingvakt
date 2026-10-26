package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"pingvakt/core/agentproto"
)

// updater byter ut agentens programfil mot en nyare version från servern.
type updater struct {
	mu      sync.Mutex
	busy    bool
	failed  map[string]time.Time
	up      *agentproto.Uploader
	server  string
	restart func()
}

func (u *updater) offer(info agentproto.UpdateInfo) {
	if !agentproto.NewerVersion(info.Version, Version) {
		return
	}
	u.mu.Lock()
	if u.busy || time.Since(u.failed[info.Version]) < time.Hour {
		u.mu.Unlock()
		return
	}
	u.busy = true
	u.mu.Unlock()
	defer func() { u.mu.Lock(); u.busy = false; u.mu.Unlock() }()

	log.Printf("update: version %s is available on the server (running %s) – downloading", info.Version, Version)
	if err := u.install(info); err != nil {
		log.Printf("update failed: %v", err)
		u.mu.Lock()
		if u.failed == nil {
			u.failed = map[string]time.Time{}
		}
		u.failed[info.Version] = time.Now()
		u.mu.Unlock()
		return
	}
	log.Printf("update: version %s installed – restarting", info.Version)
	u.restart()
}

func (u *updater) install(info agentproto.UpdateInfo) error {
	base, err := agentproto.BaseURL(u.server)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+info.Path, nil)
	if err != nil {
		return err
	}
	res, err := u.up.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("the server responded %d", res.StatusCode)
	}
	tmp := exe + ".new"
	if runtime.GOOS == "windows" {
		tmp = strings.TrimSuffix(exe, ".exe") + ".new.exe"
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, 200<<20))
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != info.SHA256 || (info.Size > 0 && n != info.Size) {
		os.Remove(tmp)
		return errors.New("checksum mismatch – the file is not used")
	}
	// kontrollera att den nya filen går att köra
	out, err := exec.Command(tmp, "version").CombinedOutput()
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("the new version does not start: %v %s", err, out)
	}
	if !strings.Contains(string(out), " "+info.Version+" ") {
		os.Remove(tmp)
		return fmt.Errorf("the file on the server is not version %s (%s)", info.Version, strings.TrimSpace(string(out)))
	}
	if runtime.GOOS == "windows" {
		// en körande .exe kan inte skrivas över men kan byta namn
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, exe); err != nil {
			_ = os.Rename(old, exe)
			return err
		}
		return nil
	}
	return os.Rename(tmp, exe)
}

// cleanupOld tar bort rester från en tidigare uppdatering.
func cleanupOld() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
		_ = os.Remove(exe + ".new")
		_ = os.Remove(strings.TrimSuffix(exe, ".exe") + ".new.exe")
	}
}

// restartSelf startar om tjänsten med den nya programfilen.
func restartSelf() {
	switch {
	case runtime.GOOS == "linux" && os.Getenv("INVOCATION_ID") != "":
		// under systemd: be systemd starta om tjänsten direkt
		if err := exec.Command("systemctl", "--no-block", "restart", "pingvakt-agent").Run(); err == nil {
			return
		}
	case runtime.GOOS == "darwin":
		if err := exec.Command("launchctl", "kickstart", "-k", "system/pingvakt-agent").Start(); err == nil {
			return
		}
	}
	// annars: avsluta med felkod – tjänstehanteraren (Windows-tjänstens återställning,
	// launchd KeepAlive, systemd Restart=always) startar agenten igen
	os.Exit(3)
}
