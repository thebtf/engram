package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

var errCodeGrantCallerDenied = errors.New("code grant caller denied")

type codeGrantStore interface {
	Issue(context.Context, gormdb.BrowserReadGrantIssue) (gormdb.BrowserReadGrant, error)
	Revoke(context.Context, int64, string, string) (gormdb.BrowserReadGrant, error)
	CanRead(context.Context, int64, string, string) (bool, error)
	Active(context.Context, int64, string, string) (gormdb.BrowserReadGrant, bool, error)
	Current(context.Context, int64) (gormdb.BrowserReadGrant, bool, error)
}

// CodeGrantApplication is the narrow authenticated boundary for browser code-read grants.
type CodeGrantApplication struct {
	grants codeGrantStore
}

// NewCodeGrantApplication creates the grant-lifecycle application.
func NewCodeGrantApplication(grants *gormdb.BrowserReadGrantStore) *CodeGrantApplication {
	return &CodeGrantApplication{grants: grants}
}

// IssueCodeGrantInput contains the exact target tuple; it deliberately carries no role,
// Space, path, source-label, or legacy-project fallback fields.
type IssueCodeGrantInput struct {
	Target     auth.BrowserSubject
	SourceID   string
	CheckoutID string
	ExpiresAt  *time.Time
}

// Issue creates or restores one exact browser grant when the real browser issuer is
// the persisted source owner. The store verifies the target user and commits its audit.
func (a *CodeGrantApplication) Issue(ctx context.Context, issuer auth.Identity, in IssueCodeGrantInput) (gormdb.BrowserReadGrant, error) {
	issuerSubject, ok := issuer.SessionBrowserSubject()
	if !ok || !in.Target.Valid() {
		return gormdb.BrowserReadGrant{}, errCodeGrantCallerDenied
	}
	if err := a.requireStore(); err != nil {
		return gormdb.BrowserReadGrant{}, err
	}
	return a.grants.Issue(ctx, gormdb.BrowserReadGrantIssue{
		IssuerUserID:    issuerSubject.UserID,
		IssuerPrincipal: issuerSubject.Principal,
		TargetUserID:    in.Target.UserID,
		SourceID:        in.SourceID,
		CheckoutID:      in.CheckoutID,
		ExpiresAt:       in.ExpiresAt,
	})
}

// Revoke revokes a grant only after the store rechecks the real browser issuer against
// the persisted source-owner principal of that grant's exact tuple.
func (a *CodeGrantApplication) Revoke(ctx context.Context, issuer auth.Identity, grantRef string) (gormdb.BrowserReadGrant, error) {
	issuerSubject, ok := issuer.SessionBrowserSubject()
	if !ok {
		return gormdb.BrowserReadGrant{}, errCodeGrantCallerDenied
	}
	if err := a.requireStore(); err != nil {
		return gormdb.BrowserReadGrant{}, err
	}
	return a.grants.Revoke(ctx, issuerSubject.UserID, issuerSubject.Principal, grantRef)
}

// CanRead returns true only for a current real browser subject with an exact active grant.
func (a *CodeGrantApplication) CanRead(ctx context.Context, caller auth.Identity, sourceID, checkoutID string) (bool, error) {
	subject, ok := caller.SessionBrowserSubject()
	if !ok {
		return false, nil
	}
	if err := a.requireStore(); err != nil {
		return false, err
	}
	return a.grants.CanRead(ctx, subject.UserID, sourceID, checkoutID)
}

// Active returns the exact current grant and its issuance epoch. It does not
// select among the caller's other grants.
func (a *CodeGrantApplication) Active(ctx context.Context, caller auth.Identity, sourceID, checkoutID string) (gormdb.BrowserReadGrant, bool, error) {
	subject, ok := caller.SessionBrowserSubject()
	if !ok {
		return gormdb.BrowserReadGrant{}, false, nil
	}
	if err := a.requireStore(); err != nil {
		return gormdb.BrowserReadGrant{}, false, err
	}
	return a.grants.Active(ctx, subject.UserID, sourceID, checkoutID)
}

// Current returns the subject's only current exact grant. Zero or multiple
// grants are deliberately unselected rather than client-resolved.
func (a *CodeGrantApplication) Current(ctx context.Context, caller auth.Identity) (gormdb.BrowserReadGrant, bool, error) {
	subject, ok := caller.SessionBrowserSubject()
	if !ok {
		return gormdb.BrowserReadGrant{}, false, nil
	}
	if err := a.requireStore(); err != nil {
		return gormdb.BrowserReadGrant{}, false, err
	}
	return a.grants.Current(ctx, subject.UserID)
}

func (a *CodeGrantApplication) requireStore() error {
	if a == nil || a.grants == nil {
		return fmt.Errorf("code grant application is not configured")
	}
	return nil
}
