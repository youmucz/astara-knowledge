// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package middleware

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/sourceauth"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

const (
	// sourceAccessBufferLimit is the maximum response body the guard will
	// buffer before deciding to flush or deny. Responses that exceed this
	// limit during handler execution are replaced with a fixed 404 body.
	sourceAccessBufferLimit = 2 * 1024 * 1024 // 2 MiB

	// sourceAccessFixedDenyBodyJSON is the canonical deny payload.
	sourceAccessFixedDenyBodyJSON = `{"success":false,"error":{"code":"NOT_FOUND","message":"Resource not found"}}`

	// sourceBatchMinIDs is the minimum number of IDs required for
	// a batch source-authorization request.
	sourceBatchMinIDs = 1

	// sourceBatchMaxIDs is the maximum number of IDs the batch
	// guard accepts per request. Matches sourceauth.maxDocuments.
	sourceBatchMaxIDs = 1000
)

// Authorizer is the injectable authorization surface the guard calls
// before and after the handler. Production wiring passes
// sourceauth.Client.Authorize; tests pass a controllable fake.
//
// The guard calls Authorize twice per embedded-session request:
//   - pre-handler: to decide whether to let the handler run at all
//   - post-handler: to re-check that authorization was not revoked
//     while the handler was executing
//
// Both calls must succeed and return identical document/revision
// triples for the buffered response to be flushed.
type Authorizer func(ctx context.Context, userID, workspaceID, operation string, documents []sourceauth.Document) sourceauth.AuthorizationResult

// SourceAccessGuard returns a gin.HandlerFunc that enforces document-level
// source authorization on embedded-session requests. Non-embedded
// (native) requests pass through unchanged.
//
// Parameters:
//   - knowledgeParam: the gin URL param holding the knowledge ID
//     (e.g. "knowledge_id", "id"). The parent route declares this.
//   - knowledgeLookup: resolves the knowledge document by ID.
//   - authorizer: the injectable authorization client.
//
// Guard contract for embedded sessions:
//  1. Read original User/Tenant from context (before any KB-guard
//     effective-tenant rewrite).
//  2. Resolve the knowledge document via the explicit param.
//  3. Fresh authorize with read-only "read" operation; deep-copy
//     the pre-handler snapshot.
//  4. Buffer the handler's response (≤ 2 MiB); refuse streaming
//     (Flush/Hijack set a violation flag → post-handler 404).
//  5. Fresh authorize again after handler returns.
//  6. Flush only when both authorize calls succeed, snapshots match
//     exactly (all triples + revision), and the response is not a
//     redirect. Any mismatch, revocation, error, oversize, redirect,
//     or streaming violation yields a fixed 404 body.
//
// All denials write a fixed {"success":false,"error":{"code":"NOT_FOUND",
// "message":"Resource not found"}} JSON body directly — c.Error + Abort
// is never used because the ErrorHandler middleware may not render after
// Abort in all configurations.
//
// The parent route owns integration (which routes get this guard).
// This file implements only the middleware and tests.
func SourceAccessGuard(
	knowledgeParam string,
	knowledgeLookup KnowledgeLookup,
	authorizer Authorizer,
) gin.HandlerFunc {
	return sourceAccessGuardResolved(func(c *gin.Context) string { return c.Param(knowledgeParam) }, knowledgeLookup, authorizer)
}

// SourceChunkReadGuard resolves the parent document without rewriting route
// params: a chunk ID must never be treated as a document ID.
func SourceChunkReadGuard(param string, chunks ChunkLookup, knowledge KnowledgeLookup, authorizer Authorizer) gin.HandlerFunc {
	return sourceAccessGuardResolved(func(c *gin.Context) string {
		if chunks == nil || knowledge == nil {
			return ""
		}
		id := c.Param(param)
		if id == "" || len(id) > 256 || strings.TrimSpace(id) != id {
			return ""
		}
		chunk, err := chunks.GetChunkByIDOnly(c.Request.Context(), id)
		if err != nil || chunk == nil || chunk.ID != id || chunk.KnowledgeID == "" {
			return ""
		}
		doc, err := knowledge.GetKnowledgeByIDOnly(c.Request.Context(), chunk.KnowledgeID)
		if err != nil || doc == nil || doc.ID != chunk.KnowledgeID || doc.KnowledgeBaseID != chunk.KnowledgeBaseID {
			return ""
		}
		return doc.ID
	}, knowledge, authorizer)
}

