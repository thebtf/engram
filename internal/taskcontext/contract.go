// Package taskcontext defines the bounded one-shot native task evidence wire.
package taskcontext

import (
	"bytes"
	"context"
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

type Response struct {
	Schema     int                                `json:"schema"`
	Status     string                             `json:"status"`
	ReasonCode string                             `json:"reason_code"`
	Project    *projectidentity.ProjectInspection `json:"project,omitempty"`
	Packet     *Packet                            `json:"packet,omitempty"`
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
	raw, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	if len(raw)+1 > limit {
		response.Packet = nil
		response.Status, response.ReasonCode = "unavailable", "BUDGET_EXCEEDED"
		raw, err = json.Marshal(response)
		if err != nil {
			return nil, err
		}
		if len(raw)+1 > limit {
			raw, err = json.Marshal(Closed("unavailable", "BUDGET_EXCEEDED"))
		}
	}
	return append(raw, '\n'), err
}
