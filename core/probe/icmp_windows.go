//go:build windows

package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
	"unsafe"
)

type icmpPinger interface {
	ping(ctx context.Context, ip net.IP, size int, timeout time.Duration) (time.Duration, error)
	close()
}

// På Windows används IcmpSendEcho/Icmp6SendEcho2 i iphlpapi.dll – samma API som
// Windows egen ping använder. Det kräver inte administratörsrättigheter.
var (
	iphlpapi            = syscall.NewLazyDLL("iphlpapi.dll")
	procIcmpCreateFile  = iphlpapi.NewProc("IcmpCreateFile")
	procIcmp6CreateFile = iphlpapi.NewProc("Icmp6CreateFile")
	procIcmpCloseHandle = iphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho    = iphlpapi.NewProc("IcmpSendEcho")
	procIcmp6SendEcho2  = iphlpapi.NewProc("Icmp6SendEcho2")
)

const invalidHandle = ^uintptr(0)

type winICMP struct{}

func newICMP() icmpPinger { return winICMP{} }

func (winICMP) close() {}

type sockaddrIn6 struct {
	Family   uint16
	Port     uint16
	Flowinfo uint32
	Addr     [16]byte
	ScopeID  uint32
}

func (winICMP) ping(ctx context.Context, ip net.IP, size int, timeout time.Duration) (time.Duration, error) {
	if d, ok := ctx.Deadline(); ok {
		if rem := time.Until(d); rem < timeout {
			timeout = rem
		}
	}
	if timeout < time.Millisecond {
		return 0, ErrTimeout
	}
	if size < 1 {
		size = 32
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = byte('a' + i%23)
	}
	reply := make([]byte, 128+size+8)
	ms := uintptr(timeout / time.Millisecond)

	type result struct {
		rtt time.Duration
		err error
	}
	ch := make(chan result, 1)
	go func() {
		if ip4 := ip.To4(); ip4 != nil {
			h, _, err := procIcmpCreateFile.Call()
			if h == invalidHandle || h == 0 {
				ch <- result{err: fmt.Errorf("IcmpCreateFile: %v", err)}
				return
			}
			defer procIcmpCloseHandle.Call(h)
			dst := binary.LittleEndian.Uint32(ip4)
			start := time.Now()
			n, _, callErr := procIcmpSendEcho.Call(h, uintptr(dst),
				uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 0,
				uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), ms)
			rtt := time.Since(start)
			if n == 0 {
				ch <- result{err: winStatusErr(callErr)}
				return
			}
			// ICMP_ECHO_REPLY: Address(4) Status(4) RoundTripTime(4) ...
			if st := binary.LittleEndian.Uint32(reply[4:8]); st != 0 {
				ch <- result{err: statusErr(st)}
				return
			}
			ch <- result{rtt: rtt}
			return
		}
		h, _, err := procIcmp6CreateFile.Call()
		if h == invalidHandle || h == 0 {
			ch <- result{err: fmt.Errorf("Icmp6CreateFile: %v", err)}
			return
		}
		defer procIcmpCloseHandle.Call(h)
		src := sockaddrIn6{Family: syscall.AF_INET6}
		dst := sockaddrIn6{Family: syscall.AF_INET6}
		copy(dst.Addr[:], ip.To16())
		start := time.Now()
		n, _, callErr := procIcmp6SendEcho2.Call(h, 0, 0, 0,
			uintptr(unsafe.Pointer(&src)), uintptr(unsafe.Pointer(&dst)),
			uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 0,
			uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), ms)
		rtt := time.Since(start)
		if n == 0 {
			ch <- result{err: winStatusErr(callErr)}
			return
		}
		// ICMPV6_ECHO_REPLY börjar med IPV6_ADDRESS_EX (26 byte, packad). Beroende på
		// packning ligger Status på offset 26 eller 28 – ett returvärde > 0 betyder svar,
		// så vi räknar bara med fel om någon av platserna har en känd ICMP-felkod.
		for _, off := range []int{28, 26} {
			if st := binary.LittleEndian.Uint32(reply[off : off+4]); st > 11000 && st <= 11050 {
				ch <- result{err: statusErr(st)}
				return
			}
		}
		ch <- result{rtt: rtt}
	}()
	select {
	case r := <-ch:
		return r.rtt, r.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func winStatusErr(err error) error {
	if en, ok := err.(syscall.Errno); ok && en != 0 {
		return statusErr(uint32(en))
	}
	return ErrTimeout
}

func statusErr(st uint32) error {
	switch st {
	case 11010: // IP_REQ_TIMED_OUT
		return ErrTimeout
	case 11002, 11003: // DEST_NET/HOST_UNREACHABLE
		return errors.New("Destination unreachable")
	case 11005: // DEST_PORT_UNREACHABLE
		return errors.New("Port unreachable")
	case 11013, 11014: // TTL_EXPIRED_TRANSIT/REASSEM
		return errors.New("TTL expired in transit")
	case 11050: // GENERAL_FAILURE
		return errors.New("General failure")
	}
	return fmt.Errorf("ICMP-status %d", st)
}