// SourceBatchReadGuard returns a gin.HandlerFunc that enforces document-level
// source authorization on embedded-session batch read requests. It parses the
// actual GetKnowledgeBatchRequest query contract: repeated ?ids=<id> parameters
// only (gin ShouldBindQuery form:"ids" semantics), no comma-separated fallback.
//
// Guard contract for embedded sessions:
//  1. Parse the ID list from repeated "ids" query parameters.
//  2. Trim and validate 1..1000 IDs; reject the request if any duplicates
//     exist (no silent dedupe).
//  3. Resolve each knowledge document via KnowledgeLookup.GetKnowledgeByIDOnly;
//     verify every ID resolved and the returned ID matches.
//  4. Build authorization documents for every resolved triple, ordered by
//     the input ID list.
//  5. Single pre-handler authorize call with all documents; deep-copy
//     the pre-handler snapshot.
//  6. Buffer the handler's response (≤ 2 MiB); refuse streaming.
//  7. Re-resolve every document with fresh lookups and re-check provider
//     mappings (tenant_id, kb_id).
//  8. Single post-handler authorize call with the same document list.
//  9. Flush only when both authorize calls succeed, all snapshots match
//     exactly (every document triple + revision), and the response is not
//     a redirect. Any mismatch, revocation, error, oversize, redirect,
//     or streaming violation yields a fixed 404.
//
// Non-embedded (native) requests pass through unchanged.
//
// Parameters:
//   - lookup: resolves knowledge documents by ID (same interface as
//     SourceAccessGuard).
//   - authorizer: the injectable authorization client.
func SourceBatchReadGuard(
	lookup KnowledgeLookup,
	authorizer Authorizer,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		// --- Non-embedded requests pass through unchanged ---
		if !isEmbeddedSession(c.Request.Context()) {
			c.Next()
			return
		}

		ctx := c.Request.Context()

		// --- Fail closed when dependencies are not configured ---
		if lookup == nil || authorizer == nil {
			logger.Warnf(ctx, "[source_access] batch guard misconfigured: missing dependencies")
			writeSourceDeny404(c)
			return
		}

		// --- Read ORIGINAL user/tenant identity ---
		user, userOK := ctx.Value(types.UserContextKey).(*types.User)
		tenant, tenantOK := types.TenantInfoFromContext(ctx)
		if !userOK || !tenantOK || user == nil || tenant == nil {
			writeSourceDeny404(c)
			return
		}

		subject, workspace, ok := sourcePrincipalIDs(user, tenant)
		if !ok {
			writeSourceDeny404(c)
			return
		}

		// --- Parse the GetKnowledgeBatchRequest query contract ---
		// ShouldBindQuery with form:"ids" on []string binds repeated
		// params (?ids=a&ids=b → ["a","b"]). No comma splitting.
		rawIDs := c.QueryArray("ids")
		ids, ok := validateBatchIDs(rawIDs)
		if !ok {
			writeSourceDeny404(c)
			return
		}

		// --- Resolve each knowledge document individually ---
		// Use the input ID order for deterministic doc ordering.
		// Snapshot field VALUES (not pointers) so post-handler
		// comparison catches in-place mutation of the knowledge struct.
		type resolvedDoc struct {
			snapTenantID string
			snapKBID     string
			doc          sourceauth.Document
		}
		resolved := make([]resolvedDoc, 0, len(ids))
		for _, id := range ids {
			k, err := lookup.GetKnowledgeByIDOnly(ctx, id)
			if err != nil {
				logger.Error(ctx, "[source_access] batch knowledge lookup failed")
				writeSourceDeny404(c)
				return
			}
			if k == nil || k.ID != id {
				writeSourceDeny404(c)
				return
			}
			resolved = append(resolved, resolvedDoc{
				snapTenantID: fmtUint64(k.TenantID),
				snapKBID:     k.KnowledgeBaseID,
				doc: sourceauth.Document{
					TenantID:        fmtUint64(k.TenantID),
					KnowledgeBaseID: k.KnowledgeBaseID,
					KnowledgeID:     k.ID,
				},
			})
		}

		// Build the document list in input ID order.
		docs := make([]sourceauth.Document, len(resolved))
		for i, r := range resolved {
			docs[i] = r.doc
		}

		// --- Pre-handler authorization (read-only, single request) ---
		preResult := authorizer(ctx, subject, workspace, "read", docs)
		if !validateSourceAuthResult(preResult, docs) {
			writeSourceDeny404(c)
			return
		}

		// Deep-copy the pre-handler snapshot so mutation during handler
		// execution cannot affect the post-handler comparison.
		preSnapshot := copyAuthSnapshot(preResult.Snapshot)

		// --- Buffer handler response (≤ 2 MiB) ---
		originalWriter := c.Writer
		buf := newResponseBuffer(originalWriter, sourceAccessBufferLimit)
		c.Writer = buf

		defer func() {
			c.Writer = originalWriter
		}()

		// Let the handler execute.
		c.Next()

		// --- Post-handler checks ---
		// 0. Streaming violation.
		if buf.streamViolation {
			writeSourceDeny404(c)
			return
		}

		// 1. Oversize check.
		if buf.oversize {
			writeSourceDeny404(c)
			return
		}

		// 2. Redirect / 304 check.
		if isRedirectStatus(buf.status) || buf.status == http.StatusNotModified {
			writeSourceDeny404(c)
			return
		}

		// --- Re-resolve every document and verify provider mappings ---
		for i, id := range ids {
			current, lookupErr := lookup.GetKnowledgeByIDOnly(ctx, id)
			if lookupErr != nil || current == nil || current.ID != id {
				writeSourceDeny404(c)
				return
			}
			if fmtUint64(current.TenantID) != resolved[i].snapTenantID ||
				current.KnowledgeBaseID != resolved[i].snapKBID {
				writeSourceDeny404(c)
				return
			}
		}

		// 3. Fresh post-handler authorization (single request, same docs).
		postResult := authorizer(ctx, subject, workspace, "read", docs)
		if !validateSourceAuthResult(postResult, docs) {
			writeSourceDeny404(c)
			return
		}

		// 4. Snapshot match — deep comparison of every document triple
		//    and revision between the pre-handler copy and the fresh
		//    post-handler result.
		if !snapshotsMatch(preSnapshot, postResult.Snapshot) {
			writeSourceDeny404(c)
			return
		}

		// Revocable responses must not become reusable cache entries.
		buf.headers.Set("Cache-Control", "private, no-store")
		buf.headers.Del("ETag")
		buf.headers.Del("Last-Modified")
		// --- All checks passed: commit buffered response ---
		buf.commitBuffer()
	}
}

