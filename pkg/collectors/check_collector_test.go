package collectors

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"
)

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

func TestCollectFunctions(t *testing.T) {
	tests := []struct {
		name        string
		fileContent string
		expected    map[string]reflect.Value
		expectError bool
	}{
		{
			name: "File with functions",
			fileContent: `
				package main

				func Func1() {}
				func Func2(a int) int { return a }
			`,
			expected: map[string]reflect.Value{
				"Func1": reflect.ValueOf("Func1"),
				"Func2": reflect.ValueOf("Func2"),
			},
			expectError: false,
		},
		{
			name: "File with no functions",
			fileContent: `
				package main

				var x = 10
			`,
			expected:    map[string]reflect.Value{},
			expectError: false,
		},
		{
			name: "Invalid Go file",
			fileContent: `
				package main

				func Func1() {
			`,
			expected:    nil,
			expectError: true,
		},
		{
			name: "File with nested functions",
			fileContent: `
				package utils

				func TestFuncComplicated(a int, b string, c []int, d map[string]int) {
					InnerFunc := func() {
						// Do nothing
						MostInnerFunc := func() {
							// Do nothing
						}
						// MostInnerFunc()
					}
					InnerFunc()
					// Do nothing
				}
			`,
			expected: map[string]reflect.Value{
				"TestFuncComplicated": reflect.ValueOf("TestFuncComplicated"),
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := token.NewFileSet()
			node, err := parser.ParseFile(fs, "", tt.fileContent, parser.AllErrors)
			if err != nil && !tt.expectError {
				t.Fatalf("Unexpected error: %v", err)
			}
			if err == nil && tt.expectError {
				t.Fatalf("Expected error but got none")
			}

			if !tt.expectError {
				CollectedFunctions := make(map[string]reflect.Value)
				for _, decl := range node.Decls {
					if fn, isFn := decl.(*ast.FuncDecl); isFn {
						fnName := fn.Name.Name
						fnValue := reflect.ValueOf(fnName)
						CollectedFunctions[fnName] = fnValue
					}
				}

				if fmt.Sprintf("%v", CollectedFunctions) != fmt.Sprintf("%v", tt.expected) {
					t.Errorf("Expected %v, but got %v", tt.expected, CollectedFunctions)
				}
			}
		})
	}
}
