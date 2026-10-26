//go:build !windows

package wifi

import "os/exec"

func hideWindow(*exec.Cmd) {}
