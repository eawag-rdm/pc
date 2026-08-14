// Package analysis is the single entry point for RUNNING the checks, shared by
// the CLI and the server: callers collect files, call Run, and render its
// Result. Taking the helpers.PDFTracker snapshot is still the caller's; the
// global logger is drained HERE (see Run).
//
// internal because the signature is provisional: it changes with the
// check-rules rework, which makes check results value-returned.
package analysis

import (
	"context"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/metadata"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/eawag-rdm/pc/pkg/utils"
)

// Result is one run's output: what the checks found, and what happened to the
// scan while finding it.
type Result struct {
	// Messages are the findings, metadata checks first (see Run).
	Messages []structs.Message
	// Diagnostics are the run's operator-facing notes. It is the whole run only
	// under the precondition below: Run drains output.GlobalLogger into it, and
	// that buffer holds anything only while the logger is in JSON (buffering)
	// mode. In stream mode the logger PRINTS instead of buffering, so the ~44
	// sites in pkg/readers, pkg/collectors, pkg/checks and pkg/server that still
	// emit there contribute nothing here - silently. Both frontends set the mode
	// at boot (main.go:59, server.go:87); a test that does not is the case to
	// watch. Frontends route these to their operator channel; structs.Diagnostic
	// documents which of them may also be acknowledged to a depositor.
	Diagnostics []structs.Diagnostic
}

// Run executes the metadata checks and then the file check pipeline over files,
// returning their messages in that order. md may be nil (no metadata collected,
// e.g. the local collector). ctx bounds the file pipeline coarsely: once it is
// done the scan stops at the next cancellation point (between files, or
// mid-archive), and the messages are then PARTIAL in an unspecified way - no
// error, no marker, so ctx.Err() is the caller's only signal. It does not bound
// the metadata checks - metadata.RunChecks takes no ctx. CLI callers pass
// context.Background().
//
// progress may be nil (server, non-TUI CLI). Both branches run the same
// file-phase dispatch; the only difference is that the WithProgress engine
// emits progress ticks.
//
// plan carries the bound rules of every check, compiled once at startup by each
// frontend (utils.Compile) so a bad pattern or parameter fails the boot rather
// than a run.
// Run is also the run's single MERGE POINT for diagnostics: it drains
// output.GlobalLogger - which the collectors have already written to by the time
// it is called, and which pkg/readers and pkg/checks write to during it - and
// concatenates the engine's own returned diagnostics. Frontends therefore
// reassemble nothing; when the remaining producers move to the value channel,
// the drain is one line to delete. The drain is Logger.Drain (take and clear
// under one lock), never GetMessages + ClearMessages, which would drop anything
// emitted between the two calls.
//
// NOT SAFE FOR CONCURRENT USE, and the reason is not local: the drained buffer
// is process-global, so two Runs in flight steal each other's diagnostics. The
// server serializes whole analyses on Handler.analysisMu (handlers.go:403) and
// the CLI runs one scan per process; a third caller must provide the same
// guarantee. Passing the logger in would remove the constraint, at the cost of
// a parameter every caller fills with the same global - revisit when the
// remaining producers migrate.
func Run(ctx context.Context, cfg config.Config, plan *utils.Plan, files []structs.File, md *metadata.Metadata, progress utils.ProgressCallback) Result {
	messages := metadata.RunChecks(md)
	var fileMessages []structs.Message
	var diagnostics []structs.Diagnostic
	if progress == nil {
		fileMessages, diagnostics = utils.ApplyAllChecks(ctx, cfg, plan, files)
	} else {
		fileMessages, diagnostics = utils.ApplyAllChecksWithProgress(ctx, cfg, plan, files, progress)
	}
	// Diagnostics are GROUPED, not chronological: everything drained from the
	// global first, then everything the engine returned. Each item carries its
	// own emission Timestamp for a consumer that needs the real order.
	return Result{
		Messages:    append(messages, fileMessages...),
		Diagnostics: append(output.GlobalLogger.Drain(), diagnostics...),
	}
}
