package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/thebtf/engram/internal/projectidentity"
)

func runProjectInit(args []string, root string, out io.Writer) error {
	flags := flag.NewFlagSet("project init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "optional project display name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("usage: engram project init [--name <display>]")
	}
	project, err := projectidentity.InitLogicalProject(context.Background(), root, *name)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(project)
}

func runProjectCommand(args []string, out io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: engram project init|resolve [--root <directory>] [--name <display>] --json")
	}
	if args[0] == "init" {
		root, err := os.Getwd()
		if err != nil {
			return err
		}
		return runProjectInit(args[1:], root, out)
	}
	if args[0] != "resolve" {
		return fmt.Errorf("unknown project command")
	}
	flags := flag.NewFlagSet("project resolve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "actual host call directory; required")
	jsonOutput := flags.Bool("json", false, "portable project JSON")
	check := flags.Bool("check", false, "read-only identity inspection; no bootstrap or marker/cache writes")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || !*jsonOutput {
		return fmt.Errorf("usage: engram project resolve --root <directory> --json [--check]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	serverURL := os.Getenv("ENGRAM_URL")
	if serverURL == "" {
		serverURL = os.Getenv("ENGRAM_SERVER_URL")
	}
	if *check {
		inspection, err := projectidentity.CheckLogicalProject(ctx, *root, serverURL, os.Getenv("ENGRAM_TOKEN"))
		if err != nil {
			_ = json.NewEncoder(out).Encode(map[string]string{"code": projectidentity.ProjectErrorCode(err), "guidance": projectidentity.RecoveryGuidance(projectidentity.ProjectErrorCode(err))})
			return err
		}
		return json.NewEncoder(out).Encode(inspection)
	}
	project, err := projectidentity.ResolveLogicalProject(ctx, *root, projectidentity.HTTPBootstrap(serverURL, os.Getenv("ENGRAM_TOKEN")))
	if err != nil {
		_ = json.NewEncoder(out).Encode(map[string]string{"code": projectidentity.ProjectErrorCode(err), "guidance": projectidentity.RecoveryGuidance(projectidentity.ProjectErrorCode(err))})
		return err
	}
	return json.NewEncoder(out).Encode(project)
}
