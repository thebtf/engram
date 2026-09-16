package legacyrelay

// NewOSProcessInspector returns the platform implementation used to bind a PID
// to its kernel-observed incarnation, executable image, and direct parent.
// Unsupported platforms fail closed from Inspect.
func NewOSProcessInspector() ProcessInspector { return osProcessInspector{} }

// NewOSPeerResolver returns the platform implementation used to read the
// connecting process PID from a Unix-domain socket or Windows named pipe.
// Unsupported transports and platforms fail closed.
func NewOSPeerResolver() PeerResolver { return osPeerResolver{} }

type (
	osProcessInspector struct{}
	osPeerResolver     struct{}
)
