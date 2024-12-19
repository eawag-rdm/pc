package structs

type Source interface {
	GetValue() []File
}

func (f File) GetValue() []File {
	return []File{f}
}

func (r Repository) GetValue() []File {
	return r.Files
}

// a struct that defines the message structure, retuned by the failed checks
type Message struct {
	// The message content.
	Content string
	// The source of the message.
	Source Source
}

// define a method for displaying the message
func (m Message) Format() string {
	return m.Content
}
