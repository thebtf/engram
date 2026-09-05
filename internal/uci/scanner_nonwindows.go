//go:build !windows

package uci

import (
	"io/fs"
	"os"
)

func scannerFileIsReparse(info os.FileInfo) bool {
	return info.Mode()&fs.ModeSymlink != 0
}
