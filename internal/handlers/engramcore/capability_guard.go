package engramcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/thebtf/engram/internal/module"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

func capabilityNotSent(message string) error {
	return &module.ModuleError{Code: "CAPABILITY_NOT_SENT", Message: "No backend tool request was sent. " + message}
}

func unconditionallySafeHealth(name string, args json.RawMessage) bool {
	if name != "check_system_health" {
		return false
	}
	if len(bytes.TrimSpace(args)) == 0 {
		return true
	}
	var fields map[string]json.RawMessage
	return json.Unmarshal(args, &fields) == nil && fields != nil && len(fields) == 0
}

// checkProxyCapability uses the existing authenticated discovery result. The
// server remains responsible for authorization and semantic validation.
func (m *Module) checkProxyCapability(ctx context.Context, conn *grpc.ClientConn, serverURL, token, projectID, name string, original, outgoing json.RawMessage) error {
	key, err := advisorProofKeyFor(serverURL, token)
	if err != nil {
		return capabilityNotSent("The configured backend identity is invalid.")
	}
	if ctx.Err() != nil {
		return capabilityNotSent("Backend discovery was cancelled before dispatch.")
	}
	m.refreshDiscovery(serverURL, token, projectID)
	waitCtx, cancel := context.WithTimeout(ctx, proxyToolsDiscoveryTimeout)
	defer cancel()
	var shutdown chan struct{}
	var catalog discoveredCatalog
	for {
		m.refreshMu.Lock()
		var ok bool
		catalog, ok = m.liveCatalog[key]
		terminal := m.terminalDiscovery[key]
		active := len(m.refreshing[key]) != 0
		refreshCtx := m.refreshCtx
		var signal chan struct{}
		if !ok && !terminal && active {
			signal = m.discoveryUpdates[key]
			if signal == nil {
				signal = make(chan struct{})
				m.discoveryUpdates[key] = signal
			}
		}
		m.refreshMu.Unlock()
		if waitCtx.Err() != nil {
			return capabilityNotSent("Backend discovery did not complete before the caller or existing discovery deadline.")
		}
		if ok && conn.GetState() == connectivity.Ready {
			break
		}
		if ok {
			m.invalidateDiscovery(key, serverURL, token)
			m.refreshDiscovery(serverURL, token, projectID)
			continue
		}
		if terminal || !active {
			return capabilityNotSent("Backend capability is not currently available.")
		}
		if shutdown == nil {
			shutdown = make(chan struct{})
			go func() {
				for {
					state := conn.GetState()
					if state == connectivity.Shutdown {
						close(shutdown)
						return
					}
					if !conn.WaitForStateChange(waitCtx, state) {
						return
					}
				}
			}()
		}
		select {
		case <-waitCtx.Done():
			return capabilityNotSent("Backend discovery did not complete before the caller or existing discovery deadline.")
		case <-shutdown:
			return capabilityNotSent("Backend connection was shut down before dispatch.")
		case <-refreshCtx.Done():
			return capabilityNotSent("Backend discovery stopped before dispatch.")
		case <-signal:
		}
	}
	for _, tool := range catalog.tools {
		if tool.Name == name {
			if err := advertisedFields(tool.InputSchema, original, false); err != nil {
				return capabilityNotSent("The backend does not advertise the supplied argument shape.")
			}
			if err := advertisedFields(tool.InputSchema, outgoing, false); err != nil {
				return capabilityNotSent("The backend does not advertise the prepared argument shape.")
			}
			return nil
		}
	}
	return capabilityNotSent("The backend does not advertise this tool.")
}

// advertisedFields checks field coverage only. It intentionally does not
// implement JSON Schema validation; handlers own types, constraints and auth.
func advertisedFields(schema, value json.RawMessage, nested bool) error {
	var definition map[string]json.RawMessage
	if json.Unmarshal(schema, &definition) != nil || definition == nil {
		return fmt.Errorf("invalid advertised schema")
	}
	value = bytes.TrimSpace(value)
	if len(value) == 0 {
		value = json.RawMessage(`{}`)
	}
	if !json.Valid(value) {
		return fmt.Errorf("invalid arguments")
	}
	if raw := definition["enum"]; len(raw) != 0 {
		var options []json.RawMessage
		if json.Unmarshal(raw, &options) != nil {
			return fmt.Errorf("invalid advertised enum")
		}
		matched := false
		for _, option := range options {
			option = bytes.TrimSpace(option)
			var actualString, optionString string
			if value[0] == '"' && len(option) != 0 && option[0] == '"' && json.Unmarshal(value, &actualString) == nil && json.Unmarshal(option, &optionString) == nil && actualString == optionString || bytes.Equal(value, option) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("unadvertised enum value")
		}
	}
	switch value[0] {
	case '{':
		var fields map[string]json.RawMessage
		if json.Unmarshal(value, &fields) != nil {
			return fmt.Errorf("invalid argument object")
		}
		var properties map[string]json.RawMessage
		if raw := definition["properties"]; len(raw) != 0 && json.Unmarshal(raw, &properties) != nil {
			return fmt.Errorf("invalid advertised properties")
		}
		for name, field := range fields {
			child, found := properties[name]
			if !found {
				additional := bytes.TrimSpace(definition["additionalProperties"])
				if bytes.Equal(additional, []byte("true")) || nested && len(definition["properties"]) == 0 && len(additional) == 0 {
					continue
				}
				if len(additional) == 0 || additional[0] != '{' {
					return fmt.Errorf("unadvertised argument")
				}
				child = additional
			}
			if err := advertisedFields(child, field, true); err != nil {
				return err
			}
		}
	case '[':
		var items []json.RawMessage
		if json.Unmarshal(value, &items) != nil {
			return fmt.Errorf("invalid argument array")
		}
		if len(items) != 0 && len(definition["items"]) == 0 {
			return fmt.Errorf("unadvertised array item")
		}
		for _, item := range items {
			if err := advertisedFields(definition["items"], item, true); err != nil {
				return err
			}
		}
	}
	return nil
}
