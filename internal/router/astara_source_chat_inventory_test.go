// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestEmbeddedUnscopedGenerationAdmission(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "routes_chat.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]int{"knowledgeChat": 1, "agentChat": 1, "knowledgeSearch": 1}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || method.Sel.Name != "POST" {
			return true
		}
		group, ok := method.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, tracked := expected[group.Name]; !tracked {
			return true
		}
		expected[group.Name]--
		guarded := false
		for _, arg := range call.Args {
			invocation, ok := arg.(*ast.CallExpr)
			if !ok {
				continue
			}
			selector, ok := invocation.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			owner, ok := selector.X.(*ast.Ident)
			if ok && owner.Name == "middleware" && selector.Sel.Name == "RequireNativeSourceRoute" {
				guarded = true
			}
		}
		if !guarded {
			t.Errorf("%s permits unscoped embedded generation/retrieval", group.Name)
		}
		return true
	})
	for group, remainder := range expected {
		if remainder != 0 {
			t.Errorf("%s registration count changed: %d", group, remainder)
		}
	}
}
