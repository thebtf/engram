package grpcserver

import (
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

// testCodeIndexDB opens a test PostgreSQL connection and applies all migrations
// via dbgorm.NewStore (which calls runMigrations internally). Tests skip when
// DATABASE_DSN is not set — same pattern as sync_project_state_test.go and
// code_chunk_store_test.go.
func testCodeIndexDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping code_index integration test")
	}
	store, err := dbgorm.NewStore(dbgorm.Config{
		DSN:      dsn,
		LogLevel: logger.Silent,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	sqlDB, _ := store.DB.DB()
	return store.DB, func() { _ = sqlDB.Close() }
}

// codeIndexServer returns a Server wired with the given db, handler=nil is fine
// because CodeIndexNegotiate / CodeIndexUpload do not use the MCP handler.
func codeIndexServer(db *gorm.DB) *Server {
	return &Server{db: db}
}

// seedChunk inserts a single code_chunk row for testing.
func seedChunk(t *testing.T, db *gorm.DB, projectID, filePath, sha string, byteStart, byteEnd int, sessionID string) {
	t.Helper()
	store := dbgorm.NewCodeChunkStore(db)
	require.NoError(t, store.Upsert(context.Background(), &dbgorm.CodeChunk{
		ProjectID:      projectID,
		FilePath:       filePath,
		ByteStart:      byteStart,
		ByteEnd:        byteEnd,
		Language:       "go",
		ChunkType:      "line-block",
		Content:        "func Placeholder() {}",
		ContentSHA256:  sha,
		IndexSessionID: sessionID,
	}))
}

// cleanProject removes all code_chunks for the project at test end.
func cleanProject(t *testing.T, db *gorm.DB, projectID string) {
	t.Helper()
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", projectID).Error
	})
}

// ----------------------------------------------------------------------------
// Pure-logic unit tests (no DB required)
// ----------------------------------------------------------------------------

// TestChunkIDFromPosition verifies that the server-side chunk_id helper produces
// the same value as the client-side codeindex.Chunk.ChunkID().
func TestChunkIDFromPosition(t *testing.T) {
	t.Parallel()
	// Reference value computed by codeindex.Chunk{FilePath:"internal/foo.go", ByteStart:0}.ChunkID()
	// sha256("internal/foo.go:0")[:8] as hex.
	got := chunkIDFromPosition("internal/foo.go", 0)
	require.Len(t, got, 16, "chunk_id must be 16 hex chars")

	// Stability: same input → same output.
	got2 := chunkIDFromPosition("internal/foo.go", 0)
	require.Equal(t, got, got2, "chunkIDFromPosition must be deterministic")

	// Different position → different id.
	other := chunkIDFromPosition("internal/foo.go", 100)
	require.NotEqual(t, got, other, "different byteStart must produce different chunk_id")
}

// TestCodeIndexNegotiate_ValidationErrors exercises the InvalidArgument path
// without touching the database.
func TestCodeIndexNegotiate_ValidationErrors(t *testing.T) {
	t.Parallel()

	srv := &Server{} // nil db — should fail with Unavailable after validation passes

	tests := []struct {
		name      string
		projectID string
		sessionID string
		wantCode  codes.Code
	}{
		{"empty project_id", "", "sess-1", codes.InvalidArgument},
		{"empty session_id", "proj-1", "", codes.InvalidArgument},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := srv.CodeIndexNegotiate(context.Background(), &pb.CodeIndexNegotiateRequest{
				ProjectId:      tc.projectID,
				IndexSessionId: tc.sessionID,
			})
			require.Error(t, err)
			require.Equal(t, tc.wantCode, status.Code(err))
		})
	}
}

// TestCodeIndexNegotiate_NilDB verifies the nil-db guard returns Unavailable.
func TestCodeIndexNegotiate_NilDB(t *testing.T) {
	t.Parallel()
	srv := &Server{db: nil}
	_, err := srv.CodeIndexNegotiate(context.Background(), &pb.CodeIndexNegotiateRequest{
		ProjectId:      "proj",
		IndexSessionId: "sess",
	})
	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err))
}

