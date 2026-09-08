// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	astaraSearchContractV1       = 1
	astaraSearchMaxBodyBytes     = 512 * 1024
	astaraSearchMaxResponseBytes = 2 * 1024 * 1024
	astaraSearchMaxQueryChars    = 2000
	astaraSearchMaxDocs          = 1000
	astaraSearchMinDocs          = 1
	astaraSearchMaxResults       = 1000
)

// astaraSearchDocument is the caller-provided document scope. All wire fields
// are strings; tenant_id is parsed to uint64 for DB lookup.
type astaraSearchDocument struct {
	TenantID        string `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	KnowledgeID     string `json:"knowledge_id"`
	Revision        string `json:"revision"`
}

// astaraSearchAuthorizedRequest is the closed POST body for search-authorized.
// Unknown fields are rejected at decode.
type astaraSearchAuthorizedRequest struct {
	ContractVersion     int                    `json:"contract_version"`
	Query               string                 `json:"query"`
	Documents           []astaraSearchDocument `json:"documents"`
	AuthorizationDigest string                 `json:"authorization_digest"`
}

// astaraSearchResultItem is one entry in the projected search response.
// tenant_id is string (canonical decimal) matching the admitted document.
type astaraSearchResultItem struct {
	TenantID        string  `json:"tenant_id"`
	KnowledgeBaseID string  `json:"knowledge_base_id"`
	KnowledgeID     string  `json:"knowledge_id"`
	Content         string  `json:"content"`
	Score           float64 `json:"score"`
	ChunkIndex      int     `json:"chunk_index"`
	KnowledgeTitle  string  `json:"knowledge_title"`
	ID              string  `json:"id"`
}

// astaraSearchAuthorizedResponse is the bounded JSON contract returned by
// POST /api/v1/astara/search-authorized.
type astaraSearchAuthorizedResponse struct {
	ContractVersion     int                      `json:"contract_version"`
	AuthorizationDigest string                   `json:"authorization_digest"`
	Results             []astaraSearchResultItem `json:"results"`
}

// searchProviderSnapshot captures mutable provider state of tenant, KB, and
// document at admission time for post-search revalidation.
type searchProviderSnapshot struct {
	// Tenant
	TenantID     uint64
	TenantStatus string
	// Knowledge base
	KBDeletedAt gorm.DeletedAt
	KBTenantID  uint64
	// Document
	DocTenantID        uint64
	DocKnowledgeBaseID string
	DocSourceRevision  int64
	DocUpdatedAt       string // RFC3339Nano for exact compare
	DocParseStatus     string
	DocEnableStatus    string
	DocDeleted         bool
}

// AstaraSearchAuthorizedHandler serves POST /api/v1/astara/search-authorized:
// scoped search over a caller-provided document allowlist. Every document is
// validated against the database (existence, tenant ownership, KB membership,
// queryable status) BEFORE the search runs. After search, provider state is
// revalidated against a pre-search snapshot. The tenant is derived from the
// validated documents — never from caller-trusted fields alone.
type AstaraSearchAuthorizedHandler struct {
	db             *gorm.DB
	sessionService interfaces.SessionService
}

// NewAstaraSearchAuthorizedHandler constructs the handler. db and
// sessionService are injected; no routes or wiring is created here.
func NewAstaraSearchAuthorizedHandler(db *gorm.DB, sessionService interfaces.SessionService) *AstaraSearchAuthorizedHandler {
	return &AstaraSearchAuthorizedHandler{db: db, sessionService: sessionService}
}

// SearchAuthorized executes the scoped search and returns a bounded JSON response.
func (h *AstaraSearchAuthorizedHandler) SearchAuthorized(c *gin.Context) {
	ctx := c.Request.Context()
	c.Header("Cache-Control", "private, no-store")

	// ── 1. Decode & validate request shape ──────────────────────────────
	var req astaraSearchAuthorizedRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, astaraSearchMaxBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid search request"})
		return
	}
	// Reject trailing data after the JSON object.
	if err := decoder.Decode(new(any)); err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid search request"})
		return
	}

	if req.ContractVersion != astaraSearchContractV1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "contract_version must be 1"})
		return
	}

	// ── 2. Validate query (query may be trimmed intentionally) ──────────
	query := strings.TrimSpace(req.Query)
	if query == "" || len([]rune(query)) > astaraSearchMaxQueryChars {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query is required and bounded"})
		return
	}

	// ── 3. Validate documents array ─────────────────────────────────────
	if len(req.Documents) < astaraSearchMinDocs || len(req.Documents) > astaraSearchMaxDocs {
		c.JSON(http.StatusBadRequest, gin.H{"error": "documents must contain 1 to 1000 entries"})
		return
	}

	// ── 4. Validate each document entry — reject, do not normalize ──────
	//    Surrounding whitespace in any document field is rejected outright.
	seenKnowledgeIDs := make(map[string]bool, len(req.Documents))
	for i, doc := range req.Documents {
		if doc.TenantID == "" || doc.TenantID != strings.TrimSpace(doc.TenantID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "document tenant_id must not contain surrounding whitespace"})
			return
		}
		parsedTenant, err := strconv.ParseUint(doc.TenantID, 10, 64)
		if err != nil || parsedTenant == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "document tenant_id must be a positive numeric string"})
			return
		}
		// Canonical decimal: reject leading zeros or non-canonical forms.
		if doc.TenantID != strconv.FormatUint(parsedTenant, 10) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "document tenant_id must be canonical decimal"})
			return
		}
		if doc.KnowledgeBaseID == "" || doc.KnowledgeBaseID != strings.TrimSpace(doc.KnowledgeBaseID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "document knowledge_base_id must not contain surrounding whitespace"})
			return
		}
		if len(doc.KnowledgeBaseID) > 256 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "document knowledge_base_id exceeds 256 bytes"})
			return
		}
		if doc.KnowledgeID == "" || doc.KnowledgeID != strings.TrimSpace(doc.KnowledgeID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "document knowledge_id must not contain surrounding whitespace"})
			return
		}
		if len(doc.KnowledgeID) > 256 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "document knowledge_id exceeds 256 bytes"})
			return
		}
		if doc.Revision == "" || doc.Revision != strings.TrimSpace(doc.Revision) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "document revision must not contain surrounding whitespace"})
			return
		}
		if len(doc.Revision) != 64 || !isLowerCaseHexString(doc.Revision) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "document revision must be a 64-char lowercase hex string"})
			return
		}
		if seenKnowledgeIDs[doc.KnowledgeID] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "document knowledge_id is duplicated"})
			return
		}
		seenKnowledgeIDs[doc.KnowledgeID] = true
		_ = i // used above for error context only
	}

	// ── 5. Verify authorization_digest (computed from ORIGINAL request) ─
	expectedDigest := computeSearchDigest(req.Documents)
	if req.AuthorizationDigest != expectedDigest {
		c.JSON(http.StatusBadRequest, gin.H{"error": "authorization_digest mismatch"})
		return
	}

	// ── 6. DB verification: per-document tenant, KB, doc ────────────────
	var tenantID uint64
	var tenantRow types.Tenant
	knowledgeIDs := make([]string, 0, len(req.Documents))
	snapshots := make(map[string]*searchProviderSnapshot, len(req.Documents))

	for _, doc := range req.Documents {
		parsedTenant, _ := strconv.ParseUint(doc.TenantID, 10, 64)

		// Verify tenant exists and is active.
		var tenant types.Tenant
		if err := h.db.WithContext(ctx).First(&tenant, parsedTenant).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "tenant not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant lookup failed"})
			return
		}
		if tenant.Status != "active" {
			c.JSON(http.StatusForbidden, gin.H{"error": "tenant is not active"})
			return
		}
		tenantRow = tenant

		// Verify knowledge base exists, belongs to tenant, not deleted.
		var kb types.KnowledgeBase
		if err := h.db.WithContext(ctx).
			Where("id = ? AND tenant_id = ?", doc.KnowledgeBaseID, parsedTenant).
			First(&kb).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "knowledge base not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge base lookup failed"})
			return
		}
		if kb.DeletedAt.Valid {
			c.JSON(http.StatusNotFound, gin.H{"error": "knowledge base not found"})
			return
		}

		// Verify document exists, belongs to tenant+KB, not deleted.
		var knowledge types.Knowledge
		if err := h.db.WithContext(ctx).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ?",
				doc.KnowledgeID, parsedTenant, doc.KnowledgeBaseID).
			First(&knowledge).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "document not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "document lookup failed"})
			return
		}
		if knowledge.DeletedAt.Valid {
			c.JSON(http.StatusNotFound, gin.H{"error": "document not found"})
			return
		}
		if knowledge.ParseStatus != types.ParseStatusCompleted {
			c.JSON(http.StatusConflict, gin.H{"error": "document is not queryable"})
			return
		}
		if knowledge.EnableStatus != "enabled" {
			c.JSON(http.StatusConflict, gin.H{"error": "document is not queryable"})
			return
		}

		// Mixed-mapping guard: all documents must belong to one tenant.
		if tenantID == 0 {
			tenantID = parsedTenant
		} else if parsedTenant != tenantID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "documents must belong to one tenant"})
			return
		}

		// Snapshot provider state for post-search revalidation.
		snapshots[doc.KnowledgeID] = &searchProviderSnapshot{
			TenantID:           tenant.ID,
			TenantStatus:       tenant.Status,
			KBDeletedAt:        kb.DeletedAt,
			KBTenantID:         kb.TenantID,
			DocTenantID:        knowledge.TenantID,
			DocKnowledgeBaseID: knowledge.KnowledgeBaseID,
			DocSourceRevision:  knowledge.SourceRevision,
			DocUpdatedAt:       knowledge.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
			DocParseStatus:     knowledge.ParseStatus,
			DocEnableStatus:    knowledge.EnableStatus,
			DocDeleted:         !knowledge.DeletedAt.Time.IsZero(),
		}

		knowledgeIDs = append(knowledgeIDs, doc.KnowledgeID)
	}

	// ── 7. Execute search via SessionService.SearchKnowledge ────────────
	//    knowledgeBaseIDs: nil — passing full KB IDs would create whole-KB
	//    targets, defeating the allowlist. Document-only knowledgeIDs produce
	//    scoped targets.
	//    tagScopes: nil — no tag-based filtering.
	searchCtx := context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	searchCtx = context.WithValue(searchCtx, types.TenantInfoContextKey, &tenantRow)
	searchResults, err := h.sessionService.SearchKnowledge(
		searchCtx,
		nil,          // knowledgeBaseIDs
		knowledgeIDs, // knowledgeIDs — explicit doc list
		nil,          // tagScopes
		query,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "search failed"})
		return
	}

	// ── 8. Validate all results are within admitted scope ───────────────
	//    ALL result IDs+KB must match admitted scope or reject WHOLE response.
	//    Enforce result count bound and (knowledge_id, id) uniqueness.
	if len(searchResults) > astaraSearchMaxResults {
		c.JSON(http.StatusBadGateway, gin.H{"error": "search returned too many results"})
		return
	}
	seenResultIDs := make(map[string]bool, len(searchResults))
	for _, r := range searchResults {
		if r == nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "search returned invalid result"})
			return
		}
		snap := snapshots[r.KnowledgeID]
		if snap == nil {
			// Result for a document not in our admitted scope.
			c.JSON(http.StatusBadGateway, gin.H{"error": "search returned result outside authorized scope"})
			return
		}
		if snap.DocKnowledgeBaseID != r.KnowledgeBaseID {
			c.JSON(http.StatusBadGateway, gin.H{"error": "search returned result with mismatched knowledge base"})
			return
		}
		// Validate result quality.
		if r.ID == "" {
			c.JSON(http.StatusBadGateway, gin.H{"error": "search returned invalid result"})
			return
		}
		if math.IsNaN(r.Score) || math.IsInf(r.Score, 0) {
			c.JSON(http.StatusBadGateway, gin.H{"error": "search returned invalid result"})
			return
		}
		if r.ChunkIndex < 0 {
			c.JSON(http.StatusBadGateway, gin.H{"error": "search returned invalid result"})
			return
		}
		// Uniqueness: (knowledge_id, id) must be unique across results.
		compositeKey := r.KnowledgeID + "\x00" + r.ID
		if seenResultIDs[compositeKey] {
			c.JSON(http.StatusBadGateway, gin.H{"error": "search returned duplicate result"})
			return
		}
		seenResultIDs[compositeKey] = true
	}

	// ── 9. Post-search revalidation: tenant, KB, doc state ─────────────
	//    Re-read all admitted tenant/KB/doc state including omitted result docs.
	for _, doc := range req.Documents {
		snap := snapshots[doc.KnowledgeID]
		parsedTenant, _ := strconv.ParseUint(doc.TenantID, 10, 64)

		// Tenant revalidation.
		var recheckTenant types.Tenant
		if err := h.db.WithContext(ctx).First(&recheckTenant, snap.TenantID).Error; err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "tenant changed during search"})
			return
		}
		if recheckTenant.Status != snap.TenantStatus {
			c.JSON(http.StatusConflict, gin.H{"error": "tenant changed during search"})
			return
		}

		// Knowledge base revalidation.
		var recheckKB types.KnowledgeBase
		if err := h.db.WithContext(ctx).
			Where("id = ?", doc.KnowledgeBaseID).
			First(&recheckKB).Error; err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "knowledge base changed during search"})
			return
		}
		if recheckKB.TenantID != snap.KBTenantID {
			c.JSON(http.StatusConflict, gin.H{"error": "knowledge base changed during search"})
			return
		}
		if !recheckKB.DeletedAt.Valid != !snap.KBDeletedAt.Valid ||
			(recheckKB.DeletedAt.Valid && !recheckKB.DeletedAt.Time.Equal(snap.KBDeletedAt.Time)) {
			c.JSON(http.StatusConflict, gin.H{"error": "knowledge base changed during search"})
			return
		}

		// Document revalidation.
		var recheckDoc types.Knowledge
		if err := h.db.WithContext(ctx).
			Where("id = ?", doc.KnowledgeID).
			First(&recheckDoc).Error; err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "document changed during search"})
			return
		}
		if recheckDoc.TenantID != snap.DocTenantID {
			c.JSON(http.StatusConflict, gin.H{"error": "document changed during search"})
			return
		}
		if recheckDoc.TenantID != parsedTenant {
			c.JSON(http.StatusConflict, gin.H{"error": "document changed during search"})
			return
		}
		if recheckDoc.KnowledgeBaseID != snap.DocKnowledgeBaseID ||
			recheckDoc.SourceRevision != snap.DocSourceRevision ||
			recheckDoc.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00") != snap.DocUpdatedAt ||
			recheckDoc.ParseStatus != snap.DocParseStatus ||
			recheckDoc.EnableStatus != snap.DocEnableStatus ||
			(!snap.DocDeleted && !recheckDoc.DeletedAt.Time.IsZero()) {
			c.JSON(http.StatusConflict, gin.H{"error": "document changed during search"})
			return
		}
	}

	// ── 10. Build projected response ────────────────────────────────────
	results := make([]astaraSearchResultItem, 0, len(searchResults))
	for _, r := range searchResults {
		results = append(results, astaraSearchResultItem{
			TenantID:        strconv.FormatUint(tenantID, 10),
			KnowledgeBaseID: r.KnowledgeBaseID,
			KnowledgeID:     r.KnowledgeID,
			Content:         r.Content,
			Score:           r.Score,
			ChunkIndex:      r.ChunkIndex,
			KnowledgeTitle:  r.KnowledgeTitle,
			ID:              r.ID,
		})
	}

	resp := astaraSearchAuthorizedResponse{
		ContractVersion:     astaraSearchContractV1,
		AuthorizationDigest: expectedDigest,
		Results:             results,
	}

	// ── 11. Bounded JSON response ───────────────────────────────────────
	payload, err := json.Marshal(resp)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "response encoding failed"})
		return
	}
	if len(payload) > astaraSearchMaxResponseBytes {
		c.JSON(http.StatusBadGateway, gin.H{"error": "search response exceeds size limit"})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}

// computeSearchDigest returns the sha256 hex of the canonical JSON
// representation of the documents array. Canonical ordering is by
// (tenant_id, knowledge_base_id, knowledge_id, revision) ascending, with
// each document represented as a sorted-key map. All values are strings.
//
// Matches Python: json.dumps(sorted_docs, sort_keys=True,
// ensure_ascii=False, separators=(',', ':')).
//
// encoding/json with SetEscapeHTML(false) preserves literal <, >, & and
// does NOT escape U+2028/U+2029 (valid in ES2019+), matching Python
// ensure_ascii=False. Control characters (< U+0020) are \u-escaped by
// both encoders.
func computeSearchDigest(docs []astaraSearchDocument) string {
	sorted := make([]astaraSearchDocument, len(docs))
	copy(sorted, docs)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.TenantID != b.TenantID {
			return a.TenantID < b.TenantID
		}
		if a.KnowledgeBaseID != b.KnowledgeBaseID {
			return a.KnowledgeBaseID < b.KnowledgeBaseID
		}
		if a.KnowledgeID != b.KnowledgeID {
			return a.KnowledgeID < b.KnowledgeID
		}
		return a.Revision < b.Revision
	})

	// Build sorted-key maps in canonical key order.
	array := make([]map[string]string, len(sorted))
	for i, doc := range sorted {
		array[i] = map[string]string{
			"knowledge_base_id": doc.KnowledgeBaseID,
			"knowledge_id":      doc.KnowledgeID,
			"revision":          doc.Revision,
			"tenant_id":         doc.TenantID,
		}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// Encode appends a trailing newline; the canonical form omits it.
	_ = enc.Encode(array)
	payload := bytes.TrimRight(buf.Bytes(), "\n")

	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}
