package readers

import (
	"fmt"
	"os"

	"github.com/thedatashed/xlsxreader"
)

func ReadXLSXFile(filePath string) ([][]byte, error) {
	// Create an instance of the reader by opening a target file
	xl, err := xlsxreader.OpenFile(filePath)
	if err != nil {
		return nil, err
	}
	// Ensure the file reader is closed once utilized
	defer xl.Close()

	// if the excel file is greater than 2MB warn the user, as it may cause performance issues
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}
	if fileInfo.Size() > 2*1024*1024 {
		fmt.Printf("Warning: The 'XSLX' file '%s' is pretty big, this may take a little longer.\n", filePath)
	}

	XLSXContent := make([][]byte, 0, len(xl.Sheets))
	for _, sheet := range xl.Sheets {
		var SheetContent []byte
		// Iterate on the rows of data
		rows := xl.ReadRows(sheet)
		for row := range rows {
			RowContent := make([]byte, 0, len(row.Cells))
			for _, cell := range row.Cells {
				if cell.Type == "string" {
					RowContent = append(RowContent, cell.Value...)
					RowContent = append(RowContent, ' ')
				}
			}
			RowContent = append(RowContent, '\n')
			SheetContent = append(SheetContent, RowContent...)
		}
		XLSXContent = append(XLSXContent, SheetContent)
	}
	return XLSXContent, nil
}
