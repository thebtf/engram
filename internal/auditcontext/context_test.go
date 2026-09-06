package auditcontext

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestUCITransportSessionRoundTripIsDistinctFromAuditValues(t *testing.T) {
	ctx := WithActor(context.Background(), "agent/alice")
	ctx = WithSourceSession(ctx, "legacy-source-session")
	ctx = WithUCITransportSession(ctx, "transport-tag-1")

	if got := UCITransportSession(ctx); got != "transport-tag-1" {
		t.Fatalf("UCI transport session = %q, want transport-tag-1", got)
	}
	if got := SourceSession(ctx); got != "legacy-source-session" {
		t.Fatalf("source session = %q, want legacy-source-session", got)
	}
	if got := Actor(ctx); got != "agent/alice" {
		t.Fatalf("actor = %q, want agent/alice", got)
	}
}

func TestUCITransportSessionRejectsInvalidTags(t *testing.T) {
	for _, value := range []string{
		"",
		" leading",
		"trailing ",
		"contains\nnewline",
		strings.Repeat("x", maxUCITransportSessionBytes+1),
		string([]byte{0xff}),
	} {
		if ValidUCITransportSession(value) {
			t.Errorf("ValidUCITransportSession(%q) = true, want false", value)
		}
		ctx := WithUCITransportSession(context.Background(), value)
		if got := UCITransportSession(ctx); got != "" {
			t.Errorf("UCITransportSession(%q) = %q, want empty", value, got)
		}
	}
}

