package ipc

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID asks the kernel which user is on the other end (LOCAL_PEERCRED).
func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid := -1
	var credErr error
	err = raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if credErr == nil {
			uid = int(cred.Uid)
		}
	})
	if err != nil {
		return -1, err
	}
	return uid, credErr
}
