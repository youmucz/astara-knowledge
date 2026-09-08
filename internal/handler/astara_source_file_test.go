// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package handler

import (
	"context"
	"errors"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/sourceauth"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedSourceFileBoundedDelivery(t *testing.T) {
	for _, size := range []int{10, 2 << 20, (2 << 20) + 1} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("GET", "/", nil)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), types.EmbeddedSessionContextKey, true))
		require.True(t, writeEmbeddedSourceFile(c, strings.NewReader(strings.Repeat("x", size))))
		if size > 2<<20 {
			require.Equal(t, 404, rec.Code)
			require.Less(t, rec.Body.Len(), 200)
		} else {
			require.Equal(t, size, rec.Body.Len())
			require.Equal(t, 200, rec.Code)
		}
		require.False(t, rec.Flushed)
	}
}

type partialSourceFileFailure struct{}

func (partialSourceFileFailure) Read(p []byte) (int, error) {
	return copy(p, "partial-secret"), errors.New("storage-secret-error")
}

func TestEmbeddedSourceFileDiscardsPartialReadFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/", nil)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), types.EmbeddedSessionContextKey, true))
	c.Header("Content-Disposition", "attachment; filename=secret.pdf")
	require.True(t, writeEmbeddedSourceFile(c, partialSourceFileFailure{}))
	require.Equal(t, 404, rec.Code)
	require.NotContains(t, rec.Body.String(), "partial-secret")
	require.NotContains(t, rec.Body.String(), "storage-secret-error")
	require.Empty(t, rec.Header().Get("Content-Disposition"))
	require.False(t, rec.Flushed)
}

type sourceFileLookup struct{}

func (sourceFileLookup) GetKnowledgeByIDOnly(context.Context, string) (*types.Knowledge, error) {
	return &types.Knowledge{ID: "doc", TenantID: 1, KnowledgeBaseID: "kb"}, nil
}

func TestEmbeddedSourceFileGuardDeliveryFence(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		calls := 0
		auth := func(_ context.Context, _, _, _ string, _ []sourceauth.Document) sourceauth.AuthorizationResult {
			calls++
			if revoke && calls == 2 {
				return sourceauth.AuthorizationResult{Err: errors.New("revoked")}
			}
			return sourceauth.AuthorizationResult{Snapshot: &sourceauth.AuthorizationResponse{ContractVersion: 1, Documents: []sourceauth.AuthorizedDocument{{TenantID: "1", KnowledgeBaseID: "kb", KnowledgeID: "doc", Revision: strings.Repeat("a", 64)}}}}
		}
		engine := gin.New()
		engine.GET("/files/:id", middleware.SourceAccessGuard("id", sourceFileLookup{}, auth), func(c *gin.Context) {
			require.True(t, writeEmbeddedSourceFile(c, strings.NewReader("protected-file-bytes")))
		})
		req := httptest.NewRequest("GET", "/files/doc", nil)
		system, subject, workspace := "astara", "source-user", "source-workspace"
		ctx := context.WithValue(req.Context(), types.EmbeddedSessionContextKey, true)
		ctx = context.WithValue(ctx, types.UserContextKey, &types.User{ID: "user", IsActive: true, ExternalSystem: &system, ExternalID: &subject})
		ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{ID: 1, Status: "active", ExternalSystem: &system, ExternalID: &workspace})
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req.WithContext(ctx))
		require.Equal(t, 2, calls)
		require.False(t, rec.Flushed)
		if revoke {
			require.Equal(t, 404, rec.Code)
			require.NotContains(t, rec.Body.String(), "protected-file-bytes")
		} else {
			require.Equal(t, 200, rec.Code)
			require.Equal(t, "protected-file-bytes", rec.Body.String())
		}
	}
}

func TestNativeSourceFileRemainsStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/", nil)
	require.False(t, writeEmbeddedSourceFile(c, strings.NewReader("native")))
	require.Zero(t, rec.Body.Len())
}
