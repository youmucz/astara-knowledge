// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Closed Plane→provider KnowledgeQA model-configuration push contract.
//
// The provider is NOT a model-configuration authority for KnowledgeQA: the
// reserved model row types.PlaneOwnedKnowledgeQAModelID is written only by
// this service-authenticated surface, and answer generation resolves its
// chat model exclusively from it. Embedding/rerank configuration stays
// provider-owned and is never touched here.

const (
	astaraModelConfigContractV1 = 1

	// planeRevisionExtraKey / planeDigestExtraKey carry the applied control
	// metadata inside the model row's opaque extra-config map, so the push
	// needs no schema migration and the manifest migration pin stays frozen.
	planeRevisionExtraKey = "plane_revision"
	planeDigestExtraKey   = "plane_digest"
)

// astaraKnowledgeModelConfigUpsert is the closed push body. Unknown fields
// are rejected at decode.
type astaraKnowledgeModelConfigUpsert struct {
	ContractVersion int    `json:"contract_version"`
	Revision        uint64 `json:"revision"`
	Digest          string `json:"digest"`
	VendorType      string `json:"vendor_type"`
	BaseURL         string `json:"base_url"`
	ModelName       string `json:"model_name"`
	APIKey          string `json:"api_key"`
}

// AstaraKnowledgeModelConfigHandler serves
// POST/DELETE/GET /api/v1/astara/knowledge-model-config.
type AstaraKnowledgeModelConfigHandler struct {
	modelRepo interfaces.ModelRepository
}

// NewAstaraKnowledgeModelConfigHandler constructs the handler over the raw
// model repository (the tenant-context model service is intentionally NOT
// used: the pushed row is tenant-global and the push runs under service
// authentication with no tenant context).
func NewAstaraKnowledgeModelConfigHandler(modelRepo interfaces.ModelRepository) *AstaraKnowledgeModelConfigHandler {
	return &AstaraKnowledgeModelConfigHandler{modelRepo: modelRepo}
}

// computeKnowledgeModelConfigDigest returns the sha256 hex of the canonical
// JSON object for the pushed configuration, including the credential. The
// canonicalization mirrors the other astara contract digests (sorted keys,
// no HTML escaping, no trailing newline).
func computeKnowledgeModelConfigDigest(fields map[string]any) string {
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(fields); err != nil {
		return ""
	}
	hash := sha256.Sum256([]byte(strings.TrimSuffix(buf.String(), "\n")))
	return hex.EncodeToString(hash[:])
}

// canonicalConfigFields builds the digest-bound field set. Revision is a JSON
// number so Plane's canonical encoder must serialize it identically.
func canonicalConfigFields(req *astaraKnowledgeModelConfigUpsert) map[string]any {
	return map[string]any{
		"api_key":          req.APIKey,
		"base_url":         req.BaseURL,
		"contract_version": req.ContractVersion,
		"model_name":       req.ModelName,
		"revision":         req.Revision,
		"vendor_type":      req.VendorType,
	}
}

// appliedRow returns the currently applied plane-owned row, or nil.
func (h *AstaraKnowledgeModelConfigHandler) appliedRow(ctx context.Context) (*types.Model, error) {
	return h.modelRepo.GetByID(ctx, 0, types.PlaneOwnedKnowledgeQAModelID)
}

func (h *AstaraKnowledgeModelConfigHandler) appliedRevision(row *types.Model) uint64 {
	if row == nil || row.DeletedAt.Valid || row.Parameters.ExtraConfig == nil {
		return 0
	}
	revision, err := strconv.ParseUint(row.Parameters.ExtraConfig[planeRevisionExtraKey], 10, 64)
	if err != nil {
		return 0
	}
	return revision
}

