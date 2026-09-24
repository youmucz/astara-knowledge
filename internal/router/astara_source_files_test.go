// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKBFileProxyAdmissionRegistration(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "files.go", nil, 0)
	require.NoError(t, err)
	registrations := map[string]int{
		"/knowledge-bases/:id/files":               0,
		"/sessions/:id/messages/:message_id/files": 0,
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) < 3 {
			return true
		}
		literal, ok := call.Args[2].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		path, _ := strconv.Unquote(literal.Value)
		if path != "/knowledge-bases/:id/files" && path != "/sessions/:id/messages/:message_id/files" {
			return true
		}
		registrations[path]++
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
			owner, owned := selector.X.(*ast.Ident)
			if owned && owner.Name == "middleware" && selector.Sel.Name == "RequireNativeSourceRoute" {
				guarded = true
			}
		}
		require.True(t, guarded, "KB file proxy lost embedded admission closure")
		return true
	})
	for path, count := range registrations {
		require.Equal(t, 1, count, "file route registration changed: %s", path)
	}
}

func TestPresignedDiagnosticEmbeddedAdmissionClosed(t *testing.T) {
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), types.EmbeddedSessionContextKey, true))
		c.Next()
	})
	// Upstream added a *config.Config parameter for the Admin role guard.
	// Nil is safe here: RequireNativeSourceRoute aborts the embedded request
	// before the role middleware ever reads it.
	servePresignedPreview(engine, nil, nil, nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/files/presigned-preview?file_path=private-object", nil))
	require.Equal(t, 404, rec.Code)
	require.NotContains(t, rec.Body.String(), "private-object")
}

func TestRawFilesEmbeddedAdmissionClosed(t *testing.T) {
	engine := gin.New()
	// Model the auth middleware's server-resolved session marker. Nil storage
	// dependencies ensure admission must reject before entering the file handler.
	engine.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), types.EmbeddedSessionContextKey, true))
		c.Next()
	})
	serveFilesWithResources(engine, nil, nil, nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest("GET", "/files?path=private-object", nil))
	require.Equal(t, 404, rec.Code)
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	require.NotContains(t, rec.Body.String(), "private-object")
}
