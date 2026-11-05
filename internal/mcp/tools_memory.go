package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	gormlib "gorm.io/gorm"

	"github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/lifecycle"
	"github.com/thebtf/engram/internal/memorysearch"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/redaction"
	"github.com/thebtf/engram/internal/retrieval"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/internal/staleness"
	"github.com/thebtf/engram/internal/writegate"
	"github.com/thebtf/engram/internal/writelint"
	"github.com/thebtf/engram/pkg/models"
)

// isValidPrivacyScope validates the 4-tier privacy_scope enum added by
// migration 125 (T001) and consumed by scope.Resolve (T003/T003b). Mirrors
// the memories_privacy_scope_chk CHECK constraint exactly.
func isValidPrivacyScope(s string) bool {
	switch s {
	case "private", "project", "shared", "global":
		return true
	default:
		return false
	}
}

// derivePrivacyScopeFromLegacy maps the legacy 2-tier scope tag value to the
// 4-tier privacy_scope enum for the dual-field deprecation window (RI-F2).
// Empty input returns empty (caller decides default handling).
func derivePrivacyScopeFromLegacy(legacy string) string {
	switch legacy {
	case "project":
		return "project"
	case "global":
		return "global"
	default:
		return ""
	}
}

// deriveLegacyScopeFromPrivacy is the inverse of derivePrivacyScopeFromLegacy:
// when a caller supplies only the new 4-tier `privacy_scope` field, the legacy
// 2-tier `scope` value used for downstream tagging + responses must be
// back-derived so legacy consumers parsing `scope:project` / `scope:global`
// (or the legacy `scope` JSON field) see a value consistent with the 4-tier
// intent. Mapping per ADR-F-005 / RI-F2:
//
//	private -> project (legacy 2-tier has no `private`; collapse to the
//	                    more conservative `project`)
//	project -> project
//	shared  -> global  (legacy 2-tier collapses `shared` into `global`)
//	global  -> global
//
// Codex P2 cycle-7 fix on 209a06e: without this mapping, callers using only
// the new field would see legacy `scope:project` tags / responses even when
// they wrote `privacy_scope="shared"`, under-sharing data for legacy 2-tier
// consumers.
func deriveLegacyScopeFromPrivacy(privacy string) string {
	switch privacy {
	case "private", "project":
		return "project"
	case "shared", "global":
		return "global"
	default:
		return ""
	}
}

func applyPrincipalMemoryMetadata(ctx context.Context, mem *models.Memory, agentVisibility, domain string) error {
	visibility := strings.TrimSpace(agentVisibility)
	if visibility != "" && !models.IsValidAgentVisibility(visibility) {
		return fmt.Errorf("invalid_agent_visibility: %q must be one of private, shared", visibility)
	}

	normalizedDomain := strings.TrimSpace(domain)
	var ownerPrincipal, ownerPrincipalKind string
	if id, ok := auth.IdentityFrom(ctx); ok {
		if principal, principalKind, hasOwner := id.MemoryOwner(); hasOwner {
			ownerPrincipal = principal
			ownerPrincipalKind = principalKind
		}
	}
	caller := scope.KeycardContext{
		Principal:     ownerPrincipal,
		PrincipalKind: ownerPrincipalKind,
	}
	decision := scope.DomainOwnershipPolicy{}.Decide(caller, scope.DomainPolicyRequest{
		Operation:          scope.DomainOperationWrite,
		Domain:             normalizedDomain,
		OwnerPrincipal:     ownerPrincipal,
		OwnerPrincipalKind: ownerPrincipalKind,
	})
	if !decision.Allowed {
		return fmt.Errorf("invalid_domain: %s", decision.Reason)
	}

	mem.Domain = normalizedDomain
	mem.OwnerPrincipal = ownerPrincipal
	mem.OwnerPrincipalKind = ownerPrincipalKind
	if visibility != "" {
		if mem.OwnerPrincipal == "" {
			return fmt.Errorf("invalid_agent_visibility: both private and shared require an authenticated principal. For ordinary project/workstation visibility without a principal, omit agent_visibility and use privacy_scope. Ownership is server-derived; principal, owner_principal, and session_id arguments cannot supply it. Principal-private visibility requires authenticated ownership")
		}
		mem.AgentVisibility = visibility
	} else if mem.OwnerPrincipal != "" {
		mem.AgentVisibility = models.AgentVisibilityShared
	}
	return nil
}

func addPrincipalMemoryFields(result map[string]any, mem *models.Memory) {
	if mem.OwnerPrincipal != "" {
		result["owner_principal"] = mem.OwnerPrincipal
	}
	if mem.OwnerPrincipalKind != "" {
		result["owner_principal_kind"] = mem.OwnerPrincipalKind
	}
	if mem.AgentVisibility != "" {
		result["agent_visibility"] = mem.AgentVisibility
	}
	if mem.Domain != "" {
		result["domain"] = mem.Domain
	}
}

type writeLintOffsetLister interface {
	ListWithOffset(ctx context.Context, project string, limit int, offset int) ([]*models.Memory, error)
}

type scopedWriteLintMemoryStore struct {
	base                 writelint.MemoryStoreInterface
	pager                writeLintOffsetLister
	caller               scope.KeycardContext
	opts                 scope.MemoryVisibilityOptions
	access               gormdb.MemoryMutationAccess
	mutationErr          error
	tx                   *gormlib.DB
	target               *models.Memory
	beforeTargetMutation func(*gormlib.DB) error
}

func newScopedWriteLintMemoryStore(base writelint.MemoryStoreInterface, caller scope.KeycardContext, opts scope.MemoryVisibilityOptions) writelint.MemoryStoreInterface {
	if base == nil {
		return nil
	}
	scoped := &scopedWriteLintMemoryStore{base: base, caller: caller, opts: opts, access: gormdb.MemoryMutationAccess{Caller: caller}}
	if pager, ok := base.(writeLintOffsetLister); ok {
		scoped.pager = pager
	}
	return scoped
}

func writeLintVisibilityCaller(ctx context.Context, sessionID string) scope.KeycardContext {
	caller := scope.KeycardContext{SessionID: strings.TrimSpace(sessionID)}
	if id, ok := auth.IdentityFrom(ctx); ok {
		caller.WorkstationID = id.WorkstationID()
		caller.Principal = id.Principal
		if _, principalKind, hasOwner := id.MemoryOwner(); hasOwner {
			caller.PrincipalKind = principalKind
		} else {
			caller.PrincipalKind = string(id.PrincipalKind)
		}
	}
	return caller
}

func writeLintVisibilityOptions() scope.MemoryVisibilityOptions {
	return scope.MemoryVisibilityOptions{
		ApplyPrivacyScope: true,
	}
}

func (s *Server) scopedWriteLintMemoryStore(ctx context.Context, sessionID, project string) writelint.MemoryStoreInterface {
	var base writelint.MemoryStoreInterface
	if s.memoryStore != nil {
		base = s.memoryStore
	} else if s.writeLint != nil {
		base = s.writeLint.MemoryStore()
	}
	access, mutationErr := memoryMutationAccessMCP(ctx, sessionID)
	if access.Project == "" {
		access.Project = project
	}
	store := newScopedWriteLintMemoryStore(base, access.Caller, writeLintVisibilityOptions())
	if scoped, ok := store.(*scopedWriteLintMemoryStore); ok {
		scoped.access = access
		scoped.mutationErr = mutationErr
	}
	return store
}

func filterVisibleWriteGateCandidates(ctx context.Context, sessionID string, existing []*models.Memory) []*models.Memory {
	caller := writeLintVisibilityCaller(ctx, sessionID)
	opts := writeLintVisibilityOptions()
	visible := make([]*models.Memory, 0, len(existing))
	for _, mem := range existing {
		if scope.ResolveMemory(caller, mem, opts) {
			visible = append(visible, mem)
		}
	}
	return visible
}

func memoryManageAllowed(ctx context.Context, mem *models.Memory) bool {
	if mem == nil {
		return false
	}
	if config.Get().EnforceSourceProject {
		project := ProjectFromContext(ctx)
		if project == "" || mem.Project != project {
			return false
		}
	}
	caller := writeLintVisibilityCaller(ctx, sessionFromContext(ctx))
	return scope.ResolveMemory(caller, mem, writeLintVisibilityOptions()) && scope.ResolveMemoryManage(caller, mem)
}

func memoryMutationAccessMCP(ctx context.Context, sessionID string) (gormdb.MemoryMutationAccess, error) {
	access := gormdb.MemoryMutationAccess{Caller: writeLintVisibilityCaller(ctx, sessionID)}
	if config.Get().EnforceSourceProject {
		access.Project = ProjectFromContext(ctx)
		if access.Project == "" {
			return access, gormlib.ErrRecordNotFound
		}
		access.RequireCanonicalProject = true
	}
	return access, nil
}

func (s *scopedWriteLintMemoryStore) List(ctx context.Context, project string, limit int) ([]*models.Memory, error) {
	if limit <= 0 {
		limit = 200
	}
	visible := make([]*models.Memory, 0, limit)
	if s.pager == nil {
		raw, err := s.base.List(ctx, project, writeLintVisibilityFetchLimit(limit))
		if err != nil {
			return nil, err
		}
		for _, mem := range raw {
			if scope.ResolveMemory(s.caller, mem, s.opts) {
				visible = append(visible, mem)
				if len(visible) == limit {
					break
				}
			}
		}
		return visible, nil
	}

	const batchSize = 500
	budget := writeLintVisibilityFetchLimit(limit)
	for offset := 0; len(visible) < limit && offset < budget; {
		currentLimit := batchSize
		if remaining := budget - offset; remaining < currentLimit {
			currentLimit = remaining
		}
		if currentLimit <= 0 {
			break
		}
		batch, err := gormdb.ListMemoryPageForSearch(ctx, s.pager, project, currentLimit, offset, scope.MemoryReadAccess{Caller: s.caller, Visibility: s.opts})
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, mem := range batch {
			if scope.ResolveMemory(s.caller, mem, s.opts) {
				visible = append(visible, mem)
				if len(visible) == limit {
					break
				}
			}
		}
		offset += len(batch)
		if len(batch) < currentLimit {
			break
		}
	}
	return visible, nil
}

func writeLintVisibilityFetchLimit(limit int) int {
	if limit <= 0 {
		limit = 200
	}
	budget := limit * 10
	if budget < 500 {
		budget = 500
	}
	if budget > 5000 {
		budget = 5000
	}
	return budget
}

func (s *scopedWriteLintMemoryStore) Get(ctx context.Context, id int64) (*models.Memory, error) {
	if s.mutationErr != nil {
		return nil, nil
	}
	if s.tx != nil {
		if s.target == nil || s.target.ID != id {
			return nil, gormlib.ErrRecordNotFound
		}
		memory := *s.target
		return &memory, nil
	}
	mem, err := s.base.Get(ctx, id)
	if err != nil || mem == nil {
		return mem, err
	}
	if (s.access.Project != "" && mem.Project != s.access.Project) || !scope.ResolveMemory(s.caller, mem, s.opts) {
		return nil, nil
	}
	return mem, nil
}

