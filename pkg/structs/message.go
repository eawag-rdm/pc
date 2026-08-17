package structs

// Source identifies what a Message refers to. Both File and Repository satisfy
// it, and Message.Format() type-switches on the concrete type.
type Source interface{}

// a struct that defines the message structure, retuned by the failed checks
type Message struct {
	// The message content.
	Content string
	// The source of the message.
	Source Source
	// The test name that generated this message.
	TestName string
	// Rules names every configured rule the message came from - one finding read
	// on behalf of several rules names them all. Empty for the synthetic messages
	// the engine and the checks emit for themselves.
	//
	// IMMUTABLE and SHARED: the slice is interned once per rule when the rule is
	// bound, and every message that rule tags holds that same array. A consumer
	// reads it; it must never append to it or write through it.
	Rules []string
	// Skipped marks this message as a skip acknowledgement rather than a check
	// failure. Skip messages describe why a file (or archive member) was not
	// content-scanned; they are surfaced in every output but never counted as issues.
	Skipped bool
	// Reason carries the human-readable explanation for a skip (only set when Skipped).
	Reason string
}

// define a method for displaying the message
func (m Message) Format() string {
	switch m.Source.(type) {
	case File:
		file := m.Source.(File)
		displayName := file.GetDisplayName()
		if file.ArchiveName != "" {
			return "- File issue in '" + file.ArchiveName + " > " + displayName + "': " + m.Content
		}
		return "- File issue in '" + displayName + "': " + m.Content
	case Repository:
		return "- Repository issue: " + m.Content
	default:
		return "- Unknown source issue: " + m.Content
	}
}
