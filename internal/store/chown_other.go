//go:build !unix

package store

import "os"

func chownLike(string, os.FileInfo) {}
