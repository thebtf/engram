package engramcore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/codeindex"
	"github.com/thebtf/engram/internal/config"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// CodeIndexResult summarises the outcome of a full index-negotiate-upload cycle.
type CodeIndexResult struct {
	// Embedded is the number of chunks successfully upserted on the server.
	Embedded int
	// Deleted is the number of stale chunks swept from the server after upload.
	Deleted int
	// Uploaded is the number of chunks sent over the wire in this session.
	Uploaded int
	// Errors holds per-chunk error strings reported by the server (non-fatal).
	Errors []string
}

// IndexCodebase walks root, negotiates with the engram server, uploads the
// delta chunks the server does not yet have, and returns a CodeIndexResult.
//
// Sequence (two-RPC split, ADR-001 §6):
//
//  1. BuildManifest to get full chunk metadata + content.
//  2. CodeIndexNegotiate → server returns need_chunks (delta) and stale_chunks
//     (informational). The server stamps surviving rows with the new session id.
//  3. Stream only the needed chunks via CodeIndexUpload. On stream close the
//     server sweeps stale rows and returns a receipt.
//
// Embedding is performed server-side by CR-004; this method sets no embeddings.
// This method is NOT yet wired to an MCP tool; CR-006 adds the tool binding.
func (m *Module) IndexCodebase(ctx context.Context, p muxcore.ProjectContext, root string) (*CodeIndexResult, error) {
	serverURL, err := m.requireServerURL(p)
	if err != nil {
		return nil, err
	}
	token := m.envFor(p, config.EnvWorkstationToken)
	slug := m.cache.Resolve(p)

	conn, err := m.pool.getOrDialGRPC(serverURL, token)
	if err != nil {
		return nil, fmt.Errorf("gRPC connect: %w", err)
	}
	client := pb.NewEngramServiceClient(conn)

	// Generate a unique session id for this index run.
	sessionID := newSessionID()

	// Walk the repository and build the manifest + full chunk slice.
	manifest, chunks, err := codeindex.BuildManifest(root, codeindex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("build manifest: %w", err)
	}

	// Map manifest entries to proto metas for the negotiate RPC.
	metas := manifestToProto(manifest)

	// Negotiate: tell the server what we have; learn what it needs.
	negResp, err := client.CodeIndexNegotiate(ctx, &pb.CodeIndexNegotiateRequest{
		ProjectId:      slug,
		IndexSessionId: sessionID,
		Manifest:       metas,
	})
	if err != nil {
		return nil, fmt.Errorf("CodeIndexNegotiate: %w", err)
	}

	// Build a set of chunk_ids the server wants.
	needSet := make(map[string]struct{}, len(negResp.GetNeedChunks()))
	for _, id := range negResp.GetNeedChunks() {
		needSet[id] = struct{}{}
	}

	// Open the upload stream and send only the needed chunks.
	stream, err := client.CodeIndexUpload(ctx)
	if err != nil {
		return nil, fmt.Errorf("CodeIndexUpload open: %w", err)
	}

	// Always send a leading identity-only sentinel so the server learns the
	// project/session and runs the stale-sweep at EOF even when the delta is
	// empty. This covers the delete-only re-index case (files removed, nothing
	// new to upload): without the sentinel the server would receive zero
	// messages, never learn the project, and skip the sweep — leaking the
	// deleted files' chunks as phantom search results.
	if err := stream.Send(&pb.CodeChunkUpload{
		ProjectId:      slug,
		IndexSessionId: sessionID,
	}); err != nil {
		return nil, fmt.Errorf("CodeIndexUpload sentinel: %w", err)
	}

	uploaded := 0
	for _, c := range chunks {
		if _, needed := needSet[c.ChunkID()]; !needed {
			continue
		}
		sendErr := stream.Send(&pb.CodeChunkUpload{
			ProjectId:      slug,
			IndexSessionId: sessionID,
			Meta:           chunkToProtoMeta(c),
			Content:        []byte(c.Content),
		})
		if sendErr != nil {
			return nil, fmt.Errorf("CodeIndexUpload send: %w", sendErr)
		}
		uploaded++
	}

	receipt, err := stream.CloseAndRecv()
	if err != nil {
		return nil, fmt.Errorf("CodeIndexUpload close: %w", err)
	}

	return &CodeIndexResult{
		Embedded: int(receipt.GetEmbedded()),
		Deleted:  int(receipt.GetDeleted()),
		Uploaded: uploaded,
		Errors:   receipt.GetErrors(),
	}, nil
}

// manifestToProto converts a codeindex.Manifest to a slice of *pb.CodeChunkMeta
// suitable for CodeIndexNegotiateRequest. Pure function; no I/O.
func manifestToProto(m codeindex.Manifest) []*pb.CodeChunkMeta {
	out := make([]*pb.CodeChunkMeta, len(m))
	for i, e := range m {
		out[i] = &pb.CodeChunkMeta{
			ChunkId:       e.ChunkID,
			FilePath:      e.FilePath,
			ByteStart:     int32(e.ByteStart),
			ByteEnd:       int32(e.ByteEnd),
			Language:      e.Language,
			ChunkType:     string(e.ChunkType),
			ContentSha256: e.ContentSHA256,
		}
	}
	return out
}

// chunkToProtoMeta converts a single codeindex.Chunk to *pb.CodeChunkMeta.
// Pure function; no I/O.
func chunkToProtoMeta(c codeindex.Chunk) *pb.CodeChunkMeta {
	return &pb.CodeChunkMeta{
		ChunkId:       c.ChunkID(),
		FilePath:      c.FilePath,
		ByteStart:     int32(c.ByteStart),
		ByteEnd:       int32(c.ByteEnd),
		Language:      c.Language,
		ChunkType:     string(c.ChunkType),
		ContentSha256: c.ContentSHA256,
	}
}

// newSessionID generates a unique session identifier for a single index run.
// Uses github.com/google/uuid (already in go.mod) for UUID v4.
func newSessionID() string {
	return uuid.NewString()
}

// newSessionIDFallback is a crypto/rand fallback implementation kept for
// reference; newSessionID uses uuid.NewString() which is already in go.mod.
// Exported only for unit-test use in code_index_client_test.go.
func newSessionIDFallback() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
