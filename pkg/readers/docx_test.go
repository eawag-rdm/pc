package readers

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReadDOCXFile(t *testing.T) {
	xlsxFile := "../../testdata/test.docx"
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
