// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/sourceauth"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// --- stubs ---

type stubKnowledgeLookup struct {
	knowledge map[string]*types.Knowledge
	err       error
}

func (s *stubKnowledgeLookup) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	if s.err != nil {
		return nil, s.err
	}
	if k, ok := s.knowledge[id]; ok {
		return k, nil
	}
	return nil, nil
}

type testAuthorizer struct {
	allow       bool
	revokeAfter bool
	calls       int
	revision    string
}

func (t *testAuthorizer) authorize(_ context.Context, _, _, _ string, _ []sourceauth.Document) sourceauth.AuthorizationResult {
	t.calls++
	if t.revokeAfter && t.calls >= 2 {
		return sourceauth.AuthorizationResult{Err: errors.New("revoked")}
	}
	if !t.allow {
		return sourceauth.AuthorizationResult{Err: errors.New("denied")}
	}
	rev := t.revision
	if rev == "" {
		rev = strings.Repeat("a", 64)
	}
	return sourceauth.AuthorizationResult{
		Snapshot: &sourceauth.AuthorizationResponse{
			ContractVersion: 1,
			Documents: []sourceauth.AuthorizedDocument{{
				TenantID:        "100",
				KnowledgeBaseID: "kb-1",
				KnowledgeID:     "k-1",
				Revision:        rev,
			}},
		},
	}
}

type driftAuthorizer struct{ calls int }

func (d *driftAuthorizer) authorize(_ context.Context, _, _, _ string, _ []sourceauth.Document) sourceauth.AuthorizationResult {
	d.calls++
	rev := strings.Repeat("a", 64)
	if d.calls >= 2 {
		rev = strings.Repeat("b", 64)
	}
	return sourceauth.AuthorizationResult{
		Snapshot: &sourceauth.AuthorizationResponse{
			ContractVersion: 1,
			Documents: []sourceauth.AuthorizedDocument{{
				TenantID: "100", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: rev,
			}},
		},
	}
}

// --- helpers ---

const (
	testSubject   = "plane-user"
	testWorkspace = "plane-workspace"
)

func ptrStr(s string) *string { return &s }

func validUser() *types.User {
	sys := "astara"
	return &types.User{
		ID: "provider-user", IsActive: true,
		ExternalSystem: &sys, ExternalID: ptrStr(testSubject),
	}
}

func validTenant() *types.Tenant {
	sys := "astara"
	return &types.Tenant{
		ID: 7, Status: "active",
		ExternalSystem: &sys, ExternalID: ptrStr(testWorkspace),
	}
}

func validKnowledge() *types.Knowledge {
	return &types.Knowledge{ID: "k-1", TenantID: 100, KnowledgeBaseID: "kb-1"}
}

func embeddedContext(req *http.Request) context.Context {
	ctx := req.Context()
	ctx = context.WithValue(ctx, types.EmbeddedSessionContextKey, true)
	sys := "astara"
	usr := &types.User{ID: "provider-user", IsActive: true, ExternalSystem: &sys, ExternalID: ptrStr(testSubject)}
	tn := &types.Tenant{ID: 7, Status: "active", ExternalSystem: &sys, ExternalID: ptrStr(testWorkspace)}
	ctx = context.WithValue(ctx, types.UserContextKey, usr)
	ctx = context.WithValue(ctx, types.UserIDContextKey, usr.ID)
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, tn)
	ctx = context.WithValue(ctx, types.TenantIDContextKey, tn.ID)
	return ctx
}

