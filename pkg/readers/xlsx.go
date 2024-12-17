package readers

import (
	"github.com/thedatashed/xlsxreader"
)

func ReadXLSXFile(filePath string) ([][]byte, error) {
	// Create an instance of the reader by opening a target file
	xl, _ := xlsxreader.OpenFile(filePath)

	// Ensure the file reader is closed once utilised
	defer xl.Close()

	XLSXContent := [][]byte{}

	for _, sheet := range xl.Sheets {
		SheetContent := ""
		// Iterate on the rows of data
		for row := range xl.ReadRows(sheet) {
			RowContent := ""
			for _, cell := range row.Cells {
				if cell.Type == "string" {
					RowContent += cell.Value + " "
				}
			}
			SheetContent += RowContent + "\n"
		}
		XLSXContent = append(XLSXContent, []byte(SheetContent))
	}
	return XLSXContent, nil
}
