package grpcserver

import (
	"fmt"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/thebtf/engram/internal/worker/projectevents"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

// globalEventSeq is a monotonically-increasing counter used to generate opaque
// event IDs for ProjectEvent.event_id. Unique per server process lifetime.
var globalEventSeq atomic.Uint64

// ProjectEvents implements the server-streaming RPC that pushes project lifecycle
// events to connected daemon clients (FR-7).
//
// The server subscribes to the in-process projectevents.Bus on entry and fans each
// event out over the gRPC stream. The stream lives until the client disconnects or
// the server shuts down (context cancellation).
//
// v0.1.0 does NOT support since_event_id replay. A non-empty value returns
// OUT_OF_RANGE per the proto contract.
func (s *Server) ProjectEvents(req *pb.ProjectEventsRequest, stream grpc.ServerStreamingServer[pb.ProjectEvent]) error {
	if err := rejectHAPCredentialWithoutProject(stream.Context()); err != nil {
		return err
	}
	if req.GetSinceEventId() != "" {
		return status.Error(codes.OutOfRange,
			"since_event_id replay is not supported in v0.1.0; reconnect without since_event_id")
	}

	if s.bus == nil {
		// No bus wired yet (server still initialising) — block until context is cancelled.
		<-stream.Context().Done()
		return nil
	}

	// Channel-based bridge between the synchronous Bus.Subscribe callback and the
	// blocking grpc.ServerStream.Send. Buffer of 64 events to absorb bursts without
	// blocking the emitter goroutine.
	evCh := make(chan projectevents.Event, 64)

	unsub := s.bus.Subscribe(func(ev projectevents.Event) {
		select {
		case evCh <- ev:
		default:
			// Channel full — drop the event. The daemon's heartbeat (SyncProjectState)
			// provides the eventually-consistent fallback for missed events.
		}
	})
	defer unsub()

	for {
		select {
		case <-stream.Context().Done():
			// Client disconnected or server shutdown — clean exit.
			return nil

		case ev := <-evCh:
			pbEv, err := busEventToProto(ev)
			if err != nil {
				// Should not happen with well-formed events.
				continue
			}
			if err := stream.Send(pbEv); err != nil {
				// Client stream closed or transport error.
				return status.Errorf(codes.Unavailable, "send project event: %v", err)
			}
		}
	}
}

// busEventToProto converts an in-process projectevents.Event to the protobuf
// wire type. Event IDs are monotonically-increasing within the server process.
func busEventToProto(ev projectevents.Event) (*pb.ProjectEvent, error) {
	seq := globalEventSeq.Add(1)

	var evType pb.ProjectEventType
	switch ev.EventType {
	case projectevents.EventTypeRemoved:
		evType = pb.ProjectEventType_PROJECT_EVENT_TYPE_REMOVED
	default:
		return nil, fmt.Errorf("unknown event type: %q", ev.EventType)
	}

	return &pb.ProjectEvent{
		EventId:         fmt.Sprintf("%d", seq),
		EventType:       evType,
		ProjectId:       ev.ProjectID,
		TimestampUnixMs: ev.TimestampUnixMs,
		Reason:          ev.Reason,
		Metadata:        ev.Metadata,
	}, nil
}

// nowUnixMs returns the current time as Unix milliseconds.
func nowUnixMs() int64 {
	return time.Now().UnixMilli()
}
