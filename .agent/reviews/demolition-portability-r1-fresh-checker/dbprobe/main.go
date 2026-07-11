package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgconn"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
)

const (
	project = "demolition-portability-r1-fresh-checker"
	session = "demolition-portability-r1-fresh-checker"
)

type result struct {
	NilCreateMetadata      string `json:"nil_create_metadata"`
	EmptyCreateMetadata    string `json:"empty_create_metadata"`
	NilUpdateMetadata      string `json:"nil_update_metadata"`
	EmptyUpdateMetadata    string `json:"empty_update_metadata"`
	InvalidCreateRejected  bool   `json:"invalid_create_rejected"`
	InvalidUpdateRejected  bool   `json:"invalid_update_rejected"`
	InvalidUpdatePreserved bool   `json:"invalid_update_preserved"`
	DBErrorMutatedMetadata string `json:"db_error_mutated_metadata"`
	DBErrorObserved        bool   `json:"db_error_observed"`
	ForeignKeySQLState     string `json:"foreign_key_sqlstate"`
	ForeignKeyConstraint   string `json:"foreign_key_constraint"`
	RejectedDanglingRows   int64  `json:"rejected_dangling_rows"`
	ResolveDangling        bool   `json:"resolve_dangling"`
	ResolveSourcePreserved bool   `json:"resolve_source_preserved"`
	LiveEnumAccepted       bool   `json:"live_enum_accepted"`
	StaleEnumRejected      bool   `json:"stale_enum_rejected"`
	FinalMemoryRows        int64  `json:"final_memory_rows"`
	FinalNodeRows          int64  `json:"final_node_rows"`
	FinalEdgeRows          int64  `json:"final_edge_rows"`
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func mustJSONEq(got []byte, want string) string {
	var gotValue any
	var wantValue any
	must(json.Unmarshal(got, &gotValue))
	must(json.Unmarshal([]byte(want), &wantValue))
	if fmt.Sprintf("%#v", gotValue) != fmt.Sprintf("%#v", wantValue) {
		panic(fmt.Sprintf("JSON mismatch: got %s want %s", got, want))
	}
	return string(got)
}

func cleanup(ctx context.Context, store *dbgorm.Store) {
	must(store.DB.WithContext(ctx).Exec(`DELETE FROM knowledge_edges WHERE source_session_id = ?`, session).Error)
	must(store.DB.WithContext(ctx).Exec(`DELETE FROM knowledge_nodes WHERE project = ?`, project).Error)
	must(store.DB.WithContext(ctx).Exec(`DELETE FROM memories WHERE project = ?`, project).Error)
}

func insertMemory(ctx context.Context, store *dbgorm.Store, content string) int64 {
	var id int64
	must(store.DB.WithContext(ctx).Raw(
		`INSERT INTO memories (project, content) VALUES (?, ?) RETURNING id`,
		project, content,
	).Row().Scan(&id))
	return id
}

func ptr(v int64) *int64 { return &v }

func main() {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		panic("DATABASE_DSN is required")
	}
	ctx := context.Background()
	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	must(err)
	cleanup(ctx, store)
	nodes := graph.NewNodesStore(store.GetDB())
	out := result{}

	nilNode, err := nodes.Create(ctx, &models.KnowledgeNode{
		NodeType: models.NodeTypeRule, ExternalRef: "nil-create", Project: project,
	})
	must(err)
	out.NilCreateMetadata = mustJSONEq(nilNode.Metadata, `{}`)

	emptyNode, err := nodes.Create(ctx, &models.KnowledgeNode{
		NodeType: models.NodeTypeRule, ExternalRef: "empty-create", Project: project, Metadata: []byte{},
	})
	must(err)
	out.EmptyCreateMetadata = mustJSONEq(emptyNode.Metadata, `{}`)

	nilNode.Metadata = nil
	nilNode.ExternalRef = "nil-update"
	nilNode, err = nodes.Update(ctx, nilNode)
	must(err)
	out.NilUpdateMetadata = mustJSONEq(nilNode.Metadata, `{}`)

	emptyNode.Metadata = []byte{}
	emptyNode.ExternalRef = "empty-update"
	emptyNode, err = nodes.Update(ctx, emptyNode)
	must(err)
	out.EmptyUpdateMetadata = mustJSONEq(emptyNode.Metadata, `{}`)

	invalidCreate := &models.KnowledgeNode{
		NodeType: models.NodeTypeRule, ExternalRef: "invalid-create", Project: project, Metadata: []byte(`{`),
	}
	_, err = nodes.Create(ctx, invalidCreate)
	if err == nil {
		panic("invalid create JSON unexpectedly succeeded")
	}
	out.InvalidCreateRejected = true
	var invalidCreateRows int64
	must(store.DB.WithContext(ctx).Table("knowledge_nodes").Where("project = ? AND external_ref = ?", project, "invalid-create").Count(&invalidCreateRows).Error)
	if invalidCreateRows != 0 {
		panic(fmt.Sprintf("invalid create left %d rows", invalidCreateRows))
	}

	beforeInvalidUpdate := emptyNode.Metadata
	emptyNode.Metadata = []byte(`{`)
	_, err = nodes.Update(ctx, emptyNode)
	if err == nil {
		panic("invalid update JSON unexpectedly succeeded")
	}
	out.InvalidUpdateRejected = true
	reloaded, err := nodes.Get(ctx, emptyNode.ID, true)
	must(err)
	out.InvalidUpdatePreserved = mustJSONEq(reloaded.Metadata, string(beforeInvalidUpdate)) != ""

	sourceID := insertMemory(ctx, store, "fk-source")
	targetID := insertMemory(ctx, store, "fk-target")
	must(store.DB.WithContext(ctx).Exec(`DELETE FROM memories WHERE id = ?`, targetID).Error)
	insert := store.DB.WithContext(ctx).Exec(`
		INSERT INTO knowledge_edges
			(source_id, target_id, edge_type, weight, source_session_id, source_type, target_type)
		VALUES (?, ?, 'uses', 1.0, ?, 'memory', 'memory')
	`, sourceID, targetID, session)
	if insert.Error == nil {
		panic("dangling edge insert unexpectedly succeeded")
	}
	var pgErr *pgconn.PgError
	if !errors.As(insert.Error, &pgErr) {
		panic(fmt.Sprintf("dangling error is not pg error: %T %v", insert.Error, insert.Error))
	}
	out.ForeignKeySQLState = pgErr.Code
	out.ForeignKeyConstraint = pgErr.ConstraintName
	if pgErr.Code != "23503" || pgErr.ConstraintName != "knowledge_edges_target_id_fkey" {
		panic(fmt.Sprintf("unexpected FK error: %s %s", pgErr.Code, pgErr.ConstraintName))
	}
	must(store.DB.WithContext(ctx).Table("knowledge_edges").Where("source_session_id = ?", session).Count(&out.RejectedDanglingRows).Error)
	if out.RejectedDanglingRows != 0 {
		panic(fmt.Sprintf("rejected dangling insert left %d rows", out.RejectedDanglingRows))
	}

	secondID := insertMemory(ctx, store, "enum-target")
	edges := graph.NewStore(store.GetDB(), nodes)
	source, target, err := edges.Resolve(ctx, &graph.Edge{
		SourceID: ptr(sourceID), TargetID: ptr(targetID), SourceType: "memory", TargetType: "memory",
	})
	if !errors.Is(err, graph.ErrDangling) || target != nil {
		panic(fmt.Sprintf("Resolve did not report dangling target: source=%v target=%v err=%v", source, target, err))
	}
	out.ResolveDangling = true
	out.ResolveSourcePreserved = source == sourceID
	if !out.ResolveSourcePreserved {
		panic(fmt.Sprintf("Resolve did not preserve valid source: got %v want %d", source, sourceID))
	}

	_, err = edges.Create(ctx, &graph.Edge{
		SourceID: ptr(sourceID), TargetID: ptr(secondID), EdgeType: graph.EdgeDependsOn,
		Weight: 1, SourceSessionID: session, SourceType: "memory", TargetType: "memory",
	})
	must(err)
	out.LiveEnumAccepted = true
	_, err = edges.Create(ctx, &graph.Edge{
		SourceID: ptr(sourceID), TargetID: ptr(secondID), EdgeType: "references",
		Weight: 1, SourceSessionID: session, SourceType: "memory", TargetType: "memory",
	})
	if err == nil {
		panic("stale references edge type unexpectedly succeeded")
	}
	out.StaleEnumRejected = true

	cleanup(ctx, store)
	must(store.DB.WithContext(ctx).Table("memories").Where("project = ?", project).Count(&out.FinalMemoryRows).Error)
	must(store.DB.WithContext(ctx).Table("knowledge_nodes").Where("project = ?", project).Count(&out.FinalNodeRows).Error)
	must(store.DB.WithContext(ctx).Table("knowledge_edges").Where("source_session_id = ?", session).Count(&out.FinalEdgeRows).Error)
	if out.FinalMemoryRows != 0 || out.FinalNodeRows != 0 || out.FinalEdgeRows != 0 {
		panic(fmt.Sprintf("fixture residue: memories=%d nodes=%d edges=%d", out.FinalMemoryRows, out.FinalNodeRows, out.FinalEdgeRows))
	}
	must(store.Close())

	closedStore, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 1})
	must(err)
	closedNodes := graph.NewNodesStore(closedStore.GetDB())
	must(closedStore.Close())
	dbErrorNode := &models.KnowledgeNode{
		NodeType: models.NodeTypeRule, ExternalRef: "closed-db", Project: project,
	}
	_, err = closedNodes.Create(ctx, dbErrorNode)
	if err == nil {
		panic("closed DB create unexpectedly succeeded")
	}
	out.DBErrorObserved = true
	out.DBErrorMutatedMetadata = mustJSONEq(dbErrorNode.Metadata, `{}`)

	encoded, err := json.MarshalIndent(out, "", "  ")
	must(err)
	fmt.Println(string(encoded))
}
