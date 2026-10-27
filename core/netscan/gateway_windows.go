package netscan

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func sysGateways() (map[string]string, []string) {
	gw := map[string]string{}
	var dns []string
	size := uint32(15000)
	var buf []byte
	for i := 0; i < 3; i++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_INET, windows.GAA_FLAG_INCLUDE_GATEWAYS|windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST, 0,
			(*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return gw, nil
		}
	}
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		if a.OperStatus != windows.IfOperStatusUp {
			continue
		}
		name := windows.UTF16PtrToString(a.FriendlyName)
		if g := a.FirstGatewayAddress; g != nil {
			if ip := g.Address.IP(); ip != nil && ip.To4() != nil {
				gw[name] = ip.String()
				for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
					if ip := d.Address.IP(); ip != nil {
						dns = append(dns, ip.String())
					}
				}
			}
		}
	}
	return gw, dns
}
