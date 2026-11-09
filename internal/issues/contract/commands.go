// Package contract contains issue commands without authentication or database
// dependencies. This avoids auth.Validator's existing dependency on IssueStore.
package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
)

type Error struct {
	Code            string `json:"code"`
	Message         string `json:"message"`
	CurrentRevision string `json:"current_revision,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

var failureMessages = map[string]string{
	"INVALID_ARGUMENT":           "Invalid issue input; correct it before retrying.",
	"PAYLOAD_TOO_LARGE":          "Issue input exceeds the admitted bounds; do not truncate it.",
	"UNAUTHENTICATED":            "Authentication is required.",
	"FORBIDDEN":                  "Issue mutation is not permitted.",
	"ISSUE_NOT_FOUND":            "Issue or canonical project is unavailable in the current read scope.",
	"ACTOR_CHANGED":              "The saved actor cannot be compared with the current actor; preserve the original intent and reconcile explicitly.",
	"IDEMPOTENCY_CONFLICT":       "The key is bound to a different original intent; reconcile without creating a replacement key.",
	"REVISION_CONFLICT":          "Read the current revision and reconcile the retained draft explicitly.",
	"REVISION_REQUIRED":          "Read the issue and supply its expected_revision before a material mutation.",
	"INVALID_TRANSITION":         "Read the current issue and choose a permitted lifecycle transition.",
	"ISSUE_GONE":                 "The original issue was deleted; the retained key cannot create another issue.",
	"PRIVACY_REJECTED":           "Existing privacy protection refused this write; no input was stored or rewritten.",
	"UNAVAILABLE":                "Issue persistence is unavailable; reconcile the original intent before retrying.",
	"DETAIL_PAGING_REQUIRED":     "Use detail_mode=paged with independent comments_offset/comments_limit and history_offset/history_limit; no partial detail was returned.",
	"TRANSPORT_RECORD_TOO_LARGE": "The intact record exceeds this transport; use authorized REST detail_mode=paged with separate comments/history pages.",
}

func Failure(code string) *Error {
	return &Error{Code: code, Message: failureMessages[code]}
}

func SafeError(err error) error {
	var safe *Error
	if errors.As(err, &safe) {
		return safe
	}
	return Failure("UNAVAILABLE")
}

type ActorDescriptor struct {
	Kind      string  `json:"kind"`
	Reference *string `json:"reference"`
}

// Actor is supplied only by the application after existing authentication. It
// must not be implemented by a transport from submitted labels or expectations.
type Actor interface {
	Descriptor() ActorDescriptor
	Namespace() string
	KeycardID() string
	IsOperator() bool
	CanWrite() bool
}

type actorIdentityKey struct{}

// ActorIdentityKey is also auth.IdentityKey. The leaf contract owns only the
// key/projection interface so IssueStore can sample the existing trusted auth
// value without an auth -> GORM -> auth import cycle or another actor policy.
var ActorIdentityKey = actorIdentityKey{}

func CaptureActor(ctx context.Context) (Actor, error) {
	if ctx == nil {
		return nil, Failure("UNAUTHENTICATED")
	}
	identity, ok := ctx.Value(ActorIdentityKey).(interface{ IssueActor() (Actor, error) })
	if !ok {
		return nil, Failure("UNAUTHENTICATED")
	}
	return identity.IssueActor()
}

type Scope struct {
	SourceProject string
	ReadProject   string
}

type issueTransportSessionKey struct{}

// TransportSession is provenance supplied by the transport, not an identity.
func WithTransportSession(ctx context.Context, session string) context.Context {
	return context.WithValue(ctx, issueTransportSessionKey{}, session)
}

func TransportSession(ctx context.Context) string {
	value, _ := ctx.Value(issueTransportSessionKey{}).(string)
	return value
}

type CreateCommand struct {
	Title            string           `json:"title"`
	Body             string           `json:"body"`
	Priority         string           `json:"priority"`
	Type             string           `json:"type"`
	TargetProject    string           `json:"target_project"`
	TargetSession    string           `json:"target_session"`
	SourceProject    string           `json:"source_project"`
	SourceAgent      string           `json:"source_agent"`
	CreatedBySession string           `json:"created_by_session"`
	SessionID        string           `json:"session_id"`
	Client           string           `json:"client"`
	TransportSession string           `json:"-"`
	Labels           []string         `json:"labels"`
	Metadata         json.RawMessage  `json:"metadata"`
	OwnerLabel       *string          `json:"owner_label"`
	RecipientLabel   *string          `json:"recipient_label"`
	RequestKey       *string          `json:"request_key"`
	ExpectedActor    *ActorDescriptor `json:"expected_actor"`
}

type MutationCommand struct {
	ExpectedRevision *string         `json:"expected_revision"`
	Title            *string         `json:"title"`
	Body             *string         `json:"body"`
	Priority         *string         `json:"priority"`
	Type             *string         `json:"type"`
	Status           *string         `json:"status"`
	Labels           *[]string       `json:"labels"`
	Metadata         json.RawMessage `json:"metadata"`
	OwnerLabel       json.RawMessage `json:"owner_label"`
	RecipientLabel   json.RawMessage `json:"recipient_label"`
	TargetSession    json.RawMessage `json:"target_session"`
	Comment          *string         `json:"comment"`
	CommentMetadata  json.RawMessage `json:"comment_metadata"`
	SourceProject    string          `json:"source_project"`
	SourceAgent      string          `json:"source_agent"`
	CreatedBySession string          `json:"created_by_session"`
	SessionID        string          `json:"session_id"`
	Client           string          `json:"client"`
	TransportSession string          `json:"-"`
}

func (m MutationCommand) Material() bool {
	return m.Title != nil || m.Body != nil || m.Priority != nil || m.Type != nil || m.Status != nil || m.Labels != nil || m.Metadata != nil || m.OwnerLabel != nil || m.RecipientLabel != nil || m.TargetSession != nil
}

func (m MutationCommand) SourceAuthority() bool {
	return m.Title != nil || m.Body != nil || m.Priority != nil || m.Type != nil || m.Labels != nil || m.Metadata != nil || m.OwnerLabel != nil || m.RecipientLabel != nil || m.TargetSession != nil
}

type Result struct {
	ID        int64  `json:"id"`
	Revision  string `json:"revision"`
	Replayed  bool   `json:"replayed"`
	Changed   bool   `json:"changed"`
	CommentID *int64 `json:"comment_id"`
	Message   string `json:"message"`
}

type Persistence interface {
	AdmitTaskScope(context.Context, Actor, Scope) error
	CreateTaskIssue(context.Context, Actor, Scope, CreateCommand) (Result, error)
	MutateTaskIssue(context.Context, Actor, Scope, int64, MutationCommand) (Result, error)
}

func ParseRevision(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 || strconv.FormatInt(parsed, 10) != value {
		return 0, Failure("INVALID_ARGUMENT")
	}
	return parsed, nil
}

func NormalizeLabels(labels []string) []string {
	result := append([]string{}, labels...)
	sort.Strings(result)
	n := 0
	for _, label := range result {
		if n == 0 || result[n-1] != label {
			result[n] = label
			n++
		}
	}
	return result[:n]
}

func numericJSON(raw []byte) (any, error) {
	var value any
	// Commands were admitted before this persistence contract. float64 yields
	// shortest deterministic JSON for all admitted equivalent numeric forms.
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return normalizeZero(value), nil
}

// CanonicalCreateV1 is solely the transient private receipt-binding input. Never
// log, return or retain it as a plaintext snapshot. Attribution/guard are excluded.
func CanonicalCreateV1(command CreateCommand) ([]byte, error) {
	metadata, err := numericJSON(command.Metadata)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version   int      `json:"version"`
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		Priority  string   `json:"priority"`
		Type      string   `json:"type"`
		Target    string   `json:"target_project"`
		Labels    []string `json:"labels"`
		Metadata  any      `json:"metadata"`
		Owner     *string  `json:"owner_label"`
		Recipient *string  `json:"recipient_label"`
	}{1, command.Title, command.Body, command.Priority, command.Type, command.TargetProject, NormalizeLabels(command.Labels), metadata, command.OwnerLabel, command.RecipientLabel})
}

// CanonicalCreateBinding retains byte-exact v1 for old empty-target receipts.
// Nonempty target sessions use a distinct v2 binding, never rewriting receipts.
func CanonicalCreateBinding(command CreateCommand) (int, []byte, error) {
	base, err := CanonicalCreateV1(command)
	if err != nil || command.TargetSession == "" {
		return 1, base, err
	}
	encoded, err := json.Marshal(struct {
		Version       int             `json:"version"`
		Payload       json.RawMessage `json:"payload"`
		TargetSession string          `json:"target_session"`
	}{2, base, command.TargetSession})
	return 2, encoded, err
}

// EqualMetadata uses exact received numbers for old stored data, not float64:
// legacy values outside the new admission domain must not become false no-ops.
func EqualMetadata(left, right json.RawMessage) bool {
	decode := func(raw []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		return value, err
	}
	a, err := decode(left)
	if err != nil {
		return false
	}
	b, err := decode(right)
	return err == nil && equalJSONValue(a, b)
}