// serveGuard creates a full engine with SourceAccessGuard, fires a
// request, and returns the recorder.
func serveGuard(
	t *testing.T,
	param string,
	kl KnowledgeLookup,
	auth Authorizer,
	handlerFn func(*gin.Context),
) *httptest.ResponseRecorder {
	t.Helper()
	engine := gin.New()
	engine.GET("/"+param+"/:"+param, SourceAccessGuard(param, kl, auth), handlerFn)
	url := "/" + param + "/k-1"
	req := httptest.NewRequest("GET", url, nil)
	req = req.WithContext(embeddedContext(req))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

// defaultHandler writes a 200 JSON body.
func defaultHandler(body string) func(*gin.Context) {
	return func(c *gin.Context) {
		c.String(http.StatusOK, body)
	}
}

func TestSourceAccessReplacesEarlierCacheHeaders(t *testing.T) {
	auth := &testAuthorizer{allow: true}
	lookup := &stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}}
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=600")
		c.Header("ETag", "earlier-tag")
		c.Next()
	})
	engine.GET("/doc/:id", SourceAccessGuard("id", lookup, auth.authorize), defaultHandler("body"))
	req := httptest.NewRequest("GET", "/doc/k-1", nil)
	req = req.WithContext(embeddedContext(req))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require.Equal(t, []string{"private, no-store"}, rec.Header().Values("Cache-Control"))
	require.Empty(t, rec.Header().Get("ETag"))
}

func TestSourceAccessDenialClearsEarlierResourceHeaders(t *testing.T) {
	engine := gin.New()
	names := []string{"ETag", "Content-Length", "Content-Encoding", "Content-Disposition", "Location", "Set-Cookie"}
	engine.Use(func(c *gin.Context) {
		for _, name := range names {
			c.Header(name, "private-metadata")
		}
		c.Next()
	})
	engine.GET("/doc/:id", SourceAccessGuard("id", nil, nil), defaultHandler("not reached"))
	req := httptest.NewRequest("GET", "/doc/k-1", nil)
	req = req.WithContext(embeddedContext(req))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
	for _, name := range names {
		require.Empty(t, rec.Header().Get(name), name)
	}
}

func TestSourceAccessUsesFinalUncommittedHandlerStatus(t *testing.T) {
	auth := &testAuthorizer{allow: true}
	lookup := &stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}}
	rec := serveGuard(t, "id", lookup, auth.authorize, func(c *gin.Context) {
		c.Status(http.StatusOK)
		c.Status(http.StatusNotModified)
	})
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSourceAccessDisablesCachingOfRevocableResponses(t *testing.T) {
	for _, allow := range []bool{true, false} {
		auth := &testAuthorizer{allow: allow}
		lookup := &stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}}
		rec := serveGuard(t, "id", lookup, auth.authorize, func(c *gin.Context) {
			c.Header("Cache-Control", "public, max-age=3600")
			c.Header("ETag", "old-tag")
			c.Header("Last-Modified", "yesterday")
			c.String(200, "protected")
		})
		require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
		require.Empty(t, rec.Header().Get("ETag"))
		require.Empty(t, rec.Header().Get("Last-Modified"))
	}
}

func TestSourceAccessRefusesReuseOfPreviouslyCachedContent(t *testing.T) {
	auth := &testAuthorizer{allow: true}
	lookup := &stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}}
	rec := serveGuard(t, "id", lookup, auth.authorize, func(c *gin.Context) {
		c.Header("ETag", "previous-content")
		c.Status(http.StatusNotModified)
	})
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Empty(t, rec.Header().Get("ETag"))
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
}

func TestSourceAccessDoesNotExposeUnderlyingWriter(t *testing.T) {
	auth := &testAuthorizer{allow: true, revokeAfter: true}
	lookup := &stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}}
	rec := serveGuard(t, "id", lookup, auth.authorize, func(c *gin.Context) {
		_, ginUnwrap := c.Writer.(interface{ Unwrap() gin.ResponseWriter })
		_, httpUnwrap := c.Writer.(interface{ Unwrap() http.ResponseWriter })
		require.False(t, ginUnwrap)
		require.False(t, httpUnwrap)
		c.String(200, "secret body")
	})
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NotContains(t, rec.Body.String(), "secret body")
}

