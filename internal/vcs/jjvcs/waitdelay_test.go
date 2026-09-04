package jjvcs

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

// The jj twin of the git regression: jj shells out to git for network
// transport, so a jj command can exit 0 while a transport helper still holds
// the pipes it inherited. Reported as exec.ErrWaitDelay, that turned a healthy
// command into a failure and discarded output jj had already produced.
//
// The stand-in jj is this test binary re-executed through PATH, which is how
// runJJ resolves the binary, so the real runner is under test.
func TestRunJJ_SucceedsWhenADescendantOutlivesASuccessfulCommand(t *testing.T) {
	previous := waitDelay
	waitDelay = 300 * time.Millisecond
	t.Cleanup(func() { waitDelay = previous })

	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary to re-exec: %v", err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "jj")
	body := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=TestLingeringDescendantProbe\n", self)
	if runtime.GOOS == "windows" {
		script += ".bat"
		body = fmt.Sprintf("@echo off\r\n\"%s\" -test.run=TestLingeringDescendantProbe\r\n", self)
	}
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the jj stub: %v", err)
	}
	t.Setenv("TREEHOUSE_LINGERING_DESCENDANT_PROBE", "1")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := runJJ("", "workspace", "root")
	if err != nil {
		t.Fatalf("a command that exited 0 must not fail because a descendant held its pipes: %v", err)
	}
	if !strings.Contains(out, probeMarker) {
		t.Fatalf("output produced before the delay must survive, got %q", out)
	}
}

// TestLingeringDescendantProbe is the stand-in binary. It is inert unless
// re-executed by the test above.
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