// Upsert applies (or rejects) one Plane push. Admission order: decode →
// shape validation → canonical digest → stale-revision fence → atomic apply.
func (h *AstaraKnowledgeModelConfigHandler) Upsert(c *gin.Context) {
	ctx := c.Request.Context()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 512*1024)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var req astaraKnowledgeModelConfigUpsert
	if err := decoder.Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid knowledge model config request"})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid knowledge model config request"})
		return
	}

	if req.ContractVersion != astaraModelConfigContractV1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "contract_version must be 1"})
		return
	}
	if req.Revision == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "revision must be a positive integer"})
		return
	}
	if len(req.Digest) != 64 || !isHexString(req.Digest) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "digest must be a 64-char hex string"})
		return
	}
	for _, field := range []string{req.VendorType, req.BaseURL, req.ModelName, req.APIKey} {
		if field == "" || field != strings.TrimSpace(field) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "vendor_type, base_url, model_name and api_key are required and unpadded"})
			return
		}
	}

	expectedDigest := computeKnowledgeModelConfigDigest(canonicalConfigFields(&req))
	if req.Digest != expectedDigest {
		c.JSON(http.StatusBadRequest, gin.H{"error": "knowledge model config digest mismatch"})
		return
	}

	existing, err := h.appliedRow(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge model config read failed"})
		return
	}
	if h.appliedRevision(existing) >= req.Revision {
		c.JSON(http.StatusConflict, gin.H{"error": "stale knowledge model config revision"})
		return
	}

	row := &types.Model{
		ID:          types.PlaneOwnedKnowledgeQAModelID,
		TenantID:    0,
		Name:        req.ModelName,
		DisplayName: req.ModelName,
		Type:        types.ModelTypeKnowledgeQA,
		// The chat-model factory dispatches on local/remote sources; the
		// vendor protocol travels in Parameters.Provider.
		Source: types.ModelSourceRemote,
		Parameters: types.ModelParameters{
			BaseURL:  req.BaseURL,
			APIKey:   req.APIKey,
			Provider: req.VendorType,
			ExtraConfig: map[string]string{
				planeRevisionExtraKey: strconv.FormatUint(req.Revision, 10),
				planeDigestExtraKey:   req.Digest,
			},
		},
		IsBuiltin: true,
		ManagedBy: types.PlaneManagedBy,
		Status:    types.ModelStatusActive,
	}
	if existing != nil {
		if err := h.modelRepo.Update(ctx, row); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge model config apply failed"})
			return
		}
	} else {
		if err := h.modelRepo.Create(ctx, row); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge model config apply failed"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"contract_version": astaraModelConfigContractV1,
		"applied_revision": req.Revision,
		"digest":           req.Digest,
	})
}

// Revoke clears the applied configuration; answers fail closed until a new
// push. Idempotent when nothing is applied.
func (h *AstaraKnowledgeModelConfigHandler) Revoke(c *gin.Context) {
	ctx := c.Request.Context()
	existing, err := h.appliedRow(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge model config read failed"})
		return
	}
	if existing != nil && !existing.DeletedAt.Valid {
		if err := h.modelRepo.Delete(ctx, 0, types.PlaneOwnedKnowledgeQAModelID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge model config revoke failed"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"contract_version": astaraModelConfigContractV1,
		"applied_revision": 0,
		"digest":           "",
	})
}

// ReadBack returns the applied state, redacted: never the credential or its
// fingerprint.
func (h *AstaraKnowledgeModelConfigHandler) ReadBack(c *gin.Context) {
	ctx := c.Request.Context()
	existing, err := h.appliedRow(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge model config read failed"})
		return
	}
	if existing == nil || existing.DeletedAt.Valid {
		c.JSON(http.StatusOK, gin.H{
			"contract_version": astaraModelConfigContractV1,
			"revision":         0,
			"digest":           "",
			"vendor_type":      "",
			"base_url":         "",
			"model_name":       "",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"contract_version": astaraModelConfigContractV1,
		"revision":         h.appliedRevision(existing),
		"digest":           existing.Parameters.ExtraConfig[planeDigestExtraKey],
		"vendor_type":      existing.Parameters.Provider,
		"base_url":         existing.Parameters.BaseURL,
		"model_name":       existing.Name,
	})
}

// ResolvePlaneOwnedChatModel returns the applied, active plane-owned
// KnowledgeQA model row, or nil when none is applied. Answer generation
// binds to this row exclusively.
func ResolvePlaneOwnedChatModel(ctx context.Context, repo interfaces.ModelRepository) (*types.Model, error) {
	row, err := repo.GetByID(ctx, 0, types.PlaneOwnedKnowledgeQAModelID)
	if err != nil || row == nil || row.DeletedAt.Valid {
		return nil, err
	}
	if row.Type != types.ModelTypeKnowledgeQA || row.Status != types.ModelStatusActive {
		return nil, nil
	}
	return row, nil
}
