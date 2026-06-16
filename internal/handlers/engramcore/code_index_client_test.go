package engramcore

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/thebtf/engram/internal/codeindex"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

// TestManifestToProto verifies that manifestToProto maps every ManifestEntry
// field to the correct proto field without loss or reordering.
func TestManifestToProto(t *testing.T) {
	t.Parallel()

	manifest := codeindex.Manifest{
		{
			FilePath:      "internal/foo.go",
			ChunkID:       "aabb1122ccdd3344",
			ContentSHA256: "sha256-aaa",
			ByteStart:     0,
			ByteEnd:       512,
			Language:      "go",
			ChunkType:     codeindex.ChunkTypeLineBlock,
		},
		{
			FilePath:      "internal/bar.go",
			ChunkID:       "11223344aabbccdd",
			ContentSHA256: "sha256-bbb",
			ByteStart:     512,
			ByteEnd:       1024,
			Language:      "go",
			ChunkType:     codeindex.ChunkTypeLineBlock,
		},
	}

	metas := manifestToProto(manifest)
	require.Len(t, metas, 2, "one meta per manifest entry")

	require.Equal(t, "aabb1122ccdd3344", metas[0].GetChunkId())
	require.Equal(t, "internal/foo.go", metas[0].GetFilePath())
	require.Equal(t, int32(0), metas[0].GetByteStart())
	require.Equal(t, int32(512), metas[0].GetByteEnd())
	require.Equal(t, "go", metas[0].GetLanguage())
	require.Equal(t, "line-block", metas[0].GetChunkType())
	require.Equal(t, "sha256-aaa", metas[0].GetContentSha256())

	require.Equal(t, "11223344aabbccdd", metas[1].GetChunkId())
	require.Equal(t, "internal/bar.go", metas[1].GetFilePath())
	require.Equal(t, int32(512), metas[1].GetByteStart())
	require.Equal(t, int32(1024), metas[1].GetByteEnd())
	require.Equal(t, "sha256-bbb", metas[1].GetContentSha256())
}

// TestManifestToProto_Empty verifies that an empty manifest produces an empty
// (not nil) slice.
func TestManifestToProto_Empty(t *testing.T) {
	t.Parallel()
	metas := manifestToProto(codeindex.Manifest{})
	require.NotNil(t, metas)
	require.Len(t, metas, 0)
}

// TestChunkToProtoMeta verifies the single-chunk mapping, including that
// ChunkID() is called (not a stored field).
func TestChunkToProtoMeta(t *testing.T) {
	t.Parallel()

	c := codeindex.Chunk{
		FilePath:      "cmd/main.go",
		ByteStart:     0,
		ByteEnd:       256,
		Language:      "go",
		ChunkType:     codeindex.ChunkTypeLineBlock,
		Content:       "func main() {}",
		ContentSHA256: "sha-main",
	}

	meta := chunkToProtoMeta(c)
	require.Equal(t, c.ChunkID(), meta.GetChunkId(), "ChunkID() must be called for the id field")
	require.Equal(t, "cmd/main.go", meta.GetFilePath())
	require.Equal(t, int32(0), meta.GetByteStart())
	require.Equal(t, int32(256), meta.GetByteEnd())
	require.Equal(t, "go", meta.GetLanguage())
	require.Equal(t, "line-block", meta.GetChunkType())
	require.Equal(t, "sha-main", meta.GetContentSha256())
}

// TestNeedSetFiltering verifies the need-set filtering logic: only chunks
// whose ChunkID() appears in the server's need_chunks response are selected
// for upload.
func TestNeedSetFiltering(t *testing.T) {
	t.Parallel()

	chunks := []codeindex.Chunk{
		{FilePath: "a.go", ByteStart: 0, ByteEnd: 100, Content: "func A(){}", ContentSHA256: "sha-a", Language: "go", ChunkType: codeindex.ChunkTypeLineBlock},
		{FilePath: "b.go", ByteStart: 0, ByteEnd: 100, Content: "func B(){}", ContentSHA256: "sha-b", Language: "go", ChunkType: codeindex.ChunkTypeLineBlock},
		{FilePath: "c.go", ByteStart: 0, ByteEnd: 100, Content: "func C(){}", ContentSHA256: "sha-c", Language: "go", ChunkType: codeindex.ChunkTypeLineBlock},
	}

	// Simulate server saying it only needs a.go and c.go.
	needChunks := []string{chunks[0].ChunkID(), chunks[2].ChunkID()}
	needSet := make(map[string]struct{}, len(needChunks))
	for _, id := range needChunks {
		needSet[id] = struct{}{}
	}

	var selected []codeindex.Chunk
	for _, c := range chunks {
		if _, needed := needSet[c.ChunkID()]; needed {
			selected = append(selected, c)
		}
	}

	require.Len(t, selected, 2, "only needed chunks must be selected")
	require.Equal(t, "a.go", selected[0].FilePath)
	require.Equal(t, "c.go", selected[1].FilePath)
}