func sourceAccessGuardResolved(resolveID func(*gin.Context) string, knowledgeLookup KnowledgeLookup, authorizer Authorizer) gin.HandlerFunc {
	return func(c *gin.Context) {
		// --- Non-embedded requests pass through unchanged ---
		if !isEmbeddedSession(c.Request.Context()) {
			c.Next()
			return
		}

		ctx := c.Request.Context()

		// --- Fail closed when the client is not configured ---
		if knowledgeLookup == nil || authorizer == nil {
			logger.Warnf(ctx, "[source_access] guard misconfigured: missing dependencies")
			writeSourceDeny404(c)
			return
		}

		// --- Read ORIGINAL user/tenant identity before any KB-guard
		// effective-tenant rewrite. ---
		user, userOK := ctx.Value(types.UserContextKey).(*types.User)
		tenant, tenantOK := types.TenantInfoFromContext(ctx)
		if !userOK || !tenantOK || user == nil || tenant == nil {
			writeSourceDeny404(c)
			return
		}

		subject, workspace, ok := sourcePrincipalIDs(user, tenant)
		if !ok {
			writeSourceDeny404(c)
			return
		}

		// --- Resolve knowledge document via explicit param ---
		knowledgeID := resolveID(c)
		if knowledgeID == "" || len(knowledgeID) > 256 || strings.TrimSpace(knowledgeID) != knowledgeID {
			writeSourceDeny404(c)
			return
		}

		knowledge, err := knowledgeLookup.GetKnowledgeByIDOnly(ctx, knowledgeID)
		if err != nil {
			logger.Error(ctx, "[source_access] knowledge lookup failed")
			writeSourceDeny404(c)
			return
		}
		if knowledge == nil {
			writeSourceDeny404(c)
			return
		}
		// Reject if the lookup returned a different ID than requested
		// (service-layer invariant violation).
		if knowledge.ID != knowledgeID {
			logger.Error(ctx, "[source_access] knowledge lookup identity mismatch")
			writeSourceDeny404(c)
			return
		}

		// --- Build authorization document (provider tenant/KB triple) ---
		docs := []sourceauth.Document{{
			TenantID:        fmtUint64(knowledge.TenantID),
			KnowledgeBaseID: knowledge.KnowledgeBaseID,
			KnowledgeID:     knowledge.ID,
		}}

		// --- Pre-handler authorization (read-only) ---
		preResult := authorizer(ctx, subject, workspace, "read", docs)
		if !validateSourceAuthResult(preResult, docs) {
			writeSourceDeny404(c)
			return
		}

		// Deep-copy the pre-handler snapshot so mutation during handler
		// execution cannot affect the post-handler comparison.
		preSnapshot := copyAuthSnapshot(preResult.Snapshot)

		// --- Buffer handler response (≤ 2 MiB) ---
		originalWriter := c.Writer
		buf := newResponseBuffer(originalWriter, sourceAccessBufferLimit)
		c.Writer = buf

		// Ensure the original writer is always restored, even on panic.
		defer func() {
			c.Writer = originalWriter
		}()

		// Let the handler execute.
		c.Next()

		// --- Post-handler checks ---
		// Order: streaming violation → oversize → redirect → authorize.

		// 0. Streaming violation — handler called Flush/Hijack.
		if buf.streamViolation {
			writeSourceDeny404(c)
			return
		}

		// 1. Oversize check — response exceeded buffer limit.
		if buf.oversize {
			writeSourceDeny404(c)
			return
		}

		// 2. Redirect check — embedded sessions must not receive
		//    redirects because the caller cannot revoke them.
		if isRedirectStatus(buf.status) || buf.status == http.StatusNotModified {
			writeSourceDeny404(c)
			return
		}

		// Re-resolve the route mapping as well as Plane authority: a chunk
		// may have moved to another document while its handler was running.
		if resolveID(c) != knowledgeID {
			writeSourceDeny404(c)
			return
		}
		current, lookupErr := knowledgeLookup.GetKnowledgeByIDOnly(ctx, knowledgeID)
		if lookupErr != nil || current == nil || current.ID != docs[0].KnowledgeID ||
			fmtUint64(current.TenantID) != docs[0].TenantID || current.KnowledgeBaseID != docs[0].KnowledgeBaseID {
			writeSourceDeny404(c)
			return
		}
		// 3. Fresh post-handler authorization.
		postResult := authorizer(ctx, subject, workspace, "read", docs)
		if !validateSourceAuthResult(postResult, docs) {
			writeSourceDeny404(c)
			return
		}

		// 4. Snapshot match — deep comparison of every document triple
		//    and revision between the pre-handler copy and the fresh
		//    post-handler result.
		if !snapshotsMatch(preSnapshot, postResult.Snapshot) {
			writeSourceDeny404(c)
			return
		}

		// Revocable responses must not become reusable cache entries.
		buf.headers.Set("Cache-Control", "private, no-store")
		buf.headers.Del("ETag")
		buf.headers.Del("Last-Modified")
		// --- All checks passed: commit buffered response ---
		buf.commitBuffer()
	}
}