// ----------------------------------------------------------------------------
// DB-backed integration tests
// ----------------------------------------------------------------------------

// TestCodeIndexNegotiate_FirstIndex covers the scenario where the server has no
// chunks for the project. Every manifest entry must appear in need_chunks;
// stale_chunks must be empty.
func TestCodeIndexNegotiate_FirstIndex(t *testing.T) {
	db, cleanup := testCodeIndexDB(t)
	defer cleanup()

	proj := fmt.Sprintf("ci-neg-first-%d", time.Now().UnixNano())
	cleanProject(t, db, proj)

	srv := codeIndexServer(db)
	resp, err := srv.CodeIndexNegotiate(context.Background(), &pb.CodeIndexNegotiateRequest{
		ProjectId:      proj,
		IndexSessionId: "sess-first",
		Manifest: []*pb.CodeChunkMeta{
			{ChunkId: "aabbccdd00112233", FilePath: "main.go", ByteStart: 0, ByteEnd: 100, ContentSha256: "sha-a"},
			{ChunkId: "1122334455667788", FilePath: "main.go", ByteStart: 100, ByteEnd: 200, ContentSha256: "sha-b"},
		},
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"aabbccdd00112233", "1122334455667788"}, resp.GetNeedChunks(),
		"all chunks must be needed on first index")
	require.Empty(t, resp.GetStaleChunks(), "no stale chunks on first index")
}

// TestCodeIndexNegotiate_UnchangedReindex covers the scenario where the client
// re-indexes with an identical manifest. need_chunks must be empty; stale_chunks
// must also be empty.
func TestCodeIndexNegotiate_UnchangedReindex(t *testing.T) {
	db, cleanup := testCodeIndexDB(t)
	defer cleanup()

	proj := fmt.Sprintf("ci-neg-unchanged-%d", time.Now().UnixNano())
	cleanProject(t, db, proj)

	// Pre-seed two chunks with the same sha as the client will report.
	seedChunk(t, db, proj, "main.go", "sha-a", 0, 100, "sess-old")
	seedChunk(t, db, proj, "main.go", "sha-b", 100, 200, "sess-old")

	srv := codeIndexServer(db)
	resp, err := srv.CodeIndexNegotiate(context.Background(), &pb.CodeIndexNegotiateRequest{
		ProjectId:      proj,
		IndexSessionId: "sess-new",
		Manifest: []*pb.CodeChunkMeta{
			{ChunkId: chunkIDFromPosition("main.go", 0), FilePath: "main.go", ByteStart: 0, ByteEnd: 100, ContentSha256: "sha-a"},
			{ChunkId: chunkIDFromPosition("main.go", 100), FilePath: "main.go", ByteStart: 100, ByteEnd: 200, ContentSha256: "sha-b"},
		},
	})
	require.NoError(t, err)
	require.Empty(t, resp.GetNeedChunks(), "unchanged chunks must not appear in need_chunks")
	require.Empty(t, resp.GetStaleChunks(), "no stale chunks when manifest matches server exactly")
}

// TestCodeIndexNegotiate_ChangedContent covers the scenario where a chunk's
// content changed (new sha256). The old key is stale; the new key is needed.
func TestCodeIndexNegotiate_ChangedContent(t *testing.T) {
	db, cleanup := testCodeIndexDB(t)
	defer cleanup()

	proj := fmt.Sprintf("ci-neg-changed-%d", time.Now().UnixNano())
	cleanProject(t, db, proj)

	// Server has sha-old for main.go:0.
	seedChunk(t, db, proj, "main.go", "sha-old", 0, 100, "sess-old")

	srv := codeIndexServer(db)
	// Client reports sha-new for the same position.
	resp, err := srv.CodeIndexNegotiate(context.Background(), &pb.CodeIndexNegotiateRequest{
		ProjectId:      proj,
		IndexSessionId: "sess-new",
		Manifest: []*pb.CodeChunkMeta{
			{ChunkId: chunkIDFromPosition("main.go", 0), FilePath: "main.go", ByteStart: 0, ByteEnd: 100, ContentSha256: "sha-new"},
		},
	})
	require.NoError(t, err)
	require.Contains(t, resp.GetNeedChunks(), chunkIDFromPosition("main.go", 0),
		"changed chunk must appear in need_chunks")
	require.Contains(t, resp.GetStaleChunks(), chunkIDFromPosition("main.go", 0),
		"old server chunk must appear in stale_chunks")
}

