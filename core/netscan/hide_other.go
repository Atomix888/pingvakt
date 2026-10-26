//go:build !windows

package netscan

import "os/exec"

func hideWindow(*exec.Cmd) {}
