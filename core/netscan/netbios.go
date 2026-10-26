package netscan

import (
	"crypto/rand"
	"net"
	"strings"
	"time"
)

// netbiosName frågar efter datorns NetBIOS-namn (UDP 137, "node status").
func netbiosName(ip string, timeout time.Duration) string {
	c, err := net.DialTimeout("udp", net.JoinHostPort(ip, "137"), timeout)
	if err != nil {
		return ""
	}
	defer c.Close()
	var id [2]byte
	_, _ = rand.Read(id[:])
	// huvud: id, flaggor 0, 1 fråga
	q := []byte{id[0], id[1], 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	// namnet "*" kodat (32 tecken) + typ NBSTAT (0x21), klass IN
	name := make([]byte, 0, 34)
	name = append(name, 0x20)
	enc := []byte("*\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	for _, b := range enc {
		name = append(name, 'A'+(b>>4), 'A'+(b&0x0f))
	}
	name = append(name, 0x00)
	q = append(q, name...)
	q = append(q, 0x00, 0x21, 0x00, 0x01)
	_ = c.SetDeadline(time.Now().Add(timeout))
	if _, err := c.Write(q); err != nil {
		return ""
	}
	buf := make([]byte, 1024)
	n, err := c.Read(buf)
	if err != nil {
		return ""
	}
	return parseNBStat(buf[:n], id)
}

func parseNBStat(b []byte, id [2]byte) string {
	if len(b) < 57 || b[0] != id[0] || b[1] != id[1] {
		return ""
	}
	// hoppa över huvud (12) + namn (34) + typ/klass/ttl (8) + längd (2)
	off := 12 + 34 + 8 + 2
	if off >= len(b) {
		return ""
	}
	count := int(b[off])
	off++
	for i := 0; i < count && off+18 <= len(b); i++ {
		nm := strings.TrimRight(string(b[off:off+15]), " \x00")
		suffix := b[off+15]
		flags := b[off+16]
		off += 18
		if suffix == 0x00 && flags&0x80 == 0 && nm != "" { // unikt arbetsstationsnamn
			return nm
		}
	}
	return ""
}
