// Package snmp hämtar status från nätverksutrustning (switchar, brandväggar,
// accesspunkter, skrivare) med SNMP v1, v2c eller v3: systeminfo, CPU, minne
// och portar med status, hastighet, trafik och fel.
package snmp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Device är en enhet som ska pollas.
type Device struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port,omitempty"` // standard 161
	Version   string `json:"version"`        // "1", "2c" eller "3"
	Community string `json:"community,omitempty"`
	// SNMPv3
	User      string `json:"user,omitempty"`
	AuthProto string `json:"authProto,omitempty"` // "", MD5, SHA, SHA256, SHA512
	AuthPass  string `json:"authPass,omitempty"`
	PrivProto string `json:"privProto,omitempty"` // "", DES, AES, AES256
	PrivPass  string `json:"privPass,omitempty"`

	IntervalSec int   `json:"intervalSec,omitempty"` // standard 60
	Watch       []int `json:"watch,omitempty"`       // portar (ifIndex) som larmar när de går ner
	Paused      bool  `json:"paused,omitempty"`
}

// Normalize sätter standardvärden.
func (d *Device) Normalize() {
	d.Host = strings.TrimSpace(d.Host)
	if d.Port <= 0 || d.Port > 65535 {
		d.Port = 161
	}
	switch d.Version {
	case "1", "3":
	default:
		d.Version = "2c"
	}
	if d.Community == "" && d.Version != "3" {
		d.Community = "public"
	}
	if d.IntervalSec < 10 {
		d.IntervalSec = 60
	}
	if d.IntervalSec > 3600 {
		d.IntervalSec = 3600
	}
}

// If är en port (gränssnitt).
type If struct {
	Index   int    `json:"i"`
	Name    string `json:"n"`
	Alias   string `json:"a,omitempty"`
	Type    int    `json:"ty,omitempty"`
	Admin   int    `json:"ad"`           // 1 = up, 2 = down, 3 = testing
	Oper    int    `json:"op"`           // 1 = up, 2 = down, … 7 = lowerLayerDown
	Speed   uint64 `json:"sp,omitempty"` // bit/s
	InOct   uint64 `json:"io"`
	OutOct  uint64 `json:"oo"`
	InErr   uint64 `json:"ie,omitempty"`
	OutErr  uint64 `json:"oe,omitempty"`
	InDisc  uint64 `json:"id,omitempty"`
	OutDisc uint64 `json:"od,omitempty"`
}

// Poll är resultatet av en avläsning.
type Poll struct {
	DeviceID string  `json:"d"`
	T        int64   `json:"t"`
	OK       bool    `json:"ok"`
	Err      string  `json:"e,omitempty"`
	RTT      float64 `json:"r,omitempty"` // ms för första svaret
	SysName  string  `json:"sn,omitempty"`
	SysDescr string  `json:"sd,omitempty"`
	Uptime   int64   `json:"up,omitempty"` // sekunder
	CPU      float64 `json:"cpu"`          // -1 = okänt
	Mem      float64 `json:"mem"`          // procent, -1 = okänt
	Ifs      []If    `json:"ifs,omitempty"`
}

const (
	oidSysDescr  = ".1.3.6.1.2.1.1.1.0"
	oidSysUptime = ".1.3.6.1.2.1.1.3.0"
	oidSysName   = ".1.3.6.1.2.1.1.5.0"
	oidIfEntry   = ".1.3.6.1.2.1.2.2.1"
	oidIfXEntry  = ".1.3.6.1.2.1.31.1.1.1"
	oidHrProc    = ".1.3.6.1.2.1.25.3.3.1.2"
	oidHrStorage = ".1.3.6.1.2.1.25.2.3.1"
	hrStorageRAM = ".1.3.6.1.2.1.25.2.1.2"
)

