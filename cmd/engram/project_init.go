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
		return fmt.Errorf("usage: engram project init|resolve|choices|select [--root <directory>] --json")
	}
	if args[0] == "init" {
		root, err := os.Getwd()
		if err != nil {
			return err
		}
		return runProjectInit(args[1:], root, out)
	}
	if args[0] == "choices" || args[0] == "select" {
		return runProjectContextCommand(args, out)
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
	caller, err := projectCommandCaller(*root, serverURL)
	var project projectidentity.Project
	if err == nil {
		project, err = projectidentity.ResolveLogicalProjectWithRecovery(ctx, caller)
	}
	if err != nil {
		_ = json.NewEncoder(out).Encode(map[string]string{"code": projectidentity.ProjectErrorCode(err), "guidance": projectidentity.RecoveryGuidance(projectidentity.ProjectErrorCode(err))})
		return err
	}
	return json.NewEncoder(out).Encode(project)
}

func runProjectContextCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("project "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "actual caller root; required")
	jsonOutput := flags.Bool("json", false, "portable project-context JSON")
	var selectionRef string
	if args[0] == "select" {
		flags.StringVar(&selectionRef, "selection-ref", "", "exact fresh opaque reference from project choices")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || !*jsonOutput || *root == "" || args[0] == "select" && selectionRef == "" {
		return fmt.Errorf("usage: engram project choices|select --root <actual-caller-root> --json [--selection-ref <fresh-reference>]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	serverURL := os.Getenv("ENGRAM_URL")
	if serverURL == "" {
		serverURL = os.Getenv("ENGRAM_SERVER_URL")
	}
	caller, err := projectCommandCaller(*root, serverURL)
	var payload any
	if err == nil {
		if args[0] == "choices" {
			payload, err = projectidentity.ProjectContextChoices(ctx, caller)
		} else {
			payload, err = projectidentity.SelectProjectContext(ctx, caller, selectionRef)
		}
	}
	if err != nil {
		code := projectidentity.ProjectErrorCode(err)
		_ = json.NewEncoder(out).Encode(map[string]string{"code": code, "guidance": projectidentity.RecoveryGuidance(code)})
		return err
	}
	return json.NewEncoder(out).Encode(payload)
}

// Read the namespace already supplied by the installation launcher. Unlike
// directClientInstanceID, inspection must not create profile identity state.
func projectCommandCaller(root, serverURL string) (projectidentity.ProjectContextCaller, error) {
	clientInstanceID := os.Getenv("ENGRAM_CLIENT_INSTANCE_ID")
	if clientInstanceID != "" && !validDirectClientInstanceID(clientInstanceID) {
		return projectidentity.ProjectContextCaller{}, &projectidentity.LogicalError{Code: "PROJECT_IDENTITY_INVALID"}
	}
	return projectidentity.ProjectContextCaller{
		Cwd: root, ServerURL: serverURL, Token: os.Getenv("ENGRAM_TOKEN"), CallerID: clientInstanceID,
	}, nil
}
