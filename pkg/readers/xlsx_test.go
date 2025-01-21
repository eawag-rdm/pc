package readers

import (
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/stretchr/testify/assert"
)

func TestReadXLSXFile(t *testing.T) {
	xlsxFile := structs.File{Path: "../../testdata/test.xlsx", Name: "test.xlsx", Size: 0, Suffix: ".xlsx"}
	content, err := ReadXLSXFile(xlsxFile)
	if err != nil {
		t.Fatalf("Failed to read XLSX file: %v", err)
	}
	expectedContent := [][]byte{[]byte("row1 column2 \nrow2 \n")}

	assert.Equal(t, expectedContent, content)
}
