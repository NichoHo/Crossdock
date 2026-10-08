// Package sqllint holds the lint rule that rejects string-built SQL (spec 17).
// Every query passed to database/sql must be a constant: a literal, a constant,
// or constants joined with +. Values go in as parameters. A query that must be
// assembled (a column name chosen from a fixed list, a table name) carries a
// `sqlok:` comment on the same or the previous line that says why it is safe.
package sqllint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// query method name -> index of the query argument
var queryArg = map[string]int{
	"Query": 0, "QueryRow": 0, "Exec": 0, "Prepare": 0,
	"QueryContext": 1, "QueryRowContext": 1, "ExecContext": 1, "PrepareContext": 1,
}

func TestNoStringBuiltSQL(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var bad []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", ".venv", "web":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		ok := map[int]bool{} // lines carrying a sqlok comment
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if strings.Contains(c.Text, "sqlok:") {
					ok[fset.Position(c.Pos()).Line] = true
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, isCall := n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			sel, isSel := call.Fun.(*ast.SelectorExpr)
			if !isSel {
				return true
			}
			i, isQuery := queryArg[sel.Sel.Name]
			if !isQuery || len(call.Args) <= i || !looksLikeDB(sel.X) {
				return true
			}
			if constant(call.Args[i]) {
				return true
			}
			line := fset.Position(call.Pos()).Line
			if ok[line] || ok[line-1] {
				return true
			}
			bad = append(bad, fset.Position(call.Pos()).String())
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bad {
		t.Errorf("%s: query is not a constant; pass values as parameters, or add a sqlok: comment saying why it is safe", b)
	}
}

// looksLikeDB keeps the rule to database handles. In this codebase they are named
// db, tx, q, s.db, e.db and similar; a method called Query on something else is not SQL.
func looksLikeDB(x ast.Expr) bool {
	var name string
	switch v := x.(type) {
	case *ast.Ident:
		name = v.Name
	case *ast.SelectorExpr:
		name = v.Sel.Name
	}
	switch strings.ToLower(name) {
	case "db", "tx", "q", "querier", "conn", "exec":
		return true
	}
	return false
}

// constant reports whether e is a string literal, a constant identifier, or those joined by +.
func constant(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING
	case *ast.Ident:
		return v.Obj != nil && v.Obj.Kind == ast.Con
	case *ast.ParenExpr:
		return constant(v.X)
	case *ast.BinaryExpr:
		return v.Op == token.ADD && constant(v.X) && constant(v.Y)
	}
	return false
}

// The rule must be able to fail: a Sprintf, a variable and a concatenation with a variable are all rejected.
func TestRuleRejectsBuiltQueries(t *testing.T) {
	for src, want := range map[string]bool{
		`"SELECT 1"`:                   true,
		`"SELECT " + "1"`:              true,
		`fmt.Sprintf("SELECT %s", x)`:  false,
		`q`:                            false,
		`"SELECT * FROM t WHERE a=" + id`: false,
	} {
		e, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatal(err)
		}
		if got := constant(e); got != want {
			t.Errorf("constant(%s) = %v, want %v", src, got, want)
		}
	}
}
