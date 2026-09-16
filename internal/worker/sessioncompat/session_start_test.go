package sessioncompat

import (
	"testing"

	"github.com/stretchr/testify/require"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

func TestMemoryIDsDeduplicatesResponseOrder(t *testing.T) {
	ids := MemoryIDs([]*pb.SessionStartMemory{
		{Id: 31},
		nil,
		{Id: 0},
		{Id: 42},
		{Id: 31},
		{Id: 7},
	})
	require.Equal(t, []int64{31, 42, 7}, ids)
}