func TestSourceAccessRejectsProviderDocumentMappingDrift(t *testing.T) {
	for _, mode := range []string{"tenant", "kb", "missing"} {
		t.Run(mode, func(t *testing.T) {
			auth := &testAuthorizer{allow: true}
			doc := validKnowledge()
			lookup := &stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": doc}}
			rec := serveGuard(t, "id", lookup, auth.authorize, func(c *gin.Context) {
				switch mode {
				case "tenant":
					doc.TenantID = 999
				case "kb":
					doc.KnowledgeBaseID = "foreign"
				case "missing":
					delete(lookup.knowledge, "k-1")
				}
				c.String(200, "must-not-escape")
			})
			require.Equal(t, http.StatusNotFound, rec.Code)
			require.NotContains(t, rec.Body.String(), "must-not-escape")
		})
	}
}

// --- tests: fail-closed paths (all return fixed 404) ---

func TestSourceAccess_NonEmbedded_PassesThrough(t *testing.T) {
	engine := gin.New()
	auth := &testAuthorizer{allow: true}
	kl := &stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}}
	engine.GET("/knowledge/:knowledge_id", SourceAccessGuard("knowledge_id", kl, auth.authorize),
		func(c *gin.Context) { c.String(200, "native-ok") })

	req := httptest.NewRequest("GET", "/knowledge/k-1", nil)
	// No EmbeddedSessionContextKey
	sys := "astara"
	usr := &types.User{ID: "u", IsActive: true, ExternalSystem: &sys, ExternalID: ptrStr("s")}
	tn := &types.Tenant{ID: 1, Status: "active", ExternalSystem: &sys, ExternalID: ptrStr("w")}
	ctx := context.WithValue(req.Context(), types.UserContextKey, usr)
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, tn)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "native-ok")
	require.Equal(t, 0, auth.calls, "authorizer must not be called for non-embedded")
}

func TestSourceAccess_MissingClient_Fixed404(t *testing.T) {
	rec := serveGuard(t, "knowledge_id", nil, nil, defaultHandler("secret"))
	require404Deny(t, rec)
}

func TestSourceAccess_MissingUser_Fixed404(t *testing.T) {
	engine := gin.New()
	engine.GET("/knowledge/:knowledge_id",
		SourceAccessGuard("knowledge_id",
			&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
			(&testAuthorizer{allow: true}).authorize),
		defaultHandler("secret"))
	req := httptest.NewRequest("GET", "/knowledge/k-1", nil)
	ctx := context.WithValue(req.Context(), types.EmbeddedSessionContextKey, true)
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, validTenant())
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require404Deny(t, rec)
}

func TestSourceAccess_MissingTenant_Fixed404(t *testing.T) {
	engine := gin.New()
	engine.GET("/knowledge/:knowledge_id",
		SourceAccessGuard("knowledge_id",
			&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
			(&testAuthorizer{allow: true}).authorize),
		defaultHandler("secret"))
	req := httptest.NewRequest("GET", "/knowledge/k-1", nil)
	ctx := context.WithValue(req.Context(), types.EmbeddedSessionContextKey, true)
	ctx = context.WithValue(ctx, types.UserContextKey, validUser())
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require404Deny(t, rec)
}

func TestSourceAccess_NativeUser_NoMapping_Fixed404(t *testing.T) {
	engine := gin.New()
	engine.GET("/knowledge/:knowledge_id",
		SourceAccessGuard("knowledge_id",
			&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
			(&testAuthorizer{allow: true}).authorize),
		defaultHandler("secret"))
	native := &types.User{ID: "native", IsActive: true}
	req := httptest.NewRequest("GET", "/knowledge/k-1", nil)
	ctx := context.WithValue(req.Context(), types.EmbeddedSessionContextKey, true)
	ctx = context.WithValue(ctx, types.UserContextKey, native)
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, validTenant())
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require404Deny(t, rec)
}

func TestSourceAccess_InactiveUser_Fixed404(t *testing.T) {
	engine := gin.New()
	engine.GET("/knowledge/:knowledge_id",
		SourceAccessGuard("knowledge_id",
			&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
			(&testAuthorizer{allow: true}).authorize),
		defaultHandler("secret"))
	req := httptest.NewRequest("GET", "/knowledge/k-1", nil)
	ctx := embeddedContext(req)
	sys := "astara"
	inactive := &types.User{ID: "p", IsActive: false, ExternalSystem: &sys, ExternalID: ptrStr("s")}
	ctx = context.WithValue(ctx, types.UserContextKey, inactive)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require404Deny(t, rec)
}

