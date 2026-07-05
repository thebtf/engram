package s6

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
)

type recordedOutcomeQuery struct {
	Project string
	Limit   int
}

type recordingOutcomeStore struct {
	queries  []recordedOutcomeQuery
	memories []*models.Memory
}

func (s *recordingOutcomeStore) ListOutcomeCandidates(ctx context.Context, project string, limit int) ([]*models.Memory, error) {
	s.queries = append(s.queries, recordedOutcomeQuery{Project: project, Limit: limit})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]*models.Memory(nil), s.memories...), nil
}

func TestOutcomeProposerImplementsCandidateProposer(t *testing.T) {
	proposer := NewOutcomeProposer(&recordingOutcomeStore{})

	var candidate cognitive.CandidateProposer = proposer
	require.NotNil(t, candidate, "S6 OutcomeProposer must satisfy the S3 CandidateProposer fan-out surface")
}

func TestOutcomeProposerEmptyStoreReturnsEmptyHints(t *testing.T) {
	store := &recordingOutcomeStore{}
	proposer := NewOutcomeProposer(store)

	proposals, err := proposer.Propose(context.Background(), cognitive.AttentionEvent{
		Type:      "user_prompt_submit",
		SessionID: "session-s6-empty",
		Project:   "project-s6-empty",
		Payload:   map[string]interface{}{"text": "look for outcome-ranked memories"},
		Timestamp: time.Unix(1700010000, 0).UTC(),
	}, 5)

	require.NoError(t, err)
	require.NotNil(t, proposals, "empty visible memory sets must return an empty hint list, not a nil success that hides an unimplemented body")
	require.Empty(t, proposals)
	require.Len(t, store.queries, 1, "empty stores should still be queried with the caller's project and bounded limit")
	require.Equal(t, "project-s6-empty", store.queries[0].Project)
	require.Equal(t, 5, store.queries[0].Limit, "S6 must push the caller limit into the bounded memory-source seam")
}

func TestOutcomeProposerEnforcesBoundedLimitAtStoreAndOutput(t *testing.T) {
	createdAt := time.Unix(1700010100, 0).UTC()
	store := &recordingOutcomeStore{
		memories: []*models.Memory{
			outcomeCandidate(101, "project-s6-limit", "best prior", "private body 101", 9, 1, createdAt),
			outcomeCandidate(102, "project-s6-limit", "second prior", "private body 102", 7, 3, createdAt.Add(time.Second)),
			outcomeCandidate(103, "project-s6-limit", "third prior", "private body 103", 5, 5, createdAt.Add(2*time.Second)),
			outcomeCandidate(104, "project-s6-limit", "fourth prior", "private body 104", 3, 7, createdAt.Add(3*time.Second)),
		},
	}
	proposer := NewOutcomeProposer(store)

	proposals, err := proposer.Propose(context.Background(), cognitive.AttentionEvent{
		Type:    "assistant_plan",
		Project: "project-s6-limit",
		Payload: map[string]interface{}{"text": "bounded outcome policy suggestions"},
	}, 2)

	require.NoError(t, err)
	require.Len(t, store.queries, 1)
	require.Equal(t, 2, store.queries[0].Limit, "S6 must bound the source read; over-fetching and truncating after ranking violates the S3 latency budget")
	require.Len(t, proposals, 2, "a buggy store that over-returns must not leak more than the caller's requested limit")
	require.Equal(t, []string{"101", "102"}, proposalIDs(proposals), "limit should keep the two strongest posterior candidates")
}

func TestOutcomeProposerOrdersByOutcomePosterior(t *testing.T) {
	createdAt := time.Unix(1700010200, 0).UTC()
	store := &recordingOutcomeStore{
		memories: []*models.Memory{
			outcomeCandidate(201, "project-s6-order", "low posterior", "low private body", 1, 9, createdAt),
			outcomeCandidate(202, "project-s6-order", "high posterior", "high private body", 8, 2, createdAt),
			outcomeCandidate(203, "project-s6-order", "middle posterior", "middle private body", 3, 3, createdAt),
		},
	}
	proposer := NewOutcomeProposer(store)

	proposals, err := proposer.Propose(context.Background(), cognitive.AttentionEvent{
		Type:    "tool_result_surprise",
		Project: "project-s6-order",
		Payload: map[string]interface{}{"text": "rank by learned outcomes, not insertion order"},
	}, 3)

	require.NoError(t, err)
	require.Equal(t, []string{"202", "203", "201"}, proposalIDs(proposals), "S6 must rank candidates by Thompson posterior outcome signal")
	require.Greater(t, proposals[0].Score, proposals[1].Score, "higher posterior memory should receive a higher proposal score")
	require.Greater(t, proposals[1].Score, proposals[2].Score, "posterior score must keep descending order")
}