func TestUCIRequestCorrelationPreservesCanonicalIdentityAndAuditClaims(t *testing.T) {
	first, ok := NewUCIRequestCorrelation(json.RawMessage(`"client-operation-42"`))
	if !ok {
		t.Fatal("NewUCIRequestCorrelation() rejected a valid string ID")
	}
	replay, ok := NewUCIRequestCorrelation(json.RawMessage(`"client-operation-42"`))
	if !ok || replay != first {
		t.Fatalf("exact retry correlation = %#v, %t; want %#v, true", replay, ok, first)
	}
	escaped, ok := NewUCIRequestCorrelation(json.RawMessage(`"client-operation-\u0034\u0032"`))
	if !ok || escaped != first {
		t.Fatalf("equivalent JSON string correlation = %#v, %t; want %#v, true", escaped, ok, first)
	}
	numeric, ok := NewUCIRequestCorrelation(json.RawMessage(`42`))
	if !ok || numeric == first || numeric.MetadataValue() == first.MetadataValue() {
		t.Fatalf("numeric correlation = %#v, %t; want a distinct canonical number", numeric, ok)
	}
	numericLexical, ok := NewUCIRequestCorrelation(json.RawMessage(`42.0`))
	if !ok || numericLexical == numeric || numericLexical.MetadataValue() == numeric.MetadataValue() {
		t.Fatalf("lexical numeric correlation = %#v, %t; want a distinct canonical number", numericLexical, ok)
	}
	numericLexicalID, ok := numericLexical.JSONRPCID()
	if !ok || string(numericLexicalID) != `42.0` {
		t.Fatalf("lexical numeric JSONRPCID() = %q, %t; want 42.0", numericLexicalID, ok)
	}
	quotedNumeric, ok := NewUCIRequestCorrelation(json.RawMessage(`"42"`))
	if !ok || quotedNumeric == numeric || quotedNumeric.MetadataValue() == numeric.MetadataValue() {
		t.Fatalf("string numeric correlation = %#v, %t; want a distinct canonical string", quotedNumeric, ok)
	}

	metadataValue := first.MetadataValue()
	if metadataValue == "" || strings.Contains(metadataValue, "client-operation-42") || strings.ContainsAny(metadataValue, "+/=") {
		t.Fatalf("metadata value %q is not opaque base64url", metadataValue)
	}
	for _, character := range metadataValue {
		if character > 0x7f {
			t.Fatalf("metadata value %q is not ASCII", metadataValue)
		}
	}
	parsed, ok := ParseUCIRequestCorrelation(metadataValue)
	if !ok || parsed != first {
		t.Fatalf("ParseUCIRequestCorrelation(%q) = %#v, %t; want %#v, true", metadataValue, parsed, ok, first)
	}
	jsonID, ok := first.JSONRPCID()
	if !ok || string(jsonID) != `"client-operation-42"` {
		t.Fatalf("JSONRPCID() = %q, %t; want canonical outer JSON-RPC ID", jsonID, ok)
	}

	for _, test := range []struct {
		name      string
		requestID json.RawMessage
		want      string
	}{
		{name: "empty", requestID: json.RawMessage(`""`), want: `""`},
		{name: "spaces", requestID: json.RawMessage(`"contains space"`), want: `"contains space"`},
		{name: "punctuation", requestID: json.RawMessage(`"/\\@:?#%="`), want: `"/\\@:?#%="`},
		{name: "unicode", requestID: json.RawMessage(`"операция-42"`), want: `"операция-42"`},
		{name: "escaped control", requestID: json.RawMessage(`"contains\nnewline"`), want: `"contains\nnewline"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			correlation, ok := NewUCIRequestCorrelation(test.requestID)
			if !ok {
				t.Fatalf("NewUCIRequestCorrelation(%s) rejected a valid opaque string ID", test.requestID)
			}
			jsonID, ok := correlation.JSONRPCID()
			if !ok || string(jsonID) != test.want {
				t.Fatalf("JSONRPCID() = %q, %t; want %s, true", jsonID, ok, test.want)
			}
			parsed, ok := ParseUCIRequestCorrelation(correlation.MetadataValue())
			if !ok || parsed != correlation {
				t.Fatalf("metadata round trip = %#v, %t; want %#v, true", parsed, ok, correlation)
			}
		})
	}

	ctx := WithActor(context.Background(), "agent/alice")
	ctx = WithSourceSession(ctx, "legacy-source-session")
	ctx = WithUCITransportSession(ctx, "transport-tag-1")
	ctx = WithUCIRequestCorrelation(ctx, first)
	if UCIRequestCorrelationRequired(ctx) {
		t.Fatal("unmarked context requires a UCI request correlation")
	}
	ctx = WithUCIRequestCorrelationRequired(ctx)
	if !UCIRequestCorrelationRequired(ctx) {
		t.Fatal("marked context does not require a UCI request correlation")
	}
	carried, ok := UCIRequestCorrelationFromContext(ctx)
	if !ok || carried != first {
		t.Fatalf("UCIRequestCorrelationFromContext() = %#v, %t; want %#v, true", carried, ok, first)
	}
	if got := Actor(ctx); got != "agent/alice" {
		t.Fatalf("actor = %q, want agent/alice", got)
	}
	if got := SourceSession(ctx); got != "legacy-source-session" {
		t.Fatalf("source session = %q, want legacy-source-session", got)
	}
	if got := UCITransportSession(ctx); got != "transport-tag-1" {
		t.Fatalf("UCI transport session = %q, want transport-tag-1", got)
	}
}

func TestUCIRequestCorrelationRejectsInvalidValues(t *testing.T) {
	for _, requestID := range []json.RawMessage{
		nil,
		json.RawMessage(`null`),
		json.RawMessage(`true`),
		json.RawMessage(`[]`),
		json.RawMessage(`{"not":"an-id"}`),
		json.RawMessage(`"unterminated`),
		json.RawMessage(`"bad\q"`),
		json.RawMessage(`42 43`),
		json.RawMessage(`"` + strings.Repeat("x", maxUCIRequestIdentityBytes+1) + `"`),
	} {
		if correlation, ok := NewUCIRequestCorrelation(requestID); ok || correlation.Valid() {
			t.Errorf("NewUCIRequestCorrelation(%s) = %#v, %t; want invalid", requestID, correlation, ok)
		}
	}

	for _, value := range []string{
		"",
		`"client-operation-42"`,
		"%%%",
		"YQ",
		strings.Repeat("a", maxUCIRequestCorrelationMetadataBytes+1),
	} {
		if correlation, ok := ParseUCIRequestCorrelation(value); ok || correlation.Valid() {
			t.Errorf("ParseUCIRequestCorrelation(%q) = %#v, %t; want invalid", value, correlation, ok)
		}
	}

	ctx := WithUCIRequestCorrelation(context.Background(), UCIRequestCorrelation{value: "untrusted"})
	if correlation, ok := UCIRequestCorrelationFromContext(ctx); ok || correlation.Valid() {
		t.Errorf("invalid context correlation = %#v, %t; want absent", correlation, ok)
	}
}
