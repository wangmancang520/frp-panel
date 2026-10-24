//go:build unix

package frp

import "syscall"

var sigterm = syscall.SIGTERM
