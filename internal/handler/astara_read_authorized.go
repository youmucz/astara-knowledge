// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	astaraReadContractV1      = 1
	astaraReadMaxBodyBytes     = 512 * 1024
	astaraReadMaxResponseBytes = 2 * 1024 * 1024
	astaraReadMinPage          = 1
	astaraReadMaxPage          = 1_000_000
	astaraReadMinPageSize      = 1
	astaraReadMaxPageSize      = 100
)

// astaraReadDocument is the caller-provided document scope. All wire fields
// are strings; tenant_id is parsed to uint64 for DB lookup.
type astaraReadDocument struct {
	TenantID        string `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	KnowledgeID     string `json:"knowledge_id"`
	Revision        string `json:"revision"`
}

// astaraReadAuthorizedRequest is the closed POST body for read-authorized.
// Unknown fields are rejected at decode. page and page_size are mandatory.
type astaraReadAuthorizedRequest struct {
	ContractVersion int               `json:"contract_version"`
	Document        astaraReadDocument `json:"document"`
	Page            int               `json:"page"`
	PageSize        int               `json:"page_size"`
}

// astaraReadDocumentMeta is the document metadata snapshot returned in the
// response. tenant_id is numeric (uint64) matching the actual DB column type.
// No signed URLs or storage paths are exposed.
type astaraReadDocumentMeta struct {
	ID              string    `json:"id"`
	TenantID        uint64    `json:"tenant_id"`
	KnowledgeBaseID string    `json:"knowledge_base_id"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	FileName        string    `json:"file_name"`
	FileType        string    `json:"file_type"`
	FileSize        int64     `json:"file_size"`
	SourceRevision  int64     `json:"source_revision"`
	ParseStatus     string    `json:"parse_status"`
	EnableStatus    string    `json:"enable_status"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// astaraReadChunk is one chunk in the paginated response. tenant_id,
// knowledge_base_id, and knowledge_id form the scoped triple for client-side
// validation. tenant_id is numeric (uint64) matching the actual DB column type.
type astaraReadChunk struct {
	ID              string `json:"id"`
	TenantID        uint64 `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	KnowledgeID     string `json:"knowledge_id"`
	ChunkIndex      int    `json:"chunk_index"`
	Content         string `json:"content"`
	ChunkType       string `json:"chunk_type"`
	StartAt         int    `json:"start_at"`
	EndAt           int    `json:"end_at"`
	IsEnabled       bool   `json:"is_enabled"`
}

// astaraReadData is the nested data field in the response. page and
// page_size echo the exact request values.
type astaraReadData struct {
	Document astaraReadDocumentMeta `json:"document"`
	Chunks   []astaraReadChunk      `json:"chunks"`
	Total    int64                  `json:"total"`
	Page     int                    `json:"page"`
	PageSize int                    `json:"page_size"`
}

// astaraReadAuthorizedResponse is the bounded JSON contract returned by
// POST /api/v1/astara/read-authorized.
type astaraReadAuthorizedResponse struct {
	ContractVersion int               `json:"contract_version"`
	Document        astaraReadDocument `json:"document"`
	Data            astaraReadData     `json:"data"`
}

// readProviderSnapshot captures mutable provider state of tenant, KB, and
// document at admission time for post-read revalidation.
type readProviderSnapshot struct {
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
	DocUpdatedAt       time.Time
	DocParseStatus     string
	DocEnableStatus    string
	DocDeleted         bool
}

// AstaraReadAuthorizedHandler serves POST /api/v1/astara/read-authorized:
// scoped read of a single document's metadata and paginated chunks. Every
// query is bounded to (tenant_id, knowledge_base_id, knowledge_id). Provider
// state is snapshot at admission and revalidated (tenant, KB, doc) before
// JSON commit. No data leaks on drift.
type AstaraReadAuthorizedHandler struct {
	db *gorm.DB
}

// NewAstaraReadAuthorizedHandler constructs the handler. db is injected;
// no routes or wiring is created here.
func NewAstaraReadAuthorizedHandler(db *gorm.DB) *AstaraReadAuthorizedHandler {
	return &AstaraReadAuthorizedHandler{db: db}
}

