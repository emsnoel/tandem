package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// declaredPauseCodes reads the string constants whose name starts with pause
// or Pause from the packages that set a run's pause code, so a code added
// there without being listed in PauseCodes fails this test.
func declaredPauseCodes(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	var codes []string
	for _, dir := range []string{".", filepath.Join("..", "workflow")} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			tree, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range tree.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if !strings.HasPrefix(name.Name, "pause") && !strings.HasPrefix(name.Name, "Pause") {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						code, err := strconv.Unquote(lit.Value)
						if err != nil {
							t.Fatal(err)
						}
						codes = append(codes, code)
					}
				}
			}
		}
	}
	sort.Strings(codes)
	return codes
}

func TestPauseCodesListsEveryConstant(t *testing.T) {
	want := declaredPauseCodes(t)
	got := PauseCodes()
	if len(want) == 0 {
		t.Fatal("found no pause constants")
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("PauseCodes() = %v\nconstants    = %v", got, want)
	}
}
