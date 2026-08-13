package output

import (
	"fmt"
	"sync"
	"time"

	"github.com/eawag-rdm/pc/pkg/structs"
)

// LogMessage is the buffered diagnostic. It is an ALIAS of structs.Diagnostic,
// not a second type: the check engine returns structs.Diagnostic values and
// they arrive here - and in the JSON body - unconverted. structs.Diagnostic
// carries the field documentation, including which diagnostics may reach a
// depositor-facing response.
type LogMessage = structs.Diagnostic

// Logger provides configurable output destinations
type Logger struct {
	jsonMode bool
	messages []LogMessage
	mu       sync.Mutex
}

var GlobalLogger = &Logger{jsonMode: false, messages: []LogMessage{}}

// SetJSONMode configures logger for JSON output mode
func (l *Logger) SetJSONMode(enabled bool) {
	l.jsonMode = enabled
}

// log buffers (JSON mode) or prints (CLI stream mode) one message. The printed
// text is identical with or without a subject, so tagging a call site with a
// subject never changes CLI output.
func (l *Logger) log(level structs.DiagLevel, subject, format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if l.jsonMode {
		l.mu.Lock()
		l.messages = append(l.messages, LogMessage{
			Level:     level,
			Message:   message,
			Timestamp: time.Now().Format(time.RFC3339),
			Subject:   subject,
		})
		l.mu.Unlock()
	} else {
		fmt.Println(message)
	}
}

// Warning prints warning messages to appropriate stream
func (l *Logger) Warning(format string, args ...interface{}) {
	l.log(structs.DiagWarning, "", format, args...)
}

// FileWarning is Warning tagged with the display name (never a path) of the
// file/archive the message is about. Use it for per-file scan diagnostics so
// the server can surface a soft skip acknowledgement for that file.
func (l *Logger) FileWarning(subject, format string, args ...interface{}) {
	l.log(structs.DiagWarning, subject, format, args...)
}

// Error prints error messages to appropriate stream
func (l *Logger) Error(format string, args ...interface{}) {
	l.log(structs.DiagError, "", format, args...)
}

// FileError is Error tagged with the display name (never a path) of the
// file/archive the message is about (see FileWarning).
func (l *Logger) FileError(subject, format string, args ...interface{}) {
	l.log(structs.DiagError, subject, format, args...)
}

// Info prints info messages to appropriate stream
func (l *Logger) Info(format string, args ...interface{}) {
	l.log(structs.DiagInfo, "", format, args...)
}

// GetMessages returns a COPY of the captured messages, leaving the buffer
// intact - the non-destructive peek, where Drain is the destructive take that
// production uses. It exists for callers that must observe the buffer without
// consuming it (chiefly tests asserting that a package wrote nothing to it).
// The copy is what keeps the buffer out of the caller's hands.
func (l *Logger) GetMessages() []LogMessage {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LogMessage(nil), l.messages...)
}

// Drain takes the captured messages and clears the buffer in one critical
// section. Callers that both read and reset - the run entry point, which turns
// the buffer into part of its returned Result - must use this rather than
// GetMessages followed by ClearMessages: between those two calls a diagnostic
// emitted by a still-running goroutine would be dropped.
func (l *Logger) Drain() []LogMessage {
	l.mu.Lock()
	defer l.mu.Unlock()
	messages := l.messages
	l.messages = nil
	return messages
}

// ClearMessages clears the captured messages
func (l *Logger) ClearMessages() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = []LogMessage{}
}
