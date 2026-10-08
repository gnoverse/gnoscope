package main

import "syscall"

// genesisMinFreeBytes is the headroom the genesis sheet import asks for: the
// ~160MB table, a 57MB download, and room for the database to keep growing.
const genesisMinFreeBytes = 1 << 30

func freeDiskBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
