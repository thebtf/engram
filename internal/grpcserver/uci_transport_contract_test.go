package grpcserver

import (
	"testing"

	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type uciTransportMethodSpec struct {
	name            string
	input           string
	output          string
	clientStreaming bool
	serverStreaming bool
}

type uciTransportFieldSpec struct {
	name           string
	number         protoreflect.FieldNumber
	kind           protoreflect.Kind
	cardinality    protoreflect.Cardinality
	message        string
	proto3Optional bool
}

func TestUCITransportContractPreservesExistingServiceSnapshot(t *testing.T) {
	service := uciTransportService(t)

	for _, spec := range []uciTransportMethodSpec{
		{name: "CallTool", input: "engram.v1.CallToolRequest", output: "engram.v1.CallToolResponse"},
		{name: "Initialize", input: "engram.v1.InitializeRequest", output: "engram.v1.InitializeResponse"},
		{name: "Ping", input: "engram.v1.PingRequest", output: "engram.v1.PingResponse"},
		{name: "SyncProjectState", input: "engram.v1.SyncProjectStateRequest", output: "engram.v1.SyncProjectStateResponse"},
		{name: "ProjectEvents", input: "engram.v1.ProjectEventsRequest", output: "engram.v1.ProjectEvent", serverStreaming: true},
		{name: "GetSessionStartContext", input: "engram.v1.GetSessionStartContextRequest", output: "engram.v1.GetSessionStartContextResponse"},
		{name: "GetAmbientCandidates", input: "engram.v1.GetAmbientCandidatesRequest", output: "engram.v1.GetAmbientCandidatesResponse"},
		{name: "NegotiateVersion", input: "engram.v1.NegotiateVersionRequest", output: "engram.v1.NegotiateVersionResponse"},
		{name: "CodeIndexNegotiate", input: "engram.v1.CodeIndexNegotiateRequest", output: "engram.v1.CodeIndexNegotiateResponse"},
		{name: "CodeIndexUpload", input: "engram.v1.CodeChunkUpload", output: "engram.v1.CodeIndexUploadReceipt", clientStreaming: true},
		{name: "RegisterProjectIdentityV3", input: "engram.v1.RegisterProjectIdentityV3Request", output: "engram.v1.RegisterProjectIdentityV3Response"},
		{name: "Bind", input: "engram.v1.HostAdvisorBindRequest", output: "engram.v1.HostAdvisorBindResponse"},
		{name: "Advise", input: "engram.v1.HostAdvisorAdviseRequest", output: "engram.v1.HostAdvisorAdviseResponse"},
		{name: "Observe", input: "engram.v1.HostAdvisorObserveRequest", output: "engram.v1.HostAdvisorObserveResponse"},
	} {
		requireUCITransportMethod(t, service, spec)
	}

	file := pb.File_proto_engram_v1_engram_proto
	requireUCITransportFields(t, file, "CallToolRequest",
		uciTransportFieldSpec{name: "tool_name", number: 1, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "arguments_json", number: 2, kind: protoreflect.BytesKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "project", number: 3, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "session_id", number: 4, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "project_identity", number: 5, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.ProjectIdentityV2"},
		uciTransportFieldSpec{name: "project_identity_v3", number: 6, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.ProjectIdentityV3"},
	)
	requireUCITransportFields(t, file, "CodeChunkMeta",
		uciTransportFieldSpec{name: "chunk_id", number: 1, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "file_path", number: 2, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "byte_start", number: 3, kind: protoreflect.Int32Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "byte_end", number: 4, kind: protoreflect.Int32Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "language", number: 5, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "chunk_type", number: 6, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "content_sha256", number: 7, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
	)
	requireUCITransportFields(t, file, "CodeIndexNegotiateRequest",
		uciTransportFieldSpec{name: "project_id", number: 1, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "index_session_id", number: 2, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "manifest", number: 3, kind: protoreflect.MessageKind, cardinality: protoreflect.Repeated, message: "engram.v1.CodeChunkMeta"},
	)
	requireUCITransportFields(t, file, "CodeIndexNegotiateResponse",
		uciTransportFieldSpec{name: "need_chunks", number: 1, kind: protoreflect.StringKind, cardinality: protoreflect.Repeated},
		uciTransportFieldSpec{name: "stale_chunks", number: 2, kind: protoreflect.StringKind, cardinality: protoreflect.Repeated},
	)
	requireUCITransportFields(t, file, "CodeChunkUpload",
		uciTransportFieldSpec{name: "project_id", number: 1, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "index_session_id", number: 2, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "meta", number: 3, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.CodeChunkMeta"},
		uciTransportFieldSpec{name: "content", number: 4, kind: protoreflect.BytesKind, cardinality: protoreflect.Optional},
	)
	requireUCITransportFields(t, file, "CodeIndexUploadReceipt",
		uciTransportFieldSpec{name: "embedded", number: 1, kind: protoreflect.Int32Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "deleted", number: 2, kind: protoreflect.Int32Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "errors", number: 3, kind: protoreflect.StringKind, cardinality: protoreflect.Repeated},
	)
}

func TestUCITransportContractRequiresScopedAdditions(t *testing.T) {
	service := uciTransportService(t)

	for _, spec := range []uciTransportMethodSpec{
		{name: "BindCodeContext", input: "engram.v1.BindCodeContextRequest", output: "engram.v1.BindCodeContextResponse"},
		{name: "BeginCodeIndex", input: "engram.v1.BeginCodeIndexRequest", output: "engram.v1.BeginCodeIndexResponse"},
		{name: "StageCodeIndex", input: "engram.v1.StageCodeIndexFrame", output: "engram.v1.StageCodeIndexResponse", clientStreaming: true},
		{name: "FinalizeCodeIndex", input: "engram.v1.FinalizeCodeIndexRequest", output: "engram.v1.FinalizeCodeIndexResponse"},
		{name: "QueryCode", input: "engram.v1.QueryCodeRequest", output: "engram.v1.QueryCodeResponse"},
		{name: "ExploreCode", input: "engram.v1.ExploreCodeRequest", output: "engram.v1.ExploreCodeResponse"},
	} {
		requireUCITransportMethod(t, service, spec)
	}
	if methods := service.Methods().Len(); methods != 20 {
		t.Fatalf("EngramService methods = %d, want 20 legacy-plus-UCI methods", methods)
	}

	file := pb.File_proto_engram_v1_engram_proto
	requireUCITransportFields(t, file, "ContextRef",
		uciTransportFieldSpec{name: "space_id", number: 1, kind: protoreflect.StringKind, cardinality: protoreflect.Optional, proto3Optional: true},
		uciTransportFieldSpec{name: "source_id", number: 2, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "checkout_id", number: 3, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "view_id", number: 4, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "generation", number: 5, kind: protoreflect.Int64Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "analysis_profile_id", number: 6, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
	)
	requireUCITransportFields(t, file, "CodeIndexScope",
		uciTransportFieldSpec{name: "source_id", number: 1, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "checkout_id", number: 2, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "incarnation_id", number: 3, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "analysis_profile_id", number: 4, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
	)
	requireUCITransportFields(t, file, "BindCodeContextRequest",
		uciTransportFieldSpec{name: "client_session_id", number: 1, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "requested_context", number: 2, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.ContextRef"},
	)
	requireUCITransportFields(t, file, "BindCodeContextResponse",
		uciTransportFieldSpec{name: "context_handle", number: 1, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "context", number: 2, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.ContextRef"},
	)
	requireUCITransportFields(t, file, "BeginCodeIndexRequest",
		uciTransportFieldSpec{name: "scope", number: 1, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.CodeIndexScope"},
		uciTransportFieldSpec{name: "owner_instance", number: 2, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "build_key", number: 3, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "expected_parent", number: 4, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.ContextRef"},
		uciTransportFieldSpec{name: "manifest_mode", number: 5, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "job_kind", number: 6, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
	)
	requireUCITransportFields(t, file, "BeginCodeIndexResponse",
		uciTransportFieldSpec{name: "scope", number: 1, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.CodeIndexScope"},
		uciTransportFieldSpec{name: "build_id", number: 2, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "lease_epoch", number: 3, kind: protoreflect.Uint64Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "lease_expires_at", number: 4, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "google.protobuf.Timestamp"},
	)
	requireUCITransportFields(t, file, "StageCodeIndexFrame",
		uciTransportFieldSpec{name: "scope", number: 1, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.CodeIndexScope"},
		uciTransportFieldSpec{name: "build_id", number: 2, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "lease_epoch", number: 3, kind: protoreflect.Uint64Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "sequence", number: 4, kind: protoreflect.Uint64Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "payload_digest", number: 5, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "payload", number: 6, kind: protoreflect.BytesKind, cardinality: protoreflect.Optional},
	)
	requireUCITransportFields(t, file, "StageCodeIndexResponse",
		uciTransportFieldSpec{name: "build_id", number: 1, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "accepted_sequence", number: 2, kind: protoreflect.Uint64Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "accepted_part_count", number: 3, kind: protoreflect.Uint64Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "part_digest", number: 4, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
	)
	requireUCITransportFields(t, file, "FinalizeCodeIndexRequest",
		uciTransportFieldSpec{name: "scope", number: 1, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.CodeIndexScope"},
		uciTransportFieldSpec{name: "build_id", number: 2, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "lease_epoch", number: 3, kind: protoreflect.Uint64Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "expected_parent", number: 4, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.ContextRef"},
		uciTransportFieldSpec{name: "manifest_part_count", number: 5, kind: protoreflect.Uint64Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "parts_digest", number: 6, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "manifest_entry_count", number: 7, kind: protoreflect.Uint64Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "manifest_digest", number: 8, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "edge_count", number: 9, kind: protoreflect.Uint64Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "edges_digest", number: 10, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "observed_filesystem_sequence", number: 11, kind: protoreflect.Uint64Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "scan_started_at", number: 12, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "google.protobuf.Timestamp"},
		uciTransportFieldSpec{name: "scan_completed_at", number: 13, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "google.protobuf.Timestamp"},
		uciTransportFieldSpec{name: "scan_outcome", number: 14, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "complete_census", number: 15, kind: protoreflect.BoolKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "coverage_json", number: 16, kind: protoreflect.BytesKind, cardinality: protoreflect.Optional},
	)
	requireUCITransportFields(t, file, "FinalizeCodeIndexResponse",
		uciTransportFieldSpec{name: "published_context", number: 1, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.ContextRef"},
		uciTransportFieldSpec{name: "build_id", number: 2, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "lease_epoch", number: 3, kind: protoreflect.Uint64Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "accepted_filesystem_sequence", number: 4, kind: protoreflect.Uint64Kind, cardinality: protoreflect.Optional},
	)
	requireUCITransportFields(t, file, "QueryCodeRequest",
		uciTransportFieldSpec{name: "context", number: 1, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.ContextRef"},
		uciTransportFieldSpec{name: "query", number: 2, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "max_results", number: 3, kind: protoreflect.Uint32Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "max_bytes", number: 4, kind: protoreflect.Uint32Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "deadline_ms", number: 5, kind: protoreflect.Uint32Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "continuation_token", number: 6, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
	)
	requireUCITransportFields(t, file, "QueryCodeResponse",
		uciTransportFieldSpec{name: "context", number: 1, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.ContextRef"},
		uciTransportFieldSpec{name: "response_json", number: 2, kind: protoreflect.BytesKind, cardinality: protoreflect.Optional},
	)
	requireUCITransportFields(t, file, "ExploreCodeRequest",
		uciTransportFieldSpec{name: "context", number: 1, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.ContextRef"},
		uciTransportFieldSpec{name: "operation", number: 2, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "subject", number: 3, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "max_depth", number: 4, kind: protoreflect.Uint32Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "max_visited_nodes", number: 5, kind: protoreflect.Uint32Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "max_result_nodes", number: 6, kind: protoreflect.Uint32Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "max_result_edges", number: 7, kind: protoreflect.Uint32Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "deadline_ms", number: 8, kind: protoreflect.Uint32Kind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "continuation_token", number: 9, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
	)
	requireUCITransportFields(t, file, "ExploreCodeResponse",
		uciTransportFieldSpec{name: "context", number: 1, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.ContextRef"},
		uciTransportFieldSpec{name: "response_json", number: 2, kind: protoreflect.BytesKind, cardinality: protoreflect.Optional},
	)
}

func uciTransportService(t *testing.T) protoreflect.ServiceDescriptor {
	t.Helper()

	file := pb.File_proto_engram_v1_engram_proto
	if services := file.Services(); services.Len() != 1 {
		t.Fatalf("services = %d, want exactly one EngramService", services.Len())
	}
	service := file.Services().Get(0)
	if service.FullName() != "engram.v1.EngramService" {
		t.Fatalf("service = %s, want engram.v1.EngramService", service.FullName())
	}
	return service
}

func requireUCITransportMethod(t *testing.T, service protoreflect.ServiceDescriptor, spec uciTransportMethodSpec) {
	t.Helper()

	method := service.Methods().ByName(protoreflect.Name(spec.name))
	if method == nil {
		t.Fatalf("missing %s descriptor on %s", spec.name, service.FullName())
	}
	if got := method.Input().FullName(); got != protoreflect.FullName(spec.input) {
		t.Fatalf("%s input = %s, want %s", spec.name, got, spec.input)
	}
	if got := method.Output().FullName(); got != protoreflect.FullName(spec.output) {
		t.Fatalf("%s output = %s, want %s", spec.name, got, spec.output)
	}
	if got := method.IsStreamingClient(); got != spec.clientStreaming {
		t.Fatalf("%s client streaming = %t, want %t", spec.name, got, spec.clientStreaming)
	}
	if got := method.IsStreamingServer(); got != spec.serverStreaming {
		t.Fatalf("%s server streaming = %t, want %t", spec.name, got, spec.serverStreaming)
	}
}

func requireUCITransportFields(t *testing.T, file protoreflect.FileDescriptor, messageName string, specs ...uciTransportFieldSpec) {
	t.Helper()

	message := file.Messages().ByName(protoreflect.Name(messageName))
	if message == nil {
		t.Fatalf("missing %s descriptor", messageName)
	}
	for _, spec := range specs {
		field := message.Fields().ByName(protoreflect.Name(spec.name))
		if field == nil {
			t.Fatalf("missing %s.%s descriptor", messageName, spec.name)
		}
		if got := field.Number(); got != spec.number {
			t.Fatalf("%s.%s number = %d, want %d", messageName, spec.name, got, spec.number)
		}
		if got := field.Kind(); got != spec.kind {
			t.Fatalf("%s.%s kind = %s, want %s", messageName, spec.name, got, spec.kind)
		}
		if got := field.Cardinality(); got != spec.cardinality {
			t.Fatalf("%s.%s cardinality = %s, want %s", messageName, spec.name, got, spec.cardinality)
		}
		if spec.proto3Optional && !field.HasOptionalKeyword() {
			t.Fatalf("%s.%s must preserve optional presence", messageName, spec.name)
		}
		if spec.message == "" {
			continue
		}
		if got := field.Message(); got == nil || got.FullName() != protoreflect.FullName(spec.message) {
			t.Fatalf("%s.%s message = %v, want %s", messageName, spec.name, got, spec.message)
		}
	}
}
