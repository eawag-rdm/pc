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
	// The test name that generated this message.
	TestName string
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
