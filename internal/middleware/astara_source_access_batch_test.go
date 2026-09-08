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

// --- batch authorizers ---

// batchAuthorizer controls authorization for batch tests. It returns a
// snapshot with one AuthorizedDocument per input Document.
type batchAuthorizer struct {
	allow       bool
	revokeAfter bool
	calls       int
	// revisions maps document index → revision. If nil, uses default
	// (all "a"s). When the map has fewer entries than docs, remaining
	// docs get the default.
	revisions map[int]string
}

func (t *batchAuthorizer) authorize(_ context.Context, _, _, _ string, docs []sourceauth.Document) sourceauth.AuthorizationResult {
	t.calls++
	if t.revokeAfter && t.calls >= 2 {
		return sourceauth.AuthorizationResult{Err: errors.New("revoked")}
	}
	if !t.allow {
		return sourceauth.AuthorizationResult{Err: errors.New("denied")}
	}
	authorizedDocs := make([]sourceauth.AuthorizedDocument, len(docs))
	for i, doc := range docs {
		rev := strings.Repeat("a", 64)
		if t.revisions != nil {
			if r, ok := t.revisions[i]; ok {
				rev = r
			}
		}
		authorizedDocs[i] = sourceauth.AuthorizedDocument{
			TenantID:        doc.TenantID,
			KnowledgeBaseID: doc.KnowledgeBaseID,
			KnowledgeID:     doc.KnowledgeID,
			Revision:        rev,
		}
	}
	return sourceauth.AuthorizationResult{
		Snapshot: &sourceauth.AuthorizationResponse{
			ContractVersion: 1,
			Documents:       authorizedDocs,
		},
	}
}

// batchDriftAuthorizer returns a stable snapshot on the first call and a
// different revision on the second call for one specific document index.
type batchDriftAuthorizer struct {
	calls    int
	driftIdx int
}

func (d *batchDriftAuthorizer) authorize(_ context.Context, _, _, _ string, docs []sourceauth.Document) sourceauth.AuthorizationResult {
	d.calls++
	authorizedDocs := make([]sourceauth.AuthorizedDocument, len(docs))
	for i, doc := range docs {
		rev := strings.Repeat("a", 64)
		if d.calls >= 2 && i == d.driftIdx {
			rev = strings.Repeat("b", 64)
		}
		authorizedDocs[i] = sourceauth.AuthorizedDocument{
			TenantID:        doc.TenantID,
			KnowledgeBaseID: doc.KnowledgeBaseID,
			KnowledgeID:     doc.KnowledgeID,
			Revision:        rev,
		}
	}
	return sourceauth.AuthorizationResult{
		Snapshot: &sourceauth.AuthorizationResponse{
			ContractVersion: 1,
			Documents:       authorizedDocs,
		},
	}
}

// --- batch helpers ---

