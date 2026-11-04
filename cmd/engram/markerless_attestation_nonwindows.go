//go:build !windows

package main

import (
	"context"
	"errors"
)

const markerlessRecoveryAvailable = false

func reconcileMarkerlessDaemon(context.Context, muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (bool, bool, error) {
	return false, false, errors.New("markerless recovery is unsupported on this platform")
}

func admitMarkerlessDaemon(context.Context, muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) error {
	return errors.New("markerless recovery is unsupported on this platform")
}
