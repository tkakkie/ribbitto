//go:build darwin

package main

import "golang.org/x/sys/unix"

func containerCTime(st unix.Stat_t) [2]int64 {
	// x/sys/unix normalizes Darwin's ctimespec field to Ctim.
	return [2]int64{st.Ctim.Sec, st.Ctim.Nsec}
}
