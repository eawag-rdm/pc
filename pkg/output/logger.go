package output

import (
	"fmt"
	"sync"
	"time"
)

// LogMessage represents a log entry with level, message and timestamp
type LogMessage struct {
	Level     string `json:"level"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
	// Subject optionally names the file/archive the message is about - always a
	// display name, never a path; empty for messages without a single-file
	// subject.
	Subject string `json:"subject,omitempty"`
}

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
func (l *Logger) log(level, subject, format string, args ...interface{}) {
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
	l.log("warning", "", format, args...)
}

// FileWarning is Warning tagged with the display name (never a path) of the
// file/archive the message is about. Use it for per-file scan diagnostics so
// the server can surface a soft skip acknowledgement for that file.
func (l *Logger) FileWarning(subject, format string, args ...interface{}) {
	l.log("warning", subject, format, args...)
}

// Error prints error messages to appropriate stream
func (l *Logger) Error(format string, args ...interface{}) {
	l.log("error", "", format, args...)
}

// FileError is Error tagged with the display name (never a path) of the
// file/archive the message is about (see FileWarning).
func (l *Logger) FileError(subject, format string, args ...interface{}) {
	l.log("error", subject, format, args...)
}

// Info prints info messages to appropriate stream
func (l *Logger) Info(format string, args ...interface{}) {
	l.log("info", "", format, args...)
}

// GetMessages returns captured messages for JSON output
func (l *Logger) GetMessages() []LogMessage {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.messages
}

// ClearMessages clears the captured messages
func (l *Logger) ClearMessages() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = []LogMessage{}
}