func (d Device) client(timeout time.Duration) *gosnmp.GoSNMP {
	g := &gosnmp.GoSNMP{Target: d.Host, Port: uint16(d.Port), Timeout: timeout, Retries: 1, MaxRepetitions: 25, MaxOids: 60}
	switch d.Version {
	case "1":
		g.Version, g.Community = gosnmp.Version1, d.Community
	case "3":
		g.Version, g.SecurityModel = gosnmp.Version3, gosnmp.UserSecurityModel
		usm := &gosnmp.UsmSecurityParameters{UserName: d.User}
		flags := gosnmp.NoAuthNoPriv
		if d.AuthPass != "" {
			flags = gosnmp.AuthNoPriv
			usm.AuthenticationPassphrase = d.AuthPass
			switch strings.ToUpper(d.AuthProto) {
			case "MD5":
				usm.AuthenticationProtocol = gosnmp.MD5
			case "SHA256":
				usm.AuthenticationProtocol = gosnmp.SHA256
			case "SHA512":
				usm.AuthenticationProtocol = gosnmp.SHA512
			default:
				usm.AuthenticationProtocol = gosnmp.SHA
			}
			if d.PrivPass != "" {
				flags = gosnmp.AuthPriv
				usm.PrivacyPassphrase = d.PrivPass
				switch strings.ToUpper(d.PrivProto) {
				case "DES":
					usm.PrivacyProtocol = gosnmp.DES
				case "AES256":
					usm.PrivacyProtocol = gosnmp.AES256
				default:
					usm.PrivacyProtocol = gosnmp.AES
				}
			}
		}
		g.MsgFlags, g.SecurityParameters = flags, usm
	default:
		g.Version, g.Community = gosnmp.Version2c, d.Community
	}
	return g
}

