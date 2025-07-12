package output

import (
	"fmt"
	"time"
)

// Logger provides configurable output destinations
type Logger struct {
	jsonMode bool
	messages []LogMessage
}

var GlobalLogger = &Logger{jsonMode: false, messages: []LogMessage{}}

// SetJSONMode configures logger for JSON output mode
func (l *Logger) SetJSONMode(enabled bool) {
	l.jsonMode = enabled
}

// Warning prints warning messages to appropriate stream
func (l *Logger) Warning(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if l.jsonMode {
		l.messages = append(l.messages, LogMessage{
			Level:     "warning",
			Message:   message,
			Timestamp: time.Now().Format(time.RFC3339),
		})
	} else {
		fmt.Printf(message+"\n")
	}
}

// Error prints error messages to appropriate stream
func (l *Logger) Error(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if l.jsonMode {
		l.messages = append(l.messages, LogMessage{
			Level:     "error",
			Message:   message,
			Timestamp: time.Now().Format(time.RFC3339),
		})
	} else {
		fmt.Printf(message+"\n")
	}
}

// Info prints info messages to appropriate stream
func (l *Logger) Info(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if l.jsonMode {
		l.messages = append(l.messages, LogMessage{
			Level:     "info",
			Message:   message,
			Timestamp: time.Now().Format(time.RFC3339),
		})
	} else {
		fmt.Printf(message+"\n")
	}
}

// GetMessages returns captured messages for JSON output
func (l *Logger) GetMessages() []LogMessage {
	return l.messages
}

// ClearMessages clears the captured messages
func (l *Logger) ClearMessages() {
	l.messages = []LogMessage{}
}