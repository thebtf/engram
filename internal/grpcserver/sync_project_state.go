package grpcserver

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gorm.io/gorm"

	pb "github.com/thebtf/engram/proto/engram/v1"
)

const (
	// maxSyncProjectIDs is the server-enforced cap on local_project_ids per request (FR-6).
	maxSyncProjectIDs = 10_000

	// heartbeatStaleThreshold is how long a project can go without a heartbeat
	// before being classified as "unknown" (orphaned by a daemon that never reconnected).
	heartbeatStaleThreshold = 24 * time.Hour
)

// SyncProjectState reconciles the daemon's local project registry against the
// server's authoritative list (FR-6). It is idempotent — multiple daemons may
// call concurrently without conflict.
//
// Algorithm:
//  1. Validate inputs.
//  2. For each reported ID: if removed_at IS NOT NULL → append to removed[].
//     If not in projects table at all → also append to removed[].
//  3. For each row in projects: if last_heartbeat > 24h ago AND removed_at IS NULL → append to unknown[].
//  4. UPDATE last_heartbeat for every reported ID that is NOT yet soft-deleted.
//  5. Return removed[], unknown[], and server_time_unix_ms.
func (s *Server) SyncProjectState(ctx context.Context, req *pb.SyncProjectStateRequest) (*pb.SyncProjectStateResponse, error) {
	if req.GetClientId() == "" {
		return nil, status.Error(codes.InvalidArgument, "client_id must not be empty")
	}
	if len(req.GetLocalProjectIds()) > maxSyncProjectIDs {
		return nil, status.Errorf(codes.InvalidArgument,
			"local_project_ids exceeds max allowed (%d), got %d",
			maxSyncProjectIDs, len(req.GetLocalProjectIds()))
	}

	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "database not ready")
	}

	localIDs := req.GetLocalProjectIds()
	now := time.Now().UTC()

	var removed, unknown []string

	if len(localIDs) > 0 {
		// Determine which reported IDs are removed or unknown to the server.
		type projectRow struct {
			ID        string
			RemovedAt *time.Time
		}

		// Query the server's projects table for all reported IDs.
		var rows []projectRow
		if err := s.db.WithContext(ctx).
			Raw("SELECT id, removed_at FROM projects WHERE id = ANY(?)", localIDs).
			Scan(&rows).Error; err != nil {
			return nil, status.Errorf(codes.Internal, "query projects: %v", err)
		}

		// Index what the server knows.
		serverKnown := make(map[string]*time.Time, len(rows))
		for _, r := range rows {
			r := r
			serverKnown[r.ID] = r.RemovedAt
		}

		// Classify each reported ID.
		for _, id := range localIDs {
			ra, known := serverKnown[id]
			if !known || ra != nil {
				// Not in DB or already soft-deleted → daemon should treat as removed.
				removed = append(removed, id)
			}
		}

		// Update last_heartbeat for all reported IDs that are still live.
		// Ignore errors — heartbeat is best-effort.
		if len(localIDs) > 0 {
			s.db.WithContext(ctx).
				Exec(
					"UPDATE projects SET last_heartbeat = ? WHERE id = ANY(?) AND removed_at IS NULL",
					now, localIDs,
				)
		}
	}

	// Find orphaned live projects (last_heartbeat > 24h AND removed_at IS NULL).
	staleThreshold := now.Add(-heartbeatStaleThreshold)
	var staleIDs []string
	if err := s.db.WithContext(ctx).
		Raw(
			"SELECT id FROM projects WHERE removed_at IS NULL AND (last_heartbeat IS NULL OR last_heartbeat < ?)",
			staleThreshold,
		).
		Pluck("id", &staleIDs).Error; err != nil {
		// Non-fatal — unknown list is informational.
		staleIDs = nil
	}
	unknown = staleIDs

	return &pb.SyncProjectStateResponse{
		Removed:          removed,
		Unknown:          unknown,
		ServerTimeUnixMs: now.UnixMilli(),
	}, nil
}

// withDB returns a copy of the Server with the given gorm.DB set.
// Used exclusively in tests to inject a real DB without reconstructing the full server.
func (s *Server) withDB(db *gorm.DB) *Server {
	return &Server{
		handler: s.handler,
		token:   s.token,
		db:      db,
		bus:     s.bus,
	}
}
