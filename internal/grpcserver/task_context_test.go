package grpcserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/taskcontext"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestTaskContextRetainsNativeRPCGuards(t *testing.T) {
	_, server := New(nil, nil)
	const project = "10000000-0000-4000-8000-000000000001"
	arguments, err := json.Marshal(taskcontext.Request{Schema: 1, HostSessionID: "host", Query: "Alpha", DeadlineUnixMS: time.Now().Add(500 * time.Millisecond).UnixMilli(), MaxBytes: 8000})
	require.NoError(t, err)
	request := &pb.CallToolRequest{ToolName: "codebase_task_context", ArgumentsJson: arguments, SessionId: "native-transport-session", Project: project}
	identity := auth.ClientWithPrincipal("read-write", "fixture-workstation", "agent/task-guard", auth.PrincipalKindAgent)
	caller := auth.WithIdentity(context.Background(), identity)
	_, err = server.CallTool(caller, request)
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "missing native locator was admitted")
	caller = metadata.NewIncomingContext(caller, metadata.Pairs(taskcontext.LocatorMetadataKey, "file:///fixture/native-worktree"))
	_, err = server.CallTool(caller, request)
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "canonical project bypassed request correlation")
	correlation, ok := auditcontext.NewUCIRequestCorrelation(json.RawMessage("7"))
	require.True(t, ok)
	caller = metadata.NewIncomingContext(caller, metadata.Pairs(taskcontext.LocatorMetadataKey, "file:///fixture/native-worktree", auditcontext.UCIRequestCorrelationMetadataKey, correlation.MetadataValue()))
	withoutProject := *request
	withoutProject.Project = ""
	_, err = server.CallTool(caller, &withoutProject)
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "project-free task request was admitted")
	noauth := auth.WithIdentity(caller, auth.AuthDisabled())
	_, err = server.CallTool(noauth, request)
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "noauth request borrowed a missing installation identity")
	hap := auth.WithIdentity(caller, auth.ClientWithPrincipal("read-write", "fixture-service", auth.ProjectServicePrincipal(project), auth.PrincipalKindService))
	_, err = server.CallTool(hap, request)
	require.Error(t, err, "task context weakened the HAP rejection boundary")
}