// TestCodeIndexNegotiate_DeletedFile covers the scenario where the client's
// manifest no longer includes a file the server has. The server chunk must
// appear in stale_chunks.
func TestCodeIndexNegotiate_DeletedFile(t *testing.T) {
	db, cleanup := testCodeIndexDB(t)
	defer cleanup()

	proj := fmt.Sprintf("ci-neg-deleted-%d", time.Now().UnixNano())
	cleanProject(t, db, proj)

	// Server has chunks for both main.go and deleted.go.
	seedChunk(t, db, proj, "main.go", "sha-main", 0, 100, "sess-old")
	seedChunk(t, db, proj, "deleted.go", "sha-del", 0, 50, "sess-old")

	srv := codeIndexServer(db)
	// Client only reports main.go — deleted.go is gone.
	resp, err := srv.CodeIndexNegotiate(context.Background(), &pb.CodeIndexNegotiateRequest{
		ProjectId:      proj,
		IndexSessionId: "sess-new",
		Manifest: []*pb.CodeChunkMeta{
			{ChunkId: chunkIDFromPosition("main.go", 0), FilePath: "main.go", ByteStart: 0, ByteEnd: 100, ContentSha256: "sha-main"},
		},
	})
	require.NoError(t, err)
	require.Empty(t, resp.GetNeedChunks(), "main.go unchanged, nothing to upload")
	require.Contains(t, resp.GetStaleChunks(), chunkIDFromPosition("deleted.go", 0),
		"deleted.go chunk must appear in stale_chunks")
}

// ----------------------------------------------------------------------------
// CodeIndexUpload tests
// ----------------------------------------------------------------------------

// fakeUploadStream implements grpc.ClientStreamingServer[pb.CodeChunkUpload, pb.CodeIndexUploadReceipt]
// (aliased as pb.EngramService_CodeIndexUploadServer). It replays a pre-built
// message slice and captures the SendAndClose receipt.
//
// The embedded noopServerStream satisfies the full grpc.ServerStream interface
// so we only need to override the three methods that CodeIndexUpload actually calls:
// Recv, SendAndClose, and Context.
type fakeUploadStream struct {
	noopServerStream
	msgs    []*pb.CodeChunkUpload
	pos     int
	receipt *pb.CodeIndexUploadReceipt
	ctx     context.Context
}

// noopServerStream satisfies grpc.ServerStream for test fakes that do not need
// real gRPC transport plumbing. All methods are no-ops or return zero values.
type noopServerStream struct{}

func (noopServerStream) SetHeader(metadata.MD) error  { return nil }
func (noopServerStream) SendHeader(metadata.MD) error { return nil }
func (noopServerStream) SetTrailer(metadata.MD)       {}
func (noopServerStream) Context() context.Context     { return context.Background() }
func (noopServerStream) SendMsg(any) error            { return nil }
func (noopServerStream) RecvMsg(any) error            { return nil }

func newFakeUploadStream(ctx context.Context, msgs []*pb.CodeChunkUpload) *fakeUploadStream {
	return &fakeUploadStream{msgs: msgs, ctx: ctx}
}

func (f *fakeUploadStream) Recv() (*pb.CodeChunkUpload, error) {
	if f.pos >= len(f.msgs) {
		return nil, io.EOF
	}
	msg := f.msgs[f.pos]
	f.pos++
	return msg, nil
}

