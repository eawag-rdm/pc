package output

import (
	"sync"
	"testing"
	"time"
)

func TestLogger_SetJSONMode(t *testing.T) {
	logger := &Logger{jsonMode: false, messages: []LogMessage{}}

	// Test enabling JSON mode
	logger.SetJSONMode(true)
	if !logger.jsonMode {
		t.Error("Expected JSON mode to be enabled")
	}

	// Test disabling JSON mode
	logger.SetJSONMode(false)
	if logger.jsonMode {
		t.Error("Expected JSON mode to be disabled")
	}
}

func TestLogger_Warning_JSONMode(t *testing.T) {
	logger := &Logger{jsonMode: true, messages: []LogMessage{}}

	logger.Warning("Test warning: %s", "example")

	if len(logger.messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(logger.messages))
	}

	msg := logger.messages[0]
	if msg.Level != "warning" {
		t.Errorf("Expected level 'warning', got '%s'", msg.Level)
	}

	expectedMessage := "Test warning: example"
	if msg.Message != expectedMessage {
		t.Errorf("Expected message '%s', got '%s'", expectedMessage, msg.Message)
	}

	if msg.Timestamp == "" {
		t.Error("Timestamp should not be empty")
	}

	// Verify timestamp format
	_, err := time.Parse(time.RFC3339, msg.Timestamp)
	if err != nil {
		t.Errorf("Invalid timestamp format: %v", err)
	}
}

func TestLogger_Error_JSONMode(t *testing.T) {
	logger := &Logger{jsonMode: true, messages: []LogMessage{}}

	logger.Error("Test error: %d", 123)

	if len(logger.messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(logger.messages))
	}

	msg := logger.messages[0]
	if msg.Level != "error" {
		t.Errorf("Expected level 'error', got '%s'", msg.Level)
	}

	expectedMessage := "Test error: 123"
	if msg.Message != expectedMessage {
		t.Errorf("Expected message '%s', got '%s'", expectedMessage, msg.Message)
	}
}

func TestLogger_Info_JSONMode(t *testing.T) {
	logger := &Logger{jsonMode: true, messages: []LogMessage{}}

	logger.Info("Test info message")

	if len(logger.messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(logger.messages))
	}

	msg := logger.messages[0]
	if msg.Level != "info" {
		t.Errorf("Expected level 'info', got '%s'", msg.Level)
	}

	if msg.Message != "Test info message" {
		t.Errorf("Expected message 'Test info message', got '%s'", msg.Message)
	}
}

func TestLogger_MultipleMessages(t *testing.T) {
	logger := &Logger{jsonMode: true, messages: []LogMessage{}}

	logger.Warning("Warning 1")
	logger.Error("Error 1")
	logger.Info("Info 1")
	logger.Warning("Warning 2")

	if len(logger.messages) != 4 {
		t.Fatalf("Expected 4 messages, got %d", len(logger.messages))
	}

	// Verify message order and types
	expectedLevels := []string{"warning", "error", "info", "warning"}
	for i, expected := range expectedLevels {
		if logger.messages[i].Level != expected {
			t.Errorf("Message %d: expected level '%s', got '%s'", i, expected, logger.messages[i].Level)
		}
	}
}

func TestLogger_GetMessages(t *testing.T) {
	logger := &Logger{jsonMode: true, messages: []LogMessage{}}

	logger.Warning("Test message")
	messages := logger.GetMessages()

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	if messages[0].Message != "Test message" {
		t.Errorf("Expected message 'Test message', got '%s'", messages[0].Message)
	}

	// Verify we get a copy/reference to the actual messages
	logger.Error("Second message")
	messages = logger.GetMessages()

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages after adding another, got %d", len(messages))
	}
}

func TestLogger_ClearMessages(t *testing.T) {
	logger := &Logger{jsonMode: true, messages: []LogMessage{}}

	logger.Warning("Test message")
	logger.Error("Test error")

	if len(logger.messages) != 2 {
		t.Fatalf("Expected 2 messages before clear, got %d", len(logger.messages))
	}

	logger.ClearMessages()

	if len(logger.messages) != 0 {
		t.Fatalf("Expected 0 messages after clear, got %d", len(logger.messages))
	}

	// Verify GetMessages returns empty slice after clear
	messages := logger.GetMessages()
	if len(messages) != 0 {
		t.Fatalf("Expected 0 messages from GetMessages after clear, got %d", len(messages))
	}
}

func TestLogger_NonJSONMode(t *testing.T) {
	logger := &Logger{jsonMode: false, messages: []LogMessage{}}

	// In non-JSON mode, messages should not be stored
	logger.Warning("Test warning")
	logger.Error("Test error")
	logger.Info("Test info")

	if len(logger.messages) != 0 {
		t.Errorf("Expected 0 stored messages in non-JSON mode, got %d", len(logger.messages))
	}
}

func TestGlobalLogger(t *testing.T) {
	// Test that GlobalLogger is initialized
	if GlobalLogger == nil {
		t.Fatal("GlobalLogger is nil")
	}

	// Reset GlobalLogger state for test
	GlobalLogger.ClearMessages()
	GlobalLogger.SetJSONMode(true)

	GlobalLogger.Warning("Global test message")

	messages := GlobalLogger.GetMessages()
	if len(messages) != 1 {
		t.Fatalf("Expected 1 message in GlobalLogger, got %d", len(messages))
	}

	if messages[0].Message != "Global test message" {
		t.Errorf("Expected 'Global test message', got '%s'", messages[0].Message)
	}

	// Clean up
	GlobalLogger.ClearMessages()
	GlobalLogger.SetJSONMode(false)
}

func TestLogger_FormattingEdgeCases(t *testing.T) {
	logger := &Logger{jsonMode: true, messages: []LogMessage{}}

	// Test with no formatting
	logger.Warning("Simple message")
	if len(logger.messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(logger.messages))
	}

	// Test with multiple format parameters
	logger.Error("Error %s %d %v", "test", 42, true)
	if len(logger.messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(logger.messages))
	}

	expectedMessage := "Error test 42 true"
	if logger.messages[1].Message != expectedMessage {
		t.Errorf("Expected formatted message '%s', got '%s'", expectedMessage, logger.messages[1].Message)
	}

	// Test with empty format string
	logger.Info("")
	if len(logger.messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d", len(logger.messages))
	}

	if logger.messages[2].Message != "" {
		t.Errorf("Expected empty message, got '%s'", logger.messages[2].Message)
	}
}

func TestLogger_ConcurrentAccess(t *testing.T) {
	logger := &Logger{jsonMode: true, messages: []LogMessage{}}
	var wg sync.WaitGroup
	numGoroutines := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			logger.Warning("Warning from %d", id)
			logger.Error("Error from %d", id)
			logger.Info("Info from %d", id)
		}(i)
	}

	wg.Wait()

	messages := logger.GetMessages()
	if len(messages) != numGoroutines*3 {
		t.Errorf("Expected %d messages, got %d", numGoroutines*3, len(messages))
	}
}