// Package taskcontext defines the bounded one-shot native task evidence wire.
package taskcontext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/uci"
)

const (
	MaxDuration        = 400 * time.Millisecond
	LocatorMetadataKey = "x-engram-native-task-locator"
	MaxInputBytes      = 20 << 10
	DefaultMaxBytes    = 16 << 10
	MaxOutputBytes     = 32 << 10
	MinOutputBytes     = 1 << 10
)

type Request struct {
	Schema         int    `json:"schema"`
	HostSessionID  string `json:"host_session_id"`
	Query          string `json:"query"`
	DeadlineUnixMS int64  `json:"deadline_unix_ms"`
	MaxBytes       int    `json:"max_bytes"`
}

type Packet struct {
	Search uci.QueryResponse  `json:"search"`
	Source *uci.QueryResponse `json:"source,omitempty"`
	Calls  *uci.QueryResponse `json:"calls,omitempty"`
}

type Evidence struct {
	Kind        string `json:"kind"`
	ID          int64  `json:"id"`
	Version     int    `json:"version,omitempty"`
	ContentHash string `json:"content_hash"`
	SourceHash  string `json:"source_hash"`
	Excerpt     bool   `json:"excerpt"`
	Path        string `json:"path,omitempty"`
	Content     string `json:"content"`
}

type Knowledge struct {
	MemoryStatus string     `json:"memory_status"`
	NoteStatus   string     `json:"note_status"`
	RuleStatus   string     `json:"rule_status"`
	Items        []Evidence `json:"items"`
}

type Response struct {
	Schema     int                                `json:"schema"`
	Status     string                             `json:"status"`
	ReasonCode string                             `json:"reason_code"`
	Project    *projectidentity.ProjectInspection `json:"project,omitempty"`
	Packet     *Packet                            `json:"packet,omitempty"`
	Knowledge  *Knowledge                         `json:"knowledge,omitempty"`
	CodeStatus string                             `json:"code_status,omitempty"`
	CodeReason string                             `json:"code_reason,omitempty"`
}

func Closed(status, reason string) Response {
	return Response{Schema: 1, Status: status, ReasonCode: reason}
}

func (request Request) Validate() error {
	if request.Schema != 1 || !validText(request.HostSessionID, 256) || !validText(request.Query, 16<<10) || request.DeadlineUnixMS <= 0 || request.DeadlineUnixMS > 9_007_199_254_740_991 || request.MaxBytes < 0 {
		return errors.New("invalid task context request")
	}
	return nil
}

func validText(value string, bound int) bool {
	return len(value) <= bound && strings.TrimSpace(value) != "" && utf8.ValidString(value) && strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) < 0
}

func (request Request) OutputLimit() int {
	if request.MaxBytes == 0 {
		return DefaultMaxBytes
	}
	return max(MinOutputBytes, min(request.MaxBytes, MaxOutputBytes))
}

// Deadline never extends either the parent or the one total native allocation.
func (request Request) Deadline(start time.Time) time.Time {
	return minTime(start.Add(MaxDuration), time.UnixMilli(request.DeadlineUnixMS))
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (request Request) Context(parent context.Context, start time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(parent, request.Deadline(start))
}

// Decode rejects duplicate and unknown keys, concatenated values and oversized input.
func Decode(input io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(input, MaxInputBytes+1))
	if err != nil || len(raw) > MaxInputBytes || !utf8.Valid(raw) {
		return errors.New("invalid task context frame")
	}
	keys := json.NewDecoder(bytes.NewReader(raw))
	token, err := keys.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("invalid task context object")
	}
	seen := make(map[string]bool, 6)
	for keys.More() {
		key, err := keys.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return errors.New("duplicate task context field")
		}
		seen[name] = true
		var value json.RawMessage
		if err := keys.Decode(&value); err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("null task context field")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("extra task context value")
	}
	return nil
}

// Encode bounds the final JSON frame, not just its source excerpt.
func Encode(response Response, limit int) ([]byte, error) {
	marshal := func(value Response) ([]byte, error) { return json.Marshal(value) }
	raw, err := marshal(response)
	if err != nil || len(raw)+1 <= limit {
		return append(raw, '\n'), err
	}
	if response.Knowledge != nil {
		knowledge := *response.Knowledge
		knowledge.Items = append([]Evidence(nil), knowledge.Items...)
		response.Knowledge = &knowledge
	}
	if response.Packet != nil {
		withoutKnowledge := response
		withoutKnowledge.Knowledge = nil
		codeOnly, codeErr := marshal(withoutKnowledge)
		if codeErr == nil && len(codeOnly)+1 <= limit {
			response = withoutKnowledge
			if response.CodeStatus != "" {
				response.Status, response.ReasonCode = response.CodeStatus, response.CodeReason
			}
		} else {
			response.Packet = nil
			response.CodeStatus, response.CodeReason = "unavailable", "BUDGET_EXCEEDED"
		}
	}
	if response.Knowledge != nil {
		for len(response.Knowledge.Items) > 0 {
			response.Status, response.ReasonCode = "ok", "OK"
			raw, err = marshal(response)
			if err != nil || len(raw)+1 <= limit {
				return append(raw, '\n'), err
			}
			items := response.Knowledge.Items
			if len(items) > 1 {
				response.Knowledge.Items = items[:len(items)-1]
				kind := items[len(items)-1].Kind
				found := false
				for _, item := range response.Knowledge.Items {
					found = found || item.Kind == kind
				}
				if !found && kind == "memory" {
					response.Knowledge.MemoryStatus = "unavailable"
				}
				if !found && kind == "note" {
					response.Knowledge.NoteStatus = "unavailable"
				}
				if !found && kind == "rule" {
					response.Knowledge.RuleStatus = "unavailable"
				}
				continue
			}
			item := &items[0]
			if len(item.Content) < 32 {
				break
			}
			fragment := item.Content[:len(item.Content)/2]
			for !utf8.ValidString(fragment) {
				fragment = fragment[:len(fragment)-1]
			}
			if strings.TrimSpace(fragment) == "" {
				break
			}
			item.Content, item.Excerpt = fragment, true
			digest := sha256.Sum256([]byte(fragment))
			item.ContentHash = hex.EncodeToString(digest[:])
		}
	}
	if response.Packet != nil {
		raw, err = marshal(response)
		if err != nil || len(raw)+1 <= limit {
			return append(raw, '\n'), err
		}
	}
	raw, err = marshal(Closed("unavailable", "BUDGET_EXCEEDED"))
	if err == nil && len(raw)+1 > limit {
		return nil, errors.New("task context output limit too small")
	}
	return append(raw, '\n'), err
}
