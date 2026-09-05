package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// Astara document seam: stable external-ID upsert/delete for the Plane Page
// source contract (source contract version 1).
//
// Semantics:
//   - Identity is (knowledge_base_id, external_system, external_id): one
//     provider record per Plane Page regardless of how many Projects link it.
//   - Upserts carry a monotonic source_revision; a write whose revision is
//     lower than the stored one is rejected with 409 so obsolete work can
//     never overwrite a newer snapshot.
//   - Content replacement is atomic per document: chunk rows for the old
//     snapshot are replaced in one transaction with the new snapshot's rows.

const astaraDocumentMaxContentRunes = 2_000_000
const astaraDocumentMaxTitleRunes = 512

type astaraDocumentUpsertRequest struct {
	ExternalSystem     string                   `json:"external_system" binding:"required"`
	ExternalID         string                   `json:"external_id" binding:"required"`
	Title              string                   `json:"title"`
	Content            string                   `json:"content" binding:"required"`
	ContentHash        string                   `json:"content_hash"`
	SourceRevision     int64                    `json:"source_revision"`
	PolicyDigest       string                   `json:"policy_digest"`
	ProjectIDs         []string                 `json:"project_ids"`
	LabelIDs           []string                 `json:"label_ids"`
	Attachments        []astaraDocumentAttachment `json:"attachments"`
	CanonicalReference string                   `json:"canonical_reference"`
}

type astaraDocumentAttachment struct {
	Kind       string `json:"kind"`
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
}

type astaraDocumentResource struct {
	ID             string `json:"id"`
	ExternalSystem string `json:"external_system"`
	ExternalID     string `json:"external_id"`
	SourceRevision int64  `json:"source_revision"`
	ContentHash    string `json:"content_hash"`
}

func documentResource(k *types.Knowledge) astaraDocumentResource {
	resource := astaraDocumentResource{ID: k.ID, SourceRevision: k.SourceRevision, ContentHash: k.ContentHash}
	if k.ExternalSystem != nil {
		resource.ExternalSystem = *k.ExternalSystem
	}
	if k.ExternalID != nil {
		resource.ExternalID = *k.ExternalID
	}
	return resource
}

func findKnowledgeByExternalID(db *gorm.DB, kbID, system, id string) (*types.Knowledge, error) {
	var knowledge types.Knowledge
	err := db.Where(
		"knowledge_base_id = ? AND external_system = ? AND external_id = ?",
		kbID, system, id,
	).First(&knowledge).Error
	if err != nil {
		return nil, err
	}
	return &knowledge, nil
}

func validateDocumentRequest(system, id string, content string, revision int64) bool {
	system, id = strings.TrimSpace(system), strings.TrimSpace(id)
	if system == "" || id == "" || len(system) > 64 || len(id) > 255 {
		return false
	}
	if revision < 0 {
		return false
	}
	if len([]rune(content)) == 0 || len([]rune(content)) > astaraDocumentMaxContentRunes {
		return false
	}
	return true
}

func documentMetadata(request *astaraDocumentUpsertRequest) types.JSON {
	metadata := map[string]interface{}{
		"external_system":     request.ExternalSystem,
		"external_id":         request.ExternalID,
		"source_revision":     request.SourceRevision,
		"policy_digest":       request.PolicyDigest,
		"canonical_reference": request.CanonicalReference,
		"channel":             "astara",
	}
	if len(request.ProjectIDs) > 0 {
		metadata["project_ids"] = request.ProjectIDs
	}
	if len(request.LabelIDs) > 0 {
		metadata["label_ids"] = request.LabelIDs
	}
	if len(request.Attachments) > 0 {
		items := make([]interface{}, 0, len(request.Attachments))
		for _, attachment := range request.Attachments {
			items = append(items, map[string]interface{}{
				"kind":       attachment.Kind,
				"identifier": attachment.Identifier,
				"name":       attachment.Name,
			})
		}
		metadata["attachments"] = items
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return types.JSON("null")
	}
	return types.JSON(encoded)
}

