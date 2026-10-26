//go:build !windows

package probe

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type icmpPinger interface {
	ping(ctx context.Context, ip net.IP, size int, timeout time.Duration) (time.Duration, error)
	close()
}

// unixICMP använder i första hand oprivilegierade ICMP-socketar ("udp4"/"udp6"),
// som fungerar utan root på macOS och på Linux när net.ipv4.ping_group_range
// tillåter det. Annars faller den tillbaka på råa socketar (kräver root/cap_net_raw).
type unixICMP struct {
	mu    sync.Mutex
	mode4 string
	mode6 string
	seq   uint32
}

func newICMP() icmpPinger { return &unixICMP{} }

func (u *unixICMP) close() {}

func (u *unixICMP) listen(v6 bool) (*icmp.PacketConn, string, error) {
	networks, addr := []string{"udp4", "ip4:icmp"}, "0.0.0.0"
	if v6 {
		networks, addr = []string{"udp6", "ip6:ipv6-icmp"}, "::"
	}
	u.mu.Lock()
	cached := u.mode4
	if v6 {
		cached = u.mode6
	}
	u.mu.Unlock()
	if cached != "" {
		c, err := icmp.ListenPacket(cached, addr)
		return c, cached, err
	}
	var firstErr error
	for _, n := range networks {
		c, err := icmp.ListenPacket(n, addr)
		if err == nil {
			u.mu.Lock()
			if v6 {
				u.mode6 = n
			} else {
				u.mode4 = n
			}
			u.mu.Unlock()
			return c, n, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	hint := ""
	if runtime.GOOS == "linux" {
		hint = ` – kör: sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"`
	}
	return nil, "", fmt.Errorf("kan inte öppna ICMP-socket (%v)%s", firstErr, hint)
}

func (u *unixICMP) ping(ctx context.Context, ip net.IP, size int, timeout time.Duration) (time.Duration, error) {
	v6 := ip.To4() == nil
	c, network, err := u.listen(v6)
	if err != nil {
		return 0, err
	}
	defer c.Close()

	seq := int(atomic.AddUint32(&u.seq, 1) & 0xffff)
	id := os.Getpid() & 0xffff
	if size < 8 {
		size = 8
	}
	payload := make([]byte, size)
	token := payload[:8]
	_, _ = rand.Read(token)
	for i := 8; i < size; i++ {
		payload[i] = byte('a' + (i-8)%23)
	}

	var typ icmp.Type = ipv4.ICMPTypeEcho
	proto := 1
	if v6 {
		typ, proto = ipv6.ICMPTypeEchoRequest, 58
	}
	msg := icmp.Message{Type: typ, Code: 0, Body: &icmp.Echo{ID: id, Seq: seq, Data: payload}}
	wb, err := msg.Marshal(nil)
	if err != nil {
		return 0, err
	}
	var dst net.Addr = &net.IPAddr{IP: ip}
	if strings.HasPrefix(network, "udp") {
		dst = &net.UDPAddr{IP: ip}
	}

	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Now()) })
	defer stop()

	start := time.Now()
	if _, err := c.WriteTo(wb, dst); err != nil {
		return 0, err
	}
	rb := make([]byte, 1500+size)
	for {
		n, _, err := c.ReadFrom(rb)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return 0, ErrTimeout
			}
			return 0, err
		}
		rtt := time.Since(start)
		m, err := icmp.ParseMessage(proto, rb[:n])
		if err != nil {
			continue
		}
		switch m.Type {
		case ipv4.ICMPTypeEchoReply, ipv6.ICMPTypeEchoReply:
			echo, ok := m.Body.(*icmp.Echo)
			if !ok || echo.Seq != seq || !bytes.HasPrefix(echo.Data, token) {
				continue
			}
			return rtt, nil
		case ipv4.ICMPTypeDestinationUnreachable, ipv6.ICMPTypeDestinationUnreachable:
			if du, ok := m.Body.(*icmp.DstUnreach); ok && embeddedMatches(du.Data, v6, id, seq) {
				return 0, errors.New("Målet kan inte nås")
			}
		case ipv4.ICMPTypeTimeExceeded, ipv6.ICMPTypeTimeExceeded:
			if te, ok := m.Body.(*icmp.TimeExceeded); ok && embeddedMatches(te.Data, v6, id, seq) {
				return 0, errors.New("TTL gick ut på vägen")
			}
		}
	}
}

// embeddedMatches kontrollerar om ett ICMP-felmeddelande gäller vår förfrågan
// (det bifogade originalpaketet innehåller vårt ID och sekvensnummer).
func embeddedMatches(data []byte, v6 bool, id, seq int) bool {
	off := 40
	if !v6 {
		if len(data) < 1 {
			return false
		}
		off = int(data[0]&0x0f) * 4
	}
	if len(data) < off+8 {
		return false
	}
	h := data[off:]
	gotID := int(h[4])<<8 | int(h[5])
	gotSeq := int(h[6])<<8 | int(h[7])
	return gotSeq == seq && gotID == id
}
