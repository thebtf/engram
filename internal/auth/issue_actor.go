package auth

import (
	"strconv"

	"github.com/thebtf/engram/internal/issues/contract"
)

// issueActor is the existing immutable issue/auth-me projection. It contains no
// credentials and never derives authority from submitted attribution or labels.
type issueActor struct {
	kind      string
	reference string
	keycard   string
	operator  bool
	writable  bool
}

func (a issueActor) Descriptor() contract.ActorDescriptor {
	if a.reference == "" {
		return contract.ActorDescriptor{Kind: "unknown"}
	}
	reference := a.reference
	return contract.ActorDescriptor{Kind: a.kind, Reference: &reference}
}
func (a issueActor) Namespace() string { return a.reference }
func (a issueActor) KeycardID() string { return a.keycard }
func (a issueActor) IsOperator() bool  { return a.operator }
func (a issueActor) CanWrite() bool    { return a.writable && a.reference != "" }

// IssueActor projects this already authenticated value for the shared issue
// owner. Keeping the mapping here lets every writer reuse it through the leaf
// contract without importing the database-dependent auth package from GORM.
func (identity Identity) IssueActor() (contract.Actor, error) {
	a := issueActor{kind: "operator", operator: identity.IsAdmin() && identity.Source != SourceClient}
	switch identity.Source {
	case SourceClient:
		if identity.KeycardID == "" {
			return nil, contract.Failure("UNAUTHENTICATED")
		}
		a.kind, a.reference, a.keycard = "client", "client:"+identity.KeycardID, identity.KeycardID
		a.writable = identity.Role == RoleReadWrite
	case SourceMaster:
		a.reference = "operator:master"
	case SourceAuthDisabled:
		a.reference = "operator:auth-disabled"
	case SourceSession:
		if subject, valid := identity.SessionBrowserSubject(); valid {
			a.reference = "browser:" + strconv.FormatInt(subject.UserID, 10)
		} else if identity.BrowserSubject != (BrowserSubject{}) {
			return nil, contract.Failure("UNAUTHENTICATED")
		} else {
			a.reference = "operator:session"
		}
	default:
		return nil, contract.Failure("UNAUTHENTICATED")
	}
	if identity.Source != SourceClient {
		a.writable = a.operator
	}
	return a, nil
}