// PollDevice läser av enheten.
func PollDevice(ctx context.Context, d Device) Poll {
	d.Normalize()
	p := Poll{DeviceID: d.ID, T: time.Now().UnixMilli(), CPU: -1, Mem: -1}
	g := d.client(3 * time.Second)
	g.Context = ctx
	if err := g.Connect(); err != nil {
		p.Err = err.Error()
		return p
	}
	defer g.Conn.Close()
	start := time.Now()
	res, err := g.Get([]string{oidSysDescr, oidSysUptime, oidSysName})
	if err != nil {
		p.Err = shortErr(err)
		return p
	}
	p.RTT = float64(time.Since(start).Microseconds()) / 1000
	for _, v := range res.Variables {
		switch v.Name {
		case oidSysDescr:
			p.SysDescr = strings.TrimSpace(str(v))
		case oidSysUptime:
			p.Uptime = int64(gosnmp.ToBigInt(v.Value).Uint64() / 100)
		case oidSysName:
			p.SysName = strings.TrimSpace(str(v))
		}
	}
	p.OK = true
	walk := func(oid string, fn func(gosnmp.SnmpPDU) error) error {
		if d.Version == "1" {
			return g.Walk(oid, fn)
		}
		return g.BulkWalk(oid, fn)
	}
	ifs := map[int]*If{}
	get := func(idx int) *If {
		if ifs[idx] == nil {
			ifs[idx] = &If{Index: idx}
		}
		return ifs[idx]
	}
	_ = walk(oidIfEntry, func(v gosnmp.SnmpPDU) error {
		col, idx := colIdx(v.Name, oidIfEntry)
		if idx < 0 {
			return nil
		}
		f := get(idx)
		n := gosnmp.ToBigInt(v.Value).Uint64()
		switch col {
		case 2:
			if f.Name == "" {
				f.Name = str(v)
			}
		case 3:
			f.Type = int(n)
		case 5:
			if f.Speed == 0 {
				f.Speed = n
			}
		case 7:
			f.Admin = int(n)
		case 8:
			f.Oper = int(n)
		case 10:
			if f.InOct == 0 {
				f.InOct = n
			}
		case 13:
			f.InDisc = n
		case 14:
			f.InErr = n
		case 16:
			if f.OutOct == 0 {
				f.OutOct = n
			}
		case 19:
			f.OutDisc = n
		case 20:
			f.OutErr = n
		}
		return nil
	})
	if d.Version != "1" {
		_ = walk(oidIfXEntry, func(v gosnmp.SnmpPDU) error {
			col, idx := colIdx(v.Name, oidIfXEntry)
			if idx < 0 {
				return nil
			}
			f := get(idx)
			n := gosnmp.ToBigInt(v.Value).Uint64()
			switch col {
			case 1:
				if s := str(v); s != "" {
					f.Name = s
				}
			case 6:
				f.InOct = n // 64-bitarsräknare går före 32-bitars
			case 10:
				f.OutOct = n
			case 15:
				if n > 0 {
					f.Speed = n * 1_000_000
				}
			case 18:
				f.Alias = str(v)
			}
			return nil
		})
	}
	for _, f := range ifs {
		if f.Admin == 0 && f.Oper == 0 && f.Name == "" {
			continue
		}
		p.Ifs = append(p.Ifs, *f)
	}
	sort.Slice(p.Ifs, func(i, j int) bool { return p.Ifs[i].Index < p.Ifs[j].Index })
	// CPU: snitt av alla processorer (HOST-RESOURCES-MIB)
	var sum float64
	n := 0
	_ = walk(oidHrProc, func(v gosnmp.SnmpPDU) error {
		sum += float64(gosnmp.ToBigInt(v.Value).Int64())
		n++
		return nil
	})
	if n > 0 {
		p.CPU = sum / float64(n)
	}
	// minne: hrStorageRam
	type st struct {
		typ               string
		units, size, used int64
	}
	sts := map[int]*st{}
	_ = walk(oidHrStorage, func(v gosnmp.SnmpPDU) error {
		col, idx := colIdx(v.Name, oidHrStorage)
		if idx < 0 || col < 2 || col > 6 {
			return nil
		}
		s := sts[idx]
		if s == nil {
			s = &st{}
			sts[idx] = s
		}
		switch col {
		case 2:
			if o, ok := v.Value.(string); ok {
				s.typ = o
			}
		case 4:
			s.units = gosnmp.ToBigInt(v.Value).Int64()
		case 5:
			s.size = gosnmp.ToBigInt(v.Value).Int64()
		case 6:
			s.used = gosnmp.ToBigInt(v.Value).Int64()
		}
		return nil
	})
	for _, s := range sts {
		if strings.TrimPrefix(s.typ, ".") == strings.TrimPrefix(hrStorageRAM, ".") && s.size > 0 {
			p.Mem = float64(s.used) * 100 / float64(s.size)
		}
	}
	return p
}

// colIdx tolkar "<bas>.<kolumn>.<index>".
func colIdx(name, base string) (int, int) {
	rest := strings.TrimPrefix(strings.TrimPrefix(name, "."), strings.TrimPrefix(base, ".")+".")
	if rest == name {
		return -1, -1
	}
	var col, idx int
	if _, err := fmt.Sscanf(rest, "%d.%d", &col, &idx); err != nil {
		return -1, -1
	}
	return col, idx
}

func str(v gosnmp.SnmpPDU) string {
	switch x := v.Value.(type) {
	case []byte:
		return strings.TrimRight(string(x), "\x00")
	case string:
		return x
	}
	return fmt.Sprint(v.Value)
}

func shortErr(err error) string {
	s := err.Error()
	if strings.Contains(s, "timeout") || strings.Contains(s, "request timeout") {
		return "no SNMP response (check address, community/user and that SNMP is turned on)"
	}
	return s
}

// Up anger om porten är uppe.
func (f If) Up() bool { return f.Oper == 1 }

// Label är portens namn med ev. beskrivning.
func (f If) Label() string {
	if f.Alias != "" && f.Alias != f.Name {
		return f.Name + " (" + f.Alias + ")"
	}
	return f.Name
}
