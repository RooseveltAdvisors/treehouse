package gitvcs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const probeMarker = "treehouse-waitdelay-probe-output"

// stubOnPath puts a stand-in for name (git, jj) first on PATH that re-executes
// this test binary's lingering-descendant probe. runGitRaw resolves "git"
// through PATH at command-construction time, so this drives the real runner.
//
// The probe writes its output, spawns a descendant that inherits stdout and
// outlives it, and exits with the requested status. That is the shape of the
// field case this pins: ssh ControlMaster/ControlPersist leaves a multiplexer
// holding git's inherited pipes long after git itself exited 0.
func stubOnPath(t *testing.T, name string, exitCode int) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary to re-exec: %v", err)
	}

	dir := t.TempDir()
	script := filepath.Join(dir, name)
	body := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=TestLingeringDescendantProbe\n", self)
	if runtime.GOOS == "windows" {
		script += ".bat"
		body = fmt.Sprintf("@echo off\r\n\"%s\" -test.run=TestLingeringDescendantProbe\r\n", self)
	}
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the %s stub: %v", name, err)
	}

	t.Setenv("TREEHOUSE_LINGERING_DESCENDANT_PROBE", "1")
	t.Setenv("TREEHOUSE_LINGERING_DESCENDANT_EXIT", strconv.Itoa(exitCode))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// shortWaitDelay keeps the test from waiting out the production delay.
func shortWaitDelay(t *testing.T) {
	t.Helper()
	previous := waitDelay
	waitDelay = 300 * time.Millisecond
	t.Cleanup(func() { waitDelay = previous })
}

// The regression: a command that exited 0 while a descendant still held the
// inherited pipes came back as exec.ErrWaitDelay, which matched neither the
// deadline branch nor the exit-status branch. `treehouse get` then aborted a
// perfectly healthy acquisition with "fetch failed: exec: WaitDelay expired
// before I/O complete", and discarded output git had already produced.
func TestRunGitRaw_SucceedsWhenADescendantOutlivesASuccessfulCommand(t *testing.T) {
	shortWaitDelay(t)
	stubOnPath(t, "git", 0)

	started := time.Now()
	out, err := runGitRaw("", "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("a command that exited 0 must not fail because a descendant held its pipes: %v", err)
	}
	if !strings.Contains(string(out), probeMarker) {
		t.Fatalf("output produced before the delay must survive, got %q", string(out))
	}
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("returned after %s: the delay was not bounded", elapsed)
	}
}

// The other half of the contract: a command that did NOT succeed still fails,
// and its error names the command rather than leaking the Go sentinel at a user
// who has no way to act on it.
func TestRunGitRaw_ReportsAFailedCommandByName(t *testing.T) {
	shortWaitDelay(t)
	stubOnPath(t, "git", 3)

	_, err := runGitRaw("", "rev-parse", "HEAD")
	if err == nil {
		t.Fatal("a command that exited non-zero must fail")
	}
	if !strings.Contains(err.Error(), "rev-parse HEAD") {
		t.Fatalf("error should name the command, got: %v", err)
	}
	if strings.Contains(err.Error(), "WaitDelay") {
		t.Fatalf("error must not surface the Go sentinel, got: %v", err)
	}
}

// runGitCombined carries the merge-base checks, where exit 1 is the answer "not
// an ancestor" rather than a failure. A lingering descendant must not turn that
// answer into an unverifiable error, which acquire and prune both fail closed
// on.
func TestRunGitCombined_KeepsTheExitCodeWhenADescendantLingers(t *testing.T) {
	shortWaitDelay(t)
	stubOnPath(t, "git", 1)

	out, err := runGitCombined("", "merge-base", "--is-ancestor", "HEAD", "refs/heads/main")
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected an ExitError carrying the exit code, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got %d", exitErr.ExitCode())
	}
	if !strings.Contains(string(out), probeMarker) {
		t.Fatalf("combined output must survive the delay, got %q", string(out))
	}
}

// TestLingeringDescendantProbe is the stand-in binary. It is inert unless
// re-executed by stubOnPath.
func TestLingeringDescendantProbe(t *testing.T) {
	if os.Getenv("TREEHOUSE_LINGERING_DESCENDANT_PROBE") != "1" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		os.Exit(97)
	}

	holder := exec.Command(self, "-test.run=TestHoldsInheritedPipeProbe")
	holder.Env = append(os.Environ(),
		"TREEHOUSE_LINGERING_DESCENDANT_PROBE=0",
		"TREEHOUSE_HOLDS_INHERITED_PIPE_PROBE=1",
	)
	holder.Stdout = os.Stdout
	holder.Stderr = os.Stderr
	if err := holder.Start(); err != nil {
		os.Exit(98)
	}

	fmt.Fprintln(os.Stdout, probeMarker)
	code, _ := strconv.Atoi(os.Getenv("TREEHOUSE_LINGERING_DESCENDANT_EXIT"))
	os.Exit(code)
}

// TestHoldsInheritedPipeProbe is the descendant: it holds the pipes it
// inherited well past the delay, then exits on its own so no test leaks a
// process.
func TestHoldsInheritedPipeProbe(t *testing.T) {
	if os.Getenv("TREEHOUSE_HOLDS_INHERITED_PIPE_PROBE") != "1" {
		return
	}
	time.Sleep(5 * time.Second)
	os.Exit(0)
}