func (f *fakeUploadStream) SendAndClose(r *pb.CodeIndexUploadReceipt) error {
	f.receipt = r
	return nil
}

func (f *fakeUploadStream) Context() context.Context { return f.ctx }

// Ensure fakeUploadStream satisfies the server interface at compile time.
var _ grpc.ClientStreamingServer[pb.CodeChunkUpload, pb.CodeIndexUploadReceipt] = (*fakeUploadStream)(nil)

// TestCodeIndexUpload_NilDB verifies the nil-db guard.
func TestCodeIndexUpload_NilDB(t *testing.T) {
	t.Parallel()
	srv := &Server{db: nil}
	err := srv.CodeIndexUpload(newFakeUploadStream(context.Background(), nil))
	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err))
}

// TestCodeIndexUpload_EmptyStream verifies that an empty stream returns an empty
// receipt without running the sweep (projectID is unknown).
func TestCodeIndexUpload_EmptyStream(t *testing.T) {
	db, cleanup := testCodeIndexDB(t)
	defer cleanup()

	srv := codeIndexServer(db)
	stream := newFakeUploadStream(context.Background(), nil)
	require.NoError(t, srv.CodeIndexUpload(stream))
	require.NotNil(t, stream.receipt)
	require.Equal(t, int32(0), stream.receipt.GetEmbedded())
	require.Equal(t, int32(0), stream.receipt.GetDeleted())
}

// TestCodeIndexUpload_InsertsChunks verifies that streaming chunks results in
// the correct embedded count and the rows can be retrieved from the DB.
func TestCodeIndexUpload_InsertsChunks(t *testing.T) {
	db, cleanup := testCodeIndexDB(t)
	defer cleanup()

	proj := fmt.Sprintf("ci-upload-insert-%d", time.Now().UnixNano())
	cleanProject(t, db, proj)

	msgs := []*pb.CodeChunkUpload{
		{
			ProjectId:      proj,
			IndexSessionId: "sess-upload",
			Meta:           &pb.CodeChunkMeta{ChunkId: "aabb", FilePath: "a.go", ByteStart: 0, ByteEnd: 50, Language: "go", ChunkType: "line-block", ContentSha256: "sha-aa"},
			Content:        []byte("func A() {}"),
		},
		{
			ProjectId:      proj,
			IndexSessionId: "sess-upload",
			Meta:           &pb.CodeChunkMeta{ChunkId: "ccdd", FilePath: "b.go", ByteStart: 0, ByteEnd: 50, Language: "go", ChunkType: "line-block", ContentSha256: "sha-bb"},
			Content:        []byte("func B() {}"),
		},
	}

	srv := codeIndexServer(db)
	stream := newFakeUploadStream(context.Background(), msgs)
	require.NoError(t, srv.CodeIndexUpload(stream))
	require.Equal(t, int32(2), stream.receipt.GetEmbedded())

	store := dbgorm.NewCodeChunkStore(db)
	count, err := store.CountByProject(context.Background(), proj)
	require.NoError(t, err)
	require.Equal(t, int64(2), count, "two chunks must be persisted")
}