// serveBatchGuard creates a full engine with SourceBatchReadGuard, fires a
// request with repeated ?ids params, and returns the recorder.
func serveBatchGuard(
	t *testing.T,
	kl KnowledgeLookup,
	auth Authorizer,
	handlerFn func(*gin.Context),
	ids []string, // explicit ID list — built into ?ids=a&ids=b form
) *httptest.ResponseRecorder {
	t.Helper()
	engine := gin.New()
	engine.GET("/knowledge/batch", SourceBatchReadGuard(kl, auth), handlerFn)
	url := "/knowledge/batch"
	for i, id := range ids {
		if i == 0 {
			url += "?ids=" + id
		} else {
			url += "&ids=" + id
		}
	}
	req := httptest.NewRequest("GET", url, nil)
	req = req.WithContext(batchEmbeddedContext(req))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func batchEmbeddedContext(req *http.Request) context.Context {
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

func batchKnowledge(id string, tenantID uint64, kbID string) *types.Knowledge {
	return &types.Knowledge{ID: id, TenantID: tenantID, KnowledgeBaseID: kbID}
}

func TestSourceBatchRejectsOmittedAuthorizationDocument(t *testing.T) {
	for _, omitAt := range []int{1, 2} {
		t.Run(fmt.Sprint(omitAt), func(t *testing.T) {
			lookup := &stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{
				"k-1": batchKnowledge("k-1", 100, "kb-1"),
				"k-2": batchKnowledge("k-2", 200, "kb-2"),
			}}
			base := &batchAuthorizer{allow: true}
			calls := 0
			auth := func(ctx context.Context, user, workspace, operation string, docs []sourceauth.Document) sourceauth.AuthorizationResult {
				calls++
				result := base.authorize(ctx, user, workspace, operation, docs)
				if calls == omitAt {
					result.Snapshot.Documents = result.Snapshot.Documents[:1]
				}
				return result
			}
			rec := serveBatchGuard(t, lookup, auth, defaultHandler("protected-batch"), []string{"k-1", "k-2"})
			require.Equal(t, http.StatusNotFound, rec.Code)
			require.NotContains(t, rec.Body.String(), "protected-batch")
		})
	}
}

// --- tests: non-embedded bypass ---

func TestSourceBatchReadGuard_NonEmbedded_PassesThrough(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{
			"k-1": batchKnowledge("k-1", 100, "kb-1"),
		},
	}
	auth := &batchAuthorizer{allow: true}
	engine := gin.New()
	engine.GET("/knowledge/batch", SourceBatchReadGuard(kl, auth.authorize),
		func(c *gin.Context) { c.String(200, "native-ok") })
	req := httptest.NewRequest("GET", "/knowledge/batch?ids=k-1", nil)
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

// --- tests: fail-closed paths ---

func TestSourceBatchReadGuard_MissingDependencies_Fixed404(t *testing.T) {
	rec := serveBatchGuard(t, nil, nil, defaultHandler("secret"), []string{"k-1"})
	require404Deny(t, rec)
}

func TestSourceBatchReadGuard_MissingUser_Fixed404(t *testing.T) {
	engine := gin.New()
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	engine.GET("/knowledge/batch", SourceBatchReadGuard(kl, auth.authorize), defaultHandler("secret"))
	req := httptest.NewRequest("GET", "/knowledge/batch?ids=k-1", nil)
	ctx := context.WithValue(req.Context(), types.EmbeddedSessionContextKey, true)
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, validTenant())
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require404Deny(t, rec)
}

func TestSourceBatchReadGuard_MissingTenant_Fixed404(t *testing.T) {
	engine := gin.New()
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	engine.GET("/knowledge/batch", SourceBatchReadGuard(kl, auth.authorize), defaultHandler("secret"))
	req := httptest.NewRequest("GET", "/knowledge/batch?ids=k-1", nil)
	ctx := context.WithValue(req.Context(), types.EmbeddedSessionContextKey, true)
	ctx = context.WithValue(ctx, types.UserContextKey, validUser())
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require404Deny(t, rec)
}

func TestSourceBatchReadGuard_NativeUser_NoMapping_Fixed404(t *testing.T) {
	engine := gin.New()
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	engine.GET("/knowledge/batch", SourceBatchReadGuard(kl, auth.authorize), defaultHandler("secret"))
	native := &types.User{ID: "native", IsActive: true}
	req := httptest.NewRequest("GET", "/knowledge/batch?ids=k-1", nil)
	ctx := context.WithValue(req.Context(), types.EmbeddedSessionContextKey, true)
	ctx = context.WithValue(ctx, types.UserContextKey, native)
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, validTenant())
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require404Deny(t, rec)
}

func TestSourceBatchReadGuard_NoIDs_Fixed404(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler("secret"), nil)
	require404Deny(t, rec)
	require.Equal(t, 0, auth.calls, "authorizer must not be called for empty IDs")
}

func TestSourceBatchReadGuard_EmptyID_Fixed404(t *testing.T) {
	// An ID that trims to empty should be rejected.
	engine := gin.New()
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	engine.GET("/knowledge/batch", SourceBatchReadGuard(kl, auth.authorize), defaultHandler("secret"))
	req := httptest.NewRequest("GET", "/knowledge/batch?ids=%20", nil)
	req = req.WithContext(batchEmbeddedContext(req))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require404Deny(t, rec)
}

func TestSourceBatchReadGuard_BatchLookupError_Fixed404(t *testing.T) {
	kl := &stubKnowledgeLookup{err: errors.New("db down")}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler("secret"), []string{"k-1"})
	require404Deny(t, rec)
}

func TestSourceBatchReadGuard_IDNotFound_Fixed404(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler("secret"), []string{"k-1", "k-missing"})
	require404Deny(t, rec)
	require.Equal(t, 0, auth.calls, "authorizer must not be called when resolution is incomplete")
}

func TestSourceBatchReadGuard_AuthDenied_Fixed404(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{
			"k-1": batchKnowledge("k-1", 100, "kb-1"),
			"k-2": batchKnowledge("k-2", 100, "kb-1"),
		},
	}
	auth := &batchAuthorizer{allow: false}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler("SECRET"), []string{"k-1", "k-2"})
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "SECRET")
}

