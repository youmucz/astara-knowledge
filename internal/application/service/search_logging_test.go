// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// SearchKnowledge is shared by native and service-authorized search. The
// admission log must not emit the query or full document identities.
func TestSearchAdmissionLogUsesCountsOnly(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "session_knowledge_qa.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "SearchKnowledge" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "logger" {
				return true
			}
			for _, arg := range call.Args {
				if id, ok := arg.(*ast.Ident); ok && (id.Name == "query" || id.Name == "knowledgeIDs" || id.Name == "knowledgeBaseIDs") {
					t.Errorf("sensitive search input logged directly: %s", id.Name)
				}
			}
			return true
		})
	}
	if !found {
		t.Fatal("SearchKnowledge moved; review logging regression coverage")
	}
}
