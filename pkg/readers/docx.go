package readers

import (
	"os"

	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/fumiama/go-docx"
)

func ReadDOCXFile(file structs.File) ([][]byte, error) {
	// Create an instance of the reader by opening a target file
	f, err := os.Open(file.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fileinfo, err := f.Stat()
	if err != nil {
		return nil, err
	}

	size := fileinfo.Size()
	doc, err := docx.Parse(f, size)
	if err != nil {
		return nil, err
	}

	// Validate document pointer
	if doc == nil {
		return [][]byte{}, nil
	}

	DOCXContent := [][]byte{}
	for _, it := range doc.Document.Body.Items {
		switch it.(type) {
		case *docx.Paragraph, *docx.Table:
			// transform it to string
			switch v := it.(type) {
			case *docx.Paragraph:
				DOCXContent = append(DOCXContent, []byte(v.String()))
			case *docx.Table:
				DOCXContent = append(DOCXContent, []byte(v.String()))
			}
		}
	}

	return DOCXContent, nil
}
