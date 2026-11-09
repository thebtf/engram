package gorm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	issues "github.com/thebtf/engram/internal/issues/contract"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// taskTransaction keeps SQL parameters (including keys, bindings and content)
// out of configured GORM loggers, including error/slow-query paths. Only stable
// application errors cross this boundary; callers cannot log a wrapped query.
func (s *IssueStore) taskTransaction(ctx context.Context, work func(*gormlib.DB) error) error {
	err := s.db.WithContext(ctx).Session(&gormlib.Session{Logger: logger.Discard}).Transaction(work)
	if err != nil {
		var pgErr *pgconn.PgError
		var pqErr *pq.Error
		var code string
		switch {
		case errors.As(err, &pgErr) && pgErr.Code == "P0001":
			code = pgErr.Message
		case errors.As(err, &pqErr) && pqErr.Code == "P0001":
			code = pqErr.Message
		}
		if code == "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP" || code == "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP" {
			return &issues.Error{Code: code, Message: "Storage refusal does not identify whether source_project or target_project failed; preserve identity and data, and never replay a mutation with an unknown outcome."}
		}
		return issues.SafeError(err)
	}
	return nil
}

// AdmitTaskScope gives ordinary authentication/source/read-scope failures
// precedence over expected_actor. Transactions repeat admission under locks.
func (s *IssueStore) AdmitTaskScope(ctx context.Context, actor issues.Actor, scope issues.Scope) error {
	if actor == nil || !actor.CanWrite() {
		return issues.Failure("FORBIDDEN")
	}
	return s.taskTransaction(ctx, func(tx *gormlib.DB) error { return admitTaskScope(ctx, tx, scope) })
}

func admitTaskProject(ctx context.Context, tx *gormlib.DB, project string) error {
	if !projectidentity.ValidProjectID(project) {
		return issues.Failure("INVALID_ARGUMENT")
	}
	// Lock before checking the existing active-source owner so a concurrent
	// removal cannot invalidate admission while a receipt is being disclosed.
	var row Project
	err := tx.Select("id").Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", project).Take(&row).Error
	if errors.Is(err, gormlib.ErrRecordNotFound) {
		return issues.Failure("ISSUE_NOT_FOUND")
	}
	if err != nil {
		return err
	}
	exists, err := (&SessionStore{db: tx}).CanonicalProjectExists(ctx, project)
	if err != nil {
		return err
	}
	if !exists {
		return issues.Failure("ISSUE_NOT_FOUND")
	}
	return nil
}

func admitTaskScope(ctx context.Context, tx *gormlib.DB, scope issues.Scope) error {
	projects := []string{scope.SourceProject}
	if scope.ReadProject != "" && scope.ReadProject != scope.SourceProject {
		projects = append(projects, scope.ReadProject)
	}
	sort.Strings(projects)
	for _, project := range projects {
		if err := admitTaskProject(ctx, tx, project); err != nil {
			return err
		}
	}
	return nil
}

func visibleTaskIssue(tx *gormlib.DB, id int64, project string) (*Issue, error) {
	query := tx.Model(&Issue{}).Where("issues.id = ?", id)
	if project != "" {
		predicate := canonicalProjectMembershipPredicate("issues", "source_project") + " OR " + canonicalProjectMembershipPredicate("issues", "target_project")
		query = query.Where("("+predicate+")", project, "issues", "issues", project, project, "issues", "issues", project)
	}
	var row Issue
	err := query.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row).Error
	if errors.Is(err, gormlib.ErrRecordNotFound) {
		return nil, issues.Failure("ISSUE_NOT_FOUND")
	}
	return &row, err
}

func taskActor(actor issues.Actor) IssueActor {
	descriptor := actor.Descriptor()
	return IssueActor{Kind: descriptor.Kind, Reference: descriptor.Reference}
}

func taskAttribution(source, agent, session string) IssueChangeAttribution {
	optional := func(value string) *string {
		if value == "" {
			return nil
		}
		return &value
	}
	return IssueChangeAttribution{SourceProject: optional(source), SourceAgent: optional(agent), Session: optional(session)}
}

