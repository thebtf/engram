//go:build !windows && !linux && !darwin

package legacyrelay

import (
	"errors"
	"net"
)

func (osProcessInspector) Inspect(int) (ProcessIdentity, error) {
	return ProcessIdentity{}, errors.New("legacy relay process identity is unsupported on this platform")
}

func (osPeerResolver) PeerPID(net.Conn) (int, error) {
	return 0, errors.New("legacy relay peer identity is unsupported on this platform")
}
