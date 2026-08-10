package main

import (
	"os"

	"github.com/thebtf/mcp-mux/muxcore/serverid"
)

type muxcoreDaemonLock struct {
	file *os.File
}

func (l *muxcoreDaemonLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlockMuxcoreDaemonFile(l.file)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func acquireMuxcoreDaemonLock() (*muxcoreDaemonLock, error) {
	file, err := os.OpenFile(serverid.DaemonLockPath("", muxcoreNamespace), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockMuxcoreDaemonFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &muxcoreDaemonLock{file: file}, nil
}