func TestSourceAccess_InactiveTenant_Fixed404(t *testing.T) {
	engine := gin.New()
	engine.GET("/knowledge/:knowledge_id",
		SourceAccessGuard("knowledge_id",
			&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
			(&testAuthorizer{allow: true}).authorize),
		defaultHandler("secret"))
	req := httptest.NewRequest("GET", "/knowledge/k-1", nil)
	ctx := embeddedContext(req)
	sys := "astara"
	inactive := &types.Tenant{ID: 7, Status: "suspended", ExternalSystem: &sys, ExternalID: ptrStr("w")}
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, inactive)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require404Deny(t, rec)
}

func TestSourceAccess_MissingKnowledgeID_Fixed404(t *testing.T) {
	engine := gin.New()
	engine.GET("/knowledge/:id",
		SourceAccessGuard("knowledge_id", // param doesn't exist in URL
			&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
			(&testAuthorizer{allow: true}).authorize),
		defaultHandler("secret"))
	req := httptest.NewRequest("GET", "/knowledge/k-1", nil)
	req = req.WithContext(embeddedContext(req))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require404Deny(t, rec)
}

func TestSourceAccess_KnowledgeNotFound_Fixed404(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{}},
		(&testAuthorizer{allow: true}).authorize,
		defaultHandler("secret"))
	require404Deny(t, rec)
}

func TestSourceAccess_KnowledgeLookupError_Fixed404(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{err: errors.New("db down")},
		(&testAuthorizer{allow: true}).authorize,
		defaultHandler("secret"))
	require404Deny(t, rec)
}

func TestSourceAccess_AuthDenied_Fixed404(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
		(&testAuthorizer{allow: false}).authorize,
		defaultHandler("SECRET"))
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "SECRET")
}

func TestSourceAccess_EmptySnapshot_Fixed404(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
		func(_ context.Context, _, _, _ string, _ []sourceauth.Document) sourceauth.AuthorizationResult {
			return sourceauth.AuthorizationResult{
				Snapshot: &sourceauth.AuthorizationResponse{ContractVersion: 1, Documents: []sourceauth.AuthorizedDocument{}},
			}
		},
		defaultHandler("secret"))
	require404Deny(t, rec)
}

func TestSourceAccess_ForgedWrongTriple_DeniedNoHandler(t *testing.T) {
	// Authorizer returns a valid-looking snapshot (non-empty, contract v1,
	// 64-hex revision) but with a WRONG tenant_id triple.
	// The guard must deny and the handler must never execute.
	handlerRan := false
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
		func(_ context.Context, _, _, _ string, docs []sourceauth.Document) sourceauth.AuthorizationResult {
			// Return a snapshot where the triple is deliberately wrong.
			return sourceauth.AuthorizationResult{
				Snapshot: &sourceauth.AuthorizationResponse{
					ContractVersion: 1,
					Documents: []sourceauth.AuthorizedDocument{{
						TenantID:        "999", // wrong — knowledge has tenant 100
						KnowledgeBaseID: docs[0].KnowledgeBaseID,
						KnowledgeID:     docs[0].KnowledgeID,
						Revision:        strings.Repeat("a", 64),
					}},
				},
			}
		},
		func(c *gin.Context) {
			handlerRan = true
			c.String(200, "leaked")
		})
	require404Deny(t, rec)
	require.False(t, handlerRan, "handler must not run when pre-auth triple is wrong")
	require.NotContains(t, rec.Body.String(), "leaked")
}

func TestSourceAccess_KnowledgeIDMismatch_Fixed404(t *testing.T) {
	// Lookup returns a knowledge with a DIFFERENT ID than what was
	// requested in the URL param — a service-layer invariant violation
	// that the guard must reject.
	mismatchKL := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{
			"k-1": {ID: "k-DIFFERENT", TenantID: 100, KnowledgeBaseID: "kb-1"},
		},
	}
	rec := serveGuard(t, "knowledge_id", mismatchKL,
		(&testAuthorizer{allow: true}).authorize,
		defaultHandler("secret"))
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "secret")
}

