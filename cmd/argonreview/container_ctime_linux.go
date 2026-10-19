//go:build linux

package main

import "golang.org/x/sys/unix"

func containerCTime(st unix.Stat_t) [2]int64 {
	return [2]int64{st.Ctim.Sec, st.Ctim.Nsec}
}
