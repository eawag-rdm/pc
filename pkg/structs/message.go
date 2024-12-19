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
	switch m.Source.(type) {
	case File:
		return "File issue in '" + m.Source.(File).Name + "': " + m.Content
	case Repository:
		return "Repository issue: " + m.Content
	default:
		return "Unknown source issue: " + m.Content
	}
}
