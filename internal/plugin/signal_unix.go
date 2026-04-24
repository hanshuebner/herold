//go:build unix

package plugin

import "syscall"

var sigterm = syscall.SIGTERM
