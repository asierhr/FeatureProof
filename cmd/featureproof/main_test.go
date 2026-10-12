package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRunDelegatesInvalidArgumentsWithoutStartingAValidation covers the executable signal wrapper.
func TestRunDelegatesInvalidArgumentsWithoutStartingAValidation(t *testing.T) {
	_ = processArgs()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"validate"}, strings.NewReader(""), &stdout, &stderr); code != 3 || stdout.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

// TestMainPropagatesTheCommandExitCode covers the process shell without terminating the test process.
func TestMainPropagatesTheCommandExitCode(t *testing.T) {
	oldArgs, oldInput, oldOutput, oldError, oldExit := processArgs, processInput, processOutput, processError, processExit
	defer func() {
		processArgs, processInput, processOutput, processError, processExit = oldArgs, oldInput, oldOutput, oldError, oldExit
	}()
	var stdout, stderr bytes.Buffer
	code := -1
	processArgs = func() []string { return []string{"validate"} }
	processInput = strings.NewReader("")
	processOutput = &stdout
	processError = &stderr
	processExit = func(value int) { code = value }
	main()
	if code != 3 {
		t.Fatalf("code=%d", code)
	}
}
