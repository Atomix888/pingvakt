//go:build !linux && !darwin && !windows

package netscan

func sysGateways() (map[string]string, []string) {
	return map[string]string{}, resolvConf("/etc/resolv.conf")
}
