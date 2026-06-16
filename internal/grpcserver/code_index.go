package grpcserver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

// CodeIndexNegotiate implements the first leg of the two-RPC code-index
// transport (CR-003, ADR-001 §6).
//
// Delta-negotiation design:
//
//  1. The client sends its full chunk manifest (metadata only, no content).
//  2. The server loads its stored identity keys (file_path, byte_start,
//     content_sha256) for the project — a lightweight read with no content/
//     embedding columns.
//  3. The client's keep-set is stamped with the new index_session_id via
//     TouchSession so that DeleteBySessionMismatch (called at CodeIndexUpload
//     EOF) can sweep any rows that were NOT part of this negotiate+upload cycle.
//  4. need_chunks  = chunks in the manifest whose identity key is absent from
//     the server (server does not have them, or content changed → new sha256).
//  5. stale_chunks = server chunks whose identity key is absent from the
//     client manifest (client has dropped the file or chunk position).
//
// The actual stale row deletion happens at CodeIndexUpload EOF, not here,
// so the two RPCs together form an atomic "mark survivors, upload delta,
// sweep orphans" cycle without requiring distributed locking.
func (s *Server) CodeIndexNegotiate(ctx context.Context, req *pb.CodeIndexNegotiateRequest) (*pb.CodeIndexNegotiateResponse, error) {
	if req.GetProjectId() == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id must not be empty")
	}
	if req.GetIndexSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "index_session_id must not be empty")
	}
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "database not ready")
	}

	store := dbgorm.NewCodeChunkStore(s.db)

	// Register this negotiate cycle as an authorization record BEFORE computing
	// the delta. Its existence — not chunk presence — authorizes the EOF sweep
	// in CodeIndexUpload, so a delete-all re-index (empty manifest → zero
	// surviving chunks) still sweeps while a stray un-negotiated upload does not.
	if err := store.RegisterSession(ctx, req.GetProjectId(), req.GetIndexSessionId()); err != nil {
		return nil, status.Errorf(codes.Internal, "register session: %v", err)
	}

	// Load server's stored identity keys for this project.
	serverIdentities, err := store.ListIdentityKeysByProject(ctx, req.GetProjectId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list identity keys: %v", err)
	}

	// Build a set of server-side stale keys for O(1) lookup.
	serverKeySet := make(map[string]dbgorm.ChunkIdentity, len(serverIdentities))
	for _, id := range serverIdentities {
		k := dbgorm.StaleKey(id.FilePath, id.ByteStart, id.ContentSHA256)
		serverKeySet[k] = id
	}

	// Build the client keep-set (stale keys for every manifest entry).
	clientKeepKeys := make([]string, 0, len(req.GetManifest()))
	for _, m := range req.GetManifest() {
		clientKeepKeys = append(clientKeepKeys, dbgorm.StaleKey(m.GetFilePath(), int(m.GetByteStart()), m.GetContentSha256()))
	}

	// Mark surviving chunks with the new session id. This is best-effort for
	// the negotiate phase; the actual sweep happens at upload EOF.
	if len(clientKeepKeys) > 0 {
		if _, err := store.TouchSession(ctx, req.GetProjectId(), req.GetIndexSessionId(), clientKeepKeys); err != nil {
			return nil, status.Errorf(codes.Internal, "touch session: %v", err)
		}
	}

	// Compute need_chunks: manifest entries whose stale key is absent from the server.
	// A new sha256 for the same (file, byteStart) means the content changed — also "needed".
	var needChunks []string
	clientKeySet := make(map[string]struct{}, len(clientKeepKeys))
	for i, m := range req.GetManifest() {
		k := clientKeepKeys[i]
		clientKeySet[k] = struct{}{}
		if _, exists := serverKeySet[k]; !exists {
			needChunks = append(needChunks, m.GetChunkId())
		}
	}

	// Compute stale_chunks: server keys absent from the client manifest.
	// We report chunk_ids using the same formula as Chunk.ChunkID() so the
	// client can correlate against its own chunk list.
	var staleChunks []string
	for k, id := range serverKeySet {
		if _, kept := clientKeySet[k]; !kept {
			staleChunks = append(staleChunks, chunkIDFromPosition(id.FilePath, id.ByteStart))
		}
	}

	return &pb.CodeIndexNegotiateResponse{
		NeedChunks:  needChunks,
		StaleChunks: staleChunks,
	}, nil
}