// TestNeedSetFiltering_NoneNeeded verifies that when the server reports no
// need_chunks, nothing is uploaded.
func TestNeedSetFiltering_NoneNeeded(t *testing.T) {
	t.Parallel()

	chunks := []codeindex.Chunk{
		{FilePath: "a.go", ByteStart: 0, ByteEnd: 100, Content: "func A(){}", ContentSHA256: "sha-a", Language: "go", ChunkType: codeindex.ChunkTypeLineBlock},
	}

	needSet := map[string]struct{}{} // empty — server has everything
	var selected []codeindex.Chunk
	for _, c := range chunks {
		if _, needed := needSet[c.ChunkID()]; needed {
			selected = append(selected, c)
		}
	}
	require.Empty(t, selected, "no chunks selected when server has everything")
}

// TestNewSessionID verifies that newSessionID returns a non-empty string and
// that two calls produce different values.
func TestNewSessionID(t *testing.T) {
	t.Parallel()
	a := newSessionID()
	b := newSessionID()
	require.NotEmpty(t, a)
	require.NotEmpty(t, b)
	require.NotEqual(t, a, b, "session IDs must be unique per call")
}

// TestNewSessionIDFallback verifies the crypto/rand fallback path for
// environments where uuid is unavailable.
func TestNewSessionIDFallback(t *testing.T) {
	t.Parallel()
	id, err := newSessionIDFallback()
	require.NoError(t, err)
	require.Len(t, id, 32, "hex(16 bytes) = 32 chars")

	id2, err := newSessionIDFallback()
	require.NoError(t, err)
	require.NotEqual(t, id, id2, "fallback session IDs must be unique")
}

// TestCodeIndexResultFields verifies the CodeIndexResult struct fields are
// correctly populated from receipt values (pure mapping, no I/O).
func TestCodeIndexResultFields(t *testing.T) {
	t.Parallel()

	// Simulate what IndexCodebase constructs from the receipt.
	receipt := &pb.CodeIndexUploadReceipt{
		Embedded: 5,
		Deleted:  2,
		Errors:   []string{"chunk x: db error"},
	}
	uploaded := 7

	result := &CodeIndexResult{
		Embedded: int(receipt.GetEmbedded()),
		Deleted:  int(receipt.GetDeleted()),
		Uploaded: uploaded,
		Errors:   receipt.GetErrors(),
	}

	require.Equal(t, 5, result.Embedded)
	require.Equal(t, 2, result.Deleted)
	require.Equal(t, 7, result.Uploaded)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "chunk x: db error", result.Errors[0])
}

// ----------------------------------------------------------------------------
// runIndexExchange tests — drive the negotiate→sentinel→delta→receipt logic
// through a fake codeIndexRPC so the orchestration is covered without a live
// gRPC server.
// ----------------------------------------------------------------------------

// fakeUploadClientStream captures everything runIndexExchange sends and replays
// a canned receipt on CloseAndRecv. Satisfies the client-streaming interface.
type fakeUploadClientStream struct {
	grpc.ClientStream
	sent    []*pb.CodeChunkUpload
	receipt *pb.CodeIndexUploadReceipt
	recvErr error
}

func (f *fakeUploadClientStream) Send(m *pb.CodeChunkUpload) error {
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeUploadClientStream) CloseAndRecv() (*pb.CodeIndexUploadReceipt, error) {
	if f.recvErr != nil {
		return nil, f.recvErr
	}
	return f.receipt, nil
}

// fakeCodeIndexRPC is a fake codeIndexRPC for runIndexExchange tests.
type fakeCodeIndexRPC struct {
	negResp   *pb.CodeIndexNegotiateResponse
	negErr    error
	negReq    *pb.CodeIndexNegotiateRequest
	stream    *fakeUploadClientStream
	uploadErr error
}

func (f *fakeCodeIndexRPC) CodeIndexNegotiate(_ context.Context, in *pb.CodeIndexNegotiateRequest, _ ...grpc.CallOption) (*pb.CodeIndexNegotiateResponse, error) {
	f.negReq = in
	if f.negErr != nil {
		return nil, f.negErr
	}
	return f.negResp, nil
}

func (f *fakeCodeIndexRPC) CodeIndexUpload(_ context.Context, _ ...grpc.CallOption) (grpc.ClientStreamingClient[pb.CodeChunkUpload, pb.CodeIndexUploadReceipt], error) {
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	return f.stream, nil
}

// Compile-time assurance the fake satisfies the interface runIndexExchange uses.
var _ codeIndexRPC = (*fakeCodeIndexRPC)(nil)

