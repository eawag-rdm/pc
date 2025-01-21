package readers

import (
	"fmt"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/stretchr/testify/assert"
)

func TestReadDOCXFile(t *testing.T) {
	xlsxFile := structs.File{Path: "../../testdata/test.docx", Name: "test.docx", Size: 0, Suffix: ".docx"}
	content, err := ReadDOCXFile(xlsxFile)
	if err != nil {
		t.Fatalf("Failed to read XLSX file: %v", err)
	}

	expectedContent := [][]byte{[]byte("PAGE 1"), []byte("|  :----: | :----: |\n| Table | 1 |"), []byte("")}
	for _, c := range content {
		fmt.Println(string(c))
	}
	assert.Equal(t, expectedContent, content)
}
