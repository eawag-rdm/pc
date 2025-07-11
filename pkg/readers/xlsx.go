package readers

import (
	"bytes"
	"sync"

	"github.com/eawag-rdm/pc/pkg/helpers"
	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/thedatashed/xlsxreader"
)

// Buffer pool to reduce memory allocations
var bufferPool = sync.Pool{
	New: func() interface{} {
		return bytes.NewBuffer(make([]byte, 0, 8192)) // 8KB initial capacity
	},
}

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
		// Use buffer pool to reduce allocations
		sheetBuffer := bufferPool.Get().(*bytes.Buffer)
		sheetBuffer.Reset()
		
		// Iterate on the rows of data
		rows := xl.ReadRows(sheet)
		for row := range rows {
			// Pre-allocate row buffer with estimated capacity
			rowBuffer := bufferPool.Get().(*bytes.Buffer)
			rowBuffer.Reset()
			
			for _, cell := range row.Cells {
				if cell.Type == "string" && len(cell.Value) > 0 {
					rowBuffer.WriteString(cell.Value)
					rowBuffer.WriteByte(' ')
				}
			}
			
			if rowBuffer.Len() > 0 {
				rowBuffer.WriteByte('\n')
				sheetBuffer.Write(rowBuffer.Bytes())
			}
			
			bufferPool.Put(rowBuffer)
		}
		
		if sheetBuffer.Len() > 0 {
			// Copy buffer content to avoid sharing underlying array
			sheetContent := make([]byte, sheetBuffer.Len())
			copy(sheetContent, sheetBuffer.Bytes())
			XLSXContent = append(XLSXContent, sheetContent)
		}
		
		// Return buffer to pool
		bufferPool.Put(sheetBuffer)
	}
	
	return XLSXContent, nil
}