// CodeIndexUpload implements the second leg of the two-RPC code-index
// transport (CR-003, ADR-001 §6).
//
// Sweep design:
//
//	After CodeIndexNegotiate has stamped the surviving rows with the new
//	index_session_id, this stream accepts the delta chunks (those the server
//	did not have). When the client closes the stream (io.EOF), we call
//	DeleteBySessionMismatch to remove any rows still carrying an old session id
//	— those are chunks the client has dropped since the previous index.
//
//	Embedding stays NULL in CR-003; the CR-004 embedding pipeline fills it via
//	a dedicated UpdateEmbedding method (see Upsert comment in code_chunk_store.go).
func (s *Server) CodeIndexUpload(stream pb.EngramService_CodeIndexUploadServer) error {
	if s.db == nil {
		return status.Error(codes.Unavailable, "database not ready")
	}

	store := dbgorm.NewCodeChunkStore(s.db)

	var (
		projectID string
		sessionID string
		embedded  int32
		errors    []string
	)

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return status.Errorf(codes.Internal, "recv: %v", err)
		}

		// Capture identity from the FIRST message regardless of whether it
		// carries a chunk. A client with an empty delta but pending deletions
		// (delete-only re-index: files removed, nothing new to upload) sends a
		// single identity-only sentinel (Meta == nil) so the EOF sweep below
		// still knows the project and runs. Without this, the sweep would be
		// skipped and the deleted files' rows would leak as phantom chunks.
		if projectID == "" {
			if msg.GetProjectId() == "" {
				// First message must establish identity.
				return status.Error(codes.InvalidArgument, "first upload message must set project_id")
			}
			projectID = msg.GetProjectId()
			sessionID = msg.GetIndexSessionId()
		} else if msg.GetProjectId() != projectID || msg.GetIndexSessionId() != sessionID {
			return status.Error(codes.InvalidArgument, "project_id or index_session_id changed mid-stream")
		}

		// Meta == nil is the identity-only sentinel (no chunk to upsert). A nil
		// meta carrying content is malformed and reported; a nil meta with no
		// content is the legitimate sentinel and silently skipped.
		if msg.GetMeta() == nil {
			if len(msg.GetContent()) > 0 {
				errors = append(errors, "received content with nil meta; skipped")
			}
			continue
		}

		m := msg.GetMeta()
		chunk := &dbgorm.CodeChunk{
			ProjectID:      projectID,
			FilePath:       m.GetFilePath(),
			ByteStart:      int(m.GetByteStart()),
			ByteEnd:        int(m.GetByteEnd()),
			Language:       m.GetLanguage(),
			ChunkType:      m.GetChunkType(),
			Content:        string(msg.GetContent()),
			ContentSHA256:  m.GetContentSha256(),
			IndexSessionID: sessionID,
			Embedding:      nil, // CR-004 fills embeddings; left NULL here
		}

		if upsertErr := store.Upsert(stream.Context(), chunk); upsertErr != nil {
			// Non-fatal per ADR: record the error and continue with the rest of
			// the stream so a single bad chunk does not abort the whole upload.
			errors = append(errors, fmt.Sprintf("chunk %s: %v", m.GetChunkId(), upsertErr))
			continue
		}
		embedded++
	}

	// projectID is empty only when the stream carried zero messages — a
	// well-behaved client always sends at least the identity-only sentinel
	// (see code_index_client.go), so an empty projectID here means an empty/
	// aborted stream. Skip the sweep to avoid operating on an empty projectID.
	var deleted int32
	if projectID != "" {
		n, sweepErr := store.DeleteBySessionMismatch(stream.Context(), projectID, sessionID)
		deleted = int32(n)
		if sweepErr != nil {
			// Non-fatal: surface the sweep failure in the receipt so the caller
			// can distinguish a failed sweep from a genuinely empty one (stale
			// rows would otherwise linger silently as phantom chunks).
			errors = append(errors, fmt.Sprintf("sweep: %v", sweepErr))
		}
		// Best-effort cleanup of the authorization record now that the cycle is
		// complete; a lingering row is harmless and overwritten on next negotiate.
		_, _ = store.DeleteSession(stream.Context(), projectID, sessionID)
	}

	return stream.SendAndClose(&pb.CodeIndexUploadReceipt{
		Embedded: embedded,
		Deleted:  deleted,
		Errors:   errors,
	})
}

// chunkIDFromPosition replicates the Chunk.ChunkID() formula from
// internal/codeindex/chunk.go: sha256(filePath + ":" + byteStart)[:8] as
// 16 hex chars. Used server-side to populate stale_chunks in the negotiate
// response so the client can correlate by chunk_id.
func chunkIDFromPosition(filePath string, byteStart int) string {
	h := sha256.Sum256([]byte(filePath + ":" + strconv.Itoa(byteStart)))
	return fmt.Sprintf("%x", h[:8])
}
