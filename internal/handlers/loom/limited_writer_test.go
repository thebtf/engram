package loom

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type controlledWriter struct {
	n   int
	err error
}

func (w controlledWriter) Write([]byte) (int, error) {
	return w.n, w.err
}

func TestLimitedWriterCrossesLimitWithoutShortWrite(t *testing.T) {
	var dst bytes.Buffer
	w := &limitedWriter{w: &dst, n: 5}

	n, err := w.Write([]byte("1234567"))
	require.NoError(t, err)
	assert.Equal(t, 7, n, "discarded overflow still counts as consumed input")
	assert.Equal(t, "12345", dst.String())
	assert.Zero(t, w.n)

	n, err = w.Write([]byte("discarded"))
	require.NoError(t, err)
	assert.Equal(t, len("discarded"), n)
	assert.Equal(t, "12345", dst.String())
}

func TestLimitedWriterPropagatesUnderlyingError(t *testing.T) {
	sentinel := errors.New("sentinel writer failure")
	w := &limitedWriter{w: controlledWriter{n: 2, err: sentinel}, n: 10}

	n, err := w.Write([]byte("1234"))
	assert.Equal(t, 2, n)
	require.ErrorIs(t, err, sentinel)
	assert.Equal(t, int64(8), w.n)
}

func TestLimitedWriterConvertsZeroErrorShortWrite(t *testing.T) {
	w := &limitedWriter{w: controlledWriter{n: 2}, n: 10}

	n, err := w.Write([]byte("1234"))
	assert.Equal(t, 2, n)
	require.ErrorIs(t, err, io.ErrShortWrite)
	assert.Equal(t, int64(8), w.n)
}
