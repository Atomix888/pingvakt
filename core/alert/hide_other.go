//go:build !windows

package alert

import "os/exec"

func hideConsole(*exec.Cmd) {}
