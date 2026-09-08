// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/types"
)

func modelConfigEngine(t *testing.T, repo *stubAuthorizedModelRepo) *gin.Engine {
	t.Helper()
	t.Setenv(astaraServiceAuthEnv, "test-service-secret")
	h := NewAstaraKnowledgeModelConfigHandler(repo)
	r := gin.New()
	group := r.Group("/api/v1/astara", AstaraServiceAuth)
	group.POST("/knowledge-model-config", h.Upsert)
	group.DELETE("/knowledge-model-config", h.Revoke)
	group.GET("/knowledge-model-config", h.ReadBack)
	return r
}

func modelConfigRequest(t *testing.T, engine *gin.Engine, method string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = encoded
	}
	req := httptest.NewRequest(method, "/api/v1/astara/knowledge-model-config", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-service-secret")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

func validConfigBody(revision uint64) map[string]any {
	fields := map[string]any{
		"contract_version": astaraModelConfigContractV1,
		"revision":         revision,
		"vendor_type":      "openai-compatible",
		"base_url":         "https://models.example.invalid/v1",
		"model_name":       "deepseek-chat",
		"api_key":          "sk-test-secret",
	}
	req := &astaraKnowledgeModelConfigUpsert{
		ContractVersion: astaraModelConfigContractV1,
		Revision:        revision,
		VendorType:      "openai-compatible",
		BaseURL:         "https://models.example.invalid/v1",
		ModelName:       "deepseek-chat",
		APIKey:          "sk-test-secret",
	}
	fields["digest"] = computeKnowledgeModelConfigDigest(canonicalConfigFields(req))
	return fields
}

func TestKnowledgeModelConfigDigestIndependentUnicodeGolden(t *testing.T) {
	// Cross-language golden computed with the shared canonical convention
	// (sorted keys, no HTML escaping, U+2028/U+2029 escaped, no trailing
	// newline). The Plane-side test asserts the identical value.
	req := &astaraKnowledgeModelConfigUpsert{
		ContractVersion: astaraModelConfigContractV1,
		Revision:        7,
		VendorType:      "openai-compatible",
		BaseURL:         "https://models.example.invalid/v1",
		ModelName:       "模型-测试",
		APIKey:          "sk-\u2028\u2029中文<&",
	}
	digest := computeKnowledgeModelConfigDigest(canonicalConfigFields(req))
	const golden = "48d250d0b26d40b0a0912be4c37055ac796117bf51679e96da48a353f421bb74"
	if digest != golden {
		t.Fatalf("digest=%s, want golden %s", digest, golden)
	}
}

func TestKnowledgeModelConfigRequiresServiceAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := modelConfigEngine(t, &stubAuthorizedModelRepo{rows: map[string]*types.Model{}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/astara/knowledge-model-config", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", recorder.Code)
	}
}

func TestKnowledgeModelConfigFailsClosedWhenSecretUnset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := modelConfigEngine(t, &stubAuthorizedModelRepo{rows: map[string]*types.Model{}})
	// Unset AFTER the engine is built: the middleware reads the environment
	// on every request and must fail closed (503) when it is empty.
	t.Setenv(astaraServiceAuthEnv, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/astara/knowledge-model-config", nil)
	req.Header.Set("Authorization", "Bearer anything")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", recorder.Code)
	}
}

