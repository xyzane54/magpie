package cloudcodec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The product imports this package before any request. Keep initialization and
// dependency expansion from restoring the removed gateway lifecycle implicitly.
func TestNoLifecycleOrLegacyDependencies(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") {
				t.Errorf("%s: wire codec imports a nonstandard dependency %s", name, path)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.FuncDecl:
				if node.Recv == nil && node.Name.Name == "init" {
					t.Errorf("%s: wire codec has an init hook", name)
				}
			case *ast.GoStmt:
				t.Errorf("%s: wire codec starts a background goroutine", name)
			}
			return true
		})
	}
}