// TestCodeIndexUpload_SweepsStaleAfterNegotiate is the end-to-end scenario:
// Negotiate marks survivors, Upload inserts new chunks, sweep removes old ones.
func TestCodeIndexUpload_SweepsStaleAfterNegotiate(t *testing.T) {
	db, cleanup := testCodeIndexDB(t)
	defer cleanup()

	proj := fmt.Sprintf("ci-upload-sweep-%d", time.Now().UnixNano())
	cleanProject(t, db, proj)
	store := dbgorm.NewCodeChunkStore(db)

	// Seed an old chunk that the client will NOT include in its new manifest.
	seedChunk(t, db, proj, "old.go", "sha-old", 0, 100, "sess-old")

	// Negotiate: client only has new.go.
	srv := codeIndexServer(db)
	_, err := srv.CodeIndexNegotiate(context.Background(), &pb.CodeIndexNegotiateRequest{
		ProjectId:      proj,
		IndexSessionId: "sess-new",
		Manifest: []*pb.CodeChunkMeta{
			{ChunkId: chunkIDFromPosition("new.go", 0), FilePath: "new.go", ByteStart: 0, ByteEnd: 80, ContentSha256: "sha-new"},
		},
	})
	require.NoError(t, err)

	// Upload the new chunk.
	msgs := []*pb.CodeChunkUpload{{
		ProjectId:      proj,
		IndexSessionId: "sess-new",
		Meta:           &pb.CodeChunkMeta{ChunkId: chunkIDFromPosition("new.go", 0), FilePath: "new.go", ByteStart: 0, ByteEnd: 80, Language: "go", ChunkType: "line-block", ContentSha256: "sha-new"},
		Content:        []byte("func New() {}"),
	}}
	stream := newFakeUploadStream(context.Background(), msgs)
	require.NoError(t, srv.CodeIndexUpload(stream))

	require.Equal(t, int32(1), stream.receipt.GetEmbedded(), "one chunk uploaded")
	require.Equal(t, int32(1), stream.receipt.GetDeleted(), "old.go chunk must be swept")

	// Verify only new.go remains.
	chunks, err := store.ListByProject(context.Background(), proj, 10)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.Equal(t, "new.go", chunks[0].FilePath)
}

// TestCodeIndexUpload_SentinelOnlySweepsDeletedFiles is the delete-only re-index
// case: the client's manifest dropped every file, so the negotiate delta is empty
// and the client uploads ZERO content chunks — only the leading identity-only
// sentinel (Meta == nil). The sweep at EOF must still run because the sentinel
// carried the project/session, removing the now-absent rows. Without the sentinel
// path the server would receive no identity, skip the sweep, and leak the deleted
// files' chunks as phantom search results.
func TestCodeIndexUpload_SentinelOnlySweepsDeletedFiles(t *testing.T) {
	db, cleanup := testCodeIndexDB(t)
	defer cleanup()

	proj := fmt.Sprintf("ci-upload-sentinel-%d", time.Now().UnixNano())
	cleanProject(t, db, proj)
	store := dbgorm.NewCodeChunkStore(db)

	// Seed two old chunks the client will NOT include in its new (empty) manifest.
	seedChunk(t, db, proj, "gone1.go", "sha-1", 0, 100, "sess-old")
	seedChunk(t, db, proj, "gone2.go", "sha-2", 0, 100, "sess-old")

	// Negotiate with an empty manifest (all files deleted). TouchSession marks
	// nothing; need_chunks is empty; stale_chunks lists both old chunks.
	srv := codeIndexServer(db)
	resp, err := srv.CodeIndexNegotiate(context.Background(), &pb.CodeIndexNegotiateRequest{
		ProjectId:      proj,
		IndexSessionId: "sess-new",
		Manifest:       nil,
	})
	require.NoError(t, err)
	require.Empty(t, resp.GetNeedChunks(), "empty manifest needs nothing")
	require.Len(t, resp.GetStaleChunks(), 2, "both old chunks are stale")

	// Upload only the identity-only sentinel (Meta == nil, no content) — exactly
	// what the client sends when the delta is empty.
	msgs := []*pb.CodeChunkUpload{{
		ProjectId:      proj,
		IndexSessionId: "sess-new",
		// Meta intentionally nil; Content intentionally empty.
	}}
	stream := newFakeUploadStream(context.Background(), msgs)
	require.NoError(t, srv.CodeIndexUpload(stream))

	require.Equal(t, int32(0), stream.receipt.GetEmbedded(), "no chunks embedded")
	require.Equal(t, int32(2), stream.receipt.GetDeleted(),
		"both deleted files' chunks must be swept via the sentinel-carried session")

	count, err := store.CountByProject(context.Background(), proj)
	require.NoError(t, err)
	require.Equal(t, int64(0), count, "index must be empty after a delete-only re-index")
}

