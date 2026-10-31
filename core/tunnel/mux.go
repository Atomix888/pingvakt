// Package tunnel låter servern öppna TCP-anslutningar i kundens nät genom agenten.
//
// Agenten kopplar upp en WebSocket mot servern (utgående, så inget behöver öppnas hos kunden),
// och över den skickas flera TCP-strömmar samtidigt med ett enkelt ramformat:
//
//	[1 byte typ][4 byte ström-ID][data]
//
// Servern öppnar strömmar (Open), agenten ansluter till adressen och skickar data fram och tillbaka.
package tunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const (
	fOpen   = 1 // server → agent: data = "värd:port"
	fOK     = 2 // agent → server: anslutningen är öppen
	fErr    = 3 // agent → server: data = felmeddelande
	fData   = 4
	fClose  = 5
	maxData = 32 << 10
)

// Frames är det som bär ramarna, normalt en WebSocket.
type Frames interface {
	ReadFrame() ([]byte, error)
	WriteFrame([]byte) error
	Close() error
}

// Dialer ansluter till en adress i kundens nät (agentens sida).
type Dialer func(ctx context.Context, addr string) (net.Conn, error)

// Mux är en tunnel med flera strömmar.
type Mux struct {
	f       Frames
	dial    Dialer // nil på serversidan
	wmu     sync.Mutex
	mu      sync.Mutex
	streams map[uint32]*stream
	next    uint32
	done    chan struct{}
	err     error
	last    time.Time
}

// New skapar en tunnel. dial anges på agentens sida och är nil på serverns.
func New(f Frames, dial Dialer) *Mux {
	return &Mux{f: f, dial: dial, streams: map[uint32]*stream{}, done: make(chan struct{}), last: time.Now()}
}

// Done stängs när tunneln har gått ner.
func (m *Mux) Done() <-chan struct{} { return m.done }

// Idle anger hur länge tunneln varit utan öppna strömmar (0 om någon är öppen).
func (m *Mux) Idle() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.streams) > 0 {
		return 0
	}
	return time.Since(m.last)
}

// Streams ger antalet öppna strömmar.
func (m *Mux) Streams() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.streams)
}

func (m *Mux) send(typ byte, id uint32, data []byte) error {
	b := make([]byte, 5+len(data))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:5], id)
	copy(b[5:], data)
	m.wmu.Lock()
	defer m.wmu.Unlock()
	return m.f.WriteFrame(b)
}

// Close stänger tunneln och alla strömmar.
func (m *Mux) Close() error {
	m.shutdown(errors.New("the tunnel was closed"))
	return nil
}

func (m *Mux) shutdown(err error) {
	m.mu.Lock()
	select {
	case <-m.done:
		m.mu.Unlock()
		return
	default:
	}
	m.err = err
	close(m.done)
	list := make([]*stream, 0, len(m.streams))
	for _, s := range m.streams {
		list = append(list, s)
	}
	m.streams = map[uint32]*stream{}
	m.mu.Unlock()
	_ = m.f.Close()
	for _, s := range list {
		s.remoteClosed(err)
	}
}

// Run läser ramar tills tunneln går ner.
func (m *Mux) Run() error {
	for {
		b, err := m.f.ReadFrame()
		if err != nil {
			m.shutdown(err)
			return err
		}
		if len(b) < 5 {
			continue
		}
		typ, id, data := b[0], binary.BigEndian.Uint32(b[1:5]), b[5:]
		switch typ {
		case fOpen:
			if m.dial != nil {
				go m.accept(id, string(data))
			}
		case fOK, fErr:
			m.mu.Lock()
			s := m.streams[id]
			m.mu.Unlock()
			if s != nil {
				var e error
				if typ == fErr {
					e = errors.New(string(data))
				}
				select {
				case s.opened <- e:
				default:
				}
			}
		case fData:
			m.mu.Lock()
			s := m.streams[id]
			m.mu.Unlock()
			if s != nil {
				s.push(append([]byte(nil), data...))
			}
		case fClose:
			m.mu.Lock()
			s := m.streams[id]
			delete(m.streams, id)
			if len(m.streams) == 0 {
				m.last = time.Now()
			}
			m.mu.Unlock()
			if s != nil {
				s.remoteClosed(io.EOF)
			}
		}
	}
}

