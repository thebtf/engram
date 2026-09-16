package worker

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// rejectLegacyDirectDelivery applies the default-off legacy direct keycard
// requirement to the old context-inject and ambient HTTP entry points. The
// gRPC session-start path applies the same check at its server boundary.
func (s *Service) rejectLegacyDirectDelivery(w http.ResponseWriter, ctx context.Context, project string) bool {
	identity, hasIdentity := auth.IdentityFrom(ctx)
	_, isHAP := identity.HAPPrincipal()
	if !config.HAP01BLegacyDirectEnforcementEnabled() && !isHAP {
		return false
	}
	var canonicalProject string
	var err error
	if s != nil && s.legacyDirectProjectResolver != nil {
		canonicalProject, err = s.legacyDirectProjectResolver(ctx, project)
	} else if s != nil && s.store != nil && s.store.DB != nil {
		canonicalProject, err = gormdb.ResolveProjectIDStrict(ctx, s.store.DB, project)
	} else {
		err = fmt.Errorf("project resolver unavailable")
	}
	if err != nil {
		http.Error(w, "legacy direct project resolution required", http.StatusForbidden)
		return true
	}
	if !hasIdentity || !identity.IsHAPLegacyDirectFor(canonicalProject, time.Now().UTC()) {
		http.Error(w, "legacy direct keycard required", http.StatusForbidden)
		return true
	}
	return false
}

func legacyDirectIdentityMatches(ctx context.Context, canonicalProject string, now time.Time) bool {
	identity, ok := auth.IdentityFrom(ctx)
	return ok && identity.IsHAPLegacyDirectFor(canonicalProject, now)
}

func hapHTTPRouteAllowed(identity auth.Identity, method, path string) bool {
	principal, isHAP := identity.HAPPrincipal()
	if !isHAP {
		return true
	}
	if principal.Class != auth.HAPPrincipalLegacyDirect {
		return false
	}
	switch path {
	case "/api/context/inject":
		return method == http.MethodPost
	case "/api/context/session-start":
		return method == http.MethodGet || method == http.MethodPost
	case "/api/hooks/ambient-candidates":
		return method == http.MethodPost
	default:
		return false
	}
}
