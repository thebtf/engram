//go:build darwin

package main

import (
	"os"
	"testing"
)

func TestLiveProcessImageIdentityBindsCurrentDarwinProcess(t *testing.T) {
	image, err := liveProcessImageIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if image.darwinUUID == [16]byte{} {
		t.Fatal("current process image UUID is zero")
	}
	if image.darwinUniqueID == 0 {
		t.Fatal("current process unique ID is zero")
	}
	if err := verifyLiveProcessImageBinding(os.Getpid(), image); err != nil {
		t.Fatal(err)
	}
	if err := image.Close(); err != nil {
		t.Fatal(err)
	}
}
