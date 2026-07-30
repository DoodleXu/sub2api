package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain initializes process-wide Gin state before parallel tests begin.
// Individual tests must not mutate Gin's global mode after calling t.Parallel.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func TestGinModeConfiguredOnlyInTestMain(t *testing.T) {
	testFiles, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("list service test files: %v", err)
	}

	for _, testFile := range testFiles {
		if filepath.Base(testFile) == "service_test_main_test.go" {
			continue
		}

		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, testFile, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", testFile, err)
		}

		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "SetMode" {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if ok && pkg.Name == "gin" {
				position := fileSet.Position(call.Pos())
				t.Errorf("%s:%d must not mutate Gin's process-wide mode", testFile, position.Line)
			}
			return true
		})
	}
}