// chunkText splits the exported markdown into bounded chunk contents.
// Deterministic: the same input always produces the same split.
func chunkText(content string, chunkLimit int) []string {
	runes := []rune(content)
	if chunkLimit <= 0 {
		chunkLimit = 1000
	}
	if len(runes) == 0 {
		return nil
	}
	chunks := make([]string, 0, len(runes)/chunkLimit+1)
	for start := 0; start < len(runes); start += chunkLimit {
		end := start + chunkLimit
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

// UpsertDocument creates or replaces the snapshot for one external document.
func (h *AstaraControlPlaneHandler) UpsertDocument(c *gin.Context) {
	kbID := strings.TrimSpace(c.Param("knowledge_base_id"))
	if kbID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "knowledge base id is required"})
		return
	}
	var request astaraDocumentUpsertRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid document request"})
		return
	}
	system, id := strings.TrimSpace(request.ExternalSystem), strings.TrimSpace(request.ExternalID)
	if !validateDocumentRequest(system, id, request.Content, request.SourceRevision) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid document request"})
		return
	}
	var kb types.KnowledgeBase
	if err := h.db.WithContext(c).First(&kb, "id = ?", kbID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "knowledge base not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge base lookup failed"})
		return
	}

	existing, err := findKnowledgeByExternalID(h.db.WithContext(c), kbID, system, id)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "document lookup failed"})
		return
	}

	// Version fence: reject obsolete revisions outright so a late-arriving
	// old export can never overwrite a newer provider snapshot.
	if existing != nil && request.SourceRevision < existing.SourceRevision {
		c.JSON(http.StatusConflict, gin.H{
			"error":           "stale source revision",
			"stored_revision": existing.SourceRevision,
		})
		return
	}

	now := time.Now().UTC()
	title := strings.TrimSpace(request.Title)
	if title == "" {
		title = "Untitled"
	}
	if len([]rune(title)) > astaraDocumentMaxTitleRunes {
		title = string([]rune(title)[:astaraDocumentMaxTitleRunes])
	}
	externalSystem, externalID := system, id
	metadata := documentMetadata(&request)
	existingID := ""

	err = h.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if existing != nil {
			// Idempotent replay: same revision and same content hash means the
			// provider already stores this exact snapshot.
			if request.SourceRevision == existing.SourceRevision && request.ContentHash != "" &&
				request.ContentHash == existing.ContentHash {
				return nil
			}
			// Atomic snapshot replacement: drop the obsolete chunks, then
			// rewrite the row in place so the provider document identity
			// (row id) stays stable across revisions.
			if err := tx.Where("knowledge_id = ?", existing.ID).Delete(&types.Chunk{}).Error; err != nil {
				return err
			}
			if err := tx.Model(&types.Knowledge{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
				"title":           title,
				"file_name":       title,
				"metadata":        metadata,
				"source_revision": request.SourceRevision,
				"content_hash":    request.ContentHash,
				"parse_status":    types.ParseStatusCompleted,
				"enable_status":   "enabled",
				"error_message":   "",
				"updated_at":      now,
			}).Error; err != nil {
				return err
			}
			existingID = existing.ID
		} else {
			knowledge := &types.Knowledge{
				ID:               uuid.NewString(),
				TenantID:         kb.TenantID,
				KnowledgeBaseID:  kbID,
				Type:             types.KnowledgeTypeManual,
				Title:            title,
				Source:           "astara",
				Channel:          "astara",
				ParseStatus:      types.ParseStatusCompleted,
				EnableStatus:     "enabled",
				EmbeddingModelID: kb.EmbeddingModelID,
				FileName:         title,
				FileType:         "markdown",
				Metadata:         metadata,
				CreatedAt:        now,
				UpdatedAt:        now,
				ExternalSystem:   &externalSystem,
				ExternalID:       &externalID,
				SourceRevision:   request.SourceRevision,
				ContentHash:      request.ContentHash,
			}
			if err := tx.Create(knowledge).Error; err != nil {
				return err
			}
			existingID = knowledge.ID
		}
		chunks := chunkText(request.Content, 1000)
		for index, content := range chunks {
			chunk := &types.Chunk{
				ID:              uuid.NewString(),
				TenantID:        kb.TenantID,
				KnowledgeID:     existingID,
				KnowledgeBaseID: kbID,
				Content:         content,
				SourceContent:   content,
				IndexStatus:     "ready",
				ChunkIndex:      index,
				IsEnabled:       true,
				Flags:           types.ChunkFlagRecommended,
				ChunkType:       types.ChunkTypeText,
				StartAt:         index * 1000,
				EndAt:           index*1000 + len([]rune(content)),
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			if err := tx.Create(chunk).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "document upsert conflict"})
		return
	}

	created, err := findKnowledgeByExternalID(h.db.WithContext(c), kbID, system, id)
	if err != nil || created == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "document upsert failed"})
		return
	}
	c.JSON(http.StatusOK, documentResource(created))
}

// FindDocumentByExternalID returns the provider record for one external id.
func (h *AstaraControlPlaneHandler) FindDocumentByExternalID(c *gin.Context) {
	kbID := strings.TrimSpace(c.Param("knowledge_base_id"))
	system, id, ok := normalizedIdentity(c.Query("external_system"), c.Query("external_id"))
	if kbID == "" || !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid external identity"})
		return
	}
	knowledge, err := findKnowledgeByExternalID(h.db.WithContext(c), kbID, system, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "document not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "document lookup failed"})
		return
	}
	c.JSON(http.StatusOK, documentResource(knowledge))
}

// FindDocument returns the provider record by provider document id.
func (h *AstaraControlPlaneHandler) FindDocument(c *gin.Context) {
	kbID := strings.TrimSpace(c.Param("knowledge_base_id"))
	documentID := strings.TrimSpace(c.Param("document_id"))
	if kbID == "" || documentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid document lookup"})
		return
	}
	var knowledge types.Knowledge
	err := h.db.WithContext(c).Where("knowledge_base_id = ? AND id = ?", kbID, documentID).First(&knowledge).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "document not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "document lookup failed"})
		return
	}
	c.JSON(http.StatusOK, documentResource(&knowledge))
}

// DeleteDocument removes the provider record and its chunks for one document.
func (h *AstaraControlPlaneHandler) DeleteDocument(c *gin.Context) {
	kbID := strings.TrimSpace(c.Param("knowledge_base_id"))
	documentID := strings.TrimSpace(c.Param("document_id"))
	if kbID == "" || documentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid document delete"})
		return
	}
	var deleted int64
	err := h.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("knowledge_base_id = ? AND id = ?", kbID, documentID).Delete(&types.Knowledge{})
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		if deleted == 0 {
			return nil
		}
		return tx.Where("knowledge_id = ?", documentID).Delete(&types.Chunk{}).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "document deletion failed"})
		return
	}
	if deleted == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "document not found"})
		return
	}
	c.Status(http.StatusNoContent)
}
