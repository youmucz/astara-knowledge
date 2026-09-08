// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package middleware

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type sourceChunkLookup struct{ chunk *types.Chunk }

func (s *sourceChunkLookup) GetChunkByIDOnly(context.Context, string) (*types.Chunk, error) {
	return s.chunk, nil
}

type forbiddenSourceLookup struct{ t *testing.T }

func (f forbiddenSourceLookup) GetKnowledgeByIDOnly(context.Context, string) (*types.Knowledge, error) {
	f.t.Error("invalid route ID reached document lookup")
	return nil, nil
}
func (f forbiddenSourceLookup) GetChunkByIDOnly(context.Context, string) (*types.Chunk, error) {
	f.t.Error("invalid route ID reached chunk lookup")
	return nil, nil
}

func TestSourceGuardRejectsOverlongRouteIDs(t *testing.T) {
	for _, chunkRoute := range []bool{false, true} {
		lookup := forbiddenSourceLookup{t: t}
		auth := &testAuthorizer{allow: true}
		guard := SourceAccessGuard("id", lookup, auth.authorize)
		if chunkRoute {
			guard = SourceChunkReadGuard("id", lookup, lookup, auth.authorize)
		}
		engine := gin.New()
		engine.GET("/doc/:id", guard, func(c *gin.Context) { t.Error("invalid ID reached handler") })
		req := httptest.NewRequest("GET", "/doc/"+strings.Repeat("a", 257), nil)
		req = req.WithContext(embeddedContext(req))
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != 404 || auth.calls != 0 {
			t.Fatal("overlong ID reached authorization")
		}
	}
}

func TestSourceChunkGuardFencesMappingChanges(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-kb", "drift"} {
		t.Run(mode, func(t *testing.T) {
			chunk := &types.Chunk{ID: "chunk-1", KnowledgeID: "k-1", KnowledgeBaseID: "kb-1"}
			if mode == "wrong-kb" {
				chunk.KnowledgeBaseID = "foreign"
			}
			docs := &stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}}
			auth := &testAuthorizer{allow: true}
			engine := gin.New()
			engine.GET("/chunks/:id", SourceChunkReadGuard("id", &sourceChunkLookup{chunk}, docs, auth.authorize), func(c *gin.Context) {
				if c.Param("id") != "chunk-1" {
					t.Error("route param mutated")
				}
				if mode == "drift" {
					chunk.KnowledgeID = "foreign"
				}
				c.String(200, "protected chunk")
			})
			req := httptest.NewRequest(http.MethodGet, "/chunks/chunk-1", nil)
			req = req.WithContext(embeddedContext(req))
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			if mode == "valid" {
				if recorder.Code != 200 || recorder.Body.String() != "protected chunk" {
					t.Fatalf("valid chunk refused: %d", recorder.Code)
				}
			} else if recorder.Code != 404 || recorder.Body.String() == "protected chunk" {
				t.Fatal("mapping mismatch leaked response")
			}
			if mode == "wrong-kb" && auth.calls != 0 {
				t.Fatal("invalid mapping reached authority")
			}
		})
	}
}
