package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func main() {
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	switch os.Getenv("LOOM_HELPER_MODE") {
	case "", "echo":
		_, _ = os.Stdout.Write(prompt)
	case "env":
		fmt.Print(os.Getenv("MY_TEST_VAR"))
	case "sleep":
		time.Sleep(30 * time.Second)
	case "stderr":
		fmt.Fprint(os.Stderr, "sentinel_error_msg")
		os.Exit(7)
	case "empty":
		return
	case "exit":
		os.Exit(9)
	case "huge":
		fmt.Print(strings.Repeat("x", 10*1024*1024+4096))
	case "huge-unaligned":
		fmt.Print("x")
		time.Sleep(50 * time.Millisecond)
		fmt.Print(strings.Repeat("x", 10*1024*1024+4096))
	case "state":
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		state := struct {
			Args   []string `json:"args"`
			CWD    string   `json:"cwd"`
			Env    string   `json:"env"`
			Prompt string   `json:"prompt"`
		}{
			Args:   os.Args[1:],
			CWD:    cwd,
			Env:    os.Getenv("MY_TEST_VAR"),
			Prompt: string(prompt),
		}
		if err := json.NewEncoder(os.Stdout).Encode(state); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(4)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown LOOM_HELPER_MODE")
		os.Exit(5)
	}
}