// TestRunIndexExchange_UploadsOnlyNeededChunks drives the happy path: the
// server needs 2 of 3 chunks; the client sends the leading sentinel plus the
// two needed chunks and reports the receipt counts.
func TestRunIndexExchange_UploadsOnlyNeededChunks(t *testing.T) {
	t.Parallel()

	chunks := []codeindex.Chunk{
		{FilePath: "a.go", ByteStart: 0, ByteEnd: 100, Content: "func A(){}", ContentSHA256: "sha-a", Language: "go", ChunkType: codeindex.ChunkTypeLineBlock},
		{FilePath: "b.go", ByteStart: 0, ByteEnd: 100, Content: "func B(){}", ContentSHA256: "sha-b", Language: "go", ChunkType: codeindex.ChunkTypeLineBlock},
		{FilePath: "c.go", ByteStart: 0, ByteEnd: 100, Content: "func C(){}", ContentSHA256: "sha-c", Language: "go", ChunkType: codeindex.ChunkTypeLineBlock},
	}
	manifest := codeindex.BuildManifestFromChunks(chunks)

	stream := &fakeUploadClientStream{receipt: &pb.CodeIndexUploadReceipt{Embedded: 2, Deleted: 1}}
	rpc := &fakeCodeIndexRPC{
		negResp: &pb.CodeIndexNegotiateResponse{NeedChunks: []string{chunks[0].ChunkID(), chunks[2].ChunkID()}},
		stream:  stream,
	}

	res, err := runIndexExchange(context.Background(), rpc, "proj-slug", "sess-1", manifest, chunks)
	require.NoError(t, err)
	require.Equal(t, 2, res.Embedded)
	require.Equal(t, 1, res.Deleted)
	require.Equal(t, 2, res.Uploaded, "only the two needed chunks are uploaded")

	// The negotiate request carries the full manifest + identity.
	require.Equal(t, "proj-slug", rpc.negReq.GetProjectId())
	require.Equal(t, "sess-1", rpc.negReq.GetIndexSessionId())
	require.Len(t, rpc.negReq.GetManifest(), 3)

	// Stream: 1 leading sentinel (nil meta) + 2 content chunks.
	require.Len(t, stream.sent, 3)
	require.Nil(t, stream.sent[0].GetMeta(), "first message is the identity-only sentinel")
	require.Equal(t, "proj-slug", stream.sent[0].GetProjectId())
	require.Equal(t, "sess-1", stream.sent[0].GetIndexSessionId())
	require.Equal(t, "a.go", stream.sent[1].GetMeta().GetFilePath())
	require.Equal(t, "c.go", stream.sent[2].GetMeta().GetFilePath())
}

// TestRunIndexExchange_DeleteOnlySendsSentinelOnly is the delete-only re-index:
// the server needs nothing, so only the leading sentinel is sent — but it IS
// sent, so the server can run its EOF sweep.
func TestRunIndexExchange_DeleteOnlySendsSentinelOnly(t *testing.T) {
	t.Parallel()

	chunks := []codeindex.Chunk{
		{FilePath: "keep.go", ByteStart: 0, ByteEnd: 100, Content: "func Keep(){}", ContentSHA256: "sha-k", Language: "go", ChunkType: codeindex.ChunkTypeLineBlock},
	}
	manifest := codeindex.BuildManifestFromChunks(chunks)

	stream := &fakeUploadClientStream{receipt: &pb.CodeIndexUploadReceipt{Embedded: 0, Deleted: 3}}
	rpc := &fakeCodeIndexRPC{
		negResp: &pb.CodeIndexNegotiateResponse{NeedChunks: nil}, // server has everything
		stream:  stream,
	}

	res, err := runIndexExchange(context.Background(), rpc, "proj-slug", "sess-2", manifest, chunks)
	require.NoError(t, err)
	require.Equal(t, 0, res.Uploaded, "no content chunks uploaded")
	require.Equal(t, 3, res.Deleted, "server swept 3 stale chunks via the sentinel-carried session")

	require.Len(t, stream.sent, 1, "only the leading sentinel is sent")
	require.Nil(t, stream.sent[0].GetMeta(), "the single message is the identity-only sentinel")
}

// TestRunIndexExchange_NegotiateErrorPropagates verifies a negotiate failure is
// wrapped and returned without opening the upload stream.
func TestRunIndexExchange_NegotiateErrorPropagates(t *testing.T) {
	t.Parallel()

	rpc := &fakeCodeIndexRPC{negErr: errors.New("boom")}
	res, err := runIndexExchange(context.Background(), rpc, "p", "s", codeindex.Manifest{}, nil)
	require.Error(t, err)
	require.Nil(t, res)
	require.Contains(t, err.Error(), "CodeIndexNegotiate")
}

// TestRunIndexExchange_CloseRecvErrorPropagates verifies a CloseAndRecv failure
// is wrapped and returned.
func TestRunIndexExchange_CloseRecvErrorPropagates(t *testing.T) {
	t.Parallel()

	stream := &fakeUploadClientStream{recvErr: errors.New("eof boom")}
	rpc := &fakeCodeIndexRPC{
		negResp: &pb.CodeIndexNegotiateResponse{},
		stream:  stream,
	}
	res, err := runIndexExchange(context.Background(), rpc, "p", "s", codeindex.Manifest{}, nil)
	require.Error(t, err)
	require.Nil(t, res)
	require.Contains(t, err.Error(), "CodeIndexUpload close")
}
