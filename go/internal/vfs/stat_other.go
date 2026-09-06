//go:build !darwin

package vfs

import (
	"syscall"
	"time"
)

// rawTimes reads the three stat timestamps. Linux names the fields
// Atim/Mtim/Ctim; see stat_darwin.go for Darwin.
func rawTimes(raw *syscall.Stat_t) (access, modify, change time.Time) {
	return time.Unix(raw.Atim.Unix()),
		time.Unix(raw.Mtim.Unix()),
		time.Unix(raw.Ctim.Unix())
}
