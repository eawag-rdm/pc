package utils

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

func TestSkipCheck(t *testing.T) {
	tests := []struct {
		name      string
		config    config.Config
		checkName string
		file      structs.File
		want      bool
	}{
		{
			name:      "No config for check",
			config:    config.Config{},
			checkName: "IsASCII",
			file:      structs.File{Name: "test.txt"},
			want:      false,
		},
		{
			name: "File in whitelist",
			config: config.Config{
				Tests: map[string]config.Test{
					"IsASCII": {
						Whitelist: []string{".+.txt"},
						Blacklist: []string{},
						Keywords:  []map[string]string{},
					},
				},
			},
			checkName: "IsASCII",
			file:      structs.File{Name: "test.txt"},
			want:      false,
		},
		{
			name: "File in blacklist",
			config: config.Config{
				Tests: map[string]config.Test{
					"IsASCII": {
						Whitelist: []string{},
						Blacklist: []string{".+.txt"},
						Keywords:  []map[string]string{},
					},
				},
			},
			checkName: "IsASCII",
			file:      structs.File{Name: "test.txt"},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := skipCheck(tt.config, tt.checkName, tt.file); got != tt.want {
				t.Errorf("skipCheck() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyChecksFiltered(t *testing.T) {
	tests := []struct {
		name   string
		config config.Config
		checks map[string]reflect.Value
		files  []structs.File
		want   []structs.Message
	}{
		{
			name:   "No checks to apply",
			config: config.Config{},
			checks: map[string]reflect.Value{},
			files:  []structs.File{{Name: "test.txt"}},
			want:   nil,
		},
		{
			name: "Apply check to file",
			config: config.Config{
				Tests: map[string]config.Test{
					"HasOnlyASCII": {
						Whitelist: []string{".+.txt"},
						Blacklist: []string{},
						Keywords:  []map[string]string{},
					},
				},
			},
			checks: map[string]reflect.Value{
				"HasOnlyASCII": reflect.ValueOf(func(file structs.File) structs.Message {
					return structs.Message{Content: "Check passed"}
				}),
			},
			files: []structs.File{{Name: "test.txt"}},
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyChecksFiltered(tt.config, tt.checks, tt.files)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ApplyChecksFiltered() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCallFunctionByName(t *testing.T) {
	tests := []struct {
		name        string
		funcName    string
		funcs       map[string]reflect.Value
		params      []interface{}
		expectPanic bool
	}{
		{
			name:     "Function exists with no params",
			funcName: "TestFuncNoParams",
			funcs: map[string]reflect.Value{
				"TestFuncNoParams": reflect.ValueOf(func() {
					fmt.Println("TestFuncNoParams called")
				}),
			},
			params:      []interface{}{},
			expectPanic: false,
		},
		{
			name:     "Function exists with params",
			funcName: "TestFuncWithParams",
			funcs: map[string]reflect.Value{
				"TestFuncWithParams": reflect.ValueOf(func(a int, b string) {
					fmt.Printf("TestFuncWithParams called with %d and %s\n", a, b)
				}),
			},
			params:      []interface{}{42, "hello"},
			expectPanic: false,
		},
		{
			name:        "Function does not exist",
			funcName:    "NonExistentFunc",
			funcs:       map[string]reflect.Value{},
			params:      []interface{}{},
			expectPanic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.expectPanic {
				defer func() {
					if r := recover(); r == nil {
						t.Errorf("Expected panic but did not get one")
					}
				}()
			}
			CallFunctionByName(tt.funcName, tt.funcs, tt.params...)
		})
	}
}
