package worker

import (
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/worker/sessioncompat"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

// CommitSessionStartDelivery records a fully formed session-start response as
// an attempted delivery. It intentionally detaches from the caller context and
// does not wait for an HTTP or OMP acknowledgement.
func (s *Service) CommitSessionStartDelivery(hostSessionRef, canonicalProject string, memories []*pb.SessionStartMemory) {
	if hostSessionRef == "" {
		return
	}
	s.initMu.RLock()
	injectionLogStore := s.injectionLogStore
	memoryStore := s.memoryStore
	s.initMu.RUnlock()
	if injectionLogStore == nil {
		return
	}
	memoryIDs := sessioncompat.MemoryIDs(memories)
	if len(memoryIDs) == 0 {
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		recordingContext, cancel := s.detachedContext(30 * time.Second)
		defer cancel()
		if err := injectionLogStore.Record(recordingContext, hostSessionRef, canonicalProject, memoryIDs); err != nil {
			log.Warn().Err(err).Str("session_id", hostSessionRef).Msg("injection_log: session-start record failed")
		}
		if memoryStore != nil {
			if err := memoryStore.BatchIncrementInjected(recordingContext, memoryIDs); err != nil {
				log.Warn().Err(err).Str("session_id", hostSessionRef).Msg("injection_count: session-start increment failed")
			}
		}
	}()
}
