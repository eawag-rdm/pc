// Package analysis is the single entry point for RUNNING the checks, shared by
// the CLI and the server: callers collect files, call Run, and render its
// messages. The rest of the pipeline stays with the caller - taking the
// helpers.PDFTracker snapshot and draining the global logger are still theirs.
//
// internal because the signature is provisional: it changes with the
// check-rules rework, which makes check results value-returned.
package analysis

import (
	"context"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/metadata"
	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/eawag-rdm/pc/pkg/utils"
)

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
// Provisional leak: ProgressCallback's message string is engine-authored
// presentation text; the check-rules rework narrows it.
//
// checksAcrossFiles is passed as true deliberately - the knob is retired from
// production use, every caller wants the repository-wide checks.
//
// plan carries the bound rules of every check, compiled once at startup by each
// frontend (utils.Compile) so a bad pattern or parameter fails the boot rather
// than a run.
func Run(ctx context.Context, cfg config.Config, plan *utils.Plan, files []structs.File, md *metadata.Metadata, progress utils.ProgressCallback) []structs.Message {
	messages := metadata.RunChecks(md)
	if progress == nil {
		return append(messages, utils.ApplyAllChecks(ctx, cfg, plan, files, true)...)
	}
	return append(messages, utils.ApplyAllChecksWithProgress(ctx, cfg, plan, files, true, progress)...)
}
