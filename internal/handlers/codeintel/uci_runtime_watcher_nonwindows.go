//go:build !windows

package codeintel

import (
	"io/fs"
	"os"
)

func uciRuntimeWatcherIsReparse(info os.FileInfo) bool {
	return info.Mode()&fs.ModeSymlink != 0
}
