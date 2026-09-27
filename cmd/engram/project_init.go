package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/thebtf/engram/internal/projectidentity"
)

func runProjectInit(args []string, root string, out io.Writer) error {
	flags := flag.NewFlagSet("project init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "project display name")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("project init: %w; usage: engram project init --name <display>", err)
	}
	if flags.NArg() != 0 || *name == "" {
		return fmt.Errorf("usage: engram project init --name <display>")
	}
	anchor, created, tracked, err := projectidentity.InitRepositoryAnchorV3(root, *name)
	if err != nil {
		return err
	}
	status := "UNTRACKED"
	if tracked {
		status = "TRACKED"
	}
	verb := "Existing"
	if created {
		verb = "Created"
	}
	fmt.Fprintf(out, "%s V3 repository anchor: %s (%s, project %s)\n", verb, filepath.Join(root, ".engram-project"), status, anchor.Name)
	if !tracked {
		fmt.Fprintln(out, "Next step (explicit): git add -- .engram-project")
	}
	return nil
}

func runProjectCommand(args []string, out io.Writer) error {
	if len(args) < 1 || args[0] != "init" {
		return fmt.Errorf("usage: engram project init --name <display>")
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("project init: current directory: %w", err)
	}
	return runProjectInit(args[1:], root, out)
}