// --- tests: success path ---

func TestSourceAccess_Success_FlushesResponse(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
		(&testAuthorizer{allow: true}).authorize,
		defaultHandler(`{"data":"hello"}`))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"data":"hello"`)
}

func TestSourceAccess_AuthorizerReceivesCorrectParams(t *testing.T) {
	var gotUserID, gotWorkspace, gotOp string
	var gotDocs []sourceauth.Document
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
		func(_ context.Context, uid, ws, op string, docs []sourceauth.Document) sourceauth.AuthorizationResult {
			gotUserID = uid
			gotWorkspace = ws
			gotOp = op
			gotDocs = docs
			return sourceauth.AuthorizationResult{
				Snapshot: &sourceauth.AuthorizationResponse{
					ContractVersion: 1,
					Documents: []sourceauth.AuthorizedDocument{{
						TenantID: "100", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1",
						Revision: strings.Repeat("a", 64),
					}},
				},
			}
		},
		defaultHandler("ok"))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, testSubject, gotUserID)
	require.Equal(t, testWorkspace, gotWorkspace)
	require.Equal(t, "read", gotOp)
	require.Len(t, gotDocs, 1)
	require.Equal(t, "100", gotDocs[0].TenantID)
	require.Equal(t, "kb-1", gotDocs[0].KnowledgeBaseID)
	require.Equal(t, "k-1", gotDocs[0].KnowledgeID)
}

// --- tests: post-handler deny paths ---

func TestSourceAccess_RevokedAfterHandler_Fixed404(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
		(&testAuthorizer{allow: true, revokeAfter: true}).authorize,
		defaultHandler("secret"))
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "secret")
}

func TestSourceAccess_RevisionDrift_Fixed404(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
		(&driftAuthorizer{}).authorize,
		defaultHandler("secret"))
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "secret")
}

func TestSourceAccess_OversizeResponse_Fixed404(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
		(&testAuthorizer{allow: true}).authorize,
		func(c *gin.Context) {
			c.Data(http.StatusOK, "application/octet-stream",
				[]byte(strings.Repeat("x", sourceAccessBufferLimit+1)))
		})
	require404Deny(t, rec)
}

func TestSourceAccess_RedirectResponse_Fixed404(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			rec := serveGuard(t, "knowledge_id",
				&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
				(&testAuthorizer{allow: true}).authorize,
				func(c *gin.Context) { c.Redirect(code, "https://evil.example.com") })
			require404Deny(t, rec)
			require.NotContains(t, rec.Body.String(), "evil.example.com")
		})
	}
}

// --- tests: streaming refusal ---

func TestSourceAccess_FlushViolation_Fixed404(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
		(&testAuthorizer{allow: true}).authorize,
		func(c *gin.Context) {
			c.Writer.Header().Set("X-Stream", "yes")
			c.Writer.WriteHeader(http.StatusOK)
			_, _ = c.Writer.WriteString("partial")
			c.Writer.Flush() // streaming violation
		})
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "partial")
	require.Empty(t, rec.Header().Get("X-Stream"), "buffered headers must not leak on violation")
}

func TestSourceAccess_HijackViolation_Fixed404(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
		(&testAuthorizer{allow: true}).authorize,
		func(c *gin.Context) {
			c.Writer.Header().Set("X-Upgrade", "yes")
			_, _, err := c.Writer.Hijack()
			if err == nil {
				t.Error("hijack should return error")
			}
		})
	require404Deny(t, rec)
	require.Empty(t, rec.Header().Get("X-Upgrade"))
}

func TestSourceAccess_StreamThenAuthRevoke_Fixed404(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
		(&testAuthorizer{allow: true, revokeAfter: true}).authorize,
		func(c *gin.Context) {
			c.Writer.WriteHeader(http.StatusOK)
			_, _ = c.Writer.WriteString("data")
			c.Writer.Flush()
		})
	require404Deny(t, rec)
}

// --- tests: no content/cookie leakage on any deny path ---

func TestSourceAccess_NoCookieLeakage_AuthDenied(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
		(&testAuthorizer{allow: false}).authorize,
		func(c *gin.Context) {
			c.SetCookie("session", "secret-token", 3600, "/", "", false, true)
			c.JSON(http.StatusOK, gin.H{"data": "leaked"})
		})
	require404Deny(t, rec)
	for _, v := range rec.Header().Values("Set-Cookie") {
		require.NotContains(t, v, "secret-token")
	}
}

func TestSourceAccess_NoCookieLeakage_PostRevoke(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}},
		(&testAuthorizer{allow: true, revokeAfter: true}).authorize,
		func(c *gin.Context) {
			c.SetCookie("session", "secret-token", 3600, "/", "", false, true)
			c.JSON(http.StatusOK, gin.H{"data": "leaked"})
		})
	require404Deny(t, rec)
	for _, v := range rec.Header().Values("Set-Cookie") {
		require.NotContains(t, v, "secret-token")
	}
}

func TestSourceAccess_NoContentLeakage_MissingClient(t *testing.T) {
	rec := serveGuard(t, "knowledge_id", nil, nil, defaultHandler("SECRET"))
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "SECRET")
}

func TestSourceAccess_DenyBody_IsFixedJSON(t *testing.T) {
	rec := serveGuard(t, "knowledge_id",
		&stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{}},
		(&testAuthorizer{allow: true}).authorize,
		defaultHandler("irrelevant"))
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, sourceAccessFixedDenyBodyJSON, rec.Body.String())
}

// --- tests: non-embedded preserved unchanged ---

func TestSourceAccess_NonEmbedded_AuthorizerNeverCalled(t *testing.T) {
	auth := &testAuthorizer{allow: true}
	engine := gin.New()
	kl := &stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}}
	engine.GET("/knowledge/:knowledge_id", SourceAccessGuard("knowledge_id", kl, auth.authorize),
		func(c *gin.Context) { c.String(200, "ok") })

	req := httptest.NewRequest("GET", "/knowledge/k-1", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require.Equal(t, 0, auth.calls)
}

// --- tests: buffer internals ---

func TestResponseBuffer_FlushSetsViolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	buf := newResponseBuffer(c.Writer, 1024)
	require.False(t, buf.streamViolation)
	buf.Flush()
	require.True(t, buf.streamViolation)
}

func TestResponseBuffer_HijackSetsViolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	buf := newResponseBuffer(c.Writer, 1024)
	_, _, err := buf.Hijack()
	require.Error(t, err)
	require.True(t, buf.streamViolation)
}

func TestResponseBuffer_CommitRefusedOnViolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	buf := newResponseBuffer(c.Writer, 1024)
	buf.streamViolation = true
	buf.body.WriteString("should not appear")
	buf.status = 200
	buf.commitBuffer()
	require.Equal(t, "", rec.Body.String(), "commit must be a no-op when violation is set")
}

func TestResponseBuffer_CommitRefusedOnOversize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	buf := newResponseBuffer(c.Writer, 1024)
	buf.oversize = true
	buf.body.WriteString("should not appear")
	buf.status = 200
	buf.commitBuffer()
	require.Equal(t, "", rec.Body.String())
}

func TestResponseBuffer_OverflowSetsOversize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	buf := newResponseBuffer(c.Writer, 10)
	_, _ = buf.Write([]byte("0123456789"))
	require.False(t, buf.oversize)
	_, _ = buf.Write([]byte("x"))
	require.True(t, buf.oversize)
	// Subsequent writes are silently dropped
	n, _ := buf.Write([]byte("dropped"))
	require.Equal(t, 7, n) // accepted but not buffered
	require.Equal(t, 10, buf.body.Len())
}

func TestResponseBuffer_WriteHeaderNow_SetsWritten(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	buf := newResponseBuffer(c.Writer, 1024)
	require.False(t, buf.Written())
	buf.WriteHeaderNow()
	require.True(t, buf.Written())
	require.Equal(t, http.StatusOK, buf.Status(), "default status after WriteHeaderNow")
}

func TestResponseBuffer_Write_SetsWritten(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	buf := newResponseBuffer(c.Writer, 1024)
	buf.Write([]byte("x"))
	require.True(t, buf.Written())
	require.Equal(t, 1, buf.Size())
}

func TestResponseBuffer_WriteString(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	buf := newResponseBuffer(c.Writer, 1024)
	n, err := buf.WriteString("hello")
	require.NoError(t, err)
	require.Equal(t, 5, n)
	require.Equal(t, "hello", buf.body.String())
}

func TestResponseBuffer_GinRedirectSentinel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	buf := newResponseBuffer(c.Writer, 1024)
	buf.WriteHeader(-1) // gin's c.Status(-1)
	require.Equal(t, 0, buf.Status(), "-1 must not latch")
	buf.WriteHeader(301) // real redirect status
	require.Equal(t, 301, buf.Status())
}

func TestResponseBuffer_CommitDefaultsTo200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	buf := newResponseBuffer(c.Writer, 1024)
	buf.body.WriteString("ok")
	buf.commitBuffer()
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "ok", rec.Body.String())
}

func TestResponseBuffer_CommitCopiesHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	buf := newResponseBuffer(c.Writer, 1024)
	buf.Header().Set("X-Custom", "value")
	buf.WriteHeader(201)
	buf.body.WriteString("created")
	buf.commitBuffer()
	require.Equal(t, 201, rec.Code)
	require.Equal(t, "value", rec.Header().Get("X-Custom"))
	require.Equal(t, "created", rec.Body.String())
}

// --- snapshot helpers ---

func TestCopyAuthSnapshot_Nil(t *testing.T) {
	require.Nil(t, copyAuthSnapshot(nil))
}

func TestCopyAuthSnapshot_DeepCopy(t *testing.T) {
	orig := &sourceauth.AuthorizationResponse{
		ContractVersion: 1,
		Documents: []sourceauth.AuthorizedDocument{
			{TenantID: "t1", KnowledgeBaseID: "kb1", KnowledgeID: "k1", Revision: strings.Repeat("a", 64)},
		},
	}
	cp := copyAuthSnapshot(orig)
	require.True(t, snapshotsMatch(orig, cp))
	// Mutate original — copy must be unaffected
	orig.Documents[0].Revision = strings.Repeat("z", 64)
	require.False(t, snapshotsMatch(orig, cp))
}

func TestSnapshotsMatch_Identical(t *testing.T) {
	a := &sourceauth.AuthorizationResponse{
		ContractVersion: 1,
		Documents: []sourceauth.AuthorizedDocument{
			{TenantID: "t", KnowledgeBaseID: "kb", KnowledgeID: "k", Revision: strings.Repeat("a", 64)},
		},
	}
	require.True(t, snapshotsMatch(a, a))
}

func TestSnapshotsMatch_DifferentRevision(t *testing.T) {
	a := &sourceauth.AuthorizationResponse{
		ContractVersion: 1,
		Documents: []sourceauth.AuthorizedDocument{
			{TenantID: "t", KnowledgeBaseID: "kb", KnowledgeID: "k", Revision: strings.Repeat("a", 64)},
		},
	}
	b := &sourceauth.AuthorizationResponse{
		ContractVersion: 1,
		Documents: []sourceauth.AuthorizedDocument{
			{TenantID: "t", KnowledgeBaseID: "kb", KnowledgeID: "k", Revision: strings.Repeat("b", 64)},
		},
	}
	require.False(t, snapshotsMatch(a, b))
}

func TestSnapshotsMatch_DifferentLength(t *testing.T) {
	a := &sourceauth.AuthorizationResponse{
		ContractVersion: 1,
		Documents: []sourceauth.AuthorizedDocument{
			{TenantID: "t", KnowledgeBaseID: "kb", KnowledgeID: "k", Revision: strings.Repeat("a", 64)},
		},
	}
	b := &sourceauth.AuthorizationResponse{ContractVersion: 1, Documents: []sourceauth.AuthorizedDocument{}}
	require.False(t, snapshotsMatch(a, b))
}

func TestSnapshotsMatch_NilEither(t *testing.T) {
	a := &sourceauth.AuthorizationResponse{ContractVersion: 1, Documents: []sourceauth.AuthorizedDocument{}}
	require.False(t, snapshotsMatch(nil, a))
	require.False(t, snapshotsMatch(a, nil))
}

// --- unit tests ---

func TestIsEmbeddedSession(t *testing.T) {
	require.False(t, isEmbeddedSession(context.Background()))
	ctx := context.WithValue(context.Background(), types.EmbeddedSessionContextKey, true)
	require.True(t, isEmbeddedSession(ctx))
	ctx = context.WithValue(context.Background(), types.EmbeddedSessionContextKey, false)
	require.False(t, isEmbeddedSession(ctx))
}

func TestIsRedirectStatus(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		require.True(t, isRedirectStatus(code), "%d", code)
	}
	for _, code := range []int{200, 201, 404, 500} {
		require.False(t, isRedirectStatus(code), "%d", code)
	}
}

func TestValidateSourceAuthResult(t *testing.T) {
	docs := []sourceauth.Document{
		{TenantID: "t", KnowledgeBaseID: "kb", KnowledgeID: "k"},
	}
	// Nil error + nil snapshot → false
	require.False(t, validateSourceAuthResult(sourceauth.AuthorizationResult{}, docs))
	// Error present → false
	require.False(t, validateSourceAuthResult(sourceauth.AuthorizationResult{Err: errors.New("x")}, docs))
	// Empty documents → false
	require.False(t, validateSourceAuthResult(sourceauth.AuthorizationResult{
		Snapshot: &sourceauth.AuthorizationResponse{ContractVersion: 1, Documents: []sourceauth.AuthorizedDocument{}},
	}, docs))
	// Wrong contract version → false
	require.False(t, validateSourceAuthResult(sourceauth.AuthorizationResult{
		Snapshot: &sourceauth.AuthorizationResponse{ContractVersion: 2, Documents: []sourceauth.AuthorizedDocument{{
			TenantID: "t", KnowledgeBaseID: "kb", KnowledgeID: "k", Revision: strings.Repeat("a", 64),
		}}},
	}, docs))
	// Wrong triple → false
	require.False(t, validateSourceAuthResult(sourceauth.AuthorizationResult{
		Snapshot: &sourceauth.AuthorizationResponse{ContractVersion: 1, Documents: []sourceauth.AuthorizedDocument{{
			TenantID: "WRONG", KnowledgeBaseID: "kb", KnowledgeID: "k", Revision: strings.Repeat("a", 64),
		}}},
	}, docs))
	// Revision not 64 hex → false
	require.False(t, validateSourceAuthResult(sourceauth.AuthorizationResult{
		Snapshot: &sourceauth.AuthorizationResponse{ContractVersion: 1, Documents: []sourceauth.AuthorizedDocument{{
			TenantID: "t", KnowledgeBaseID: "kb", KnowledgeID: "k", Revision: "tooshort",
		}}},
	}, docs))
	// Valid → true
	require.True(t, validateSourceAuthResult(sourceauth.AuthorizationResult{
		Snapshot: &sourceauth.AuthorizationResponse{ContractVersion: 1, Documents: []sourceauth.AuthorizedDocument{{
			TenantID: "t", KnowledgeBaseID: "kb", KnowledgeID: "k", Revision: strings.Repeat("a", 64),
		}}},
	}, docs))
}

func TestFmtUint64(t *testing.T) {
	require.Equal(t, "0", fmtUint64(0))
	require.Equal(t, "1", fmtUint64(1))
	require.Equal(t, "42", fmtUint64(42))
	require.Equal(t, "18446744073709551615", fmtUint64(^uint64(0)))
}

// --- require404Deny asserts the fixed deny contract ---
func require404Deny(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusNotFound, rec.Code, "must be 404")
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, sourceAccessFixedDenyBodyJSON, rec.Body.String(), "deny body must be the fixed JSON")
}
