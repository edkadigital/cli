package main

import (
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

func openPTY() (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	fd := int(master.Fd())
	var n uint32
	err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0)
	if err == nil {
		n, err = unix.IoctlGetUint32(fd, unix.TIOCGPTN)
	}
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	tty, err := os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(n), 10), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, tty, nil
}
