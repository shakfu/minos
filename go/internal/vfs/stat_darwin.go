package vfs

import (
	"syscall"
	"time"
)

// rawTimes reads the three stat timestamps. Darwin and the BSDs name the
// fields Atimespec/Mtimespec/Ctimespec; see stat_other.go for the rest.
func rawTimes(raw *syscall.Stat_t) (access, modify, change time.Time) {
	return time.Unix(raw.Atimespec.Unix()),
		time.Unix(raw.Mtimespec.Unix()),
		time.Unix(raw.Ctimespec.Unix())
}
