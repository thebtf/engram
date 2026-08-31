package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	input, err := parseCommandInput(os.Args[1:], os.Getenv)
	if err == nil {
		var receipt qualificationReceipt
		receipt, err = runQualification(context.Background(), input)
		if err == nil {
			err = writeReceipt(os.Stdout, receipt)
		}
	}
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "hap02a taskmemory qualification:", publicErrorCode(err))
	os.Exit(1)
}

func parseCommandInput(args []string, getenv func(string) string) (commandInput, error) {
	if getenv == nil {
		return commandInput{}, boundary("ENVIRONMENT_UNAVAILABLE")
	}
	flags := flag.NewFlagSet("hap02a-taskmemory", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	runID := flags.String("run-id", "", "")
	sourceCommit := flags.String("source-commit", "", "")
	sourceTree := flags.String("source-tree", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return commandInput{}, boundary("INVALID_ARGUMENTS")
	}
	input := commandInput{
		RunID:        *runID,
		SourceCommit: *sourceCommit,
		SourceTree:   *sourceTree,
		DatabaseDSN:  getenv("HAP02A_DATABASE_DSN"),
	}
	if err := validateCommandInput(input); err != nil {
		return commandInput{}, err
	}
	return input, nil
}

func writeReceipt(output io.Writer, receipt qualificationReceipt) error {
	if output == nil {
		return boundary("OUTPUT_UNAVAILABLE")
	}
	encoded, err := marshalReceipt(receipt)
	if err != nil {
		return boundary("RECEIPT_ENCODE_FAILED")
	}
	if _, err := output.Write(append(encoded, '\n')); err != nil {
		return boundary("OUTPUT_UNAVAILABLE")
	}
	return nil
}

func publicErrorCode(err error) string {
	var refusal *boundaryError
	if errors.As(err, &refusal) && refusal != nil {
		return refusal.code
	}
	return "QUALIFICATION_FAILED"
}