// isEmbeddedSession reports whether ctx was authenticated through the
// Plane-hosted embedded session cookie. Returns false (fail-closed) when
// the key is absent or the value is not a bool-true.
func isEmbeddedSession(ctx context.Context) bool {
	v, ok := ctx.Value(types.EmbeddedSessionContextKey).(bool)
	return ok && v
}

// isRedirectStatus reports whether the HTTP status code is a redirect.
func isRedirectStatus(status int) bool {
	return status == http.StatusMovedPermanently ||
		status == http.StatusFound ||
		status == http.StatusSeeOther ||
		status == http.StatusTemporaryRedirect ||
		status == http.StatusPermanentRedirect
}

// hex64Regex matches exactly 64 lowercase hex characters (revision format).
var hex64Regex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// validateSourceAuthResult reports whether an AuthorizationResult
// carries a valid snapshot that satisfies the contract:
//   - Err is nil
//   - Snapshot is non-nil
//   - ContractVersion == 1
//   - Document count matches len(expectedDocs)
//   - Each document triple (tenant_id, kb_id, knowledge_id) matches
//     the corresponding expected document exactly
//   - Each revision is a 64-character lowercase hex string
func validateSourceAuthResult(r sourceauth.AuthorizationResult, expectedDocs []sourceauth.Document) bool {
	if r.Err != nil || r.Snapshot == nil {
		return false
	}
	if r.Snapshot.ContractVersion != 1 {
		return false
	}
	if len(r.Snapshot.Documents) != len(expectedDocs) {
		return false
	}
	for i, doc := range r.Snapshot.Documents {
		exp := expectedDocs[i]
		if doc.TenantID != exp.TenantID ||
			doc.KnowledgeBaseID != exp.KnowledgeBaseID ||
			doc.KnowledgeID != exp.KnowledgeID {
			return false
		}
		if !hex64Regex.MatchString(doc.Revision) {
			return false
		}
	}
	return true
}

