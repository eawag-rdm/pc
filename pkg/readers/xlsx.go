package readers

import (
	"github.com/eawag-rdm/pc/pkg/helpers"
	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/thedatashed/xlsxreader"
)

func ReadXLSXFile(file structs.File) ([][]byte, error) {
	// Create an instance of the reader by opening a target file
	xl, err := xlsxreader.OpenFile(file.Path)
	if err != nil {
		return nil, err
	}
	// Ensure the file reader is closed once utilized
	defer xl.Close()

	helpers.WarnForLargeFile(file, 2*1024*1024, "pretty big file, this may take a little longer.")

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
