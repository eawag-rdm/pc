package readers

import (
	"os"

	"github.com/fumiama/go-docx"
)

func ReadDOCXFile(filePath string) ([][]byte, error) {
	// Create an instance of the reader by opening a target file
	f, err := os.Open(filePath)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	fileinfo, err := f.Stat()
	if err != nil {
		panic(err)
	}

	size := fileinfo.Size()
	doc, err := docx.Parse(f, size)
	if err != nil {
		panic(err)
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