// copyAuthSnapshot returns a deep copy of an AuthorizationResponse so
// that the pre-handler snapshot is immutable during handler execution.
func copyAuthSnapshot(src *sourceauth.AuthorizationResponse) *sourceauth.AuthorizationResponse {
	if src == nil {
		return nil
	}
	cp := &sourceauth.AuthorizationResponse{
		ContractVersion: src.ContractVersion,
	}
	if len(src.Documents) > 0 {
		cp.Documents = make([]sourceauth.AuthorizedDocument, len(src.Documents))
		copy(cp.Documents, src.Documents)
	}
	return cp
}

// snapshotsMatch compares two AuthorizationResponses document-by-document
// on every field of the triple (tenant_id, kb_id, knowledge_id) plus
// the revision. Returns false if either is nil, lengths differ, or any
// field mismatches.
func snapshotsMatch(a, b *sourceauth.AuthorizationResponse) bool {
	if a == nil || b == nil {
		return false
	}
	if len(a.Documents) != len(b.Documents) {
		return false
	}
	for i := range a.Documents {
		// AuthorizedDocument is a flat struct (no pointers/slices), so
		// direct field comparison is safe.
		if a.Documents[i].TenantID != b.Documents[i].TenantID ||
			a.Documents[i].KnowledgeBaseID != b.Documents[i].KnowledgeBaseID ||
			a.Documents[i].KnowledgeID != b.Documents[i].KnowledgeID ||
			a.Documents[i].Revision != b.Documents[i].Revision {
			return false
		}
	}
	return true
}

// writeSourceDeny404 bypasses the response buffer and writes the fixed
// deny response directly to the underlying writer. The buffer (if any)
// is discarded — its contents are never flushed. The response body
// carries no detail so attackers cannot distinguish the failure reason.
func writeSourceDeny404(c *gin.Context) {
	// Restore the underlying writer so the 404 bypasses any buffer.
	if buf, ok := c.Writer.(*responseBuffer); ok {
		c.Writer = buf.underlying
	}
	// A refusal must not retain upstream resource or cookie headers.
	for _, name := range []string{"ETag", "Last-Modified", "Content-Length", "Content-Encoding", "Content-Disposition", "Content-Range", "Location", "Set-Cookie"} {
		c.Writer.Header().Del(name)
	}
	c.Writer.Header().Set("Cache-Control", "private, no-store")
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.WriteHeader(http.StatusNotFound)
	_, _ = c.Writer.WriteString(sourceAccessFixedDenyBodyJSON)
	c.Abort()
}

