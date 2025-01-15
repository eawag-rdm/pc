package pkg

import (
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/stretchr/testify/assert"
)

func initFileCollector(mockFiles []structs.File) func(cfg config.Config) ([]structs.File, error) {

	fileCollector := func(cfg config.Config) ([]structs.File, error) {
		return mockFiles, nil
	}
	return fileCollector
}

func TestMainLogic_Success(t *testing.T) {

	tests := []struct {
		collector func(cfg config.Config) ([]structs.File, error)
		expected  []structs.Message
	}{
		{
			collector: initFileCollector(
				[]structs.File{{Name: "file1"}, {Name: "file2"}},
			),
			expected: nil,
		},
		{
			collector: initFileCollector(
				[]structs.File{{Name: "space in file name"}, {Name: "file2"}},
			),
			expected: []structs.Message{
				{Content: "File contains spaces.", Source: structs.File{Name: "space in file name"}},
			},
		},
		{
			collector: initFileCollector(
				[]structs.File{{Name: "space in file name"}, {Name: "file2"}},
			),
			expected: []structs.Message{
				{Content: "File contains spaces.", Source: structs.File{Name: "space in file name"}},
			},
		},
		{
			collector: initFileCollector(
				[]structs.File{{Name: "Non ascĩĩ and space"}, {Name: "file2"}},
			),
			expected: []structs.Message{
				{Content: "File contains non-ASCII character: ĩĩ", Source: structs.File{Name: "Non ascĩĩ and space"}},
				{Content: "File contains spaces.", Source: structs.File{Name: "Non ascĩĩ and space"}},
			},
		},
	}

	for _, test := range tests {
		messagess := MainLogic("../testdata/test_config.toml", test.collector, false)

		if !assert.ElementsMatch(t, messagess, test.expected) {
			t.Errorf("MainLogic(...) = %v; want %v", messagess, test.expected)
		}
	}

}
