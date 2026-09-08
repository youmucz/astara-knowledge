// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// database/sql may retain a context after ServeHTTP returns. Gin pools its
// Context objects, so these control-plane handlers must pass Request.Context()
// rather than the Gin object itself. Runtime race tests exercise this too.
func TestAstaraDatabaseUsesRequestContext(t *testing.T) {
	for _, path := range []string{"astara_control_plane.go", "astara_document_upsert.go", "astara_identity_exchange.go"} {
		t.Run(path, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			checked := 0
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "WithContext" {
					return true
				}
				checked++
				if len(call.Args) != 1 {
					t.Error("invalid WithContext call")
					return true
				}
				requestCall, ok := call.Args[0].(*ast.CallExpr)
				if !ok {
					t.Error("database context must be Request.Context(), not pooled Gin context")
					return true
				}
				contextMethod, ok := requestCall.Fun.(*ast.SelectorExpr)
				if !ok || contextMethod.Sel.Name != "Context" || len(requestCall.Args) != 0 {
					t.Error("unexpected database context expression")
					return true
				}
				request, ok := contextMethod.X.(*ast.SelectorExpr)
				if !ok || request.Sel.Name != "Request" {
					t.Error("database context not derived from HTTP request")
				}
				return true
			})
			if checked == 0 {
				t.Fatal("no database context calls checked; review moved implementation")
			}
		})
	}
}
