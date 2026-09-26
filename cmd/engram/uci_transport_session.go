package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"github.com/thebtf/engram/internal/auditcontext"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"github.com/thebtf/mcp-mux/muxcore/engine"
	muxregistry "github.com/thebtf/mcp-mux/muxcore/registry"
)

const (
	muxcoreDaemonFlag                    = "--muxcore-daemon"
	muxcoreEmbeddedVersion               = "v0.29.1"
	muxcoreNamespaceBase                 = "engram"
	uciTransportSessionBytes             = 32
	uciTransportSessionUnavailableReason = "uci_transport_session_unavailable"
)

var muxcoreNamespace = muxcoreNamespaceBase

func muxcoreInstallationNamespace(clientInstanceID string) string {
	digest := sha256.Sum256([]byte(clientInstanceID))
	return muxcoreNamespaceBase + "-" + hex.EncodeToString(digest[:16])
}

func muxcoreBaseConfig() engine.Config {
	return engine.Config{
		Name:         "engram",
		Namespace:    muxcoreNamespace,
		DaemonFlag:   muxcoreDaemonFlag,
		SkipSnapshot: true,
		Registry: &muxregistry.Config{
			ProductName:    "engram",
			MuxcoreVersion: muxcoreEmbeddedVersion,
			Capabilities:   muxregistry.Capabilities{ListOwners: true},
		},
	}
}

type uciTransportSessionGenerator func() (string, error)

func newUCITransportSession() (string, error) {
	var bytes [uciTransportSessionBytes]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}

// muxcoreDaemonConfig preserves the daemon configuration API while admitting
// each transport under a fresh opaque correlation tag.
func muxcoreDaemonConfig(handler muxcore.SessionHandler) engine.Config {
	return muxcoreDaemonConfigWithSessionGenerator(handler, newUCITransportSession)
}

// muxcoreDaemonConfigWithSessionGenerator makes entropy failure testable
// without mutable production globals. The tag is correlation-only and is
// carried in muxcore's per-connection TenantID field.
func muxcoreDaemonConfigWithSessionGenerator(handler muxcore.SessionHandler, generate uciTransportSessionGenerator) engine.Config {
	cfg := muxcoreBaseConfig()
	cfg.Persistent = true // daemon owns durable module/background state
	cfg.SessionHandler = handler
	cfg.AuthorizeSession = func(context.Context, muxcore.ConnInfo, muxcore.ProjectContext) muxcore.SessionAuth {
		if generate == nil {
			return muxcore.SessionAuth{Decision: muxcore.AuthDeny, Reason: uciTransportSessionUnavailableReason}
		}
		tag, err := generate()
		if err != nil || !auditcontext.ValidUCITransportSession(tag) {
			return muxcore.SessionAuth{Decision: muxcore.AuthDeny, Reason: uciTransportSessionUnavailableReason}
		}
		return muxcore.SessionAuth{Decision: muxcore.AuthAllow, TenantID: tag}
	}
	return cfg
}