func checkTaskActorExpectation(actor issues.Actor, command issues.CreateCommand) error {
	if command.ExpectedActor == nil {
		return nil
	}
	expected := command.ExpectedActor
	if expected.Kind != "client" && expected.Kind != "operator" && expected.Kind != "unknown" {
		return issues.Failure("INVALID_ARGUMENT")
	}
	if command.RequestKey == nil {
		return issues.Failure("INVALID_ARGUMENT")
	}
	actual := actor.Descriptor()
	if expected.Kind == "unknown" || expected.Reference == nil || *expected.Reference == "" || actual.Reference == nil || expected.Kind != actual.Kind || *expected.Reference != *actual.Reference {
		return issues.Failure("ACTOR_CHANGED")
	}
	return nil
}

// CreateTaskIssue is the persistence half of issues.Application, not a new legacy
// writer. Call the application with received raw JSON, never a pre-rounded map.
func (s *IssueStore) CreateTaskIssue(ctx context.Context, actor issues.Actor, scope issues.Scope, command issues.CreateCommand) (issues.Result, error) {
	var result issues.Result
	if !actor.CanWrite() {
		return result, issues.Failure("FORBIDDEN")
	}
	err := s.taskTransaction(ctx, func(tx *gormlib.DB) error {
		if err := admitTaskScope(ctx, tx, scope); err != nil {
			return err
		}
		if err := admitTaskProject(ctx, tx, command.TargetProject); err != nil {
			return err
		}
		if !ValidIssueSessionSelector(command.TargetSession) {
			return issues.Failure("INVALID_ARGUMENT")
		}
		// These are the ordinary reader's exact project roles, not an ACL.
		if scope.ReadProject != "" && scope.ReadProject != scope.SourceProject && scope.ReadProject != command.TargetProject {
			return issues.Failure("ISSUE_NOT_FOUND")
		}
		if err := checkTaskActorExpectation(actor, command); err != nil {
			return err
		}
		var receipt issueCreateReceipt
		if command.RequestKey != nil {
			// The unique constraint remains authoritative. The transaction mutex
			// avoids speculative losing issue inserts and serializes equal keys
			// through commit, including the no-receipt-yet case. JSON tuple
			// encoding is unambiguous for arbitrary opaque UTF-8 keys.
			namespace, err := json.Marshal([]string{actor.Namespace(), scope.SourceProject, *command.RequestKey})
			if err != nil {
				return err
			}
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 513))", string(namespace)).Error; err != nil {
				return err
			}
			err = tx.Where("actor_identity_key = ? AND canonical_source_project_id = ? AND request_key = ?", actor.Namespace(), scope.SourceProject, *command.RequestKey).Take(&receipt).Error
			if err != nil && !errors.Is(err, gormlib.ErrRecordNotFound) {
				return err
			}
			if err == nil {
				if receipt.IssueID == nil {
					// A tombstone has no retained target/body. Only project-free or
					// original-source scope can disclose the safe gone outcome.
					if scope.ReadProject != "" && scope.ReadProject != scope.SourceProject {
						return issues.Failure("ISSUE_NOT_FOUND")
					}
					return issues.Failure("ISSUE_GONE")
				}
				row, err := visibleTaskIssue(tx, *receipt.IssueID, scope.ReadProject)
				if err != nil {
					return err
				}
				version, canonical, err := issues.CanonicalCreateBinding(command)
				if err != nil {
					return err
				}
				binding := sha256.Sum256(canonical)
				if receipt.PayloadVersion != int16(version) || !bytes.Equal(receipt.PayloadHash, binding[:]) {
					return issues.Failure("IDEMPOTENCY_CONFLICT")
				}
				result = issues.Result{ID: row.ID, Revision: strconv.FormatInt(row.Revision, 10), Replayed: true, Message: "issue replayed"}
				return nil
			}
		}
		now := time.Now()
		reported := command.SessionID
		if reported == "" {
			reported = command.CreatedBySession
		}
		attribution := selectIssueAttribution(command.TransportSession, reported, command.Client)
		row := Issue{
			Title: command.Title, Body: command.Body, Status: "open", Priority: command.Priority, Type: command.Type,
			SourceProject: scope.SourceProject, TargetProject: command.TargetProject, TargetSession: command.TargetSession, SourceAgent: command.SourceAgent,
			CreatedBySession: attribution.Session, CreatedBySessionSource: attribution.Source, SourceClient: attribution.Client, CreatorKeycardID: actor.KeycardID(), Labels: models.JSONStringArray(command.Labels),
			Metadata: command.Metadata, Revision: 1, OwnerLabel: command.OwnerLabel, RecipientLabel: command.RecipientLabel,
			Actor: taskActor(actor), CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		change := IssueChange{
			IssueID: row.ID, Revision: 1, Action: "created", ChangedFields: models.JSONStringArray{}, Actor: taskActor(actor),
			Attribution: taskAttribution(scope.SourceProject, command.SourceAgent, attribution.Session), Changes: json.RawMessage(`{}`), CreatedAt: now,
		}
		if err := tx.Create(&change).Error; err != nil {
			return err
		}
		if command.RequestKey != nil {
			version, canonical, err := issues.CanonicalCreateBinding(command)
			if err != nil {
				return err
			}
			binding := sha256.Sum256(canonical)
			receipt = issueCreateReceipt{
				ActorIdentityKey: actor.Namespace(), CanonicalSourceProjectID: scope.SourceProject, RequestKey: *command.RequestKey,
				IssueID: &row.ID, PayloadVersion: int16(version), PayloadHash: binding[:], CreatedAt: now,
			}
			if err := tx.Create(&receipt).Error; err != nil {
				return err
			}
		}
		result = issues.Result{ID: row.ID, Revision: "1", Message: "issue created"}
		return nil
	})
	if err != nil {
		return issues.Result{}, err
	}
	return result, nil
}

