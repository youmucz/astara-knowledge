// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// Pin actual registrations, not merely the behavior of an isolated guard.
// These endpoints remain unavailable to embedded sessions until document
// authorization is implemented; removing admission closure needs new evidence.
func TestUnscopedSourceReadAdmissionInventory(t *testing.T) {
	expected := map[string]int{
		"kRead.GET /move/progress/:task_id":           1,
		"kb.GET /copy/progress/:task_id":              1,
		"g.apiKeyRoute /faq/import/progress/:task_id": 1,
		"kb.GET /:id/move-targets":                    1,
		"r.GET /knowledge-bases/:id/activity":         1,
		"kbTagsRead.GET ":                             1,
		"kb.GET ":                                     1,
		"kb.GET /:id":                                 1,
		"kRead.GET /search":                           1,
		"kb.POST /:id/hybrid-search":                  1,
		"kb.GET /:id/hybrid-search":                   1,
		"kbRead.GET ":                                 1,
		"kbRead.GET /folders":                         1,
		"faqRead.GET /entries":                        1,
		"faqRead.GET /entries/export":                 1,
		"faqRead.GET /entries/:entry_id":              1,
		"faqRead.POST /search":                        1,
	}
	for _, path := range []string{"/pages", "/pages/*slug", "/revisions/*slug", "/folders", "/index", "/graph", "/stats", "/search", "/lint", "/issues"} {
		expected["wikiRead.GET "+path] = 1
	}
	file, err := parser.ParseFile(token.NewFileSet(), "routes_knowledge.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		groupExpr := method.X
		if chain, chained := groupExpr.(*ast.CallExpr); chained {
			if with, valid := chain.Fun.(*ast.SelectorExpr); valid && with.Sel.Name == "With" {
				groupExpr = with.X
			}
		}
		group, ok := groupExpr.(*ast.Ident)
		if !ok {
			return true
		}
		pathIndex := 0
		if method.Sel.Name == "apiKeyRoute" {
			pathIndex = 2
		}
		if len(call.Args) <= pathIndex {
			return true
		}
		literal, ok := call.Args[pathIndex].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		path, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Fatal(err)
		}
		key := group.Name + "." + method.Sel.Name + " " + path
		if _, tracked := expected[key]; !tracked {
			return true
		}
		expected[key]--
		found := false
		for _, arg := range call.Args[1:] {
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
				found = true
			}
		}
		if !found {
			t.Errorf("unscoped route lost embedded admission closure: %s", key)
		}
		return true
	})
	for key, count := range expected {
		if count != 0 {
			t.Errorf("registration count changed for %s: remainder %d", key, count)
		}
	}
}
