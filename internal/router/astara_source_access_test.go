// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// Verify the actual router configuration adapter, not an injected authorizer:
// absent operator configuration cannot silently bypass embedded authorization.
func TestSourceRouteAdaptersFailClosedWithoutConfiguration(t *testing.T) {
	t.Setenv("ASTARA_SOURCE_AUTH_URL", "")
	t.Setenv("ASTARA_SOURCE_AUTH_SECRET", "")
	guards := &rbacGuards{}
	for _, kind := range []string{"document", "chunk", "batch"} {
		for _, embedded := range []bool{true, false} {
			t.Run(kind+map[bool]string{true: "-embedded", false: "-native"}[embedded], func(t *testing.T) {
				guard := guards.SourceDocumentRead("id")
				if kind == "chunk" {
					guard = guards.SourceChunkRead("id")
				} else if kind == "batch" {
					guard = guards.SourceBatchRead()
				}
				engine := gin.New()
				called := false
				engine.GET("/resource/:id", guard, func(c *gin.Context) { called = true; c.String(200, "native content") })
				req := httptest.NewRequest(http.MethodGet, "/resource/id", nil)
				if embedded {
					req = req.WithContext(context.WithValue(req.Context(), types.EmbeddedSessionContextKey, true))
				}
				rec := httptest.NewRecorder()
				engine.ServeHTTP(rec, req)
				if embedded {
					if called || rec.Code != 404 {
						t.Fatalf("missing configuration allowed handler: called=%v status=%d", called, rec.Code)
					}
					if rec.Header().Get("Cache-Control") != "private, no-store" {
						t.Fatal("denial was cacheable")
					}
				} else if !called || rec.Code != 200 {
					t.Fatal("native route unexpectedly changed")
				}
			})
		}
	}
}
