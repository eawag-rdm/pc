package structs

// DiagLevel is the severity of a Diagnostic. Producers set it from the
// constants below and both consumers (the JSON formatter's errors[]/warnings[]
// split, the server's slog severity mapping) switch on those same constants,
// so the set is closed by use rather than by an untyped literal matching by
// luck.
type DiagLevel string

const (
	DiagError   DiagLevel = "error"
	DiagWarning DiagLevel = "warning"
	DiagInfo    DiagLevel = "info"
)

// Diagnostic is an operator-facing note about the RUN rather than about the
// data: an unreadable archive, a check that panicked, a rule that matched no
// file. Checks report findings as Message; everything about how the scan itself
// went is a Diagnostic.
//
// AUDIENCE PROTOCOL. Subject is what splits the two classes:
//
//   - Subject == "": run-wide. Operator only - CLI diagnostics, server log.
//     Configuration diagnostics (a rule that matches nothing) belong here and
//     must leave Subject empty.
//   - Subject != "": concerns one file, named by its display name, never a
//     path. The raw text still goes to the operator only, but the server
//     additionally emits ONE path-free "not scanned" acknowledgement per
//     distinct subject into the depositor-facing response.
//
// WIRE SHAPE. The json tags are the server/CLI response shape: output.LogMessage
// is an alias of this type, so a diagnostic travels from the engine into the
// JSON body (ScanResult.Errors/Warnings) without conversion. That is what keeps
// the emission timestamps of the still-unmigrated producers intact, and it is
// also the hazard: a field added here SHIPS INTO THE PUBLIC RESPONSE without
// touching pkg/output or pkg/output/json. Adding one is a wire decision. The
// alternative - a tagged copy owned by package json, mapped at two sites, the
// way structs.Message is - was weighed and declined for this commit because the
// conversion would drop the timestamps the 44 unmigrated sites still set.
type Diagnostic struct {
	Level   DiagLevel `json:"level"`
	Message string    `json:"message"`
	// Timestamp is RFC3339, stamped when the diagnostic is emitted.
	Timestamp string `json:"timestamp"`
	Subject   string `json:"subject,omitempty"`
}