func TestKnowledgeModelConfigAppliesAndReadsBackRedacted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &stubAuthorizedModelRepo{rows: map[string]*types.Model{}}
	engine := modelConfigEngine(t, repo)
	recorder := modelConfigRequest(t, engine, http.MethodPost, validConfigBody(1))
	if recorder.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	row := repo.rows[types.PlaneOwnedKnowledgeQAModelID]
	if row == nil {
		t.Fatal("plane-owned row was not created")
	}
	if row.ManagedBy != types.PlaneManagedBy || row.Type != types.ModelTypeKnowledgeQA || !row.IsBuiltin {
		t.Fatalf("row identity wrong: managed_by=%q type=%q builtin=%v", row.ManagedBy, row.Type, row.IsBuiltin)
	}
	if row.Parameters.APIKey != "sk-test-secret" || row.Parameters.BaseURL != "https://models.example.invalid/v1" {
		t.Fatalf("row parameters not applied: base=%q", row.Parameters.BaseURL)
	}

	read := modelConfigRequest(t, engine, http.MethodGet, nil)
	if read.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", read.Code, read.Body.String())
	}
	body := read.Body.String()
	for _, want := range []string{`"revision":1`, `"model_name":"deepseek-chat"`, `"base_url":"https://models.example.invalid/v1"`, `"vendor_type":"openai-compatible"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("read-back missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "sk-test-secret") || strings.Contains(body, "api_key") {
		t.Fatalf("read-back leaked the credential: %s", body)
	}
}

func TestKnowledgeModelConfigRejectsDigestMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &stubAuthorizedModelRepo{rows: map[string]*types.Model{}}
	engine := modelConfigEngine(t, repo)
	body := validConfigBody(1)
	body["digest"] = strings.Repeat("f", 64)
	recorder := modelConfigRequest(t, engine, http.MethodPost, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
	if len(repo.rows) != 0 {
		t.Fatalf("digest-mismatch push mutated the repository: %v", repo.rows)
	}
}

func TestKnowledgeModelConfigRejectsStaleRevision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &stubAuthorizedModelRepo{rows: map[string]*types.Model{}}
	engine := modelConfigEngine(t, repo)
	if recorder := modelConfigRequest(t, engine, http.MethodPost, validConfigBody(5)); recorder.Code != http.StatusOK {
		t.Fatalf("first apply status=%d", recorder.Code)
	}
	// Same revision again is stale; a lower one is stale too.
	if recorder := modelConfigRequest(t, engine, http.MethodPost, validConfigBody(5)); recorder.Code != http.StatusConflict {
		t.Fatalf("replay status=%d, want 409", recorder.Code)
	}
	if recorder := modelConfigRequest(t, engine, http.MethodPost, validConfigBody(4)); recorder.Code != http.StatusConflict {
		t.Fatalf("lower revision status=%d, want 409", recorder.Code)
	}
	row := repo.rows[types.PlaneOwnedKnowledgeQAModelID]
	if row.Parameters.ExtraConfig[planeRevisionExtraKey] != "5" {
		t.Fatalf("applied revision=%q, want 5", row.Parameters.ExtraConfig[planeRevisionExtraKey])
	}
}

func TestKnowledgeModelConfigRejectsUnknownFieldsAndPadding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &stubAuthorizedModelRepo{rows: map[string]*types.Model{}}
	engine := modelConfigEngine(t, repo)
	unknown := validConfigBody(1)
	unknown["extra"] = "nope"
	if recorder := modelConfigRequest(t, engine, http.MethodPost, unknown); recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field status=%d, want 400", recorder.Code)
	}
	padded := validConfigBody(1)
	padded["base_url"] = " https://models.example.invalid/v1 "
	digestFields := &astaraKnowledgeModelConfigUpsert{
		ContractVersion: astaraModelConfigContractV1,
		Revision:        1,
		VendorType:      "openai-compatible",
		BaseURL:         " https://models.example.invalid/v1 ",
		ModelName:       "deepseek-chat",
		APIKey:          "sk-test-secret",
	}
	padded["digest"] = computeKnowledgeModelConfigDigest(canonicalConfigFields(digestFields))
	if recorder := modelConfigRequest(t, engine, http.MethodPost, padded); recorder.Code != http.StatusBadRequest {
		t.Fatalf("padded-field status=%d, want 400", recorder.Code)
	}
	if len(repo.rows) != 0 {
		t.Fatalf("rejected pushes mutated the repository: %v", repo.rows)
	}
}

func TestKnowledgeModelConfigRevokeClears(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &stubAuthorizedModelRepo{rows: map[string]*types.Model{}}
	engine := modelConfigEngine(t, repo)
	if recorder := modelConfigRequest(t, engine, http.MethodPost, validConfigBody(1)); recorder.Code != http.StatusOK {
		t.Fatalf("apply status=%d", recorder.Code)
	}
	if recorder := modelConfigRequest(t, engine, http.MethodDelete, nil); recorder.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, exists := repo.rows[types.PlaneOwnedKnowledgeQAModelID]; exists {
		t.Fatal("revoke left the plane-owned row behind")
	}
	read := modelConfigRequest(t, engine, http.MethodGet, nil)
	if !strings.Contains(read.Body.String(), `"revision":0`) {
		t.Fatalf("read-back after revoke: %s", read.Body.String())
	}
	// Idempotent second revoke.
	if recorder := modelConfigRequest(t, engine, http.MethodDelete, nil); recorder.Code != http.StatusOK {
		t.Fatalf("second revoke status=%d", recorder.Code)
	}
}
