package obs_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/module/obs"
)

// TestMeterFor_SameNameSameScope verifies that two calls with the same module
// name both return usable meters: each can create an Int64Counter without error
// and without panicking. Meters are stateless instrument factories; two calls
// with the same scope name must both succeed.
func TestMeterFor_SameNameSameScope(t *testing.T) {
	t.Parallel()

	m1 := obs.MeterFor("samescope")
	m2 := obs.MeterFor("samescope")

	c1, err1 := m1.Int64Counter("test.counter.a")
	require.NoError(t, err1)
	assert.NotNil(t, c1)

	c2, err2 := m2.Int64Counter("test.counter.b")
	require.NoError(t, err2)
	assert.NotNil(t, c2)
}

// TestMeterFor_EmptyName verifies that MeterFor("") does not panic and returns
// a usable meter (one whose Int64Counter call succeeds).
func TestMeterFor_EmptyName(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		m := obs.MeterFor("")
		assert.NotNil(t, m)

		_, err := m.Int64Counter("test.empty.counter")
		require.NoError(t, err)
	})
}
