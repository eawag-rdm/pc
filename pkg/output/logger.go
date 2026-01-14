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

// Warning prints warning messages to appropriate stream
func (l *Logger) Warning(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if l.jsonMode {
		l.mu.Lock()
		l.messages = append(l.messages, LogMessage{
			Level:     "warning",
			Message:   message,
			Timestamp: time.Now().Format(time.RFC3339),
		})
		l.mu.Unlock()
	} else {
		fmt.Printf(message + "\n")
	}
}

// Error prints error messages to appropriate stream
func (l *Logger) Error(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if l.jsonMode {
		l.mu.Lock()
		l.messages = append(l.messages, LogMessage{
			Level:     "error",
			Message:   message,
			Timestamp: time.Now().Format(time.RFC3339),
		})
		l.mu.Unlock()
	} else {
		fmt.Printf(message + "\n")
	}
}

// Info prints info messages to appropriate stream
func (l *Logger) Info(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if l.jsonMode {
		l.mu.Lock()
		l.messages = append(l.messages, LogMessage{
			Level:     "info",
			Message:   message,
			Timestamp: time.Now().Format(time.RFC3339),
		})
		l.mu.Unlock()
	} else {
		fmt.Printf(message + "\n")
	}
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