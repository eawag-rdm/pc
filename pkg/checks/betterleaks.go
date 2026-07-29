package checks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// blFinding is the subset of a betterleaks JSON report entry the leak check
// consumes. The Secret/Match report fields are deliberately not decoded: the
// scanner runs with --redact=100 and secret values must never reach analysis
// output.
type blFinding struct {
	RuleID    string `json:"RuleID"`
	StartLine int    `json:"StartLine"`
	File      string `json:"File"`
}

const (
	// betterleaksMaxReportBytes caps the JSON report accepted from the scanner;
	// a larger report aborts the scan instead of exhausting memory.
	betterleaksMaxReportBytes = 64 * 1024 * 1024
	// betterleaksMaxStderrBytes caps captured scanner logging kept for error messages.
	betterleaksMaxStderrBytes = 8 * 1024
)

// limitedBuffer accepts writes up to max bytes and silently discards the rest,
// so a runaway child process cannot grow pc-server's heap unbounded.
type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := l.max - l.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
			l.truncated = true
		}
		l.buf.Write(p)
	} else if n > 0 {
		l.truncated = true
	}
	return n, nil
}

// runBetterleaks scans the given absolute paths with one betterleaks
// invocation and returns the parsed findings. All unpacking and size gating
// happens in pc before this call, so the scanner itself never opens archives
// (--max-archive-depth 0). maxProcs > 0 caps the scanner's CPU use via
// GOMAXPROCS. The caller bounds the run through ctx; on cancellation the child
// process is killed.
func runBetterleaks(ctx context.Context, binary string, paths []string, maxProcs int) ([]blFinding, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	// Absolute paths only: relative names could be parsed as flags by the
	// scanner and are ambiguous against pc-server's working directory.
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("betterleaks: refusing non-absolute path %q", p)
		}
	}

	args := []string{"dir",
		"--no-banner", "--no-color",
		"--log-level", "error",
		"--exit-code", "0", // findings are results, not a process failure
		"--redact=100", // never emit secret values into the report
		"--report-format", "json",
		"--report-path", "-",
		"--max-archive-depth", "0", // pc unpacks archives itself (size-gated)
	}
	args = append(args, paths...)

	cmd := exec.CommandContext(ctx, binary, args...)
	stdout := &limitedBuffer{max: betterleaksMaxReportBytes}
	stderr := &limitedBuffer{max: betterleaksMaxStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if maxProcs > 0 {
		cmd.Env = append(os.Environ(), "GOMAXPROCS="+strconv.Itoa(maxProcs))
	}

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("betterleaks: scan aborted: %v", ctx.Err())
		}
		return nil, fmt.Errorf("betterleaks: %v (stderr: %s)", err, strings.TrimSpace(stderr.buf.String()))
	}

	report := bytes.TrimSpace(stdout.buf.Bytes())
	// betterleaks writes the JSON literal `null` when there are no findings.
	if len(report) == 0 || string(report) == "null" {
		return nil, nil
	}
	var findings []blFinding
	if err := json.Unmarshal(report, &findings); err != nil {
		if stdout.truncated {
			return nil, fmt.Errorf("betterleaks: report exceeded %d bytes", betterleaksMaxReportBytes)
		}
		return nil, fmt.Errorf("betterleaks: unparsable report: %v", err)
	}
	return findings, nil
}
