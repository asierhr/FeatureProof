// Command featureproof exposes local on-demand validation.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/asierhr/featureProof/internal/localvalidation"
)

// commandArgs isolates process arguments so main remains verifiable without spawning a child process.
func commandArgs() []string { return os.Args[1:] }

var processArgs = commandArgs
var processInput io.Reader = os.Stdin
var processOutput io.Writer = os.Stdout
var processError io.Writer = os.Stderr
var processExit = os.Exit

// run installs the production signal context around the otherwise injectable command boundary.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return localvalidation.RunCommand(localvalidation.TerminalSignalContext(ctx), args, stdin, stdout, stderr)
}

// main delegates process termination to the stable command exit code.
func main() { processExit(run(processArgs(), processInput, processOutput, processError)) }