func authorizeTaskMutation(row *Issue, actor issues.Actor, command issues.MutationCommand) error {
	if !actor.CanWrite() {
		return issues.Failure("FORBIDDEN")
	}
	if command.SourceAuthority() && !actor.IsOperator() && (row.CreatorKeycardID == "" || row.CreatorKeycardID != actor.KeycardID()) {
		return issues.Failure("FORBIDDEN")
	}
	if command.Status != nil && (*command.Status == "open" || *command.Status == "acknowledged" || *command.Status == "rejected") && !actor.IsOperator() {
		return issues.Failure("FORBIDDEN")
	}
	return nil
}

// MutateTaskIssue locks the authorized current row, then checks the supplied CAS
// before computing no-op/material effects. A comment is in the same transaction.
func (s *IssueStore) MutateTaskIssue(ctx context.Context, actor issues.Actor, scope issues.Scope, id int64, command issues.MutationCommand) (issues.Result, error) {
	var result issues.Result
	if !actor.CanWrite() {
		return result, issues.Failure("FORBIDDEN")
	}
	err := s.taskTransaction(ctx, func(tx *gormlib.DB) error {
		if scope.SourceProject != "" {
			if err := admitTaskScope(ctx, tx, scope); err != nil {
				return err
			}
		}
		row, err := visibleTaskIssue(tx, id, scope.ReadProject)
		if err != nil {
			return err
		}
		if err := authorizeTaskMutation(row, actor, command); err != nil {
			return err
		}
		if scope.SourceProject == "" {
			// Existing PATCH callers may omit attribution. Admit the current
			// canonical source after row/action authorization, without claiming
			// it was the caller's source or using it as an authorization grant.
			roles, err := NewIssueStore(tx).ProjectRoles(ctx, []int64{id})
			if err != nil {
				return err
			}
			if roles[id].SourceProjectID == nil {
				return issues.Failure("ISSUE_NOT_FOUND")
			}
			if err := admitTaskProject(ctx, tx, *roles[id].SourceProjectID); err != nil {
				return err
			}
		}
		result, err = mutateLockedTaskIssueTx(tx, row, actor, scope, command, time.Now())
		return err
	})
	if err != nil {
		return issues.Result{}, err
	}
	return result, nil
}

