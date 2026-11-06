package engramcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/config"
	modulepkg "github.com/thebtf/engram/internal/module"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"google.golang.org/grpc"
)

const advisorFixtureToken = "engram_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type advisorProofTestServer struct {
	pb.UnimplementedEngramServiceServer

	mu              sync.Mutex
	initializeProof []byte
}

func (s *advisorProofTestServer) Initialize(_ context.Context, _ *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	s.mu.Lock()
	proof := bytes.Clone(s.initializeProof)
	s.mu.Unlock()
	return &pb.InitializeResponse{
		Tools:                           []*pb.ToolDefinition{{Name: "recall", Description: "fixture", InputSchemaJson: []byte(`{"type":"object"}`)}},
		AuthenticatedSubjectProofSha256: proof,
	}, nil
}

func (s *advisorProofTestServer) setInitializeProof(proof []byte) {
	s.mu.Lock()
	s.initializeProof = bytes.Clone(proof)
	s.mu.Unlock()
}

type advisorProofFixture struct {
	server    *advisorProofTestServer
	serverURL string
	module    *Module
	proof     advisorSubjectProof
	notifier  *discoveryNotifier
	env       map[string]string
}

func newAdvisorProofFixture(t *testing.T) *advisorProofFixture {
	t.Helper()
	proof := sha256.Sum256([]byte("advisor-initialize-subject"))
	server := &advisorProofTestServer{initializeProof: proof[:]}
	serverURL := startAdvisorProofServer(t, server)
	env := map[string]string{
		config.EnvServerURL:        serverURL,
		config.EnvWorkstationToken: advisorFixtureToken,
	}
	module := NewModule()
	if err := module.Init(context.Background(), modulepkg.ModuleDeps{}); err != nil {
		t.Fatal(err)
	}
	notifier := &discoveryNotifier{notifications: make(chan string, 8)}
	module.SetNotifier(notifier)
	t.Cleanup(func() { _ = module.Shutdown(context.Background()) })
	return &advisorProofFixture{
		server:    server,
		serverURL: serverURL,
		module:    module,
		proof:     advisorSubjectProof(proof),
		notifier:  notifier,
		env:       cloneAdvisorEnv(env),
	}
}

func startAdvisorProofServer(t *testing.T, server *advisorProofTestServer) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	pb.RegisterEngramServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	return "http://" + listener.Addr().String()
}

func cloneAdvisorEnv(env map[string]string) map[string]string {
	clone := make(map[string]string, len(env))
	for key, value := range env {
		clone[key] = value
	}
	return clone
}

func (f *advisorProofFixture) proxyTools(projectID, cwd string) error {
	project := muxcore.ProjectContext{ID: projectID, Cwd: cwd, Env: cloneAdvisorEnv(f.env)}
	_, err := f.module.ProxyTools(context.Background(), project)
	return err
}

func (f *advisorProofFixture) requireProxyTools(t *testing.T, projectID, cwd string) {
	t.Helper()
	if err := f.proxyTools(projectID, cwd); err != nil {
		t.Fatalf("ProxyTools: %v", err)
	}
	select {
	case <-f.notifier.notifications:
	case <-time.After(5 * time.Second):
		t.Fatal("advisor discovery did not complete")
	}
}

func TestProxyToolsCachesOnlyAcceptedInitializeSubjectProof(t *testing.T) {
	fixture := newAdvisorProofFixture(t)
	projectCWD := t.TempDir()
	fixture.requireProxyTools(t, "project-one", projectCWD)

	_, proof, ok, err := fixture.module.advisorProofs.lookup(fixture.serverURL, advisorFixtureToken)
	if err != nil || !ok || proof != fixture.proof {
		t.Fatalf("accepted proof lookup proof=%x ok=%t err=%v", proof, ok, err)
	}

	fixture.server.setInitializeProof(nil)
	fixture.requireProxyTools(t, "project-two", t.TempDir())
	_, _, ok, err = fixture.module.advisorProofs.lookup(fixture.serverURL, advisorFixtureToken)
	if err != nil || ok {
		t.Fatalf("empty old-server proof remained cached ok=%t err=%v", ok, err)
	}

	fixture.server.setInitializeProof(fixture.proof[:])
	fixture.requireProxyTools(t, "project-three", t.TempDir())
	fixture.server.setInitializeProof([]byte("malformed"))
	if err := fixture.proxyTools("project-four", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, _, ok, err = fixture.module.advisorProofs.lookup(fixture.serverURL, advisorFixtureToken)
		if err != nil || !ok {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("malformed proof did not invalidate cached proof")
		case <-ticker.C:
		}
	}
	_, _, ok, err = fixture.module.advisorProofs.lookup(fixture.serverURL, advisorFixtureToken)
	if err != nil || ok {
		t.Fatalf("malformed proof remained cached ok=%t err=%v", ok, err)
	}
}

func TestAdvisorProofCacheIsBounded(t *testing.T) {
	cache := newAdvisorProofCache()
	proof := sha256.Sum256([]byte("bounded-proof"))
	for index := range maxAdvisorSubjectProofCacheItems {
		serverURL := fmt.Sprintf("http://127.0.0.1:%d", 30000+index)
		if err := cache.record(serverURL, advisorFixtureToken, proof[:]); err != nil {
			t.Fatalf("record proof %d: %v", index, err)
		}
	}
	if err := cache.record("http://127.0.0.1:40000", advisorFixtureToken, proof[:]); err == nil {
		t.Fatal("proof cache accepted an unbounded authority")
	}
}