// --- tests: success path ---

func TestSourceBatchReadGuard_Success_FlushesResponse(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{
			"k-1": batchKnowledge("k-1", 100, "kb-1"),
			"k-2": batchKnowledge("k-2", 200, "kb-2"),
		},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler(`{"data":"batch-ok"}`), []string{"k-1", "k-2"})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"data":"batch-ok"`)
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	require.Empty(t, rec.Header().Get("ETag"))
	require.Equal(t, 2, auth.calls, "authorizer must be called twice (pre + post)")
}

func TestSourceBatchReadGuard_AuthorizerReceivesAllDocs(t *testing.T) {
	var gotDocs []sourceauth.Document
	var gotSubject, gotWorkspace, gotOp string
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{
			"k-1": batchKnowledge("k-1", 100, "kb-1"),
			"k-2": batchKnowledge("k-2", 200, "kb-2"),
		},
	}
	rec := serveBatchGuard(t, kl,
		func(_ context.Context, uid, ws, op string, docs []sourceauth.Document) sourceauth.AuthorizationResult {
			gotSubject = uid
			gotWorkspace = ws
			gotOp = op
			gotDocs = docs
			authorizedDocs := make([]sourceauth.AuthorizedDocument, len(docs))
			for i, doc := range docs {
				authorizedDocs[i] = sourceauth.AuthorizedDocument{
					TenantID: doc.TenantID, KnowledgeBaseID: doc.KnowledgeBaseID,
					KnowledgeID: doc.KnowledgeID, Revision: strings.Repeat("a", 64),
				}
			}
			return sourceauth.AuthorizationResult{
				Snapshot: &sourceauth.AuthorizationResponse{ContractVersion: 1, Documents: authorizedDocs},
			}
		},
		defaultHandler("ok"), []string{"k-1", "k-2"})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, testSubject, gotSubject)
	require.Equal(t, testWorkspace, gotWorkspace)
	require.Equal(t, "read", gotOp)
	require.Len(t, gotDocs, 2)
}

func TestSourceBatchReadGuard_SingleDocument_Success(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler("ok"), []string{"k-1"})
	require.Equal(t, http.StatusOK, rec.Code)
}

// --- tests: duplicate rejection ---

func TestSourceBatchReadGuard_DuplicateIDs_Fixed404(t *testing.T) {
	// Duplicate IDs in the request must be rejected, not deduped.
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler("secret"), []string{"k-1", "k-1"})
	require404Deny(t, rec)
	require.Equal(t, 0, auth.calls, "authorizer must not be called for duplicates")
}

func TestSourceBatchReadGuard_DuplicateAcrossThree_Fixed404(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{
			"k-1": batchKnowledge("k-1", 100, "kb-1"),
			"k-2": batchKnowledge("k-2", 100, "kb-1"),
		},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler("secret"), []string{"k-1", "k-2", "k-1"})
	require404Deny(t, rec)
}

// --- tests: oversize ---

func TestSourceBatchReadGuard_ExceedsMaxIDs_Fixed404(t *testing.T) {
	// Build 1001 IDs.
	ids := make([]string, 1001)
	for i := range ids {
		ids[i] = fmt.Sprintf("k-%d", i)
	}
	kl := &stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{}}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler("secret"), ids)
	require404Deny(t, rec)
	require.Equal(t, 0, auth.calls, "authorizer must not be called for oversize batch")
}

// --- tests: post-handler deny paths ---

func TestSourceBatchReadGuard_RevokedAfterHandler_Fixed404(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{
			"k-1": batchKnowledge("k-1", 100, "kb-1"),
			"k-2": batchKnowledge("k-2", 100, "kb-1"),
		},
	}
	auth := &batchAuthorizer{allow: true, revokeAfter: true}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler("secret"), []string{"k-1", "k-2"})
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "secret")
}

func TestSourceBatchReadGuard_RevisionDrift_SecondDoc_Fixed404(t *testing.T) {
	// Drift on the second document (index 1) between pre and post calls.
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{
			"k-1": batchKnowledge("k-1", 100, "kb-1"),
			"k-2": batchKnowledge("k-2", 100, "kb-1"),
		},
	}
	auth := &batchDriftAuthorizer{driftIdx: 1}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler("secret"), []string{"k-1", "k-2"})
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "secret")
	require.Equal(t, 2, auth.calls, "authorizer must be called twice (pre + post)")
}

func TestSourceBatchReadGuard_RevisionDrift_FirstDoc_Fixed404(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{
			"k-1": batchKnowledge("k-1", 100, "kb-1"),
			"k-2": batchKnowledge("k-2", 100, "kb-1"),
		},
	}
	auth := &batchDriftAuthorizer{driftIdx: 0}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler("secret"), []string{"k-1", "k-2"})
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "secret")
}

func TestSourceBatchReadGuard_PostHandler_DocMappingDrift_Tenant_Fixed404(t *testing.T) {
	// After the handler runs, the knowledge document's TenantID changes
	// (simulated by mutating the lookup's map entry). The guard must deny.
	original := batchKnowledge("k-1", 100, "kb-1")
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": original},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, func(c *gin.Context) {
		original.TenantID = 999
		c.String(200, "must-not-escape")
	}, []string{"k-1"})
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "must-not-escape")
}

func TestSourceBatchReadGuard_PostHandler_DocMappingDrift_KB_Fixed404(t *testing.T) {
	original := batchKnowledge("k-1", 100, "kb-1")
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": original},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, func(c *gin.Context) {
		original.KnowledgeBaseID = "foreign-kb"
		c.String(200, "must-not-escape")
	}, []string{"k-1"})
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "must-not-escape")
}

func TestSourceBatchReadGuard_PostHandler_DocDisappears_Fixed404(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, func(c *gin.Context) {
		delete(kl.knowledge, "k-1")
		c.String(200, "must-not-escape")
	}, []string{"k-1"})
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "must-not-escape")
}

func TestSourceBatchReadGuard_PostHandler_LookupError_Fixed404(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	lookupErrOnSecond := false
	failingKL := &failingSecondLookup{
		inner:      kl,
		shouldFail: func() bool { return lookupErrOnSecond },
	}
	engine := gin.New()
	engine.GET("/knowledge/batch", SourceBatchReadGuard(failingKL, auth.authorize), func(c *gin.Context) {
		lookupErrOnSecond = true
		c.String(200, "secret")
	})
	req := httptest.NewRequest("GET", "/knowledge/batch?ids=k-1", nil)
	req = req.WithContext(batchEmbeddedContext(req))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	require404Deny(t, rec)
}

// failingSecondLookup wraps a stubKnowledgeLookup and returns an error
// when shouldFail() returns true.
type failingSecondLookup struct {
	inner      *stubKnowledgeLookup
	shouldFail func() bool
}

func (f *failingSecondLookup) GetKnowledgeByIDOnly(ctx context.Context, id string) (*types.Knowledge, error) {
	if f.shouldFail() {
		return nil, errors.New("simulated post-handler failure")
	}
	return f.inner.GetKnowledgeByIDOnly(ctx, id)
}

// --- tests: streaming refusal ---

func TestSourceBatchReadGuard_FlushViolation_Fixed404(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, func(c *gin.Context) {
		c.Writer.Header().Set("X-Stream", "yes")
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.WriteString("partial")
		c.Writer.Flush()
	}, []string{"k-1"})
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "partial")
	require.Empty(t, rec.Header().Get("X-Stream"))
}

func TestSourceBatchReadGuard_HijackViolation_Fixed404(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, func(c *gin.Context) {
		c.Writer.Header().Set("X-Upgrade", "yes")
		_, _, err := c.Writer.Hijack()
		if err == nil {
			t.Error("hijack should return error")
		}
	}, []string{"k-1"})
	require404Deny(t, rec)
	require.Empty(t, rec.Header().Get("X-Upgrade"))
}

// --- tests: redirect/304 rejection ---

func TestSourceBatchReadGuard_RedirectResponse_Fixed404(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			kl := &stubKnowledgeLookup{
				knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
			}
			auth := &batchAuthorizer{allow: true}
			rec := serveBatchGuard(t, kl, auth.authorize,
				func(c *gin.Context) { c.Redirect(code, "https://evil.example.com") },
				[]string{"k-1"})
			require404Deny(t, rec)
			require.NotContains(t, rec.Body.String(), "evil.example.com")
		})
	}
}

func TestSourceBatchReadGuard_304NotModified_Fixed404(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, func(c *gin.Context) {
		c.Status(http.StatusNotModified)
	}, []string{"k-1"})
	require404Deny(t, rec)
}

// --- tests: oversize response ---

func TestSourceBatchReadGuard_OversizeResponse_Fixed404(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, func(c *gin.Context) {
		c.Data(http.StatusOK, "application/octet-stream",
			[]byte(strings.Repeat("x", sourceAccessBufferLimit+1)))
	}, []string{"k-1"})
	require404Deny(t, rec)
}

// --- tests: cache header security ---

func TestSourceBatchReadGuard_DisablesCaching(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true}
	rec := serveBatchGuard(t, kl, auth.authorize, func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		c.Header("ETag", "old-tag")
		c.Header("Last-Modified", "yesterday")
		c.String(200, "protected")
	}, []string{"k-1"})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	require.Empty(t, rec.Header().Get("ETag"))
	require.Empty(t, rec.Header().Get("Last-Modified"))
}

// --- tests: deny body is fixed JSON ---

func TestSourceBatchReadGuard_DenyBody_IsFixedJSON(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: false}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler("irrelevant"), []string{"k-1"})
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, sourceAccessFixedDenyBodyJSON, rec.Body.String())
}

// --- tests: cookie and content leakage ---

func TestSourceBatchReadGuard_NoCookieLeakage_AuthDenied(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: false}
	rec := serveBatchGuard(t, kl, auth.authorize, func(c *gin.Context) {
		c.SetCookie("session", "secret-token", 3600, "/", "", false, true)
		c.JSON(http.StatusOK, gin.H{"data": "leaked"})
	}, []string{"k-1"})
	require404Deny(t, rec)
	for _, v := range rec.Header().Values("Set-Cookie") {
		require.NotContains(t, v, "secret-token")
	}
}

func TestSourceBatchReadGuard_NoContentLeakage_Denied(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: false}
	rec := serveBatchGuard(t, kl, auth.authorize, defaultHandler("SECRET"), []string{"k-1"})
	require404Deny(t, rec)
	require.NotContains(t, rec.Body.String(), "SECRET")
}

// --- tests: does not expose underlying writer ---

func TestSourceBatchReadGuard_DoesNotExposeUnderlyingWriter(t *testing.T) {
	kl := &stubKnowledgeLookup{
		knowledge: map[string]*types.Knowledge{"k-1": batchKnowledge("k-1", 100, "kb-1")},
	}
	auth := &batchAuthorizer{allow: true, revokeAfter: true}
	rec := serveBatchGuard(t, kl, auth.authorize, func(c *gin.Context) {
		_, ginUnwrap := c.Writer.(interface{ Unwrap() gin.ResponseWriter })
		_, httpUnwrap := c.Writer.(interface{ Unwrap() http.ResponseWriter })
		require.False(t, ginUnwrap)
		require.False(t, httpUnwrap)
		c.String(200, "secret body")
	}, []string{"k-1"})
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NotContains(t, rec.Body.String(), "secret body")
}

// --- tests: validateBatchIDs unit ---

func TestValidateBatchIDs(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		_, ok := validateBatchIDs(nil)
		require.False(t, ok)
	})
	t.Run("empty", func(t *testing.T) {
		_, ok := validateBatchIDs([]string{})
		require.False(t, ok)
	})
	t.Run("blank_only", func(t *testing.T) {
		_, ok := validateBatchIDs([]string{"", " ", ""})
		require.False(t, ok)
	})
	t.Run("single", func(t *testing.T) {
		ids, ok := validateBatchIDs([]string{"a"})
		require.True(t, ok)
		require.Equal(t, []string{"a"}, ids)
	})
	t.Run("trim", func(t *testing.T) {
		ids, ok := validateBatchIDs([]string{" a ", "  b  "})
		require.False(t, ok)
		require.Nil(t, ids)
	})
	t.Run("duplicate_rejected", func(t *testing.T) {
		_, ok := validateBatchIDs([]string{"a", "b", "a"})
		require.False(t, ok)
	})
	t.Run("oversize", func(t *testing.T) {
		ids := make([]string, 1001)
		for i := range ids {
			ids[i] = fmt.Sprintf("k-%d", i)
		}
		_, ok := validateBatchIDs(ids)
		require.False(t, ok)
	})
	t.Run("overlong_id", func(t *testing.T) {
		_, ok := validateBatchIDs([]string{strings.Repeat("a", 257)})
		require.False(t, ok)
	})
	t.Run("exactly_max", func(t *testing.T) {
		ids := make([]string, 1000)
		for i := range ids {
			ids[i] = fmt.Sprintf("k-%d", i)
		}
		result, ok := validateBatchIDs(ids)
		require.True(t, ok)
		require.Len(t, result, 1000)
	})
	t.Run("blank_id_rejected", func(t *testing.T) {
		_, ok := validateBatchIDs([]string{"a", "", "b"})
		require.False(t, ok)
	})
}