// mutateLockedTaskIssueTx is the one material/comment committer. Its caller
// owns the row lock, canonical admission and existing action authorization;
// legacy selection callers bridge a revision only after their original bound
// timestamp has matched this locked row, never by refreshing a stale target.
func mutateLockedTaskIssueTx(tx *gormlib.DB, row *Issue, actor issues.Actor, scope issues.Scope, command issues.MutationCommand, now time.Time) (issues.Result, error) {
	var result issues.Result
	id := row.ID
	reported := command.SessionID
	if reported == "" {
		reported = command.CreatedBySession
	}
	attribution := selectIssueAttribution(command.TransportSession, reported, command.Client)
	conditionalLifecycle := command.Status != nil && !command.SourceAuthority() && (*command.Status == "resolved" || *command.Status == "reopened" || *command.Status == "closed")
	if command.Material() && command.ExpectedRevision == nil && !conditionalLifecycle {
		return result, issues.Failure("REVISION_REQUIRED")
	}
	if command.ExpectedRevision != nil {
		expected, err := issues.ParseRevision(*command.ExpectedRevision)
		if err != nil {
			return result, err
		}
		if expected != row.Revision {
			conflict := issues.Failure("REVISION_CONFLICT")
			conflict.CurrentRevision = strconv.FormatInt(row.Revision, 10)
			return result, conflict
		}
	}
	updates := make(map[string]any)
	set := func(field string, current string, desired *string) {
		if desired != nil && *desired != current {
			updates[field] = *desired
		}
	}
	set("title", row.Title, command.Title)
	set("body", row.Body, command.Body)
	set("priority", row.Priority, command.Priority)
	set("type", row.Type, command.Type)
	if command.TargetSession != nil {
		var target string
		if err := json.Unmarshal(command.TargetSession, &target); err != nil || !ValidIssueSessionSelector(target) {
			return result, issues.Failure("INVALID_ARGUMENT")
		}
		set("target_session", row.TargetSession, &target)
	}
	if command.Labels != nil && !reflect.DeepEqual(issues.NormalizeLabels(row.Labels), *command.Labels) {
		labels, err := json.Marshal(*command.Labels)
		if err != nil {
			return result, err
		}
		updates["labels"] = string(labels)
	}
	if command.Metadata != nil && !issues.EqualMetadata(row.Metadata, command.Metadata) {
		updates["metadata"] = string(command.Metadata)
	}
	for _, label := range []struct {
		field string
		old   *string
		raw   json.RawMessage
	}{
		{"owner_label", row.OwnerLabel, command.OwnerLabel}, {"recipient_label", row.RecipientLabel, command.RecipientLabel},
	} {
		if label.raw != nil {
			var desired *string
			if err := json.Unmarshal(label.raw, &desired); err != nil {
				return result, issues.Failure("INVALID_ARGUMENT")
			}
			if !reflect.DeepEqual(label.old, desired) {
				updates[label.field] = desired
			}
		}
	}
	if command.Status != nil {
		status := *command.Status
		if status == "reopened" && row.Status != "resolved" && row.Status != "closed" {
			return result, issues.Failure("INVALID_TRANSITION")
		}
		if status == "closed" && (row.Status == "closed" || (!actor.IsOperator() && row.Status != "resolved" && row.Status != "reopened")) {
			return result, issues.Failure("INVALID_TRANSITION")
		}
		if status == "rejected" && (command.Comment == nil || *command.Comment == "") {
			return result, issues.Failure("INVALID_ARGUMENT")
		}
		if status != row.Status {
			updates["status"] = status
			switch status {
			case "resolved":
				updates["resolved_at"] = now
			case "acknowledged":
				updates["acknowledged_at"] = now
			case "reopened":
				updates["reopened_at"] = now
			case "closed", "rejected":
				updates["closed_at"] = now
			}
		}
	}
	changed := len(updates) != 0
	if changed {
		if row.Revision == math.MaxInt64 {
			return result, issues.Failure("INVALID_TRANSITION")
		}
		fields := make(models.JSONStringArray, 0, len(updates))
		for field := range updates {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		previous := row.Revision
		updates["revision"], updates["updated_at"] = previous+1, now
		if err := tx.Model(&Issue{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return result, err
		}
		change := IssueChange{
			IssueID: id, Revision: previous + 1, PreviousRevision: &previous, Action: "updated", ChangedFields: fields,
			Actor: taskActor(actor), Attribution: taskAttribution(scope.SourceProject, command.SourceAgent, attribution.Session), Changes: json.RawMessage(`{}`), CreatedAt: now,
		}
		if err := tx.Create(&change).Error; err != nil {
			return result, err
		}
		if _, statusChanged := updates["status"]; statusChanged {
			event := IssueHistoryEvent{IssueID: id, Status: *command.Status, AuthorProject: scope.SourceProject, AuthorAgent: command.SourceAgent, SessionID: attribution.Session, SessionSource: attribution.Source, Client: attribution.Client, CreatedAt: now}
			if err := tx.Create(&event).Error; err != nil {
				return result, err
			}
		}
		row.Revision++
	}
	var commentID *int64
	if command.Comment != nil {
		body := *command.Comment
		if command.Status != nil && *command.Status == "rejected" {
			body = "Rejected: " + body
		}
		comment := IssueComment{
			IssueID: id, AuthorProject: scope.SourceProject, AuthorAgent: command.SourceAgent, Body: body,
			AuthorSession: attribution.Session, AuthorSessionSource: attribution.Source, AuthorClient: attribution.Client,
			Metadata: command.CommentMetadata, Actor: taskActor(actor), CreatedAt: now,
		}
		if err := tx.Create(&comment).Error; err != nil {
			return result, err
		}
		commentID = &comment.ID
		if !changed {
			if err := tx.Model(&Issue{}).Where("id = ?", id).Update("updated_at", now).Error; err != nil {
				return result, err
			}
		}
	}
	return issues.Result{ID: id, Revision: strconv.FormatInt(row.Revision, 10), Changed: changed, CommentID: commentID, Message: "issue updated"}, nil
}

func issueAttributedMutation(attribution IssueChangeAttribution) (issues.Scope, issues.MutationCommand) {
	var scope issues.Scope
	var command issues.MutationCommand
	if attribution.SourceProject != nil {
		scope.SourceProject = *attribution.SourceProject
	}
	if attribution.SourceAgent != nil {
		command.SourceAgent = *attribution.SourceAgent
	}
	if attribution.Session != nil {
		command.CreatedBySession = *attribution.Session
	}
	return scope, command
}

func issueSelectionMutation(operation IssueSelectionOperation) (issues.Scope, issues.MutationCommand) {
	attribution := operation.Attribution
	if attribution.SourceProject == nil && operation.Action.AuthorProject != "" {
		attribution.SourceProject = &operation.Action.AuthorProject
	}
	if attribution.SourceAgent == nil && operation.Action.AuthorAgent != "" {
		attribution.SourceAgent = &operation.Action.AuthorAgent
	}
	scope, command := issueAttributedMutation(attribution)
	switch operation.Action.Kind {
	case IssueSelectionAcknowledge:
		status := "acknowledged"
		command.Status = &status
	case IssueSelectionPriority:
		command.Priority = operation.Action.Priority
	case IssueSelectionLabels:
		labels := issues.NormalizeLabels(*operation.Action.Labels)
		command.Labels = &labels
	case IssueSelectionStatus:
		command.Status = operation.Action.Status
		if operation.Action.Comment != nil && *operation.Action.Comment != "" {
			command.Comment = operation.Action.Comment
			command.CommentMetadata = json.RawMessage(`{}`)
		}
	}
	return scope, command
}

// Missing caller attribution is not synthesized from issue ownership. Admit
// only the existing canonical row sources while keeping history attribution
// unknown, just as the ordinary shared mutation owner does.
func admitTaskRowSources(ctx context.Context, tx *gormlib.DB, ids []int64) error {
	roles, err := NewIssueStore(tx).ProjectRoles(ctx, ids)
	if err != nil {
		return err
	}
	projects := make(map[string]struct{})
	for _, id := range ids {
		if roles[id].SourceProjectID == nil {
			return issues.Failure("ISSUE_NOT_FOUND")
		}
		projects[*roles[id].SourceProjectID] = struct{}{}
	}
	ordered := make([]string, 0, len(projects))
	for project := range projects {
		ordered = append(ordered, project)
	}
	sort.Strings(ordered)
	for _, project := range ordered {
		if err := admitTaskProject(ctx, tx, project); err != nil {
			return err
		}
	}
	return nil
}

// Preserve legacy body-free selection/HTTP failure categories, never SQL or
// content-bearing database errors from the shared transaction dependency.
func issueWriterError(err error) error {
	for _, sentinel := range []error{ErrIssueNotFound, ErrIssueForbidden, ErrIssueInvalidInput, ErrIssueInvalidTransition, ErrIssueSelectionConflict} {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	var failure *issues.Error
	if errors.As(err, &failure) {
		switch failure.Code {
		case "UNAUTHENTICATED", "FORBIDDEN":
			return ErrIssueForbidden
		case "ISSUE_NOT_FOUND":
			return ErrIssueNotFound
		case "INVALID_ARGUMENT", "PAYLOAD_TOO_LARGE", "PRIVACY_REJECTED":
			return ErrIssueInvalidInput
		case "INVALID_TRANSITION":
			return ErrIssueInvalidTransition
		case "REVISION_CONFLICT", "REVISION_REQUIRED":
			return ErrIssueSelectionConflict
		}
	}
	return issues.Failure("UNAVAILABLE")
}
