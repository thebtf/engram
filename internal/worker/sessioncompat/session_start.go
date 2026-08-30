// Package sessioncompat owns the small compatibility seam shared by the HTTP
// session-start route and the private bridge gRPC facade.
package sessioncompat

import pb "github.com/thebtf/engram/proto/engram/v1"

// DeliveryCommitter records a formed session-start response as an attempted
// delivery. Implementations must not wait for a client acknowledgement.
type DeliveryCommitter interface {
	CommitSessionStartDelivery(hostSessionRef, canonicalProject string, memories []*pb.SessionStartMemory)
}

// MemoryIDs extracts deduplicated, non-zero memory IDs while preserving their
// response order. Rules are intentionally excluded from injection accounting.
func MemoryIDs(memories []*pb.SessionStartMemory) []int64 {
	if len(memories) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(memories))
	seen := make(map[int64]struct{}, len(memories))
	for _, memory := range memories {
		if memory == nil || memory.GetId() == 0 {
			continue
		}
		if _, ok := seen[memory.GetId()]; ok {
			continue
		}
		seen[memory.GetId()] = struct{}{}
		ids = append(ids, memory.GetId())
	}
	return ids
}
