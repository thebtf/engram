package engramcore

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestSafeRemoteURL_RemovesUserinfoAndDropsMalformedRemote(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want string
	}{
		{raw: "https://fixture-user:fixture-credential@example.invalid/acme/identity.git", want: "https://example.invalid/acme/identity.git"},
		{raw: "//fixture-user:fixture-credential@example.invalid/%zz", want: "[invalid remote URL]"},
	} {
		if got := safeRemoteURL(tt.raw); got != tt.want || strings.Contains(got, "fixture-credential") {
			t.Fatal("credential-bearing remote was not sanitized")
		}
	}
}

func TestHAP01SourceDiagnostic_TokenInterceptorProjectsPresenceOnly(t *testing.T) {
	const fixtureToken = "hap01-fixture-keycard"
	metadataPresent := false
	metadataLength := 0
	err := tokenInterceptor(fixtureToken)(context.Background(), "/engram.v1.EngramService/Initialize", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			values, ok := metadata.FromOutgoingContext(ctx)
			authorization := values.Get("authorization")
			metadataPresent = ok && len(authorization) == 1
			if metadataPresent {
				metadataLength = len(authorization[0])
			}
			return nil
		})
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if !metadataPresent || metadataLength != len("Bearer ")+len(fixtureToken) {
		t.Fatal("token interceptor did not project authorization metadata")
	}
}
