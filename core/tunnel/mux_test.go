package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// pipeFrames kopplar ihop två sidor i minnet.
type pipeFrames struct {
	in, out chan []byte
	closed  chan struct{}
}

func pair() (*pipeFrames, *pipeFrames) {
	a, b := make(chan []byte, 64), make(chan []byte, 64)
	c := make(chan struct{})
	return &pipeFrames{in: a, out: b, closed: c}, &pipeFrames{in: b, out: a, closed: c}
}

func (p *pipeFrames) ReadFrame() ([]byte, error) {
	select {
	case b := <-p.in:
		return b, nil
	case <-p.closed:
		return nil, io.EOF
	}
}
func (p *pipeFrames) WriteFrame(b []byte) error {
	select {
	case p.out <- append([]byte(nil), b...):
		return nil
	case <-p.closed:
		return io.EOF
	}
}
func (p *pipeFrames) Close() error {
	select {
	case <-p.closed:
	default:
		close(p.closed)
	}
	return nil
}

func TestMuxEcho(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	sf, af := pair()
	srv := New(sf, nil)
	agent := New(af, func(ctx context.Context, addr string) (net.Conn, error) {
		if addr == "10.255.255.1:1" {
			return nil, errors.New("connection refused")
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	})
	go srv.Run()
	go agent.Run()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 3; i++ {
		c, err := srv.Open(ctx, ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		msg := make([]byte, 100000)
		for j := range msg {
			msg[j] = byte(j + i)
		}
		go func() { _, _ = c.Write(msg) }()
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(c, got); err != nil {
			t.Fatal(err)
		}
		for j := range msg {
			if got[j] != msg[j] {
				t.Fatalf("byte %d differs", j)
			}
		}
		c.Close()
	}
	if _, err := srv.Open(ctx, "10.255.255.1:1"); err == nil || err.Error() != "connection refused" {
		t.Fatalf("want refused, got %v", err)
	}
	srv.Close()
	select {
	case <-agent.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the agent side did not notice the close")
	}
}
