//go:build !windows

package trace

import "os/exec"

func hideWindow(*exec.Cmd) {}