func (s *scopedWriteLintMemoryStore) Create(ctx context.Context, m *models.Memory) (*models.Memory, error) {
	if store, ok := s.base.(*gormdb.MemoryStore); ok && s.tx != nil {
		return store.CreateTx(ctx, s.tx, m)
	}
	return s.base.Create(ctx, m)
}

func (s *scopedWriteLintMemoryStore) Update(ctx context.Context, m *models.Memory) (*models.Memory, error) {
	if m == nil {
		return nil, fmt.Errorf("memory must not be nil")
	}
	if s.mutationErr != nil {
		return nil, s.mutationErr
	}
	access := s.access
	if store, ok := s.base.(*gormdb.MemoryStore); ok {
		if s.tx != nil {
			if s.target == nil || s.target.ID != m.ID {
				return nil, gormlib.ErrRecordNotFound
			}
			return store.Update(ctx, m)
		}
		if access.Project == "" {
			access.Project = m.Project
		}
		_, after, err := store.UpdateAuthorized(ctx, m.ID, access, func(current *models.Memory) error {
			current.Content, current.Tags = m.Content, m.Tags
			current.SourceAgent, current.EditedBy = m.SourceAgent, m.EditedBy
			return nil
		})
		return after, err
	}
	before, err := s.Get(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	if !scope.ResolveMemory(s.caller, before, writeLintVisibilityOptions()) || !scope.ResolveMemoryManage(s.caller, before) {
		return nil, gormlib.ErrRecordNotFound
	}
	return s.base.Update(ctx, m)
}

func (s *scopedWriteLintMemoryStore) MarkSuperseded(ctx context.Context, olderID, newID int64) error {
	if s.mutationErr != nil {
		return s.mutationErr
	}
	access := s.access
	if store, ok := s.base.(*gormdb.MemoryStore); ok {
		if s.tx != nil {
			if s.target == nil || s.target.ID != olderID {
				return gormlib.ErrRecordNotFound
			}
			return store.MarkSupersededTx(ctx, s.tx, olderID, newID)
		}
		return store.WithAuthorizedMemories(ctx, []int64{olderID}, access, func(tx *gormlib.DB, _ map[int64]*models.Memory) error {
			return store.MarkSupersededTx(ctx, tx, olderID, newID)
		})
	}
	before, err := s.Get(ctx, olderID)
	if err != nil {
		return err
	}
	if !scope.ResolveMemory(s.caller, before, writeLintVisibilityOptions()) || !scope.ResolveMemoryManage(s.caller, before) {
		return gormlib.ErrRecordNotFound
	}
	return s.base.MarkSuperseded(ctx, olderID, newID)
}

func (s *scopedWriteLintMemoryStore) WithTargetMutation(ctx context.Context, id int64, project string, mutate func(writelint.MemoryStoreInterface, writelint.AuditLoggerInterface) error) error {
	if s.mutationErr != nil {
		return s.mutationErr
	}
	access := s.access
	if access.Project != "" && access.Project != project {
		return gormlib.ErrRecordNotFound
	}
	access.Project = project
	store, ok := s.base.(*gormdb.MemoryStore)
	if !ok {
		// Non-database stores are injected test seams, not the shipped path.
		if s.beforeTargetMutation != nil {
			if err := s.beforeTargetMutation(nil); err != nil {
				return err
			}
		}
		return mutate(s, nil)
	}
	var domainErr error
	err := store.WithAuthorizedMemories(ctx, []int64{id}, access, func(tx *gormlib.DB, memories map[int64]*models.Memory) error {
		if s.beforeTargetMutation != nil {
			domainErr = s.beforeTargetMutation(tx)
			if errors.Is(domainErr, principalmemory.ErrDomainWriteRejected) {
				return nil // Commit only the established rejection audit.
			}
			if domainErr != nil {
				return domainErr
			}
		}
		bound := *s
		bound.base = gormdb.NewMemoryStore(&gormdb.Store{DB: tx})
		bound.tx, bound.target = tx, memories[id]
		return mutate(&bound, gormdb.NewAuditStore(tx))
	})
	if err != nil {
		return err
	}
	return domainErr
}

// memoryEditor is the minimal interface over *gorm.MemoryStore that
// handleEditMemory uses. Kept narrow so tests can inject a mock without
// wiring a full database (mirrors the auditWriter test-injection pattern).
type memoryEditor interface {
	Get(ctx context.Context, id int64) (*models.Memory, error)
	Update(ctx context.Context, m *models.Memory) (*models.Memory, error)
}

// effectiveMemoryEditor returns the active memoryEditor for s.
// Precedence: testMemoryEditor (set in tests) → concrete memoryStore.
func (s *Server) effectiveMemoryEditor() memoryEditor {
	if s.testMemoryEditor != nil {
		return s.testMemoryEditor
	}
	if s.memoryStore != nil {
		return s.memoryStore
	}
	return nil
}

func (s *Server) updateMemoryAsCaller(ctx context.Context, id int64, update gormdb.MemoryUpdate) (before, after *models.Memory, err error) {
	access, err := memoryMutationAccessMCP(ctx, sessionFromContext(ctx))
	if err != nil {
		return nil, nil, err
	}
	if s.testMemoryEditor == nil {
		return s.memoryStore.UpdateAuthorized(ctx, id, access, update)
	}
	// The nontransactional editor exists only for injected unit-test stores.
	before, err = s.testMemoryEditor.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if !memoryManageAllowed(ctx, before) {
		return nil, nil, gormlib.ErrRecordNotFound
	}
	updated := *before
	if err := update(&updated); err != nil {
		return nil, nil, err
	}
	after, err = s.testMemoryEditor.Update(ctx, &updated)
	return before, after, err
}

func isValidStoreObservationType(obsType models.ObservationType) bool {
	switch obsType {
	case models.ObsTypeDecision,
		models.ObsTypeBugfix,
		models.ObsTypeFeature,
		models.ObsTypeRefactor,
		models.ObsTypeDiscovery,
		models.ObsTypeChange,
		models.ObsTypeGuidance,
		models.ObsTypeCredential,
		models.ObsTypeEntity,
		models.ObsTypeWiki,
		models.ObsTypePitfall,
		models.ObsTypeOperational,
		models.ObsTypeTimeline:
		return true
	default:
		return false
	}
}

// handleStoreMemory explicitly stores a memory in the v5 memories table.
// T044 (FR-F6.b): dry_run=true returns a legacy-path preview JSON without any DB writes.
// The nil-store guard is deferred until after dry_run is parsed so that dry_run=true
// always succeeds (TG5-absent nil-safe seam).
func (s *Server) handleStoreMemory(ctx context.Context, args json.RawMessage) (string, error) {
	// T035: write-lint orchestrator has its own MemoryStoreInterface; nil memoryStore
	// is only fatal on the legacy create path. writeLintEnabled and dry_run are both
	// exemptions; the nil-store guard is deferred until after params are parsed so both
	// exemptions can be evaluated together.
	writeLintEnabled := s.writeLint != nil

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	for _, key := range []string{"content", "title", "type", "scope", "privacy_scope", "session_id", "agent_visibility", "domain", "project", "agent_source", "resolution_token", "option"} {
		if _, _, fieldErr := optionalStringArg(m, key); fieldErr != nil {
			return "", fmt.Errorf("store_memory: %w", fieldErr)
		}
	}
	inputTags, _, err := optionalStringSliceArg(m, "tags")
	if err != nil {
		return "", fmt.Errorf("store_memory: %w", err)
	}
	inputRejected, _, err := optionalStringSliceArg(m, "rejected")
	if err != nil {
		return "", fmt.Errorf("store_memory: %w", err)
	}
	supersedes, supersedesPresent, err := optionalInt64SliceArg(m, "supersedes")
	if err != nil {
		return "", fmt.Errorf("store_memory: %w", err)
	}
	if supersedesPresent {
		for i, id := range supersedes {
			if id <= 0 {
				return "", fmt.Errorf("store_memory: supersedes[%d] must be > 0", i)
			}
		}
	}
	alwaysInject, _, err := optionalBoolArg(m, "always_inject")
	if err != nil {
		return "", fmt.Errorf("store_memory: %w", err)
	}
	dryRun, _, err := optionalBoolArg(m, "dry_run")
	if err != nil {
		return "", fmt.Errorf("store_memory: %w", err)
	}
	if _, _, err = optionalBoolArg(m, "force"); err != nil {
		return "", fmt.Errorf("store_memory: %w", err)
	}
	importance, importancePresent, err := optionalFloat64Arg(m, "importance")
	if err != nil {
		return "", fmt.Errorf("store_memory: %w", err)
	}
	inputTTLDays, ttlPresent, err := optionalInt64Arg(m, "ttl_days")
	if err != nil {
		return "", fmt.Errorf("store_memory: %w", err)
	}
	if ttlPresent && (inputTTLDays <= 0 || inputTTLDays > int64(math.MaxInt)) {
		return "", fmt.Errorf("store_memory: ttl_days must be an in-range positive integer")
	}
	targetMemoryID, targetPresent, err := optionalInt64Arg(m, "target_memory_id")
	if err != nil {
		return "", fmt.Errorf("store_memory: %w", err)
	}
	if targetPresent && targetMemoryID <= 0 {
		return "", fmt.Errorf("store_memory: target_memory_id must be > 0")
	}

	var params struct {
		Tags            []string
		Rejected        []string
		Supersedes      []int64
		Content         string
		Title           string
		Type            string
		Scope           string
		PrivacyScope    string // T004 — vNext F, 4-tier enum
		SessionID       string // T004 — vNext F, caller's session for SourceSessions
		AgentVisibility string
		Domain          string
		Project         string
		AgentSource     string
		Importance      *float64
		TtlDays         *int
		AlwaysInject    bool
		DryRun          bool // T044 — FR-F6.b dry-run preview
	}
	params.Tags = inputTags
	params.Rejected = inputRejected
	params.Supersedes = supersedes
	params.Content = coerceString(m["content"], "")
	params.Title = coerceString(m["title"], "")
	params.Type = coerceString(m["type"], "")
	params.Scope = coerceString(m["scope"], "")
	params.PrivacyScope = coerceString(m["privacy_scope"], "")
	params.SessionID = coerceString(m["session_id"], "")
	params.AgentVisibility = coerceString(m["agent_visibility"], "")
	params.Domain = coerceString(m["domain"], "")
	params.AgentSource = coerceString(m["agent_source"], "")
	if config.Get().EnforceSourceProject {
		params.Project = ProjectFromContext(ctx)
		if params.Project == "" {
			params.Project = coerceString(m["project"], "")
		}
	} else {
		params.Project = coerceString(m["project"], "")
		if params.Project == "" {
			defaultScope := params.Scope
			if params.PrivacyScope != "" {
				defaultScope = deriveLegacyScopeFromPrivacy(params.PrivacyScope)
			}
			if !alwaysInject || defaultScope != string(models.ScopeGlobal) {
				params.Project = ProjectFromContext(ctx)
			}
		}
	}
	params.AlwaysInject = alwaysInject
	params.DryRun = dryRun
	if importancePresent {
		params.Importance = &importance
	}
	if ttlPresent {
		d := int(inputTTLDays)
		params.TtlDays = &d
	}
	if params.Content == "" {
		return "", fmt.Errorf("content is required for store")
	}
	if params.Importance != nil && (*params.Importance < 0 || *params.Importance > 1) {
		return "", fmt.Errorf("importance must be between 0 and 1")
	}

	// Deferred nil-store guard: runs after params are parsed so all exemptions are known.
	// Exemptions: writeLintEnabled (T035 — orchestrator manages its own store) and
	// dry_run=true (T044 — preview exits before any DB access; no store required).
	if s.memoryStore == nil && !writeLintEnabled && !params.DryRun {
		return "", fmt.Errorf("memory store not available")
	}

	cfg := config.Get()
	hardLimit := cfg.StoreMemoryHardLimit
	if hardLimit <= 0 {
		hardLimit = 10000
	}
	softLimit := cfg.StoreMemorySoftLimit
	if softLimit <= 0 {
		softLimit = 1000
	}
	if utf8.RuneCountInString(params.Content) > hardLimit {
		return "", fmt.Errorf("content exceeds maximum length of %d characters", hardLimit)
	}
	if utf8.RuneCountInString(params.Content) > softLimit {
		params.Content = string([]rune(params.Content)[:softLimit])
		log.Debug().
			Int("soft_limit", softLimit).
			Msg("store: content truncated to soft limit")
	}

	if privacy.ContainsSecrets(params.Content) {
		log.Warn().Msg("store: content contains secrets — redacting before storage")
		params.Content = privacy.RedactSecrets(params.Content)
	}

	// ADR-F-004: operator redaction layer — runs AFTER privacy.RedactSecrets,
	// BEFORE write-lint gate. Rules are pre-compiled at startup from
	// ENGRAM_REDACTION_RULES_PATH (EC-F9: no hot-reload; restart required).
	// ScrubCompiled is the hot path: no per-call regexp.Compile (finding 7 fix).
	if len(s.redactionRules) > 0 {
		scrubbed, _, scrubErr := redaction.ScrubCompiled(params.Content, s.redactionRules)
		if scrubErr != nil {
			if errors.Is(scrubErr, redaction.ErrContentFullyRedacted) {
				// EC-F5: all content was removed by operator redaction rules.
				return "", fmt.Errorf("content_fully_redacted: all content was removed by operator redaction rules")
			}
			return "", fmt.Errorf("redaction: %w", scrubErr)
		}
		params.Content = scrubbed
	}

	principalPreview := &models.Memory{}
	if err := applyPrincipalMemoryMetadata(ctx, principalPreview, params.AgentVisibility, params.Domain); err != nil {
		return "", err
	}

	// T044 dry-run return (FR-F6.b): after content size limits AND redaction so
	// would_store reflects the post-redacted content (honest preview per ADR-F-004).
	//
	// Composition with TG5 write-lint (ADR: dry_run×write-lint design):
	//   - write-lint active (writeLintEnabled=true): Phase1 would run signal detection
	//     (duplicate / conflict / supersession) before committing. dry_run does NOT
	//     invoke Phase1 because that would either commit on the no-signal path or mint
	//     a resolution token that expires unused — both are write side-effects
	//     incompatible with preview semantics. Instead, the preview declares that
	//     lint signals are "deferred" — the caller knows lint would intercept but
	//     must use a live (non-dry-run) call to see actual signals.
	//   - write-lint absent (writeLintEnabled=false / flag OFF): legacy-path preview
	//     with no lint signals (original TG6 T044 behavior).
	//
	// No DB write, no token mint, no snapshot row for dry_run in either case.
	if params.DryRun {
		dryRunNote := "dry_run preview — no memory row written; redaction applied above"
		if writeLintEnabled {
			dryRunNote = "dry_run preview — no memory row written; redaction applied; write-lint Phase1 (duplicate/conflict detection) would run on live call — signals deferred"
		}
		preview := map[string]any{
			"dry_run":     true,
			"would_store": params.Content,
			"project":     params.Project,
			"type":        params.Type,
			"scope":       params.Scope,
			"tags":        params.Tags,
			"write_lint":  writeLintEnabled,
			"note":        dryRunNote,
		}
		addPrincipalMemoryFields(preview, principalPreview)
		out, jsonErr := json.MarshalIndent(preview, "", "  ")
		if jsonErr != nil {
			return "", fmt.Errorf("store dry_run marshal: %w", jsonErr)
		}
		return string(out), nil
	}

	// T035 (engram vNext Milestone F TG5) — two-phase write-lint protocol.
	//
	// Gate conditions:
	//   1. writeLint orchestrator is wired (non-nil).
	//   2. ENGRAM_VNEXT_F_ENABLED=true.
	//   3. force=true → bypass write-lint, proceed to legacy create path + audit.
	//   4. resolution_token non-empty → Phase2 delegation; return result directly.
	//   5. Otherwise → Phase1; if not stored, return Phase1 JSON; if stored, format as success.
	if s.writeLint != nil {
		// finding 12 fix: declare write-lint locals inside the gate block so they
		// are not allocated on the common non-writelint path.
		wlForce := coerceBool(m["force"], false)
		wlResolutionToken := coerceString(m["resolution_token"], "")
		wlOption := coerceString(m["option"], "")
		// round-3 finding 1 fix: always_inject=true routes to behavioralRulesStore
		// (legacy path below). Write-lint must not intercept it — Phase1 would run
		// against an empty-project memory list (global scope → Project=="") and fail,
		// and even for project-scoped rules Phase1 would store a plain memory instead
		// of a behavioral_rule. Treat always_inject the same as force: bypass write-lint
		// and fall through to the legacy path which handles behavioralRulesStore routing.
		wlAlwaysInject := params.AlwaysInject
		if !wlForce && !wlAlwaysInject {
			wlActor := actorFromContext(ctx)
			// Use the already-normalized params.Project (which respects
			// EnforceSourceProject) instead of reading raw m["project"]. This
			// prevents a client from bypassing source-project isolation by
			// supplying a different project in the MCP args.
			wlProject := params.Project

			// Build a memory model carrying available metadata so Phase1/Phase2
			// persist the full record (Tags, PrivacyScope, AgentSource, etc.) on
			// the no-signal path and on Phase2 create paths. SourceWorkstationID
			// and the fully-resolved privacy/scope values are not yet computed here
			// (they require the legacy normalization below), so we populate what we
			// have. The vnextF guard ensures PrivacyScope is relevant only when ON.
			wlMem := &models.Memory{
				Content:      params.Content,
				Project:      wlProject,
				Tags:         params.Tags,
				PrivacyScope: params.PrivacyScope,
				SourceAgent:  params.AgentSource,
			}
			if params.SessionID != "" {
				wlMem.SourceSessions = []string{params.SessionID}
			}
			if id, ok := auth.IdentityFrom(ctx); ok {
				wlMem.SourceWorkstationID = id.WorkstationID()
			}
			if err := applyPrincipalMemoryMetadata(ctx, wlMem, params.AgentVisibility, params.Domain); err != nil {
				return "", err
			}

			// round-4 finding 2 fix: reject private-scope writes that lack a
			// workstation identity before reaching Phase1/Phase2. The legacy
			// path performs this check at line 597 (Codex P1 cycle-4 fix).
			// Without mirroring it here, a no-signal write-lint path (Phase1
			// returns stored=true) or a Phase2 create path (ignore_signals /
			// supersede) persists a private memory with empty
			// source_workstation_id. scope.Resolve fail-closes such rows
			// (internal/scope/filter.go:85-87), making them permanently
			// unreadable — including to the writer itself.
			if wlMem.PrivacyScope == "private" && wlMem.SourceWorkstationID == "" {
				return "", fmt.Errorf("invalid_privacy_scope: private requires a non-empty workstation identity from a SourceClient keycard (master/session sources cannot write private-scope memories)")
			}
			var domainDecision *principalmemory.DomainWriteDecision

			scopedWLStore := s.scopedWriteLintMemoryStore(ctx, params.SessionID, params.Project)
			if scopedWLStore == nil {
				return "", fmt.Errorf("write_lint memory store not available")
			}
			if wlResolutionToken != "" && (wlOption == "supersede" || wlOption == "merge_with") {
				scoped := scopedWLStore.(*scopedWriteLintMemoryStore)
				scoped.beforeTargetMutation = func(tx *gormlib.DB) error {
					var err error
					domainDecision, err = s.checkDomainWriteMCPTx(ctx, wlMem, params.SessionID, tx)
					if err != nil {
						return fmt.Errorf("domain registry check failed: %w", err)
					}
					return nil
				}
			} else {
				domainDecision, err = s.checkDomainWriteMCP(ctx, wlMem, params.SessionID)
				if err != nil {
					return "", fmt.Errorf("domain registry check failed: %w", err)
				}
			}

			if wlResolutionToken != "" {
				// Phase2: caller is committing with a previously minted token.
				p2req := writelint.Phase2Request{
					Token:          wlResolutionToken,
					Option:         wlOption,
					Content:        params.Content,
					Project:        wlProject,
					Actor:          wlActor,
					TargetMemoryID: nil,
					Mem:            wlMem,
				}
				if targetPresent {
					tid := targetMemoryID
					p2req.TargetMemoryID = &tid
				}
				p2resp, p2err := s.writeLint.Phase2WithMemoryStore(ctx, p2req, scopedWLStore)
				if p2err != nil {
					return "", fmt.Errorf("write_lint_phase2: %w", p2err)
				}
				// Phase2 also COMMITS the content (ignore_signals / supersede /
				// merge_with / link_contradiction), so the rank-3 staleness advisory must
				// fire here too — otherwise relative-time content that required conflict
				// resolution commits without the nudge (Codex review). p2resp is a typed
				// struct; round-trip it to a map to attach the advisory key when relevant.
				if terms := staleness.DetectRelativeTime(params.Content); len(terms) > 0 || (domainDecision != nil && domainDecision.Warning != nil) {
					out, marshalErr := marshalStoreMemoryAugmented(p2resp, domainDecision, terms)
					if marshalErr != nil {
						return "", fmt.Errorf("write_lint_phase2: marshal: %w", marshalErr)
					}
					return out, nil
				}
				out, marshalErr := json.MarshalIndent(p2resp, "", "  ")
				if marshalErr != nil {
					return "", fmt.Errorf("write_lint_phase2: marshal: %w", marshalErr)
				}
				return string(out), nil
			}

			// Phase1: inspect for duplicates/conflicts/supersessions.
			p1resp, p1err := s.writeLint.Phase1WithMemoryStore(ctx, wlMem, wlActor, scopedWLStore)
			if p1err != nil {
				return "", fmt.Errorf("write_lint_phase1: %w", p1err)
			}
			if !p1resp.Stored {
				// Signals detected — return Phase1 response to caller for resolution.
				out, marshalErr := json.MarshalIndent(p1resp, "", "  ")
				if marshalErr != nil {
					return "", fmt.Errorf("write_lint_phase1: marshal: %w", marshalErr)
				}
				return string(out), nil
			}
			// finding 6 fix: no-signal stored=true carries the same fields as the
			// consolidated store response (NFR-F1): id, storage, scope, privacy_scope,
			// quality_signals. Phase1 returns MemoryID when Stored=true.
			wlResult := map[string]any{
				"stored":          true,
				"id":              p1resp.MemoryID,
				"storage":         "memories",
				"scope":           params.Scope,
				"privacy_scope":   params.PrivacyScope,
				"quality_signals": []any{},
				"message":         "Memory stored successfully via write-lint (no conflicts detected)",
			}
			addPrincipalMemoryFields(wlResult, wlMem)
			addDomainWriteDecisionFields(wlResult, domainDecision)
			// Rank-3 staleness advisory must also fire on the write-lint success path —
			// this is the primary store path when ENGRAM_VNEXT_F_ENABLED=true, the same
			// config that activates the serve-time hint, so the advisory cannot be
			// legacy-path-only (Codex review). Keyed on params.Content (post-redaction).
			if terms := staleness.DetectRelativeTime(params.Content); len(terms) > 0 {
				wlResult["staleness_advisory"] = staleAdvisory(terms)
			}
			out, marshalErr := json.MarshalIndent(wlResult, "", "  ")
			if marshalErr != nil {
				return "", fmt.Errorf("write_lint_phase1: marshal: %w", marshalErr)
			}
			return string(out), nil
		}
		// wlForce=true: fall through to legacy create path; audit "legacy_force_write" below.
	}
	// force=true OR writeLint not wired: fall through to legacy create path.
	// Require a live memoryStore for the legacy create path.
	if s.memoryStore == nil {
		return "", fmt.Errorf("memory store not available")
	}

	resolvedScope := params.Scope
	if resolvedScope == "" {
		resolvedScope = string(models.ScopeProject)
	}
	if resolvedScope != string(models.ScopeProject) && resolvedScope != string(models.ScopeGlobal) {
		return "", fmt.Errorf("invalid scope %q: must be one of project, global", resolvedScope)
	}

	// T004 (engram vNext Milestone F TG1) — resolve the 4-tier privacy_scope.
	// Flag OFF: leave empty so the DB DEFAULT 'project' from migration 125
	// applies on the column. Flag ON: prefer explicit privacy_scope param;
	// fall back to deriving from the legacy 2-tier scope (RI-F2 bridge).
	// Validate against the migration 125 CHECK constraint enum.
	var resolvedPrivacyScope string
	{
		resolvedPrivacyScope = params.PrivacyScope
		if resolvedPrivacyScope == "" {
			resolvedPrivacyScope = derivePrivacyScopeFromLegacy(resolvedScope)
		}
		if resolvedPrivacyScope == "" {
			resolvedPrivacyScope = "project"
		}
		if !isValidPrivacyScope(resolvedPrivacyScope) {
			// Structured error per spec FR-F1 AMEND (T005): clients parse the
			// 'invalid_privacy_scope:' prefix as an error_code; the trailing
			// message names the offending value + accepted enum.
			return "", fmt.Errorf("invalid_privacy_scope: %q must be one of private, project, shared, global", resolvedPrivacyScope)
		}
		// Codex P2 cycle-7 fix on 209a06e + cycle-9 fix on 69398d9: under
		// flag ON, the 4-tier `privacy_scope` is the authoritative field;
		// the legacy 2-tier `scope` must be a derived view of it so the two
		// representations cannot disagree on a single write. Cycle-7
		// originally only back-derived when `params.Scope` was empty, but
		// that left conflicting explicit pairs intact (e.g.,
		// `scope="project"` + `privacy_scope="shared"` would store shared
		// visibility while emitting legacy `scope:project` — RI-F2 bridge
		// gap). Always recompute `resolvedScope` from the resolved privacy
		// tier when `params.PrivacyScope` was explicitly provided OR
		// `params.Scope` was omitted. Mapping per ADR-F-005:
		//   private/project -> project
		//   shared/global   -> global
		// If the caller provided ONLY legacy `scope` (no `privacy_scope`),
		// the early derivation at line 184 already produced a consistent
		// pair, so this block is a no-op in that case.
		if params.PrivacyScope != "" || params.Scope == "" {
			if derived := deriveLegacyScopeFromPrivacy(resolvedPrivacyScope); derived != "" {
				resolvedScope = derived
			}
		}
	}

	if params.Project == "" && !(params.AlwaysInject && resolvedScope == string(models.ScopeGlobal)) {
		return "", fmt.Errorf("project is required for store in v5 unless always_inject=true with scope=global")
	}

	obsTypeStr := params.Type
	if obsTypeStr == "" {
		cl := strings.ToLower(params.Content)
		switch {
		case strings.Contains(cl, "decided") || strings.Contains(cl, "decision") || strings.Contains(cl, "chose"):
			obsTypeStr = "decision"
		case strings.Contains(cl, "bug") || strings.Contains(cl, "fix") || strings.Contains(cl, "error"):
			obsTypeStr = "bugfix"
		case strings.Contains(cl, "pattern") || strings.Contains(cl, "practice") || strings.Contains(cl, "convention"):
			obsTypeStr = "discovery"
		case strings.Contains(cl, "refactor") || strings.Contains(cl, "rename") || strings.Contains(cl, "move"):
			obsTypeStr = "refactor"
		default:
			obsTypeStr = "feature"
		}
	}
	obsType := models.ObservationType(obsTypeStr)
	if !isValidStoreObservationType(obsType) {
		return "", fmt.Errorf("invalid type %q: must be one of decision, bugfix, feature, refactor, discovery, change, guidance, credential, entity, wiki, pitfall, operational, timeline", obsTypeStr)
	}

	seen := make(map[string]bool)
	tags := make([]string, 0, len(params.Tags)+3)
	for _, tag := range params.Tags {
		for _, part := range expandTagHierarchy(tag) {
			if !seen[part] {
				seen[part] = true
				tags = append(tags, part)
			}
		}
	}

	if !seen["type:"+obsTypeStr] {
		tags = append(tags, "type:"+obsTypeStr)
		seen["type:"+obsTypeStr] = true
	}
	if !seen["scope:"+resolvedScope] {
		tags = append(tags, "scope:"+resolvedScope)
		seen["scope:"+resolvedScope] = true
	}
	if params.TtlDays != nil && !seen[fmt.Sprintf("ttl:%d", *params.TtlDays)] {
		ttlTag := fmt.Sprintf("ttl:%d", *params.TtlDays)
		tags = append(tags, ttlTag)
		seen[ttlTag] = true
	}

	ttlDays := computeTTLDays(params.TtlDays, tags)
	ttlApplied := ttlDays > 0
	if ttlApplied {
		ttlTag := fmt.Sprintf("ttl:%d", ttlDays)
		if !seen[ttlTag] {
			tags = append(tags, ttlTag)
			seen[ttlTag] = true
		}
	}

	if params.AlwaysInject {
		if candidate, err := s.captureActiveRuleIntent(ctx, ruleIntentCapture{
			Content:     params.Content,
			Project:     params.Project,
			Scope:       resolvedScope,
			Audience:    "developer",
			SessionID:   params.SessionID,
			Actor:       params.AgentSource,
			SourceTool:  "store",
			EvidenceTag: "always_inject",
		}); err != nil {
			return "", fmt.Errorf("store rule candidate: %w", err)
		} else if candidate != nil {
			return marshalRuleCandidateIntentResponse(candidate, map[string]any{
				"title":         truncateTitle(candidate.ProposedContent, 80),
				"type":          string(models.ObsTypeGuidance),
				"scope":         resolvedScope,
				"always_inject": true,
			})
		}
		if s.behavioralRulesStore == nil {
			return "", fmt.Errorf("always_inject=true requires behavioral rules store")
		}
		var project *string
		if resolvedScope != string(models.ScopeGlobal) {
			p := params.Project
			project = &p
		}
		rule := &models.BehavioralRule{
			Project:  project,
			Content:  params.Content,
			Priority: 0,
		}
		created, err := s.behavioralRulesStore.Create(ctx, rule)
		if err != nil {
			return "", fmt.Errorf("store behavioral rule: %w", err)
		}

		result := map[string]any{
			"id":            created.ID,
			"title":         truncateTitle(created.Content, 80),
			"type":          string(models.ObsTypeGuidance),
			"scope":         resolvedScope,
			"storage":       "behavioral_rules",
			"always_inject": true,
			"message":       "Behavioral rule stored successfully",
		}
		out, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal result: %w", err)
		}
		return string(out), nil
	}

	agentSource := string(models.AgentUnknown)
	if params.AgentSource != "" {
		if models.IsValidAgentSource(params.AgentSource) {
			agentSource = params.AgentSource
		} else {
			return "", fmt.Errorf("invalid agent_source %q: must be one of claude-code, codex, gemini, other, unknown", params.AgentSource)
		}
	}

	memory := &models.Memory{
		Project:        params.Project,
		Content:        params.Content,
		Tags:           tags,
		SourceAgent:    agentSource,
		ImportanceBase: 0.5,
	}

	// T004 — vNext F TG1: populate the new lifecycle/identity fields when the
	// flag is ON. PrivacyScope falls through to DB DEFAULT 'project' when
	// flag is OFF (empty Go string lets the column default apply). Workstation
	// id is derived from the caller's keycard via auth.Identity.WorkstationID
	// added in T003b. SourceSessions is populated from the explicit session_id
	// param when supplied — the MCP layer has no implicit session-id ctx key
	// in v6.4.x, so callers (e.g., the engram client proxy) advertise their
	// session via the param. When absent, SourceSessions stays empty and
	// scope.Resolve falls back to the workstation-only-suffices branch per
	// spec FR-F1 AMEND 2026-05-25.
	{
		memory.PrivacyScope = resolvedPrivacyScope
		if id, ok := auth.IdentityFrom(ctx); ok {
			memory.SourceWorkstationID = id.WorkstationID()
		}
		if params.SessionID != "" {
			memory.SourceSessions = []string{params.SessionID}
		}
		// Codex P1 cycle-4 fix on 783c0be: reject private-scope writes when
		// the caller has no non-empty workstation identity. scope.Resolve
		// fail-closes private memories whose source_workstation_id is empty
		// (`internal/scope/filter.go:85-87` — "if memorySource.WorkstationID
		// == \"\" { return false }"), so persisting such a row would make
		// it permanently unreadable to every caller including the writer
		// itself. Master and bare-session sources cannot produce a
		// non-empty WorkstationID (auth/identity.go:111-116 — returns
		// KeycardID only when Source == SourceClient).
		if resolvedPrivacyScope == "private" && memory.SourceWorkstationID == "" {
			return "", fmt.Errorf("invalid_privacy_scope: private requires a non-empty workstation identity from a SourceClient keycard (master/session sources cannot write private-scope memories)")
		}
	}
	if err := applyPrincipalMemoryMetadata(ctx, memory, params.AgentVisibility, params.Domain); err != nil {
		return "", err
	}
	var domainDecision *principalmemory.DomainWriteDecision
	var supersededIDs []int64
	var supersedeTargets []*models.Memory

	// --- Write gate (vNext Phase A) ---
	// When ENGRAM_VNEXT_ENABLED=true, evaluate novelty before creation.
	// Low-novelty memories are stored with Status="flagged" so callers can
	// detect duplicates; the quality_signals key is added to the response.
	var gateResult writegate.GateResult

	checkWriteGate := func(store *gormdb.MemoryStore) {
		if params.Project != "" {
			existing, listErr := store.List(ctx, params.Project, 100)
			if listErr != nil {
				log.Warn().Err(listErr).Msg("store: write gate could not load existing memories, skipping gate")
			} else {
				existing = filterVisibleWriteGateCandidates(ctx, params.SessionID, existing)
				gateResult = writegate.Check(ctx, params.Content, existing)
			}
		}
		if gateResult.Decision == "flag" {
			memory.Status = "flagged"
		}
	}

	// Lifecycle fields (Milestone B): only set when lifecycle is enabled
	{
		if tier := coerceString(m["tier"], ""); tier != "" {
			if lifecycle.ValidTier(tier) {
				memory.Tier = tier
			}
		} else {
			// B4 resolution (2026-06-11): spec FR-B2 specifies 'episodic' as the default
			// tier for new memories — fresh, unverified knowledge is episodic until promoted.
			// DB column default is now 'episodic' (migration 131), but memoryRowForCreate
			// only copies Tier when it is non-empty. Set the explicit default here so
			// CreateWithLifecycle persists 'episodic' rather than relying on the DB default
			// (belt-and-suspenders: explicit intent is clearer than relying on DB default).
			memory.Tier = lifecycle.TierEpisodic
		}
		if et := coerceString(m["epistemic_type"], ""); et != "" {
			if lifecycle.ValidEpistemicType(et) {
				memory.EpistemicType = et
			}
		}
		if def := coerceString(m["defeasibility"], ""); def != "" {
			if lifecycle.ValidDefeasibility(def) {
				memory.Defeasibility = def
			}
		}
		if memory.Stability == 0 {
			memory.Stability = lifecycle.ComputeStability(30.0, memory.Tier, memory.EpistemicType, 0)
		}
		memory.Confidence = lifecycle.ComputeConfidence(lifecycle.ConfidenceInputs{})
		memory.Retrievability = 1.0
	}

	var created *models.Memory
	if len(params.Supersedes) == 0 {
		domainDecision, err = s.checkDomainWriteMCP(ctx, memory, params.SessionID)
		if err != nil {
			return "", fmt.Errorf("domain registry check failed: %w", err)
		}
		checkWriteGate(s.memoryStore)
		created, err = s.memoryStore.CreateWithLifecycle(ctx, memory)
	} else {
		access, accessErr := memoryMutationAccessMCP(ctx, params.SessionID)
		if accessErr != nil {
			return "", fmt.Errorf("store: supersede target not found")
		}
		var checkErr error
		err = s.memoryStore.WithAuthorizedMemories(ctx, params.Supersedes, access, func(tx *gormlib.DB, memories map[int64]*models.Memory) error {
			// Target authorization precedes even domain-warning audit effects.
			domainDecision, checkErr = s.checkDomainWriteMCPTx(ctx, memory, params.SessionID, tx)
			if errors.Is(checkErr, principalmemory.ErrDomainWriteRejected) {
				// Preserve the existing rejection audit without changing targets.
				return nil
			}
			if checkErr != nil {
				return fmt.Errorf("domain registry check failed: %w", checkErr)
			}
			seen := make(map[int64]bool, len(params.Supersedes))
			for _, sid := range params.Supersedes {
				if seen[sid] {
					continue
				}
				seen[sid] = true
				before := memories[sid]
				if before.Status != "active" {
					continue
				}
				oldImportance, supersedeErr := s.memoryStore.SupersedeTx(ctx, tx, sid)
				if supersedeErr != nil {
					return supersedeErr
				}
				if memory.SupersedesID == nil {
					primaryID := sid
					memory.SupersedesID = &primaryID
					memory.ImportanceBase = math.Max(memory.ImportanceBase, oldImportance*0.7)
				}
				supersededIDs = append(supersededIDs, sid)
				supersedeTargets = append(supersedeTargets, before)
			}
			// The novelty corpus must observe supersession before its LIMIT,
			// just as the original create path did, now within this transaction.
			checkWriteGate(gormdb.NewMemoryStore(&gormdb.Store{DB: tx}))
			var createErr error
			created, createErr = s.memoryStore.CreateWithLifecycleTx(ctx, tx, memory)
			return createErr
		})
		if err == nil && checkErr != nil {
			err = fmt.Errorf("domain registry check failed: %w", checkErr)
		}
	}
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			return "", fmt.Errorf("store: supersede target not found")
		}
		return "", fmt.Errorf("store memory: %w", err)
	}
	for _, before := range supersedeTargets {
		logAuditSupersede(ctx, s, before, actorFromContext(ctx))
	}

	// Audit: fire-and-forget create event (FR-D2 / NFR-D4). Actor derived from
	// session context when available, else "agent".
	actor := actorFromContext(ctx)
	logAuditCreate(ctx, s, created, actor)

	// T035: when write-lint is wired AND force=true was set, emit an additional
	// "legacy_force_write" audit entry to record that the lint gate was bypassed.
	// finding 12 fix: wlForce is now scoped inside the gate block; re-derive here.
	if s.writeLint != nil && coerceBool(m["force"], false) {
		logAuditGeneric(ctx, s, created, actor, "legacy_force_write")
	}

	// Async embedding: fire-and-forget goroutine so the MCP response is not blocked
	// by the embedding HTTP call. Captures local copies of created fields and store
	// pointers to avoid capturing the mutable request-scoped variables.
	if s.embeddingClient != nil && s.embeddingStore != nil {
		memID := created.ID
		memContent := created.Content
		embClient := s.embeddingClient
		embStore := s.embeddingStore
		go func() {
			gCtx := context.Background()
			vectors, embErr := embClient.Embed(gCtx, []string{memContent})
			if embErr != nil {
				log.Error().Err(embErr).Int64("memory_id", memID).Msg("async embedding failed")
				return
			}
			if len(vectors) == 0 || len(vectors[0]) == 0 {
				return
			}
			chunk := embedding.Chunk{
				MemoryID:  memID,
				Seq:       0,
				Text:      memContent,
				Embedding: pgvector.NewVector(vectors[0]),
				Model:     embClient.Model(),
			}
			if storeErr := embStore.StoreChunks(gCtx, []embedding.Chunk{chunk}); storeErr != nil {
				log.Error().Err(storeErr).Int64("memory_id", memID).Msg("async embedding store failed")
				return // don't run cosine check if store failed
			}
			// Async cosine duplicate guard
			writegate.CheckCosine(gCtx, memID, memContent, embClient, embStore, s.memoryStore)
		}()
	}

	result := map[string]any{
		"id":      created.ID,
		"title":   truncateTitle(created.Content, 80),
		"type":    obsTypeStr,
		"scope":   resolvedScope,
		"storage": "memories",
		"message": "Memory stored successfully",
	}
	// T004 — vNext F TG1: dual-field response per RI-F2. Legacy `scope`
	// (2-tier) continues to appear above for backward compat; add new
	// `privacy_scope` (4-tier) alongside when flag is ON.
	{
		result["privacy_scope"] = resolvedPrivacyScope
		if created.SourceWorkstationID != "" {
			result["source_workstation_id"] = created.SourceWorkstationID
		}
		if len(created.SourceSessions) > 0 {
			result["source_sessions"] = created.SourceSessions
		}
	}
	addPrincipalMemoryFields(result, created)
	addDomainWriteDecisionFields(result, domainDecision)
	{
		result["quality_signals"] = map[string]any{
			"gate_result":      gateResult.Decision,
			"novelty_score":    gateResult.NoveltyScore,
			"max_jaccard":      gateResult.MaxJaccard,
			"similar_existing": gateResult.SimilarExisting,
		}
	}
	if len(supersededIDs) > 0 {
		result["superseded_ids"] = supersededIDs
	}
	if ttlApplied {
		result["ttl_days"] = ttlDays
	}
	if params.Importance != nil {
		result["importance_note"] = "importance metadata is not stored in v5 memories schema"
	}
	if len(params.Rejected) > 0 {
		result["rejected_note"] = "rejected alternatives are not stored in v5 memories schema"
	}
	// Rank-3 staleness advisory (non-blocking): if the content uses relative-time
	// language, nudge the author toward an absolute date/version anchor. "currently X"
	// read months later looks like a current fact — this is the silent-staleness
	// friction caught at the source. Advisory only; the memory is already stored.
	if terms := staleness.DetectRelativeTime(created.Content); len(terms) > 0 {
		result["staleness_advisory"] = staleAdvisory(terms)
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// handleEditMemory updates an existing memory's content and/or narrative.
// Introduced in Milestone D T003 to fill the audit trail gap for the edit path.
// Before-state is captured, Update is called, then logAuditEdit fires async.
func (s *Server) handleEditMemory(ctx context.Context, args json.RawMessage) (string, error) {
	ms := s.effectiveMemoryEditor()
	if ms == nil {
		return "", fmt.Errorf("memory store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	id, err := requireInt64Arg(m, "id")
	if err != nil || id <= 0 {
		return "", fmt.Errorf("edit_memory: id must be a positive integer")
	}
	narrative, _, err := optionalStringArg(m, "narrative")
	if err != nil {
		return "", fmt.Errorf("edit_memory: %w", err)
	}
	tags, _, err := optionalStringSliceArg(m, "tags")
	if err != nil {
		return "", fmt.Errorf("edit_memory: %w", err)
	}

	if narrative != "" {
		// Finding 1: apply the same hard-limit, soft-truncation, and secret-redaction
		// as the create path (tools_memory.go:104-126) before assigning content.
		cfg := config.Get()
		hardLimit := cfg.StoreMemoryHardLimit
		if hardLimit <= 0 {
			hardLimit = 10000
		}
		softLimit := cfg.StoreMemorySoftLimit
		if softLimit <= 0 {
			softLimit = 1000
		}
		if utf8.RuneCountInString(narrative) > hardLimit {
			return "", fmt.Errorf("edit_memory: content exceeds maximum length of %d characters", hardLimit)
		}
		if utf8.RuneCountInString(narrative) > softLimit {
			narrative = string([]rune(narrative)[:softLimit])
			log.Debug().
				Int("soft_limit", softLimit).
				Msg("edit_memory: content truncated to soft limit")
		}
		if privacy.ContainsSecrets(narrative) {
			log.Warn().Msg("edit_memory: content contains secrets — redacting before storage")
			narrative = privacy.RedactSecrets(narrative)
		}
	}
	before, after, err := s.updateMemoryAsCaller(ctx, id, func(updated *models.Memory) error {
		if narrative != "" {
			updated.Content = narrative
		}
		// An absent tags key preserves tags; an explicit empty array clears them.
		if _, tagsPresent := m["tags"]; tagsPresent {
			updated.Tags = tags
		}
		if updated.Content == "" {
			return fmt.Errorf("content must not be empty after edit")
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			return "", fmt.Errorf("edit_memory: memory %d not found", id)
		}
		return "", fmt.Errorf("edit_memory: %w", err)
	}

	// Audit: fire-and-forget update event.
	logAuditEdit(ctx, s, before, after, actorFromContext(ctx))

	result := map[string]any{
		"id":      after.ID,
		"title":   truncateTitle(after.Content, 80),
		"storage": "memories",
		"message": "Memory updated successfully",
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// computeTTLDays determines the TTL for an observation based on explicit override or auto-TTL from tags.
// Returns 0 if no TTL should be applied.
func computeTTLDays(explicit *int, concepts []string) int {
	// 1. Explicit override takes priority
	if explicit != nil && *explicit > 0 {
		return *explicit
	}

	// 2. Auto-TTL only applies to observations with "verified" tag
	hasVerified := false
	for _, c := range concepts {
		if c == "verified" {
			hasVerified = true
			break
		}
	}
	if !hasVerified {
		return 0
	}

	// 3. Auto-TTL by concept tags (exact match) — use minimum TTL from all matching tags
	autoTTL := map[string]int{
		"api": 7, "endpoint": 7,
		"library": 30, "framework": 30,
		"language-feature": 90,
		"architecture":     180, "pattern": 180,
	}
	minTTL := 0
	for _, c := range concepts {
		if days, ok := autoTTL[c]; ok && (minTTL == 0 || days < minTTL) {
			minTTL = days
		}
	}
	if minTTL > 0 {
		return minTTL
	}

	// 4. Default for verified facts with no matching tag
	return 30
}

// truncateTitle creates a short title from content, truncating at a word boundary.
func truncateTitle(content string, maxLen int) string {
	content = strings.TrimSpace(content)
	if utf8.RuneCountInString(content) <= maxLen {
		return content
	}
	truncated := string([]rune(content)[:maxLen])
	if i := strings.LastIndexAny(truncated, " \t\n"); i > 0 {
		truncated = truncated[:i]
	}
	return truncated + "..."
}

func keepRecallMemory(mem *models.Memory, queryLower, obsType string, tagSet map[string]struct{}, caller scope.KeycardContext, visibilityOpts scope.MemoryVisibilityOptions, tierFilterSet map[string]bool, tg3Active bool, tg3ConfidenceMin float64) bool {
	if !keepRecallMemoryFilters(mem, queryLower, obsType, tagSet, tierFilterSet, tg3Active, tg3ConfidenceMin) {
		return false
	}
	if !scope.ResolveMemory(caller, mem, visibilityOpts) {
		return false
	}
	return true
}

func keepRecallMemoryFilters(mem *models.Memory, queryLower, obsType string, tagSet map[string]struct{}, tierFilterSet map[string]bool, tg3Active bool, tg3ConfidenceMin float64) bool {
	contentLower := strings.ToLower(mem.Content)
	if queryLower != "" && !strings.Contains(contentLower, queryLower) {
		matchedTag := false
		for _, tag := range mem.Tags {
			if strings.Contains(strings.ToLower(tag), queryLower) {
				matchedTag = true
				break
			}
		}
		if !matchedTag {
			return false
		}
	}
	if obsType != "" {
		typeTag := strings.ToLower("type:" + obsType)
		typeMatched := false
		for _, tag := range mem.Tags {
			if strings.ToLower(tag) == typeTag {
				typeMatched = true
				break
			}
		}
		if !typeMatched {
			return false
		}
	}
	if len(tagSet) > 0 {
		tagMatched := false
		for _, tag := range mem.Tags {
			if _, ok := tagSet[strings.ToLower(tag)]; ok {
				tagMatched = true
				break
			}
		}
		if !tagMatched {
			return false
		}
	}
	if len(tierFilterSet) > 0 {
		tier := mem.Tier
		if tier == "" {
			tier = lifecycle.TierEpisodic
		}
		if !tierFilterSet[tier] {
			return false
		}
	}
	if tg3Active && tg3ConfidenceMin > 0 && mem.Confidence < tg3ConfidenceMin {
		return false
	}
	return true
}

// handleRecallMemory retrieves memories from the v5 memories table.
//
// Flag-OFF (ENGRAM_VNEXT_ENABLED != "true"): byte-identical to the previous
// List-based in-memory-filter path. No schema change; no new parameters accepted.
//
// Flag-ON (ENGRAM_VNEXT_ENABLED == "true"): uses the HybridSearch pipeline
// (FR-C4: FTS+vector RRF + FR-C4 scoring + optional Tier2 graph expansion).
// New parameters: expand_graph, min_confidence, tier_filter, explain.
func (s *Server) handleRecallMemory(ctx context.Context, args json.RawMessage) (string, error) {
	if s.memoryStore == nil {
		return "", fmt.Errorf("recall_memory: memory store not configured")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	query := strings.TrimSpace(coerceString(m["query"], ""))
	obsType := strings.TrimSpace(coerceString(m["type"], ""))
	format := coerceString(m["format"], "")
	limit := coerceInt(m["limit"], 0)
	project := strings.TrimSpace(coerceString(m["project"], ""))
	tags := coerceStringSlice(m["tags"])

	// Hybrid tier_filter selects retrieval legs, not lifecycle memory tiers.
	// Preserve the lifecycle-only filter when hybrid retrieval is disabled.

	var tierFilterSet map[string]bool
	{
		for _, tf := range coerceStringSlice(m["tier_filter"]) {
			switch tf {
			case "tier0_exact", "tier1_fts", "tier1_vector", "tier2_graph":
			default:
				return "", fmt.Errorf("invalid_tier_filter: %q must be one of tier0_exact, tier1_fts, tier1_vector, tier2_graph", tf)
			}
		}
	}

	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	if format == "" {
		format = "text"
	}
	if project == "" {
		project = strings.TrimSpace(ProjectFromContext(ctx))
	}
	if project == "" {
		return "", fmt.Errorf("project is required for recall_memory in v5")
	}
	if err := s.memoryStore.CheckCanonicalProject(ctx, project); err != nil {
		return "", err
	}

	// ── Visibility params — parsed unconditionally so both hybrid and legacy
	// paths enforce the same memory visibility contract. ENGRAM_VNEXT_F_ENABLED
	// gates legacy privacy_scope only; CR-004 principal-private rows are always
	// filtered fail-safe.
	callerSessionID := strings.TrimSpace(coerceString(m["session_id"], ""))

	includeScopes := make(map[string]bool)
	{
		for _, sc := range coerceStringSlice(m["include_scopes"]) {
			switch sc {
			case "private", "project", "shared", "global":
				includeScopes[sc] = true
			default:
				return "", fmt.Errorf("invalid_include_scopes: %q must be one of private, project, shared, global", sc)
			}
		}
	}
	caller := scope.KeycardContext{SessionID: callerSessionID}
	if id, ok := auth.IdentityFrom(ctx); ok {
		caller.WorkstationID = id.WorkstationID()
		caller.Principal = id.Principal
		caller.PrincipalKind = string(id.PrincipalKind)
	}
	visibilityOpts := scope.MemoryVisibilityOptions{
		ApplyPrivacyScope: true,
		IncludeScopes:     includeScopes,
	}
	includePrincipals, includePrincipalsSet, err := parseRecallIncludedPrincipals(m["include_principals"])
	if err != nil {
		return "", err
	}

	// ── T018 (engram vNext Milestone F TG3) — new filter + rationale params ──
	// Parsed unconditionally from the args map; gated on vnextFEnabled() at
	// the dispatch point below.  Default values (false / 0.0) produce
	// byte-identical behaviour to v6.4.x — no response-shape change, no
	// ListWithFilters dispatch — satisfying NFR-F1 backward-compat invariant.
	tg3ConfidenceMin := coerceFloat64(m["confidence_min"], 0.0)
	tg3IncludeSuperseded := coerceBool(m["include_superseded"], false)
	tg3IncludeRationale := coerceBool(m["include_rationale"], false)

	// tg3Active is true only when at least one TG3 param is non-default AND the
	// vNext-F flag is on.  Flag-OFF callers that accidentally pass the new params
	// still get the legacy path — a defensive gate that mirrors the include_scopes
	// flag-gate pattern in T005.
	tg3Active := (tg3ConfidenceMin > 0 || tg3IncludeSuperseded || tg3IncludeRationale)

	// ── vnext hybrid path ───────────────────────────────────────────────────
	// Caller identity + visibility context are fully built above; pass them into
	// handleRecallMemoryHybrid so hybrid results are subject to the same
	// predicate as the legacy List path below.
	if !includePrincipalsSet {
		return s.handleRecallMemoryHybrid(ctx, m, query, project, format, limit, obsType, tags, caller, visibilityOpts, tg3Active, tg3ConfidenceMin, tg3IncludeSuperseded, tg3IncludeRationale)
	}
	// ── legacy List-based path with shared visibility filtering ─

	queryLower := strings.ToLower(query)
	tagSet := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tagSet[strings.ToLower(tag)] = struct{}{}
	}

	// keepMemory applies the existing query/type/tags filters AND the new
	// vNext-F scope filter to a single memory. Returns true when it should
	// be included in the response.
	keepMemory := func(mem *models.Memory) bool {
		return keepRecallMemory(mem, queryLower, obsType, tagSet, caller, visibilityOpts, tierFilterSet, tg3Active, tg3ConfidenceMin)
	}
	keepIncludedPrincipalMemory := func(mem *models.Memory) bool {
		return keepRecallMemoryFilters(mem, queryLower, obsType, tagSet, tierFilterSet, tg3Active, tg3ConfidenceMin)
	}

	filtered := make([]*models.Memory, 0, limit)
	if tg3Active {
		// Batch-loop through ListWithFilters so include_superseded and
		// confidence_min stay SQL-backed while visibility filtering can still
		// backfill past invisible rows before the final limit is applied.
		const batchSize = 500
		offset := 0
		for len(filtered) < limit {
			batch, err := s.memoryStore.ListWithFiltersForSearch(ctx, project, gormdb.ListOptions{
				ConfidenceMin:     tg3ConfidenceMin,
				IncludeSuperseded: tg3IncludeSuperseded,
				Limit:             batchSize,
				Offset:            offset,
			}, scope.MemoryReadAccess{Caller: caller, Visibility: visibilityOpts})
			if err != nil {
				return "", fmt.Errorf("recall_memory: %w", err)
			}
			if len(batch) == 0 {
				break
			}
			for _, mem := range batch {
				if keepMemory(mem) {
					filtered = append(filtered, mem)
					if len(filtered) >= limit {
						break
					}
				}
			}
			offset += len(batch)
			if len(batch) < batchSize {
				break
			}
		}
	} else {
		// Batch-loop via ListWithOffset (codex P1 cycle-3 + cycle-4 pattern)
		// so invisible newest rows do not truncate visible recall before older
		// eligible rows reach the requested limit.
		const batchSize = 500
		offset := 0
		for len(filtered) < limit {
			batch, err := s.memoryStore.ListWithFiltersForSearch(ctx, project, gormdb.ListOptions{Limit: batchSize, Offset: offset}, scope.MemoryReadAccess{Caller: caller, Visibility: visibilityOpts})
			if err != nil {
				return "", fmt.Errorf("recall_memory: %w", err)
			}
			if len(batch) == 0 {
				break
			}
			for _, mem := range batch {
				if keepMemory(mem) {
					filtered = append(filtered, mem)
					if len(filtered) >= limit {
						break
					}
				}
			}
			offset += len(batch)
			if len(batch) < batchSize {
				break
			}
		}
	}
	if includePrincipalsSet {
		filtered, err = s.appendRecallIncludedPrincipalMemories(ctx, filtered, includePrincipals, project, query, callerSessionID, limit, keepIncludedPrincipalMemory)
		if err != nil {
			return "", err
		}
	}

	// Reconsolidation: every retrieval updates access_count, last_retrieved_at,
	// and recalculates stability (Nader 2000). Fire-and-forget to avoid blocking.
	if len(filtered) > 0 {
		go func() {
			for _, mem := range filtered {
				fields := map[string]any{
					"access_count":      gormlib.Expr("access_count + 1"),
					"last_retrieved_at": gormlib.Expr("now()"),
				}
				if mem.Stability > 0 {
					newStability := lifecycle.Reconsolidate(mem.Stability, mem.Retrievability)
					if newStability != mem.Stability {
						fields["stability"] = newStability
					}
				}
				updated, err := s.memoryStore.UpdateRecallBookkeeping(context.Background(), mem.ID, fields)
				if err != nil {
					log.Warn().Err(err).Int64("memory_id", mem.ID).Msg("recall bookkeeping failed")
				} else if !updated {
					log.Debug().Int64("memory_id", mem.ID).Msg("recall bookkeeping skipped for retained historical selector")
				}
			}
		}()
	}

	// T018 (TG3): when include_rationale=true, emit a structured JSON response
	// regardless of format — the rationale block only makes sense in structured
	// output. This path overrides the format switch below.
	//
	// The response shape matches spec §FR-F3 (REVISE 2026-05-25):
	//   { "memories": [..., { ..., "ranking_rationale": { 6 fields } }], "count": N,
	//     "query_metadata": { "candidates_before_filter", "candidates_after_filter",
	//                         "elapsed_ms" } }
	//
	// Backward-compat: this branch is only entered when tg3IncludeRationale=true
	// AND tg3Active=true (vnextFEnabled() check is inside tg3Active). Zero-flags
	// callers never reach this branch — their response is byte-identical v6.4.x.
	if tg3IncludeRationale && tg3Active {
		// Build active filter descriptors for rationale.filters_applied.
		var filterDescs []string
		filterDescs = append(filterDescs, "project="+project)
		if tg3ConfidenceMin > 0 {
			filterDescs = append(filterDescs, fmt.Sprintf("confidence_min=%.4g", tg3ConfidenceMin))
		}
		if tg3IncludeSuperseded {
			filterDescs = append(filterDescs, "include_superseded=true")
		}

		type memWithRationale struct {
			*models.Memory
			RankingRationale retrieval.RankingRationale `json:"ranking_rationale"`
		}
		results := make([]memWithRationale, 0, len(filtered))
		for _, mem := range filtered {
			// substring_match: true when query found in content (mirrors keepMemory logic).
			contentMatched := queryLower != "" && strings.Contains(strings.ToLower(mem.Content), queryLower)
			rat := retrieval.AssembleRationale(mem, query, contentMatched, filterDescs)
			results = append(results, memWithRationale{Memory: mem, RankingRationale: rat})
		}

		response := map[string]any{
			"memories": results,
			"count":    len(results),
		}
		if query != "" {
			response["query"] = query
		}
		out, err := json.Marshal(response)
		if err != nil {
			return "", fmt.Errorf("recall_memory rationale marshal: %w", err)
		}
		return string(out), nil
	}

	switch format {
	case "items":
		type item struct {
			Tags               []string `json:"tags,omitempty"`
			Title              string   `json:"title"`
			Type               string   `json:"type,omitempty"`
			Content            string   `json:"content"`
			SourceAgent        string   `json:"source_agent,omitempty"`
			OwnerPrincipal     string   `json:"owner_principal,omitempty"`
			OwnerPrincipalKind string   `json:"owner_principal_kind,omitempty"`
			AgentVisibility    string   `json:"agent_visibility,omitempty"`
			Domain             string   `json:"domain,omitempty"`
			Project            string   `json:"project"`
			ID                 int64    `json:"id"`
		}
		items := make([]item, 0, len(filtered))
		for _, mem := range filtered {
			memoryType := ""
			for _, tag := range mem.Tags {
				if strings.HasPrefix(tag, "type:") {
					memoryType = strings.TrimPrefix(tag, "type:")
					break
				}
			}
			items = append(items, item{
				ID:                 mem.ID,
				Title:              truncateTitle(mem.Content, 80),
				Type:               memoryType,
				Content:            mem.Content,
				Tags:               mem.Tags,
				SourceAgent:        mem.SourceAgent,
				OwnerPrincipal:     mem.OwnerPrincipal,
				OwnerPrincipalKind: mem.OwnerPrincipalKind,
				AgentVisibility:    mem.AgentVisibility,
				Domain:             mem.Domain,
				Project:            mem.Project,
			})
		}
		out, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal result: %w", err)
		}
		return string(out), nil

	case "detailed":
		out, err := json.MarshalIndent(filtered, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal result: %w", err)
		}
		return string(out), nil

	default:
		if len(filtered) == 0 {
			return "No memories found matching the query.", nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d memories for query: %q\n\n", len(filtered), query))
		for i, mem := range filtered {
			typeLabel := "MEMORY"
			for _, tag := range mem.Tags {
				if strings.HasPrefix(tag, "type:") {
					typeLabel = strings.ToUpper(strings.TrimPrefix(tag, "type:"))
					break
				}
			}
			sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, typeLabel, truncateTitle(mem.Content, 80)))
			content := mem.Content
			if len(content) > 300 {
				content = content[:300] + "..."
			}
			sb.WriteString(fmt.Sprintf("   %s\n", content))
			if len(mem.Tags) > 0 {
				sb.WriteString(fmt.Sprintf("   tags: %s\n", strings.Join(mem.Tags, ", ")))
			}
			sb.WriteString("\n")
		}
		return sb.String(), nil
	}
}

// handleRecallMemoryHybrid is the vnext path for recall_memory.
// Called only when ENGRAM_VNEXT_ENABLED == "true".
// It accepts the vnext-gated parameters (expand_graph, min_confidence,
// tier_filter, explain) in addition to the base recall_memory params.
//
// staleAdvisory builds the non-blocking rank-3 write-time staleness advisory shared
// by every store path (legacy create, write-lint no-signal success, write-lint
// Phase2 commit), so the advisory shape cannot drift between them.
func staleAdvisory(terms []string) map[string]any {
	return map[string]any{
		"relative_time_terms": terms,
		"note":                "content uses relative-time language; prefer an absolute date or version anchor (e.g. 'as of 2026-06-17' / 'in v6.16.0') so the fact stays interpretable when recalled later",
	}
}

// marshalWithStaleAdvisory marshals a typed Phase2 response with the staleness
// advisory attached. The response is a struct, so it is round-tripped through a
// generic map to add the advisory key without coupling to the orchestrator's type.
func marshalWithStaleAdvisory(resp any, terms []string) (string, error) {
	raw, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		// Response was not a JSON object (unexpected); fall back to the plain form
		// rather than dropping the commit result.
		return string(raw), nil
	}
	m["staleness_advisory"] = staleAdvisory(terms)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Caller-only recovery payload belongs in JSON-RPC error data, never Error().
type recallHistoryRecoveryError struct {
	arguments json.RawMessage
}

func (*recallHistoryRecoveryError) Error() string {
	return memorysearch.ErrIncludeSuperseded.Error() + `; for read-only history use recall(action="search") with the same caller identity and error.data.recovery.arguments`
}

func (*recallHistoryRecoveryError) Unwrap() error {
	return memorysearch.ErrIncludeSuperseded
}

// caller, scopeEnabled, and includeScopes are forwarded from handleRecallMemory
// so that privacy_scope visibility (scope.Resolve) is enforced on hybrid results.
// This is required by the integration contract established in d9eea82: the hybrid
// path MUST NOT bypass scope filtering (same predicate as the legacy List path).
// Scope filter runs BEFORE final limit truncation so the limit reflects visible
// memories only, consistent with d9eea82 fix #4.
func (s *Server) handleRecallMemoryHybrid(
	ctx context.Context,
	m map[string]any,
	query, project, format string,
	limit int,
	obsType string,
	tags []string,
	caller scope.KeycardContext,
	visibilityOpts scope.MemoryVisibilityOptions,
	tg3Active bool,
	tg3ConfidenceMin float64,
	tg3IncludeSuperseded bool,
	tg3IncludeRationale bool,
) (result string, resultErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	defer func() {
		if err := ctx.Err(); err != nil {
			result = ""
			if resultErr == nil {
				resultErr = err
			}
		}
	}()
	explain := coerceBool(m["explain"], false)
	var graphStore retrieval.GraphStoreInterface
	if s.graphStore != nil {
		graphStore = s.graphStore
	}
	var embeddingStore retrieval.EmbeddingStoreInterface
	if s.embeddingStore != nil {
		embeddingStore = s.embeddingStore
	}
	searchResult, err := memorysearch.Search(ctx, memorysearch.Dependencies{
		Memories: s.memoryStore, Embeddings: embeddingStore, Graph: graphStore,
		Embedder: s.embeddingClient, Reranker: s.rerankClient,
	}, memorysearch.Request{
		Project: project, Query: query, Limit: limit,
		Options: retrieval.HybridOptions{
			TierFilter:    coerceStringSlice(m["tier_filter"]),
			MinConfidence: coerceFloat64(m["min_confidence"], 0.0),
			VecThreshold:  coerceFloat64(m["vec_threshold"], 0.0),
			ExpandGraph:   coerceBool(m["expand_graph"], false), Explain: explain,
		},
		Caller: caller, Visibility: visibilityOpts, Type: obsType, Tags: tags,
		ConfidenceMin:     tg3ConfidenceMin,
		IncludeSuperseded: tg3Active && tg3IncludeSuperseded,
	})
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err != nil {
		if errors.Is(err, memorysearch.ErrIncludeSuperseded) {
			recovery := map[string]any{
				"action": "search", "project": project, "query": query, "limit": limit,
				"include_superseded": true, "confidence_min": tg3ConfidenceMin,
			}
			if caller.SessionID != "" {
				recovery["session_id"] = caller.SessionID
			}
			if scopes := coerceStringSlice(m["include_scopes"]); len(scopes) > 0 {
				recovery["include_scopes"] = scopes
			}
			if tg3IncludeRationale {
				recovery["include_rationale"] = true
			}
			arguments, marshalErr := json.Marshal(recovery)
			if marshalErr != nil {
				return "", fmt.Errorf("%w; use read-only recall(action=\"search\") with the same project and caller identity; recovery arguments could not be encoded: %v", err, marshalErr)
			}
			return "", &recallHistoryRecoveryError{arguments: arguments}
		}
		return "", fmt.Errorf("recall_memory %w", err)
	}
	scored, explanations := searchResult.Matches, searchResult.Explanations

	queryLower := strings.ToLower(query)

	type hybridResult struct {
		RankingExplanation *retrieval.RankingExplanation `json:"ranking_explanation,omitempty"`
		Tags               []string                      `json:"tags,omitempty"`
		Title              string                        `json:"title"`
		Type               string                        `json:"type,omitempty"`
		Content            string                        `json:"content"`
		SourceAgent        string                        `json:"source_agent,omitempty"`
		OwnerPrincipal     string                        `json:"owner_principal,omitempty"`
		OwnerPrincipalKind string                        `json:"owner_principal_kind,omitempty"`
		AgentVisibility    string                        `json:"agent_visibility,omitempty"`
		Domain             string                        `json:"domain,omitempty"`
		Project            string                        `json:"project"`
		ID                 int64                         `json:"id"`
		Score              float64                       `json:"score"`
	}

	explByID := make(map[int64]retrieval.RankingExplanation, len(explanations))
	for _, e := range explanations {
		explByID[e.MemoryID] = e
	}

	items := make([]hybridResult, 0, limit)
	for _, sm := range scored {
		mem := sm.Memory

		memoryType := ""
		for _, tag := range mem.Tags {
			if strings.HasPrefix(tag, "type:") {
				memoryType = strings.TrimPrefix(tag, "type:")
				break
			}
		}

		r := hybridResult{
			ID:                 mem.ID,
			Title:              truncateTitle(mem.Content, 80),
			Type:               memoryType,
			Content:            mem.Content,
			Tags:               mem.Tags,
			SourceAgent:        mem.SourceAgent,
			OwnerPrincipal:     mem.OwnerPrincipal,
			OwnerPrincipalKind: mem.OwnerPrincipalKind,
			AgentVisibility:    mem.AgentVisibility,
			Domain:             mem.Domain,
			Project:            mem.Project,
			Score:              sm.Score,
		}
		if explain {
			if e, ok := explByID[mem.ID]; ok {
				r.RankingExplanation = &e
			}
		}
		items = append(items, r)
	}

	// Build a lookup from memory ID → ScoredMemory used by both the
	// reconsolidation block (lifecycle) and the detailed format serializer.
	scoredByID := make(map[int64]retrieval.ScoredMemory, len(scored))
	for _, sm := range scored {
		scoredByID[sm.Memory.ID] = sm
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	// Reconsolidation: fire-and-forget lifecycle updates on the FINAL response set only
	// (not the wider candidate pool). This ensures access_count reflects actual retrieval.
	if len(items) > 0 {
		// Snapshot item IDs before the goroutine runs to avoid data races.
		type reconItem struct {
			id             int64
			stability      float64
			retrievability float64
		}
		toRecon := make([]reconItem, 0, len(items))
		for _, item := range items {
			if sm, ok := scoredByID[item.ID]; ok {
				toRecon = append(toRecon, reconItem{
					id:             sm.Memory.ID,
					stability:      sm.Memory.Stability,
					retrievability: sm.Memory.Retrievability,
				})
			}
		}
		go func() {
			for _, ri := range toRecon {
				fields := map[string]any{
					"access_count":      gormlib.Expr("access_count + 1"),
					"last_retrieved_at": gormlib.Expr("now()"),
				}
				if ri.stability > 0 {
					newStability := lifecycle.Reconsolidate(ri.stability, ri.retrievability)
					if newStability != ri.stability {
						fields["stability"] = newStability
					}
				}
				updated, err := s.memoryStore.UpdateRecallBookkeeping(context.Background(), ri.id, fields)
				if err != nil {
					log.Warn().Err(err).Int64("memory_id", ri.id).Msg("recall bookkeeping failed")
				} else if !updated {
					log.Debug().Int64("memory_id", ri.id).Msg("recall bookkeeping skipped for retained historical selector")
				}
			}
		}()
	}

	// T018+T019 TG3: hybrid path include_rationale — when set, emit v5-surface
	// RankingRationale alongside W3's existing ranking_explanation. The two
	// fields coexist: ranking_explanation carries W3 FR-C4 scores (relevance,
	// recency, importance, fused_score, source_tier); ranking_rationale carries
	// the 6 TG3 v5-surface fields (recency_days, confidence, citation_count,
	// tier, substring_match, filters_applied). See T019 schema description.
	if tg3IncludeRationale && tg3Active {
		var filterDescs []string
		filterDescs = append(filterDescs, "project="+project)
		if tg3ConfidenceMin > 0 {
			filterDescs = append(filterDescs, fmt.Sprintf("confidence_min=%.4g", tg3ConfidenceMin))
		}
		// Note: include_superseded is rejected before reaching here (structured
		// error at hybrid-path entry), so it is never listed as applied.

		type hybridRationaleResult struct {
			RankingExplanation *retrieval.RankingExplanation `json:"ranking_explanation,omitempty"`
			RankingRationale   *retrieval.RankingRationale   `json:"ranking_rationale"`
			Tags               []string                      `json:"tags,omitempty"`
			Title              string                        `json:"title"`
			Type               string                        `json:"type,omitempty"`
			Content            string                        `json:"content"`
			SourceAgent        string                        `json:"source_agent,omitempty"`
			OwnerPrincipal     string                        `json:"owner_principal,omitempty"`
			OwnerPrincipalKind string                        `json:"owner_principal_kind,omitempty"`
			AgentVisibility    string                        `json:"agent_visibility,omitempty"`
			Domain             string                        `json:"domain,omitempty"`
			Project            string                        `json:"project"`
			ID                 int64                         `json:"id"`
			Score              float64                       `json:"score"`
		}
		rationaleItems := make([]hybridRationaleResult, 0, len(items))
		for _, item := range items {
			sm, ok := scoredByID[item.ID]
			if !ok {
				continue
			}
			contentMatched := strings.Contains(strings.ToLower(sm.Memory.Content), queryLower)
			rat := retrieval.AssembleRationale(sm.Memory, query, contentMatched, filterDescs)
			r := hybridRationaleResult{
				ID:                 item.ID,
				Title:              item.Title,
				Type:               item.Type,
				Content:            item.Content,
				Tags:               item.Tags,
				SourceAgent:        item.SourceAgent,
				OwnerPrincipal:     item.OwnerPrincipal,
				OwnerPrincipalKind: item.OwnerPrincipalKind,
				AgentVisibility:    item.AgentVisibility,
				Domain:             item.Domain,
				Project:            item.Project,
				Score:              item.Score,
				RankingRationale:   &rat,
			}
			if explain {
				if e, ok := explByID[item.ID]; ok {
					r.RankingExplanation = &e
				}
			}
			rationaleItems = append(rationaleItems, r)
		}
		response := map[string]any{
			"memories": rationaleItems,
			"count":    len(rationaleItems),
			"query":    query,
		}
		out, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return "", fmt.Errorf("marshal hybrid rationale result: %w", marshalErr)
		}
		return string(out), nil
	}

	switch format {
	case "items":
		out, marshalErr := json.MarshalIndent(items, "", "  ")
		if marshalErr != nil {
			return "", fmt.Errorf("marshal hybrid result: %w", marshalErr)
		}
		return string(out), nil

	case "detailed":
		// detailed returns full models.Memory records (matching legacy flag-OFF
		// behaviour) plus per-memory score and optional ranking explanation.
		// items + scoredByID + explByID are already built by the reconsolidation
		// block above, so we can reconstruct the ordered detailed slice here
		// without duplicating the filter pipeline.
		type detailedHybridResult struct {
			*models.Memory
			Score              float64                       `json:"score"`
			RankingExplanation *retrieval.RankingExplanation `json:"ranking_explanation,omitempty"`
		}
		detailed := make([]detailedHybridResult, 0, len(items))
		for _, item := range items {
			sm, ok := scoredByID[item.ID]
			if !ok {
				continue
			}
			dr := detailedHybridResult{
				Memory: sm.Memory,
				Score:  sm.Score,
			}
			if explain {
				if e, ok := explByID[item.ID]; ok {
					dr.RankingExplanation = &e
				}
			}
			detailed = append(detailed, dr)
		}
		out, marshalErr := json.MarshalIndent(detailed, "", "  ")
		if marshalErr != nil {
			return "", fmt.Errorf("marshal hybrid detailed result: %w", marshalErr)
		}
		return string(out), nil
	default: // "text"
		if len(items) == 0 {
			return "No memories found matching the query.", nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d memories for query: %q\n\n", len(items), query))
		for i, r := range items {
			typeLabel := "MEMORY"
			if r.Type != "" {
				typeLabel = strings.ToUpper(r.Type)
			}
			sb.WriteString(fmt.Sprintf("%d. [%s] %s (score: %.3f)\n", i+1, typeLabel, r.Title, r.Score))
			content := r.Content
			if len(content) > 300 {
				content = content[:300] + "..."
			}
			sb.WriteString(fmt.Sprintf("   %s\n", content))
			if len(r.Tags) > 0 {
				sb.WriteString(fmt.Sprintf("   tags: %s\n", strings.Join(r.Tags, ", ")))
			}
			if explain && r.RankingExplanation != nil {
				e := r.RankingExplanation
				sb.WriteString(fmt.Sprintf("   explanation: relevance=%.3f recency=%.3f importance=%.3f fused=%.3f tier=%s\n",
					e.Relevance, e.Recency, e.Importance, e.FusedScore, e.SourceTier))
			}
			sb.WriteString("\n")
		}
		return sb.String(), nil
	}
}

// handleSuppressMemory suppresses a v5 memory via soft-delete in the memories table.
func (s *Server) handleSuppressMemory(ctx context.Context, args json.RawMessage) (string, error) {
	if s.memoryStore == nil {
		return "", fmt.Errorf("memory store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	id, err := requireInt64Arg(m, "id")
	if err != nil || id <= 0 {
		return "", fmt.Errorf("suppress_memory: id must be a positive integer")
	}

	access, err := memoryMutationAccessMCP(ctx, sessionFromContext(ctx))
	if err != nil {
		return "", fmt.Errorf("feedback suppress: memory %d not found", id)
	}
	var beforeMem *models.Memory
	err = s.memoryStore.WithAuthorizedMemories(ctx, []int64{id}, access, func(tx *gormlib.DB, memories map[int64]*models.Memory) error {
		beforeMem = memories[id]
		return s.memoryStore.DeleteTx(ctx, tx, id)
	})
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			return "", fmt.Errorf("feedback suppress: memory %d not found", id)
		}
		return "", fmt.Errorf("feedback suppress: %w", err)
	}

	// Audit: fire-and-forget delete event.
	logAuditDelete(ctx, s, beforeMem, actorFromContext(ctx))

	return fmt.Sprintf("Memory %d suppressed", id), nil
}