func TestOutcomeProposerEmitsOnlyContentFreeHintFields(t *testing.T) {
	createdAt := time.Unix(1700010300, 0).UTC()
	store := &recordingOutcomeStore{memories: []*models.Memory{
		outcomeCandidate(301, "project-s6-content-free", "Release checklist", "needle-private-outcome-body-token must never leave the store", 6, 2, createdAt),
	}}
	proposer := NewOutcomeProposer(store)

	proposals, err := proposer.Propose(context.Background(), cognitive.AttentionEvent{
		Type:    "user_prompt_submit",
		Project: "project-s6-content-free",
		Payload: map[string]interface{}{"text": "needle-private-event-token can guide retrieval but must not be emitted"},
	}, 1)

	require.NoError(t, err)
	require.Len(t, proposals, 1, "a non-empty outcome source must produce an expandable hint, not a silent empty stub")
	require.Equal(t, cognitive.HintProposal{
		ID:        "301",
		Title:     "Memory 301",
		Tags:      []string{"outcome", "policy"},
		CreatedAt: createdAt,
		Source:    "s6.outcome_policy",
	}, cognitive.HintProposal{
		ID:        proposals[0].ID,
		Title:     proposals[0].Title,
		Tags:      proposals[0].Tags,
		CreatedAt: proposals[0].CreatedAt,
		Source:    proposals[0].Source,
	})
	require.NotZero(t, proposals[0].Score, "proposal score must expose the outcome rank signal; a default zero score hides ranking regressions")

	payload, err := json.Marshal(proposals[0])
	require.NoError(t, err)
	serialized := strings.ToLower(string(payload))
	require.NotContains(t, serialized, "content", "S6 proposals must not expose a content field; callers expand explicitly by memory id")
	require.NotContains(t, serialized, "needle-private-outcome-body-token", "S6 proposals must not leak stored memory body text")
	require.NotContains(t, serialized, "needle-private-event-token", "S6 proposals must not echo raw attention-event text into hint payloads")

	var fields map[string]interface{}
	require.NoError(t, json.Unmarshal(payload, &fields))
	allowed := map[string]bool{
		"id": true, "title": true, "tags": true, "created_at": true,
		"score": true, "source": true, "reason": true,
	}
	for field := range fields {
		require.Truef(t, allowed[field], "S6 CandidateProposer leaked field %q in %s", field, string(payload))
	}
}

func TestOutcomeProposerFiltersByProjectScope(t *testing.T) {
	createdAt := time.Unix(1700010400, 0).UTC()
	store := &recordingOutcomeStore{memories: []*models.Memory{
		outcomeCandidate(401, "project-s6-visible", "visible project memory", "visible body", 8, 2, createdAt),
		outcomeCandidate(402, "project-s6-other", "cross-project memory", "other project body", 9, 1, createdAt),
	}}
	proposer := NewOutcomeProposer(store)

	proposals, err := proposer.Propose(context.Background(), cognitive.AttentionEvent{
		Type:    "segment_shift",
		Project: " project-s6-visible ",
		Payload: map[string]interface{}{"text": "project-scoped outcome suggestions"},
	}, 5)

	require.NoError(t, err)
	require.Len(t, store.queries, 1)
	require.Equal(t, "project-s6-visible", store.queries[0].Project, "S6 must trim and pass the attention event project into its bounded source query")
	require.Equal(t, []string{"401"}, proposalIDs(proposals), "S6 must not return memories outside the event project even if the source seam over-returns")
}

