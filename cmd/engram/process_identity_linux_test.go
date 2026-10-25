//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLinuxProcessExitProofLifecycle(t *testing.T) {
	if os.Getenv("ENGRAM_TEST_PIDFD_CHILD") == "1" {
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			t.Fatal(err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLinuxProcessExitProofLifecycle$")
	command.Env = append(os.Environ(), "ENGRAM_TEST_PIDFD_CHILD=1")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	waited := false
	var proof muxcoreProcessExitProof
	t.Cleanup(func() {
		_ = input.Close()
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
		if proof != nil {
			if err := proof.Close(); err != nil {
				t.Error(err)
			}
		}
		t.Logf("PIDFD_CHILD_CLOSED: pid=%d reaped=%v", command.Process.Pid, command.ProcessState != nil && command.ProcessState.Exited())
	})
	proof, err = openProcessExitProof(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if exited, err := proof.Exited(); err != nil || exited {
		t.Fatalf("live child exit proof = %v, %v", exited, err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	waitErr := command.Wait()
	waited = true
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		exited, err := proof.Exited()
		if err != nil {
			t.Fatal(err)
		}
		if exited {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("reaped child exit was not confirmed before deadline")
		case <-ticker.C:
		}
	}
	if err := proof.Close(); err != nil {
		t.Fatal(err)
	}
	proof = nil
	t.Log("PIDFD_LIFECYCLE_PASS: real child alive, actual exit observed, pidfd closed; EINTR was not injected")
}

func TestLinuxProcessExitProofRejectsInvalidDescriptor(t *testing.T) {
	proof := &linuxProcessExitProof{poll: [1]unix.PollFd{{Fd: 1<<31 - 1, Events: unix.POLLIN}}}
	if exited, err := proof.Exited(); exited || err == nil || !strings.Contains(err.Error(), "unexpected pidfd poll status") || proof.poll[0].Revents&unix.POLLNVAL == 0 {
		t.Fatalf("invalid descriptor was accepted: exited=%v error=%v revents=%d", exited, err, proof.poll[0].Revents)
	}
	if err := proof.Close(); !errors.Is(err, unix.EBADF) {
		t.Fatalf("invalid descriptor close error = %v, want EBADF", err)
	}
	t.Log("PIDFD_ERROR_PASS: real POLLNVAL is not exit/success and close retains EBADF")
}
