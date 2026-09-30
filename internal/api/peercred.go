package api

import (
	"log/slog"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// peerCheckListener drops connections from users other than the server's
// own user and root, in case the socket's file permissions are looser
// than intended (e.g. api_socket in a shared directory).
type peerCheckListener struct {
	net.Listener
	allow map[uint32]bool
	log   *slog.Logger
}

func allowedUIDs() map[uint32]bool {
	return map[uint32]bool{uint32(os.Getuid()): true, 0: true}
}

func (l *peerCheckListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uid, ok := peerUID(c)
		if ok && l.allow[uid] {
			return c, nil
		}
		l.log.Warn("api connection refused", "peer_uid", uid, "known", ok)
		c.Close()
	}
}

// peerUID returns the connecting process's user ID (SO_PEERCRED).
func peerUID(c net.Conn) (uint32, bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || cerr != nil {
		return 0, false
	}
	return cred.Uid, true
}