func TestOutcomeProposerHonorsCanceledContextBeforeQueryingStore(t *testing.T) {
	store := &recordingOutcomeStore{memories: []*models.Memory{
		outcomeCandidate(501, "project-s6-cancel", "should not be queried", "cancel private body", 9, 1, time.Unix(1700010500, 0).UTC()),
	}}
	proposer := NewOutcomeProposer(store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	proposals, err := proposer.Propose(ctx, cognitive.AttentionEvent{
		Type:    "failed_command",
		Project: "project-s6-cancel",
		Payload: map[string]interface{}{"text": "cancelled outcome request"},
	}, 3)

	require.True(t, errors.Is(err, context.Canceled), "canceled S3 candidate requests must surface context.Canceled, got %v", err)
	require.Empty(t, proposals)
	require.Empty(t, store.queries, "a context canceled before Propose must not spend the S3 latency budget on an outcome store query")
}

func TestOutcomeProposerAdvisoryBoundaryDoesNotLeakMemoryOrEventContent(t *testing.T) {
	createdAt := time.Unix(1700010600, 0).UTC()
	store := &recordingOutcomeStore{memories: []*models.Memory{
		outcomeCandidate(601, "project-s6-advisory-content", "needle-raw-memory-title-secret", "needle-raw-memory-body-secret must stay behind explicit expansion", 8, 2, createdAt),
	}}
	proposer := NewOutcomeProposer(store)

	proposals, err := proposer.Propose(context.Background(), cognitive.AttentionEvent{
		Type:    "user_prompt_submit",
		Project: "project-s6-advisory-content",
		Payload: map[string]interface{}{"text": "needle-raw-event-secret should influence ranking only through the caller, never be echoed"},
	}, 1)

	require.NoError(t, err)
	require.Len(t, proposals, 1, "advisory S6 should return an index-only hint for the selected memory")
	payload, err := json.Marshal(proposals[0])
	require.NoError(t, err)
	serialized := strings.ToLower(string(payload))
	for _, forbidden := range []string{
		"needle-raw-memory-title-secret",
		"needle-raw-memory-body-secret",
		"needle-raw-event-secret",
	} {
		require.NotContains(t, serialized, forbidden, "S6 proposer output must be content-free; %q leaked in %s", forbidden, string(payload))
	}
}

func TestOutcomeProposerAdvisoryBoundaryDoesNotMutateCandidateRows(t *testing.T) {
	createdAt := time.Unix(1700010700, 0).UTC()
	memory := outcomeCandidate(701, "project-s6-read-only", "read only title", "read only private body", 4, 2, createdAt)
	beforeContent := memory.Content
	beforeTags := append([]string(nil), memory.Tags...)
	beforeAlpha := memory.TsAlpha
	beforeBeta := memory.TsBeta
	beforeImportance := memory.ImportanceBase
	store := &recordingOutcomeStore{memories: []*models.Memory{memory}}
	proposer := NewOutcomeProposer(store)

	proposals, err := proposer.Propose(context.Background(), cognitive.AttentionEvent{
		Type:    "segment_shift",
		Project: "project-s6-read-only",
		Payload: map[string]interface{}{"text": "rank without mutating candidate state"},
	}, 1)

	require.NoError(t, err)
	require.Len(t, proposals, 1, "read-only advisory ranking should still return the selected hint")
	require.Equal(t, beforeContent, memory.Content, "proposer must not rewrite candidate content while deriving hints")
	require.Equal(t, beforeTags, memory.Tags, "proposer must copy tags into the hint without mutating the memory row")
	require.Equal(t, beforeAlpha, memory.TsAlpha, "proposer must not update Thompson alpha from the advisory path")
	require.Equal(t, beforeBeta, memory.TsBeta, "proposer must not update Thompson beta from the advisory path")
	require.Equal(t, beforeImportance, memory.ImportanceBase, "proposer must not rewrite significance fields from the advisory path")
}

func TestOutcomeProposerAdvisoryBoundarySourceGuards(t *testing.T) {
	t.Parallel()

	files := []string{"proposer.go", "store_adapter.go", "subsystem.go"}
	forbiddenImports := []string{
		"github.com/thebtf/engram/internal/graph",
		"github.com/thebtf/engram/internal/reranking",
		"github.com/thebtf/engram/internal/search",
	}
	mutationPrefixes := []string{"Create", "Delete", "Enqueue", "Emit", "Insert", "Mark", "Mutate", "Patch", "Promote", "Rate", "Save", "Set", "Store", "Suppress", "Update", "Upsert", "Write"}

	for _, file := range files {
		parsed := parseS6ProductionFile(t, file)
		for _, imported := range parsed.Imports {
			path := strings.Trim(imported.Path.Value, "\"")
			for _, forbidden := range forbiddenImports {
				require.Falsef(t, strings.HasPrefix(path, forbidden), "S6 advisory proposer path must not resurrect demolished v5 graph/rerank/internal-search dependency %q in %s", path, file)
			}
		}
	}

	proposerFile := parseS6ProductionFile(t, "proposer.go")
	for _, decl := range proposerFile.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != "OutcomeStore" {
				continue
			}
			iface, ok := typeSpec.Type.(*ast.InterfaceType)
			require.True(t, ok, "OutcomeStore must remain an interface-shaped bounded read seam")
			for _, method := range iface.Methods.List {
				for _, name := range method.Names {
					assertNoMutationPrefix(t, name.Name, mutationPrefixes, "OutcomeStore")
				}
			}
		}
	}
	ast.Inspect(proposerFile, func(node ast.Node) bool {
		fn, ok := node.(*ast.FuncDecl)
		if ok && fn.Name.Name == "Propose" {
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok {
					assertNoMutationPrefix(t, selector.Sel.Name, mutationPrefixes, "OutcomeProposer.Propose")
				}
				return true
			})
			return false
		}
		return true
	})
}

func parseS6ProductionFile(t *testing.T, file string) *ast.File {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	require.NoError(t, err, "parse S6 production file %s", file)
	return parsed
}

func assertNoMutationPrefix(t *testing.T, name string, prefixes []string, surface string) {
	t.Helper()
	for _, prefix := range prefixes {
		require.Falsef(t, strings.HasPrefix(name, prefix), "%s must remain advisory/read-only; found mutation-shaped call or seam %q", surface, name)
	}
}

func outcomeCandidate(id int64, project, title, content string, alpha, beta float64, createdAt time.Time) *models.Memory {
	return &models.Memory{
		ID:             id,
		Project:        project,
		Content:        title + "\n" + content,
		Tags:           []string{"outcome", "policy"},
		CreatedAt:      createdAt,
		TsAlpha:        alpha,
		TsBeta:         beta,
		ImportanceBase: 0.5,
	}
}

func proposalIDs(proposals []cognitive.HintProposal) []string {
	ids := make([]string, len(proposals))
	for i, proposal := range proposals {
		ids[i] = proposal.ID
	}
	return ids
}
