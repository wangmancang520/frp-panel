//go:build !unix

package frp

import "os"

var sigterm = os.Kill