// ReadAuthorized executes the scoped read and returns a bounded JSON response.
func (h *AstaraReadAuthorizedHandler) ReadAuthorized(c *gin.Context) {
	ctx := c.Request.Context()
	c.Header("Cache-Control", "private, no-store")

	// ── 1. Decode & validate request shape ──────────────────────────────
	var req astaraReadAuthorizedRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, astaraReadMaxBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid read request"})
		return
	}
	// Reject trailing data after the JSON object.
	if err := decoder.Decode(new(any)); err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid read request"})
		return
	}

	if req.ContractVersion != astaraReadContractV1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "contract_version must be 1"})
		return
	}

	// ── 2. Validate document fields (reject, do not normalize) ───────────
	doc := req.Document
	if doc.TenantID == "" || doc.TenantID != strings.TrimSpace(doc.TenantID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "document.tenant_id is required and must not contain surrounding whitespace"})
		return
	}
	parsedTenant, err := strconv.ParseUint(doc.TenantID, 10, 64)
	if err != nil || parsedTenant == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "document.tenant_id must be a positive numeric string"})
		return
	}
	// Canonical decimal: reject leading zeros or non-canonical forms.
	if doc.TenantID != strconv.FormatUint(parsedTenant, 10) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "document.tenant_id must be canonical decimal"})
		return
	}
	if doc.KnowledgeBaseID == "" || doc.KnowledgeBaseID != strings.TrimSpace(doc.KnowledgeBaseID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "document.knowledge_base_id must not contain surrounding whitespace"})
		return
	}
	if len(doc.KnowledgeBaseID) > 256 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "document.knowledge_base_id exceeds 256 bytes"})
		return
	}
	if doc.KnowledgeID == "" || doc.KnowledgeID != strings.TrimSpace(doc.KnowledgeID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "document.knowledge_id must not contain surrounding whitespace"})
		return
	}
	if len(doc.KnowledgeID) > 256 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "document.knowledge_id exceeds 256 bytes"})
		return
	}
	if doc.Revision == "" || doc.Revision != strings.TrimSpace(doc.Revision) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "document.revision must not contain surrounding whitespace"})
		return
	}
	if len(doc.Revision) != 64 || !isLowerCaseHexString(doc.Revision) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "document.revision must be a 64-char lowercase hex string"})
		return
	}

	// ── 3. Strict page / page_size validation (no defaults) ─────────────
	if req.Page < astaraReadMinPage || req.Page > astaraReadMaxPage {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("page must be between %d and %d", astaraReadMinPage, astaraReadMaxPage),
		})
		return
	}
	if req.PageSize < astaraReadMinPageSize || req.PageSize > astaraReadMaxPageSize {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("page_size must be between %d and %d", astaraReadMinPageSize, astaraReadMaxPageSize),
		})
		return
	}
	// Overflow guard: (page-1)*pageSize must fit in int.
	offset := (req.Page - 1) * req.PageSize
	if req.Page > 1 && offset < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "page offset overflow"})
		return
	}

	// ── 4. DB verification: tenant ──────────────────────────────────────
	var tenant types.Tenant
	if err := h.db.WithContext(ctx).First(&tenant, parsedTenant).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "tenant not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant lookup failed"})
		return
	}

	// ── 5. DB verification: knowledge base ──────────────────────────────
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

	// ── 6. DB verification: document ────────────────────────────────────
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

	// ── 7. Snapshot provider state for post-read revalidation ───────────
	snapshot := readProviderSnapshot{
		TenantID:           tenant.ID,
		TenantStatus:       tenant.Status,
		KBDeletedAt:        kb.DeletedAt,
		KBTenantID:         kb.TenantID,
		DocTenantID:        knowledge.TenantID,
		DocKnowledgeBaseID: knowledge.KnowledgeBaseID,
		DocSourceRevision:  knowledge.SourceRevision,
		DocUpdatedAt:       knowledge.UpdatedAt,
		DocParseStatus:     knowledge.ParseStatus,
		DocEnableStatus:    knowledge.EnableStatus,
		DocDeleted:         !knowledge.DeletedAt.Time.IsZero(),
	}

	// ── 8. Count total scoped chunks ────────────────────────────────────
	var total int64
	if err := h.db.WithContext(ctx).
		Model(&types.Chunk{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND knowledge_id = ? AND deleted_at IS NULL",
			parsedTenant, doc.KnowledgeBaseID, doc.KnowledgeID).
		Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "chunk count failed"})
		return
	}

	// ── 9. Query paginated chunks ───────────────────────────────────────
	// Deterministic ordering: chunk_index ASC, id ASC.
	var chunks []types.Chunk
	if err := h.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND knowledge_id = ? AND deleted_at IS NULL",
			parsedTenant, doc.KnowledgeBaseID, doc.KnowledgeID).
		Order("chunk_index ASC, id ASC").
		Offset(offset).
		Limit(req.PageSize).
		Find(&chunks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "chunk query failed"})
		return
	}

	// ── 10. Post-read revalidation: tenant, KB, document ────────────────
	// Tenant revalidation.
	var recheckTenant types.Tenant
	if err := h.db.WithContext(ctx).First(&recheckTenant, snapshot.TenantID).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "tenant changed during read"})
		return
	}
	if recheckTenant.Status != snapshot.TenantStatus {
		c.JSON(http.StatusConflict, gin.H{"error": "tenant changed during read"})
		return
	}

	// Knowledge base revalidation.
	var recheckKB types.KnowledgeBase
	if err := h.db.WithContext(ctx).
		Where("id = ?", doc.KnowledgeBaseID).
		First(&recheckKB).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "knowledge base changed during read"})
		return
	}
	if recheckKB.TenantID != snapshot.KBTenantID {
		c.JSON(http.StatusConflict, gin.H{"error": "knowledge base changed during read"})
		return
	}
	if !recheckKB.DeletedAt.Valid != !snapshot.KBDeletedAt.Valid ||
		(recheckKB.DeletedAt.Valid && !recheckKB.DeletedAt.Time.Equal(snapshot.KBDeletedAt.Time)) {
		c.JSON(http.StatusConflict, gin.H{"error": "knowledge base changed during read"})
		return
	}

	// Document revalidation.
	var recheckDoc types.Knowledge
	if err := h.db.WithContext(ctx).
		Where("id = ?", doc.KnowledgeID).
		First(&recheckDoc).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "document changed during read"})
		return
	}
	if recheckDoc.TenantID != snapshot.DocTenantID {
		c.JSON(http.StatusConflict, gin.H{"error": "document changed during read"})
		return
	}
	if recheckDoc.KnowledgeBaseID != snapshot.DocKnowledgeBaseID ||
		recheckDoc.SourceRevision != snapshot.DocSourceRevision ||
		!recheckDoc.UpdatedAt.Equal(snapshot.DocUpdatedAt) ||
		recheckDoc.ParseStatus != snapshot.DocParseStatus ||
		recheckDoc.EnableStatus != snapshot.DocEnableStatus ||
		(!snapshot.DocDeleted && !recheckDoc.DeletedAt.Time.IsZero()) {
		c.JSON(http.StatusConflict, gin.H{"error": "document changed during read"})
		return
	}

	// ── 11. Build response ──────────────────────────────────────────────
	readChunks := make([]astaraReadChunk, len(chunks))
	for i, ch := range chunks {
		readChunks[i] = astaraReadChunk{
			ID:              ch.ID,
			TenantID:        ch.TenantID,
			KnowledgeBaseID: ch.KnowledgeBaseID,
			KnowledgeID:     ch.KnowledgeID,
			ChunkIndex:      ch.ChunkIndex,
			Content:         ch.Content,
			ChunkType:       ch.ChunkType,
			StartAt:         ch.StartAt,
			EndAt:           ch.EndAt,
			IsEnabled:       ch.IsEnabled,
		}
	}

	resp := astaraReadAuthorizedResponse{
		ContractVersion: astaraReadContractV1,
		Document:        req.Document,
		Data: astaraReadData{
			Document: astaraReadDocumentMeta{
				ID:              knowledge.ID,
				TenantID:        knowledge.TenantID,
				KnowledgeBaseID: knowledge.KnowledgeBaseID,
				Title:           knowledge.Title,
				Description:     knowledge.Description,
				FileName:        knowledge.FileName,
				FileType:        knowledge.FileType,
				FileSize:        knowledge.FileSize,
				SourceRevision:  knowledge.SourceRevision,
				ParseStatus:     knowledge.ParseStatus,
				EnableStatus:    knowledge.EnableStatus,
				CreatedAt:       knowledge.CreatedAt,
				UpdatedAt:       knowledge.UpdatedAt,
			},
			Chunks:   readChunks,
			Total:    total,
			Page:     req.Page,
			PageSize: req.PageSize,
		},
	}

	// ── 12. Bounded JSON response ───────────────────────────────────────
	payload, err := json.Marshal(resp)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "response encoding failed"})
		return
	}
	if len(payload) > astaraReadMaxResponseBytes {
		c.JSON(http.StatusBadGateway, gin.H{"error": "read response exceeds size limit"})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}

// isLowerCaseHexString returns true if s is non-empty, consists only of
// [0-9a-f] (no uppercase), and has the given length.
func isLowerCaseHexString(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
