package grpcserver

import (
	"context"
	"io"
	"testing"
	"time"

	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
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
		uciTransportFieldSpec{name: "context_handle", number: 3, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
	)
	requireUCITransportFields(t, file, "BindCodeContextResponse",
		uciTransportFieldSpec{name: "context_handle", number: 1, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "context", number: 2, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.ContextRef"},
		uciTransportFieldSpec{name: "index_scope", number: 3, kind: protoreflect.MessageKind, cardinality: protoreflect.Optional, message: "engram.v1.CodeIndexScope"},
		uciTransportFieldSpec{name: "local_root_id", number: 4, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
		uciTransportFieldSpec{name: "workstation_id", number: 5, kind: protoreflect.StringKind, cardinality: protoreflect.Optional},
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

const (
	uciTransportContractSpaceID       = "11111111-1111-4111-8111-111111111111"
	uciTransportContractSourceID      = "22222222-2222-4222-8222-222222222222"
	uciTransportContractCheckoutID    = "33333333-3333-4333-8333-333333333333"
	uciTransportContractViewID        = "44444444-4444-4444-8444-444444444444"
	uciTransportContractProfileID     = "55555555-5555-4555-8555-555555555555"
	uciTransportContractIncarnationID = "66666666-6666-4666-8666-666666666666"
	uciTransportContractDigest        = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestUCITransportContractDelegatesValidatedRequests(t *testing.T) {
	ctx := context.Background()
	server := &Server{}
	if _, err := server.BindCodeContext(ctx, uciTransportContractBindRequest()); status.Code(err) != codes.Unavailable {
		t.Fatalf("default-dark BindCodeContext code = %s, want %s", status.Code(err), codes.Unavailable)
	}

	runtime := &uciTransportContractFake{}
	server.SetUCITransport(runtime)

	if _, err := server.BindCodeContext(ctx, uciTransportContractBindRequest()); err != nil {
		t.Fatalf("BindCodeContext() error = %v", err)
	}
	if runtime.bindRequest == nil {
		t.Fatal("BindCodeContext did not delegate")
	}

	begin := uciTransportContractBeginRequest()
	if begin.GetExpectedParent() != nil {
		t.Fatal("initial BeginCodeIndex request fabricated an expected parent")
	}
	if _, err := server.BeginCodeIndex(ctx, begin); err != nil {
		t.Fatalf("BeginCodeIndex() error = %v", err)
	}
	if runtime.beginRequest != begin {
		t.Fatal("BeginCodeIndex did not delegate the validated request")
	}

	if _, err := server.FinalizeCodeIndex(ctx, uciTransportContractFinalizeRequest()); err != nil {
		t.Fatalf("FinalizeCodeIndex() error = %v", err)
	}
	if runtime.finalizeRequest == nil {
		t.Fatal("FinalizeCodeIndex did not delegate")
	}

	if _, err := server.QueryCode(ctx, uciTransportContractQueryRequest()); err != nil {
		t.Fatalf("QueryCode() error = %v", err)
	}
	if runtime.queryRequest == nil {
		t.Fatal("QueryCode did not delegate")
	}

	if _, err := server.ExploreCode(ctx, uciTransportContractExploreRequest()); err != nil {
		t.Fatalf("ExploreCode() error = %v", err)
	}
	if runtime.exploreRequest == nil {
		t.Fatal("ExploreCode did not delegate")
	}

	runtime.queryErr = status.Error(codes.PermissionDenied, "runtime denied")
	if _, err := server.QueryCode(ctx, uciTransportContractQueryRequest()); status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "runtime denied" {
		t.Fatalf("QueryCode runtime status = %v, want preserved PermissionDenied", err)
	}

	runtime.exploreRequest = nil
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := server.ExploreCode(canceled, uciTransportContractExploreRequest()); status.Code(err) != codes.Canceled {
		t.Fatalf("canceled ExploreCode code = %s, want %s", status.Code(err), codes.Canceled)
	}
	if runtime.exploreRequest != nil {
		t.Fatal("canceled ExploreCode reached the runtime")
	}
}

func TestUCITransportContractAcceptsCompleteNoViewHandleBinding(t *testing.T) {
	runtime := &uciTransportContractFake{}
	response := uciTransportContractBindResponse()
	response.Context = nil
	runtime.bindResponse = response
	server := &Server{}
	server.SetUCITransport(runtime)

	bound, err := server.BindCodeContext(context.Background(), uciTransportContractBindHandleRequest())
	if err != nil {
		t.Fatalf("BindCodeContext() error = %v", err)
	}
	if bound.GetContext() != nil {
		t.Fatalf("no-View binding context = %v, want nil", bound.GetContext())
	}
	if bound.GetContextHandle() != uciTransportContractBindHandleRequest().GetContextHandle() || bound.GetIndexScope() == nil || bound.GetLocalRootId() == "" || bound.GetWorkstationId() == "" {
		t.Fatalf("incomplete no-View binding = %#v", bound)
	}
}

func TestUCITransportContractStagesOnlyValidatedConsistentFrames(t *testing.T) {
	server := &Server{}
	runtime := &uciTransportContractFake{}
	server.SetUCITransport(runtime)

	for _, test := range []struct {
		name   string
		frames []*pb.StageCodeIndexFrame
	}{
		{name: "empty EOF", frames: nil},
		{name: "first sequence is not zero", frames: []*pb.StageCodeIndexFrame{uciTransportContractStageFrame(1)}},
		{name: "sequence gap", frames: []*pb.StageCodeIndexFrame{uciTransportContractStageFrame(0), uciTransportContractStageFrame(2)}},
		{name: "scope changes", frames: func() []*pb.StageCodeIndexFrame {
			frames := []*pb.StageCodeIndexFrame{uciTransportContractStageFrame(0), uciTransportContractStageFrame(1)}
			frames[1].Scope.CheckoutId = "77777777-7777-4777-8777-777777777777"
			return frames
		}()},
		{name: "build changes", frames: func() []*pb.StageCodeIndexFrame {
			frames := []*pb.StageCodeIndexFrame{uciTransportContractStageFrame(0), uciTransportContractStageFrame(1)}
			frames[1].BuildId = "other-build"
			return frames
		}()},
		{name: "lease changes", frames: func() []*pb.StageCodeIndexFrame {
			frames := []*pb.StageCodeIndexFrame{uciTransportContractStageFrame(0), uciTransportContractStageFrame(1)}
			frames[1].LeaseEpoch++
			return frames
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &uciTransportContractStageStream{ctx: context.Background(), frames: test.frames}
			if err := server.StageCodeIndex(stream); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("StageCodeIndex() code = %s, want %s", status.Code(err), codes.InvalidArgument)
			}
			if stream.response != nil {
				t.Fatal("invalid stream received a close response")
			}
		})
	}
	if runtime.stageCalls != 0 {
		t.Fatalf("runtime received %d invalid stage calls", runtime.stageCalls)
	}

	frames := []*pb.StageCodeIndexFrame{uciTransportContractStageFrame(0), uciTransportContractStageFrame(1)}
	stream := &uciTransportContractStageStream{ctx: context.Background(), frames: frames}
	if err := server.StageCodeIndex(stream); err != nil {
		t.Fatalf("StageCodeIndex() error = %v", err)
	}
	if runtime.stageCalls != 1 || len(runtime.stageFrames) != len(frames) {
		t.Fatalf("runtime stage delegation = calls:%d frames:%d, want 1/%d", runtime.stageCalls, len(runtime.stageFrames), len(frames))
	}
	if stream.response == nil || stream.response.GetAcceptedSequence() != 1 || stream.response.GetAcceptedPartCount() != 2 {
		t.Fatalf("stage close response = %#v", stream.response)
	}
}

type uciTransportContractFake struct {
	calls            int
	bindRequest      *pb.BindCodeContextRequest
	bindResponse     *pb.BindCodeContextResponse
	beginRequest     *pb.BeginCodeIndexRequest
	beginResponse    *pb.BeginCodeIndexResponse
	stageFrames      []*pb.StageCodeIndexFrame
	stageCalls       int
	stageResponse    *pb.StageCodeIndexResponse
	finalizeRequest  *pb.FinalizeCodeIndexRequest
	finalizeResponse *pb.FinalizeCodeIndexResponse
	queryRequest     *pb.QueryCodeRequest
	queryResponse    *pb.QueryCodeResponse
	exploreRequest   *pb.ExploreCodeRequest
	exploreResponse  *pb.ExploreCodeResponse
	queryErr         error
}

func (fake *uciTransportContractFake) BindCodeContext(_ context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
	fake.calls++
	fake.bindRequest = request
	if fake.bindResponse != nil {
		return fake.bindResponse, nil
	}
	return uciTransportContractBindResponse(), nil
}

func (fake *uciTransportContractFake) BeginCodeIndex(_ context.Context, request *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error) {
	fake.calls++
	fake.beginRequest = request
	if fake.beginResponse != nil {
		return fake.beginResponse, nil
	}
	return &pb.BeginCodeIndexResponse{Scope: request.GetScope(), BuildId: "server-build", LeaseEpoch: 7, LeaseExpiresAt: timestamppb.Now()}, nil
}

func (fake *uciTransportContractFake) StageCodeIndex(_ context.Context, frames []*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error) {
	fake.calls++
	fake.stageCalls++
	fake.stageFrames = frames
	if fake.stageResponse != nil {
		return fake.stageResponse, nil
	}
	return &pb.StageCodeIndexResponse{
		BuildId:           frames[0].GetBuildId(),
		AcceptedSequence:  frames[len(frames)-1].GetSequence(),
		AcceptedPartCount: uint64(len(frames)),
		PartDigest:        uciTransportContractDigest,
	}, nil
}

func (fake *uciTransportContractFake) FinalizeCodeIndex(_ context.Context, request *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error) {
	fake.calls++
	fake.finalizeRequest = request
	if fake.finalizeResponse != nil {
		return fake.finalizeResponse, nil
	}
	return &pb.FinalizeCodeIndexResponse{
		PublishedContext:           uciTransportContractContext(),
		BuildId:                    request.GetBuildId(),
		LeaseEpoch:                 request.GetLeaseEpoch(),
		AcceptedFilesystemSequence: request.GetObservedFilesystemSequence(),
	}, nil
}

func (fake *uciTransportContractFake) QueryCode(_ context.Context, request *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error) {
	fake.calls++
	fake.queryRequest = request
	if fake.queryErr != nil {
		return nil, fake.queryErr
	}
	if fake.queryResponse != nil {
		return fake.queryResponse, nil
	}
	return &pb.QueryCodeResponse{Context: request.GetContext(), ResponseJson: []byte(`{"status":"ok"}`)}, nil
}

func (fake *uciTransportContractFake) ExploreCode(_ context.Context, request *pb.ExploreCodeRequest) (*pb.ExploreCodeResponse, error) {
	fake.calls++
	fake.exploreRequest = request
	if fake.exploreResponse != nil {
		return fake.exploreResponse, nil
	}
	return &pb.ExploreCodeResponse{Context: request.GetContext(), ResponseJson: []byte(`{"status":"ok"}`)}, nil
}

var _ UCITransport = (*uciTransportContractFake)(nil)

type uciTransportContractStageStream struct {
	ctx      context.Context
	frames   []*pb.StageCodeIndexFrame
	position int
	response *pb.StageCodeIndexResponse
}

func (stream *uciTransportContractStageStream) SetHeader(metadata.MD) error  { return nil }
func (stream *uciTransportContractStageStream) SendHeader(metadata.MD) error { return nil }
func (stream *uciTransportContractStageStream) SetTrailer(metadata.MD)       {}
func (stream *uciTransportContractStageStream) Context() context.Context     { return stream.ctx }
func (stream *uciTransportContractStageStream) SendMsg(any) error            { return nil }
func (stream *uciTransportContractStageStream) RecvMsg(any) error            { return nil }

func (stream *uciTransportContractStageStream) Recv() (*pb.StageCodeIndexFrame, error) {
	if stream.position >= len(stream.frames) {
		return nil, io.EOF
	}
	frame := stream.frames[stream.position]
	stream.position++
	return frame, nil
}

func (stream *uciTransportContractStageStream) SendAndClose(response *pb.StageCodeIndexResponse) error {
	stream.response = response
	return nil
}

var _ grpc.ClientStreamingServer[pb.StageCodeIndexFrame, pb.StageCodeIndexResponse] = (*uciTransportContractStageStream)(nil)

func uciTransportContractContext() *pb.ContextRef {
	spaceID := uciTransportContractSpaceID
	return &pb.ContextRef{
		SpaceId:           &spaceID,
		SourceId:          uciTransportContractSourceID,
		CheckoutId:        uciTransportContractCheckoutID,
		ViewId:            uciTransportContractViewID,
		Generation:        1,
		AnalysisProfileId: uciTransportContractProfileID,
	}
}

func uciTransportContractScope() *pb.CodeIndexScope {
	return &pb.CodeIndexScope{
		SourceId:          uciTransportContractSourceID,
		CheckoutId:        uciTransportContractCheckoutID,
		IncarnationId:     uciTransportContractIncarnationID,
		AnalysisProfileId: uciTransportContractProfileID,
	}
}

func uciTransportContractBindRequest() *pb.BindCodeContextRequest {
	return &pb.BindCodeContextRequest{ClientSessionId: "client-session", RequestedContext: uciTransportContractContext()}
}

func uciTransportContractBindHandleRequest() *pb.BindCodeContextRequest {
	return &pb.BindCodeContextRequest{ClientSessionId: "client-session", ContextHandle: "context-handle"}
}

func uciTransportContractBindResponse() *pb.BindCodeContextResponse {
	return &pb.BindCodeContextResponse{
		ContextHandle: "context-handle",
		Context:       uciTransportContractContext(),
		IndexScope:    uciTransportContractScope(),
		LocalRootId:   "local-root-id",
		WorkstationId: "workstation-id",
	}
}

func uciTransportContractBeginRequest() *pb.BeginCodeIndexRequest {
	return &pb.BeginCodeIndexRequest{
		Scope:         uciTransportContractScope(),
		OwnerInstance: "daemon-instance",
		BuildKey:      "client-build-key",
		ManifestMode:  "full",
		JobKind:       "initial_index",
	}
}

func uciTransportContractFinalizeRequest() *pb.FinalizeCodeIndexRequest {
	startedAt := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	return &pb.FinalizeCodeIndexRequest{
		Scope:                      uciTransportContractScope(),
		BuildId:                    "server-build",
		LeaseEpoch:                 7,
		ManifestPartCount:          2,
		PartsDigest:                uciTransportContractDigest,
		ManifestEntryCount:         3,
		ManifestDigest:             uciTransportContractDigest,
		EdgeCount:                  4,
		EdgesDigest:                uciTransportContractDigest,
		ObservedFilesystemSequence: 9,
		ScanStartedAt:              timestamppb.New(startedAt),
		ScanCompletedAt:            timestamppb.New(startedAt.Add(time.Second)),
		ScanOutcome:                "complete",
		CompleteCensus:             true,
		CoverageJson:               []byte(`{"structural":"complete"}`),
	}
}

func uciTransportContractQueryRequest() *pb.QueryCodeRequest {
	return &pb.QueryCodeRequest{Context: uciTransportContractContext(), Query: "needle", MaxResults: 10, MaxBytes: 1024, DeadlineMs: 1_000}
}

func uciTransportContractExploreRequest() *pb.ExploreCodeRequest {
	return &pb.ExploreCodeRequest{
		Context:         uciTransportContractContext(),
		Operation:       "impact",
		Subject:         "needle",
		MaxDepth:        4,
		MaxVisitedNodes: 100,
		MaxResultNodes:  10,
		MaxResultEdges:  20,
		DeadlineMs:      1_000,
	}
}

func uciTransportContractStageFrame(sequence uint64) *pb.StageCodeIndexFrame {
	return &pb.StageCodeIndexFrame{
		Scope:         uciTransportContractScope(),
		BuildId:       "server-build",
		LeaseEpoch:    7,
		Sequence:      sequence,
		PayloadDigest: uciTransportContractDigest,
		Payload:       []byte("payload"),
	}
}

func TestUCITransportContractRejectsClosedInputsAndInvalidRuntimeResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		invoke func(*Server) error
	}{
		{name: "control identifier", invoke: func(server *Server) error {
			request := uciTransportContractBindRequest()
			request.ClientSessionId = "client\x00session"
			_, err := server.BindCodeContext(context.Background(), request)
			return err
		}},
		{name: "missing bind selector", invoke: func(server *Server) error {
			_, err := server.BindCodeContext(context.Background(), &pb.BindCodeContextRequest{ClientSessionId: "client-session"})
			return err
		}},
		{name: "multiple bind selectors", invoke: func(server *Server) error {
			request := uciTransportContractBindRequest()
			request.ContextHandle = "context-handle"
			_, err := server.BindCodeContext(context.Background(), request)
			return err
		}},
		{name: "invalid context handle", invoke: func(server *Server) error {
			request := uciTransportContractBindHandleRequest()
			request.ContextHandle = "context\x00handle"
			_, err := server.BindCodeContext(context.Background(), request)
			return err
		}},
		{name: "invalid manifest mode", invoke: func(server *Server) error {
			request := uciTransportContractBeginRequest()
			request.ManifestMode = "snapshot"
			_, err := server.BeginCodeIndex(context.Background(), request)
			return err
		}},
		{name: "invalid job kind", invoke: func(server *Server) error {
			request := uciTransportContractBeginRequest()
			request.JobKind = "backfill"
			_, err := server.BeginCodeIndex(context.Background(), request)
			return err
		}},
		{name: "invalid payload digest", invoke: func(server *Server) error {
			frame := uciTransportContractStageFrame(0)
			frame.PayloadDigest = "sha256:UPPERCASE"
			return server.StageCodeIndex(&uciTransportContractStageStream{ctx: context.Background(), frames: []*pb.StageCodeIndexFrame{frame}})
		}},
		{name: "invalid parts digest", invoke: func(server *Server) error {
			request := uciTransportContractFinalizeRequest()
			request.PartsDigest = "digest"
			_, err := server.FinalizeCodeIndex(context.Background(), request)
			return err
		}},
		{name: "invalid manifest digest", invoke: func(server *Server) error {
			request := uciTransportContractFinalizeRequest()
			request.ManifestDigest = "digest"
			_, err := server.FinalizeCodeIndex(context.Background(), request)
			return err
		}},
		{name: "invalid edges digest", invoke: func(server *Server) error {
			request := uciTransportContractFinalizeRequest()
			request.EdgesDigest = "digest"
			_, err := server.FinalizeCodeIndex(context.Background(), request)
			return err
		}},
		{name: "invalid scan outcome", invoke: func(server *Server) error {
			request := uciTransportContractFinalizeRequest()
			request.ScanOutcome = "unknown"
			_, err := server.FinalizeCodeIndex(context.Background(), request)
			return err
		}},
		{name: "coverage is not an object", invoke: func(server *Server) error {
			request := uciTransportContractFinalizeRequest()
			request.CoverageJson = []byte("[]")
			_, err := server.FinalizeCodeIndex(context.Background(), request)
			return err
		}},
		{name: "unsupported explore operation", invoke: func(server *Server) error {
			request := uciTransportContractExploreRequest()
			request.Operation = "traverse"
			_, err := server.ExploreCode(context.Background(), request)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &uciTransportContractFake{}
			server := &Server{}
			server.SetUCITransport(runtime)
			if err := test.invoke(server); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("rejection code = %s, want %s", status.Code(err), codes.InvalidArgument)
			}
			if runtime.calls != 0 {
				t.Fatalf("invalid request reached runtime %d times", runtime.calls)
			}
		})
	}

	for _, test := range []struct {
		name   string
		invoke func(*Server, *uciTransportContractFake) error
	}{
		{name: "bind context mismatch", invoke: func(server *Server, runtime *uciTransportContractFake) error {
			request := uciTransportContractBindRequest()
			response := uciTransportContractBindResponse()
			response.Context.Generation++
			runtime.bindResponse = response
			_, err := server.BindCodeContext(context.Background(), request)
			return err
		}},
		{name: "bind requested context omits view", invoke: func(server *Server, runtime *uciTransportContractFake) error {
			response := uciTransportContractBindResponse()
			response.Context = nil
			runtime.bindResponse = response
			_, err := server.BindCodeContext(context.Background(), uciTransportContractBindRequest())
			return err
		}},
		{name: "bind handle changes opaque handle", invoke: func(server *Server, runtime *uciTransportContractFake) error {
			response := uciTransportContractBindResponse()
			response.Context = nil
			response.ContextHandle = "other-handle"
			runtime.bindResponse = response
			_, err := server.BindCodeContext(context.Background(), uciTransportContractBindHandleRequest())
			return err
		}},
		{name: "bind no-View omits root evidence", invoke: func(server *Server, runtime *uciTransportContractFake) error {
			response := uciTransportContractBindResponse()
			response.Context = nil
			response.LocalRootId = ""
			runtime.bindResponse = response
			_, err := server.BindCodeContext(context.Background(), uciTransportContractBindHandleRequest())
			return err
		}},
		{name: "begin scope mismatch", invoke: func(server *Server, runtime *uciTransportContractFake) error {
			request := uciTransportContractBeginRequest()
			scope := uciTransportContractScope()
			scope.CheckoutId = "77777777-7777-4777-8777-777777777777"
			runtime.beginResponse = &pb.BeginCodeIndexResponse{Scope: scope, BuildId: "server-build", LeaseEpoch: 7, LeaseExpiresAt: timestamppb.Now()}
			_, err := server.BeginCodeIndex(context.Background(), request)
			return err
		}},
		{name: "stage acknowledgement mismatch", invoke: func(server *Server, runtime *uciTransportContractFake) error {
			runtime.stageResponse = &pb.StageCodeIndexResponse{BuildId: "server-build", AcceptedSequence: 0, AcceptedPartCount: 1, PartDigest: uciTransportContractDigest}
			return server.StageCodeIndex(&uciTransportContractStageStream{ctx: context.Background(), frames: []*pb.StageCodeIndexFrame{uciTransportContractStageFrame(0), uciTransportContractStageFrame(1)}})
		}},
		{name: "finalize build mismatch", invoke: func(server *Server, runtime *uciTransportContractFake) error {
			request := uciTransportContractFinalizeRequest()
			runtime.finalizeResponse = &pb.FinalizeCodeIndexResponse{PublishedContext: uciTransportContractContext(), BuildId: "other-build", LeaseEpoch: request.GetLeaseEpoch(), AcceptedFilesystemSequence: request.GetObservedFilesystemSequence()}
			_, err := server.FinalizeCodeIndex(context.Background(), request)
			return err
		}},
		{name: "query payload is not an object", invoke: func(server *Server, runtime *uciTransportContractFake) error {
			request := uciTransportContractQueryRequest()
			runtime.queryResponse = &pb.QueryCodeResponse{Context: request.GetContext(), ResponseJson: []byte("[]")}
			_, err := server.QueryCode(context.Background(), request)
			return err
		}},
		{name: "explore context mismatch", invoke: func(server *Server, runtime *uciTransportContractFake) error {
			request := uciTransportContractExploreRequest()
			responseContext := uciTransportContractContext()
			responseContext.Generation++
			runtime.exploreResponse = &pb.ExploreCodeResponse{Context: responseContext, ResponseJson: []byte(`{"status":"ok"}`)}
			_, err := server.ExploreCode(context.Background(), request)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &uciTransportContractFake{}
			server := &Server{}
			server.SetUCITransport(runtime)
			if err := test.invoke(server, runtime); status.Code(err) != codes.Internal {
				t.Fatalf("invalid response code = %s, want %s", status.Code(err), codes.Internal)
			}
			if runtime.calls != 1 {
				t.Fatalf("invalid response runtime calls = %d, want 1", runtime.calls)
			}
		})
	}
}