// fmtUint64 formats a uint64 as a decimal string without importing strconv.
func fmtUint64(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// validateBatchIDs validates exact IDs from repeated query parameters.
// Rejects whitespace normalization, empty/overlong IDs, duplicates and >1000 IDs.
func validateBatchIDs(raw []string) ([]string, bool) {
	if len(raw) < sourceBatchMinIDs || len(raw) > sourceBatchMaxIDs {
		return nil, false
	}
	seen := make(map[string]struct{}, len(raw))
	ids := make([]string, 0, len(raw))
	for _, item := range raw {
		id := strings.TrimSpace(item)
		if id == "" || id != item || len(id) > 256 {
			return nil, false
		}
		if _, dup := seen[id]; dup {
			return nil, false
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, true
}

// responseBuffer is a gin.ResponseWriter that captures all handler
// output (headers, status code, body) into an in-memory buffer instead
// of writing to the underlying connection. This lets the guard
// post-authorize the response before deciding whether to commit it.
//
// Streaming prevention: if the handler calls Flush() or Hijack(), the
// streamViolation flag is set and the guard replaces the response with
// a fixed 404 regardless of authorization status.
type responseBuffer struct {
	underlying      gin.ResponseWriter
	status          int
	headers         http.Header
	body            bytes.Buffer
	limit           int
	oversize        bool
	streamViolation bool
	written         bool // true once any body bytes are written
}

func newResponseBuffer(w gin.ResponseWriter, limit int) *responseBuffer {
	return &responseBuffer{
		underlying: w,
		headers:    make(http.Header),
		limit:      limit,
	}
}

// Header returns the captured response headers.
func (b *responseBuffer) Header() http.Header {
	return b.headers
}

// WriteHeaderNow marks headers as committed (matches gin semantics:
// once called, the status is latched). Does NOT write to the underlying
// writer — the guard defers that until post-authorization.
func (b *responseBuffer) WriteHeaderNow() {
	if b.status <= 0 {
		b.status = http.StatusOK
	}
	b.written = true
}

// Status returns the captured HTTP status code. Defaults to 0 if
// neither WriteHeader nor WriteHeaderNow was called.
func (b *responseBuffer) Status() int {
	return b.status
}

// Size returns the number of bytes written into the body buffer.
func (b *responseBuffer) Size() int {
	return b.body.Len()
}

// Written returns true if any body bytes were written or WriteHeaderNow
// was called (matches gin's commit semantics).
func (b *responseBuffer) Written() bool {
	return b.written
}

// WriteHeader captures the HTTP status code. The sentinel -1 used by
// gin's c.Redirect (which calls c.Status(-1) before http.Redirect
// writes the real code) is intentionally not latched.
func (b *responseBuffer) WriteHeader(code int) {
	if code == -1 {
		return // gin sentinel — don't latch
	}
	if !b.written {
		b.status = code
	}
}

// Write captures body bytes into the buffer. Exceeding the limit sets
// oversize; subsequent writes are silently dropped.
func (b *responseBuffer) Write(data []byte) (int, error) {
	b.WriteHeaderNow()
	if b.oversize {
		return len(data), nil
	}
	if b.body.Len()+len(data) > b.limit {
		b.oversize = true
		return len(data), nil
	}
	return b.body.Write(data)
}

// WriteString captures a string body write into the buffer.
func (b *responseBuffer) WriteString(s string) (int, error) {
	return b.Write([]byte(s))
}

// Flush implements http.Flusher. Because the guard must not expose
// streaming, calling Flush marks a streaming violation. The guard will
// replace the response with a fixed 404 after the handler returns.
func (b *responseBuffer) Flush() {
	b.streamViolation = true
}

// CloseNotify returns a channel that never fires. Cancellation is
// handled by the request context.
func (b *responseBuffer) CloseNotify() <-chan bool {
	return make(chan bool)
}

// Hijack is explicitly refused. Marking a streaming violation ensures
// the guard denies the response even if the caller ignores the error.
func (b *responseBuffer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	b.streamViolation = true
	return nil, nil, http.ErrNotSupported
}

// Pusher returns nil — server push is not supported for buffered
// embedded-session responses.
func (b *responseBuffer) Pusher() http.Pusher {
	return nil
}

// commitBuffer copies the captured headers, status, and body to the
// underlying writer. Called only by the guard after all checks pass.
func (b *responseBuffer) commitBuffer() {
	if b.oversize || b.streamViolation {
		return
	}
	// Replace captured fields rather than appending to earlier middleware
	// headers: conflicting public/private cache directives are unsafe.
	for k, vs := range b.headers {
		b.underlying.Header()[k] = append([]string(nil), vs...)
	}
	b.underlying.Header().Del("ETag")
	b.underlying.Header().Del("Last-Modified")
	status := b.status
	if status <= 0 {
		status = http.StatusOK
	}
	b.underlying.WriteHeader(status)
	if b.body.Len() > 0 {
		_, _ = b.underlying.Write(b.body.Bytes())
	}
}
