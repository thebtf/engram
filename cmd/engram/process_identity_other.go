//go:build !windows && !linux && !darwin

package main

import (
	"errors"
	"net"
)

var errProcessIdentityUnsupported = errors.New("process identity lookup is unsupported on this platform")

func liveProcessImageIdentity(int) (*processImageIdentity, error) {
	return nil, errProcessIdentityUnsupported
}

func verifyLiveProcessImageBinding(int, *processImageIdentity) error {
	return errProcessIdentityUnsupported
}

func muxcoreControlPeerPID(net.Conn) (int, error) {
	return 0, errProcessIdentityUnsupported
}
