package legacyrelay

import (
	"context"
	"testing"

	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

type trackingNext struct {
	requests    int
	connects    int
	disconnects int
}

func (n *trackingNext) HandleRequest(context.Context, muxcore.ProjectContext, []byte) ([]byte, error) {
	n.requests++
	return []byte(`{"ok":true}`), nil
}

func (n *trackingNext) OnProjectConnect(muxcore.ProjectContext) { n.connects++ }
func (n *trackingNext) OnProjectDisconnect(string)              { n.disconnects++ }

func TestSessionTrackingHandlerObservesPeerWithoutChangingMCPResponse(t *testing.T) {
	child := mustProcess(t, 220, "child-incarnation", "/opt/engram", 110)
	inspector := &fakeProcessInspector{processes: map[int]ProcessIdentity{220: child}}
	children := NewChildRegistry(inspector, ChildImageGateFunc(func(image ProcessImage) bool {
		return image.Value() == "/opt/engram"
	}))
	next := &trackingNext{}
	handler, err := NewSessionTrackingHandler(next, children)
	if err != nil {
		t.Fatalf("NewSessionTrackingHandler: %v", err)
	}
	project := muxcore.ProjectContext{ID: "diagnostic-project", Env: map[string]string{"CHILD_ONLY": "yes"}}
	response, err := handler.HandleRequestWithSessionMeta(context.Background(), project, muxcore.SessionMeta{
		Conn: muxcore.ConnInfo{PeerPid: child.PID()},
	}, []byte(`{"jsonrpc":"2.0"}`))
	if err != nil || string(response) != `{"ok":true}` || next.requests != 1 {
		t.Fatalf("response=%s err=%v requests=%d", response, err, next.requests)
	}
	records := children.acceptedRecords()
	if len(records) != 1 {
		t.Fatalf("accepted child records=%d, want 1", len(records))
	}
	config, ok := children.ConfigFor(records[0].binding.ConfigRef())
	if !ok || config["CHILD_ONLY"] != "yes" {
		t.Fatalf("child config=%v ok=%v", config, ok)
	}
}

func TestSessionTrackingDisconnectPreservesLiveSiblingEvidence(t *testing.T) {
	first := mustProcess(t, 220, "child-one", "/opt/engram", 110)
	second := mustProcess(t, 221, "child-two", "/opt/engram", 111)
	inspector := &fakeProcessInspector{processes: map[int]ProcessIdentity{220: first, 221: second}}
	children := NewChildRegistry(inspector, ChildImageGateFunc(func(ProcessImage) bool { return true }))
	next := &trackingNext{}
	handler, err := NewSessionTrackingHandler(next, children)
	if err != nil {
		t.Fatalf("NewSessionTrackingHandler: %v", err)
	}
	project := muxcore.ProjectContext{ID: "same-diagnostic-project"}
	_, _ = handler.HandleRequestWithSessionMeta(context.Background(), project, muxcore.SessionMeta{Conn: muxcore.ConnInfo{PeerPid: first.PID()}}, nil)
	_, _ = handler.HandleRequestWithSessionMeta(context.Background(), project, muxcore.SessionMeta{Conn: muxcore.ConnInfo{PeerPid: second.PID()}}, nil)
	handler.OnProjectDisconnect(project.ID)
	if next.disconnects != 1 {
		t.Fatalf("delegated disconnects=%d, want 1", next.disconnects)
	}
	if got := len(children.acceptedRecords()); got != 2 {
		t.Fatalf("live child records after ambiguous project disconnect=%d, want 2", got)
	}
}
