package collectors

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
)

func CollectFunctions(filePath string) (map[string]reflect.Value, error) {
	CollectedFunctions := make(map[string]reflect.Value)

	fs := token.NewFileSet()
	node, err := parser.ParseFile(fs, filePath, nil, parser.AllErrors)
	if err != nil {
		return nil, err
	}

	for _, decl := range node.Decls {
		if fn, isFn := decl.(*ast.FuncDecl); isFn {
			fnName := fn.Name.Name
			fnValue := reflect.ValueOf(fnName)
			CollectedFunctions[fnName] = fnValue
		}
	}

	return CollectedFunctions, nil
}

func CallFunctionByName(name string, CollectedFunctions map[string]reflect.Value, params ...interface{}) {
	if fn, exists := CollectedFunctions[name]; exists {
		fnParams := make([]reflect.Value, len(params))
		for i, param := range params {
			fnParams[i] = reflect.ValueOf(param)
		}
		fn.Call(fnParams)
	} else {
		fmt.Printf("Function %s not found\n", name)
	}
}

func CollectChecks() (map[string]reflect.Value, error) {
	allChecks := make(map[string]reflect.Value)

	files, err := filepath.Glob("pkg/checks/checks/*.go")
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if !strings.HasSuffix(file, "_test.go") {
			checks, err := CollectFunctions(file)
			if err != nil {
				return nil, err
			}
			for k, v := range checks {
				allChecks[k] = v
			}
		}
	}
	return allChecks, nil
}