// TestCodeIndexUpload_MissingIdentityRejects verifies that a first message with
// no project_id (neither sentinel nor real chunk established identity) is rejected
// rather than silently skipping the sweep.
func TestCodeIndexUpload_MissingIdentityRejects(t *testing.T) {
	t.Parallel()
	srv := &Server{db: &gorm.DB{}} // non-nil db to pass the guard; never queried
	msgs := []*pb.CodeChunkUpload{{
		// ProjectId empty, Meta nil — malformed first message.
	}}
	stream := newFakeUploadStream(context.Background(), msgs)
	err := srv.CodeIndexUpload(stream)
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestCodeIndexUpload_EmptySessionSafetyGuard verifies that the safety guard in
// DeleteBySessionMismatch prevents the sweep when no row carries the session id
// (i.e. a stray Upload without a prior Negotiate).
func TestCodeIndexUpload_EmptySessionSafetyGuard(t *testing.T) {
	db, cleanup := testCodeIndexDB(t)
	defer cleanup()

	proj := fmt.Sprintf("ci-upload-guard-%d", time.Now().UnixNano())
	cleanProject(t, db, proj)
	store := dbgorm.NewCodeChunkStore(db)

	// Seed a chunk with session "sess-existing".
	seedChunk(t, db, proj, "existing.go", "sha-exist", 0, 100, "sess-existing")

	// Upload with a brand-new session that was never negotiated.
	// The safety guard must prevent the sweep of the existing chunk.
	msgs := []*pb.CodeChunkUpload{{
		ProjectId:      proj,
		IndexSessionId: "sess-orphan",
		Meta:           &pb.CodeChunkMeta{ChunkId: "orphan01", FilePath: "orphan.go", ByteStart: 0, ByteEnd: 40, Language: "go", ChunkType: "line-block", ContentSha256: "sha-orphan"},
		Content:        []byte("func Orphan() {}"),
	}}
	srv := codeIndexServer(db)
	stream := newFakeUploadStream(context.Background(), msgs)
	require.NoError(t, srv.CodeIndexUpload(stream))

	require.Equal(t, int32(1), stream.receipt.GetEmbedded())
	require.Equal(t, int32(0), stream.receipt.GetDeleted(),
		"safety guard must prevent sweep when no rows carry the orphan session")

	// Both chunks must still exist.
	count, err := store.CountByProject(context.Background(), proj)
	require.NoError(t, err)
	require.Equal(t, int64(2), count, "existing chunk must not be swept by un-negotiated upload")
}

// TestCodeIndexUpload_MixedSessionRejects verifies that a mid-stream project_id
// change returns InvalidArgument.
func TestCodeIndexUpload_MixedSessionRejects(t *testing.T) {
	db, cleanup := testCodeIndexDB(t)
	defer cleanup()

	proj := fmt.Sprintf("ci-upload-mixed-%d", time.Now().UnixNano())
	cleanProject(t, db, proj)

	msgs := []*pb.CodeChunkUpload{
		{
			ProjectId: proj, IndexSessionId: "sess-1",
			Meta:    &pb.CodeChunkMeta{ChunkId: "aa", FilePath: "a.go", ByteStart: 0, ByteEnd: 10, ContentSha256: "sha-a"},
			Content: []byte("func A(){}"),
		},
		{
			ProjectId: "DIFFERENT-PROJECT", IndexSessionId: "sess-1",
			Meta:    &pb.CodeChunkMeta{ChunkId: "bb", FilePath: "b.go", ByteStart: 0, ByteEnd: 10, ContentSha256: "sha-b"},
			Content: []byte("func B(){}"),
		},
	}
	srv := codeIndexServer(db)
	stream := newFakeUploadStream(context.Background(), msgs)
	err := srv.CodeIndexUpload(stream)
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
