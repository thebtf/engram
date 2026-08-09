package loom

import (
	"bytes"
	"errors"
	"testing"
)

func TestLimitedWriterRetainsAtMostCapacity(t *testing.T) {
	tests := []struct {
		name string
		limit int64
		input string
		want  string
	}{
		{name: "under capacity", limit: 4, input: "abc", want: "abc"},
		{name: "at capacity", limit: 3, input: "abc", want: "abc"},
		{name: "over capacity", limit: 3, input: "abcde", want: "abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dst bytes.Buffer
			writer := &limitedWriter{w: &dst, n: tt.limit}

			n, err := writer.Write([]byte(tt.input))

			if err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if n != len(tt.input) {
				t.Errorf("Write() returned %d, want %d", n, len(tt.input))
			}
			if got := dst.String(); got != tt.want {
				t.Errorf("written output = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLimitedWriterRetainsCapacityAcrossWrites(t *testing.T) {
	var dst bytes.Buffer
	writer := &limitedWriter{w: &dst, n: 5}

	for _, input := range []string{"abc", "def"} {
		n, err := writer.Write([]byte(input))
		if err != nil {
			t.Fatalf("Write(%q) error = %v", input, err)
		}
		if n != len(input) {
			t.Errorf("Write(%q) returned %d, want %d", input, n, len(input))
		}
	}

	if got := dst.String(); got != "abcde" {
		t.Errorf("written output = %q, want %q", got, "abcde")
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write(p []byte) (int, error) {
	return 0, w.err
}

func TestLimitedWriterPropagatesUnderlyingError(t *testing.T) {
	wantErr := errors.New("write failed")
	input := "abcde"
	writer := &limitedWriter{w: failingWriter{err: wantErr}, n: 3}

	n, err := writer.Write([]byte(input))

	if !errors.Is(err, wantErr) {
		t.Errorf("Write() error = %v, want %v", err, wantErr)
	}
	if n != len(input) {
		t.Errorf("Write() returned %d, want %d", n, len(input))
	}
}