func (m *Mux) newStream(id uint32, addr string) *stream {
	s := &stream{m: m, id: id, addr: addr, opened: make(chan error, 1), wake: make(chan struct{}, 1)}
	m.mu.Lock()
	m.streams[id] = s
	m.mu.Unlock()
	return s
}

func (m *Mux) forget(id uint32) {
	m.mu.Lock()
	delete(m.streams, id)
	if len(m.streams) == 0 {
		m.last = time.Now()
	}
	m.mu.Unlock()
}

// Open öppnar en TCP-anslutning till addr i kundens nät (serverns sida).
func (m *Mux) Open(ctx context.Context, addr string) (net.Conn, error) {
	m.mu.Lock()
	m.next++
	id := m.next
	m.mu.Unlock()
	s := m.newStream(id, addr)
	if err := m.send(fOpen, id, []byte(addr)); err != nil {
		m.forget(id)
		return nil, err
	}
	select {
	case err := <-s.opened:
		if err != nil {
			m.forget(id)
			return nil, err
		}
		return s, nil
	case <-ctx.Done():
		m.forget(id)
		_ = m.send(fClose, id, nil)
		return nil, ctx.Err()
	case <-m.done:
		return nil, errors.New("the tunnel to the agent was closed")
	}
}

// accept ansluter till adressen och kopplar ihop den med strömmen (agentens sida).
func (m *Mux) accept(id uint32, addr string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	c, err := m.dial(ctx, addr)
	cancel()
	if err != nil {
		_ = m.send(fErr, id, []byte(err.Error()))
		return
	}
	s := m.newStream(id, addr)
	if err := m.send(fOK, id, nil); err != nil {
		c.Close()
		return
	}
	go func() {
		_, _ = io.Copy(c, s)
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	_, _ = io.Copy(s, c)
	c.Close()
	_ = s.Close()
}

// stream är en TCP-ström i tunneln och fungerar som en net.Conn.
type stream struct {
	m      *Mux
	id     uint32
	addr   string
	opened chan error
	mu     sync.Mutex
	buf    [][]byte
	rerr   error
	closed bool
	wake   chan struct{}
	dl     time.Time
}

func (s *stream) push(b []byte) {
	s.mu.Lock()
	s.buf = append(s.buf, b)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *stream) remoteClosed(err error) {
	s.mu.Lock()
	if s.rerr == nil {
		s.rerr = err
	}
	s.mu.Unlock()
	select {
	case s.opened <- err:
	default:
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *stream) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if len(s.buf) > 0 {
			n := copy(p, s.buf[0])
			if n == len(s.buf[0]) {
				s.buf = s.buf[1:]
			} else {
				s.buf[0] = s.buf[0][n:]
			}
			s.mu.Unlock()
			return n, nil
		}
		if s.rerr != nil {
			err := s.rerr
			s.mu.Unlock()
			return 0, err
		}
		dl := s.dl
		s.mu.Unlock()
		if dl.IsZero() {
			<-s.wake
			continue
		}
		d := time.Until(dl)
		if d <= 0 {
			return 0, timeoutErr{}
		}
		t := time.NewTimer(d)
		select {
		case <-s.wake:
			t.Stop()
		case <-t.C:
			return 0, timeoutErr{}
		}
	}
}

func (s *stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return 0, net.ErrClosed
	}
	n := 0
	for len(p) > 0 {
		c := p
		if len(c) > maxData {
			c = c[:maxData]
		}
		if err := s.m.send(fData, s.id, c); err != nil {
			return n, err
		}
		n += len(c)
		p = p[len(c):]
	}
	return n, nil
}

func (s *stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.rerr == nil {
		s.rerr = net.ErrClosed
	}
	s.mu.Unlock()
	s.m.forget(s.id)
	return s.m.send(fClose, s.id, nil)
}

type tunnelAddr string

func (a tunnelAddr) Network() string { return "tunnel" }
func (a tunnelAddr) String() string  { return string(a) }

func (s *stream) LocalAddr() net.Addr  { return tunnelAddr("agent") }
func (s *stream) RemoteAddr() net.Addr { return tunnelAddr(s.addr) }
func (s *stream) SetDeadline(t time.Time) error {
	return s.SetReadDeadline(t)
}
func (s *stream) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	s.dl = t
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}
func (s *stream) SetWriteDeadline(time.Time) error { return nil }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }
