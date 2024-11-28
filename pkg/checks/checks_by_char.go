package checks

const (
	SP  = 0x20 //      Space
	DEL = 0x7F //      Delete
)

// Return true if c is a valid ASCII character; otherwise, return false.
func isASCII(c byte) bool { return c <= DEL }

// Return true if c is a space character; otherwise, return false.
func isSpace(c byte) bool { return c == SP }
