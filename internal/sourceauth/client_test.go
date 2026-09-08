// Copyright (c) 2024 Astara. All rights reserved.
// Use of this source code is governed by a license that can be found
// in the LICENSE file.

package sourceauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testSecret is a 32-byte secret for testing.
var testSecret = strings.Repeat("a", 32)

// testDocuments returns a slice of test documents.
func testDocuments() []Document {
	return []Document{
		{
			TenantID:        "tenant1",
			KnowledgeBaseID: "kb1",
			KnowledgeID:     "k1",
		},
		{
			TenantID:        "tenant1",
			KnowledgeBaseID: "kb1",
			KnowledgeID:     "k2",
		},
	}
}

// testResponse returns a valid authorization response for test documents.
func testResponse() AuthorizationResponse {
	return AuthorizationResponse{
		ContractVersion: 1,
		Documents: []AuthorizedDocument{
			{
				TenantID:        "tenant1",
				KnowledgeBaseID: "kb1",
				KnowledgeID:     "k1",
				Revision:        strings.Repeat("a", 64),
			},
			{
				TenantID:        "tenant1",
				KnowledgeBaseID: "kb1",
				KnowledgeID:     "k2",
				Revision:        strings.Repeat("b", 64),
			},
		},
	}
}

func TestNewClient_ValidConfig(t *testing.T) {
	config := ClientConfig{
		URL:    "https://auth.example.com/authorize",
		Secret: testSecret,
	}

	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if client == nil {
		t.Fatal("expected client, got nil")
	}
}

func TestNewClient_EmptyURL(t *testing.T) {
	config := ClientConfig{
		URL:    "",
		Secret: testSecret,
	}

	_, err := NewClient(config)
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
	if !strings.Contains(err.Error(), "URL is required") {
		t.Fatalf("expected 'URL is required' error, got %v", err)
	}
}

func TestNewClient_InvalidURLScheme(t *testing.T) {
	config := ClientConfig{
		URL:    "ftp://auth.example.com/authorize",
		Secret: testSecret,
	}

	_, err := NewClient(config)
	if err == nil {
		t.Fatal("expected error for non-http(s) scheme")
	}
	if !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("expected 'http or https' error, got %v", err)
	}
}

func TestNewClient_URLWithCredentials(t *testing.T) {
	config := ClientConfig{
		URL:    "https://user:pass@auth.example.com/authorize",
		Secret: testSecret,
	}

	_, err := NewClient(config)
	if err == nil {
		t.Fatal("expected error for URL with credentials")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("expected 'credentials' error, got %v", err)
	}
}

func TestNewClient_URLWithFragment(t *testing.T) {
	config := ClientConfig{
		URL:    "https://auth.example.com/authorize#fragment",
		Secret: testSecret,
	}

	_, err := NewClient(config)
	if err == nil {
		t.Fatal("expected error for URL with fragment")
	}
	if !strings.Contains(err.Error(), "fragment") {
		t.Fatalf("expected 'fragment' error, got %v", err)
	}
}

func TestNewClient_URLWithQuery(t *testing.T) {
	config := ClientConfig{
		URL:    "https://auth.example.com/authorize?key=value",
		Secret: testSecret,
	}

	_, err := NewClient(config)
	if err == nil {
		t.Fatal("expected error for URL with query")
	}
	if !strings.Contains(err.Error(), "query") {
		t.Fatalf("expected 'query' error, got %v", err)
	}
}

func TestNewClient_ShortSecret(t *testing.T) {
	config := ClientConfig{
		URL:    "https://auth.example.com/authorize",
		Secret: "short",
	}

	_, err := NewClient(config)
	if err == nil {
		t.Fatal("expected error for short secret")
	}
	if !strings.Contains(err.Error(), "32 bytes") {
		t.Fatalf("expected '32 bytes' error, got %v", err)
	}
}

func TestAuthorize_Success(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify method
		if r.Method != http.MethodPost {
			t.Errorf("expected POST method, got %s", r.Method)
		}

		// Verify Content-Type
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}

		// Verify Authorization header
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			t.Errorf("expected Authorization Bearer %s, got %s", testSecret, r.Header.Get("Authorization"))
		}

		// Decode request
		var req AuthorizationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Verify request fields
		if req.ContractVersion != 1 {
			t.Errorf("expected contract_version 1, got %d", req.ContractVersion)
		}
		if req.UserID != "user1" {
			t.Errorf("expected user_id user1, got %s", req.UserID)
		}
		if req.WorkspaceID != "workspace1" {
			t.Errorf("expected workspace_id workspace1, got %s", req.WorkspaceID)
		}
		if req.Operation != "read" {
			t.Errorf("expected operation read, got %s", req.Operation)
		}

		// Send response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(testResponse())
	}))
	defer server.Close()

	// Create client
	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// Execute authorization
	result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())

	// Verify result
	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	if result.Snapshot == nil {
		t.Fatal("expected snapshot, got nil")
	}

	// Verify snapshot
	resp := testResponse()
	if result.Snapshot.ContractVersion != resp.ContractVersion {
		t.Errorf("expected contract_version %d, got %d", resp.ContractVersion, result.Snapshot.ContractVersion)
	}
	if len(result.Snapshot.Documents) != len(resp.Documents) {
		t.Fatalf("expected %d documents, got %d", len(resp.Documents), len(result.Snapshot.Documents))
	}

	for i, doc := range result.Snapshot.Documents {
		expected := resp.Documents[i]
		if doc.TenantID != expected.TenantID {
			t.Errorf("documents[%d].tenant_id: expected %s, got %s", i, expected.TenantID, doc.TenantID)
		}
		if doc.KnowledgeBaseID != expected.KnowledgeBaseID {
			t.Errorf("documents[%d].knowledge_base_id: expected %s, got %s", i, expected.KnowledgeBaseID, doc.KnowledgeBaseID)
		}
		if doc.KnowledgeID != expected.KnowledgeID {
			t.Errorf("documents[%d].knowledge_id: expected %s, got %s", i, expected.KnowledgeID, doc.KnowledgeID)
		}
		if doc.Revision != expected.Revision {
			t.Errorf("documents[%d].revision: expected %s, got %s", i, expected.Revision, doc.Revision)
		}
	}
}

func TestAuthorize_ResponseMismatch(t *testing.T) {
	// Create test server that returns mismatched documents
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := AuthorizationResponse{
			ContractVersion: 1,
			Documents: []AuthorizedDocument{
				{
					TenantID:        "tenant1",
					KnowledgeBaseID: "kb1",
					KnowledgeID:     "k1",
					Revision:        strings.Repeat("a", 64),
				},
				{
					TenantID:        "tenant1",
					KnowledgeBaseID: "kb1",
					KnowledgeID:     "k3", // Mismatch: k3 vs k2
					Revision:        strings.Repeat("b", 64),
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for mismatched response")
	}
	if !strings.Contains(result.Err.Error(), "knowledge_id mismatch") {
		t.Fatalf("expected 'knowledge_id mismatch' error, got %v", result.Err)
	}
}

func TestAuthorize_ResponseReorder(t *testing.T) {
	// Create test server that returns documents in different order
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := AuthorizationResponse{
			ContractVersion: 1,
			Documents: []AuthorizedDocument{
				{
					TenantID:        "tenant1",
					KnowledgeBaseID: "kb1",
					KnowledgeID:     "k2", // Reordered
					Revision:        strings.Repeat("b", 64),
				},
				{
					TenantID:        "tenant1",
					KnowledgeBaseID: "kb1",
					KnowledgeID:     "k1",
					Revision:        strings.Repeat("a", 64),
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for reordered response")
	}
	if !strings.Contains(result.Err.Error(), "knowledge_id mismatch") {
		t.Fatalf("expected 'knowledge_id mismatch' error, got %v", result.Err)
	}
}

func TestAuthorize_EmptyResponse(t *testing.T) {
	// Create test server that returns empty documents
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := AuthorizationResponse{
			ContractVersion: 1,
			Documents:       []AuthorizedDocument{},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for empty response")
	}
	if !strings.Contains(result.Err.Error(), "document count") {
		t.Fatalf("expected 'document count' error, got %v", result.Err)
	}
}

func TestAuthorize_Redirect(t *testing.T) {
	// Create test server that redirects
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com", http.StatusFound)
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for redirect")
	}
	if !strings.Contains(result.Err.Error(), "redirect") {
		t.Fatalf("expected 'redirect' error, got %v", result.Err)
	}
}

func TestAuthorize_OversizeResponse(t *testing.T) {
	// Create test server that returns oversize response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Create a response that exceeds 512 KiB
		largeRevision := strings.Repeat("a", int(maxBodySize)+1)
		resp := AuthorizationResponse{
			ContractVersion: 1,
			Documents: []AuthorizedDocument{
				{
					TenantID:        "tenant1",
					KnowledgeBaseID: "kb1",
					KnowledgeID:     "k1",
					Revision:        largeRevision,
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for oversize response")
	}
}

func TestAuthorize_ContextCancellation(t *testing.T) {
	// Create test server with delay
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		json.NewEncoder(w).Encode(testResponse())
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// Create context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := client.Authorize(ctx, "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for context cancellation")
	}
}

func TestAuthorize_BadSecretConfig(t *testing.T) {
	// Create test server that checks secret
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(testResponse())
	}))
	defer server.Close()

	// Use wrong secret
	config := ClientConfig{
		URL:    server.URL,
		Secret: strings.Repeat("b", 32),
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for bad secret")
	}
	if !strings.Contains(result.Err.Error(), "401") {
		t.Fatalf("expected '401' error, got %v", result.Err)
	}
}

func TestAuthorize_InvalidResponseContractVersion(t *testing.T) {
	// Create test server with wrong contract version
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := AuthorizationResponse{
			ContractVersion: 2, // Wrong version
			Documents: []AuthorizedDocument{
				{
					TenantID:        "tenant1",
					KnowledgeBaseID: "kb1",
					KnowledgeID:     "k1",
					Revision:        strings.Repeat("a", 64),
				},
				{
					TenantID:        "tenant1",
					KnowledgeBaseID: "kb1",
					KnowledgeID:     "k2",
					Revision:        strings.Repeat("b", 64),
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for wrong contract version")
	}
	if !strings.Contains(result.Err.Error(), "contract version") {
		t.Fatalf("expected 'contract version' error, got %v", result.Err)
	}
}

func TestAuthorize_InvalidRevisionFormat(t *testing.T) {
	// Create test server with invalid revision
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := AuthorizationResponse{
			ContractVersion: 1,
			Documents: []AuthorizedDocument{
				{
					TenantID:        "tenant1",
					KnowledgeBaseID: "kb1",
					KnowledgeID:     "k1",
					Revision:        "invalid", // Not 64 hex chars
				},
				{
					TenantID:        "tenant1",
					KnowledgeBaseID: "kb1",
					KnowledgeID:     "k2",
					Revision:        strings.Repeat("b", 64),
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for invalid revision format")
	}
	if !strings.Contains(result.Err.Error(), "64 hex") {
		t.Fatalf("expected '64 hex' error, got %v", result.Err)
	}
}

func TestAuthorize_Non200Status(t *testing.T) {
	// Create test server that returns non-200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for non-200 status")
	}
	if !strings.Contains(result.Err.Error(), "500") {
		t.Fatalf("expected '500' error, got %v", result.Err)
	}
}

func TestAuthorize_UnknownFields(t *testing.T) {
	// Create test server with unknown fields
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Use raw JSON to include unknown fields
		resp := `{
			"contract_version": 1,
			"unknown_field": "value",
			"documents": [
				{
					"tenant_id": "tenant1",
					"knowledge_base_id": "kb1",
					"knowledge_id": "k1",
					"revision": "` + strings.Repeat("a", 64) + `"
				},
				{
					"tenant_id": "tenant1",
					"knowledge_base_id": "kb1",
					"knowledge_id": "k2",
					"revision": "` + strings.Repeat("b", 64) + `"
				}
			]
		}`

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, resp)
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for unknown fields")
	}
}

func TestAuthorize_EmptyDocumentsRequest(t *testing.T) {
	config := ClientConfig{
		URL:    "https://auth.example.com/authorize",
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", []Document{})

	if result.Err == nil {
		t.Fatal("expected error for empty documents")
	}
	if !strings.Contains(result.Err.Error(), "non-empty") {
		t.Fatalf("expected 'non-empty' error, got %v", result.Err)
	}
}

func TestAuthorize_DuplicateDocuments(t *testing.T) {
	config := ClientConfig{
		URL:    "https://auth.example.com/authorize",
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	docs := []Document{
		{TenantID: "t1", KnowledgeBaseID: "kb1", KnowledgeID: "k1"},
		{TenantID: "t1", KnowledgeBaseID: "kb1", KnowledgeID: "k1"}, // Duplicate
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", docs)

	if result.Err == nil {
		t.Fatal("expected error for duplicate documents")
	}
	if !strings.Contains(result.Err.Error(), "duplicate") {
		t.Fatalf("expected 'duplicate' error, got %v", result.Err)
	}
}

func TestAuthorize_InvalidOperation(t *testing.T) {
	config := ClientConfig{
		URL:    "https://auth.example.com/authorize",
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "invalid", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for invalid operation")
	}
	if !strings.Contains(result.Err.Error(), "query") || !strings.Contains(result.Err.Error(), "read") {
		t.Fatalf("expected 'query' or 'read' in error, got %v", result.Err)
	}
}

func TestAuthorize_MissingUserID(t *testing.T) {
	config := ClientConfig{
		URL:    "https://auth.example.com/authorize",
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for missing user_id")
	}
	if !strings.Contains(result.Err.Error(), "user_id") {
		t.Fatalf("expected 'user_id' error, got %v", result.Err)
	}
}

func TestAuthorize_MissingWorkspaceID(t *testing.T) {
	config := ClientConfig{
		URL:    "https://auth.example.com/authorize",
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for missing workspace_id")
	}
	if !strings.Contains(result.Err.Error(), "workspace_id") {
		t.Fatalf("expected 'workspace_id' error, got %v", result.Err)
	}
}

func TestAuthorize_MissingDocumentField(t *testing.T) {
	config := ClientConfig{
		URL:    "https://auth.example.com/authorize",
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	docs := []Document{
		{TenantID: "", KnowledgeBaseID: "kb1", KnowledgeID: "k1"},
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", docs)

	if result.Err == nil {
		t.Fatal("expected error for missing document field")
	}
	if !strings.Contains(result.Err.Error(), "tenant_id") {
		t.Fatalf("expected 'tenant_id' error, got %v", result.Err)
	}
}

func TestAuthorize_ContextTimeout(t *testing.T) {
	// Create test server with delay
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		json.NewEncoder(w).Encode(testResponse())
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	result := client.Authorize(ctx, "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for context timeout")
	}
}

func TestAuthorize_NoCache(t *testing.T) {
	// Create test server that counts requests
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		json.NewEncoder(w).Encode(testResponse())
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// Make multiple requests
	for i := 0; i < 3; i++ {
		result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())
		if result.Err != nil {
			t.Fatalf("request %d: expected no error, got %v", i, result.Err)
		}
	}

	// Verify all requests were made (no caching)
	if requestCount != 3 {
		t.Fatalf("expected 3 requests, got %d", requestCount)
	}
}

func TestAuthorize_ContextCancelled(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(testResponse())
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := client.Authorize(ctx, "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestAuthorize_HTTPAllowedURL(t *testing.T) {
	// Verify that HTTP URLs are allowed
	config := ClientConfig{
		URL:    "http://auth.example.com/authorize",
		Secret: testSecret,
	}

	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("expected no error for HTTP URL, got %v", err)
	}
	if client == nil {
		t.Fatal("expected client, got nil")
	}
}

func TestAuthorize_HTTPSAllowedURL(t *testing.T) {
	// Verify that HTTPS URLs are allowed
	config := ClientConfig{
		URL:    "https://auth.example.com/authorize",
		Secret: testSecret,
	}

	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("expected no error for HTTPS URL, got %v", err)
	}
	if client == nil {
		t.Fatal("expected client, got nil")
	}
}

func TestNewClient_OpaqueURL(t *testing.T) {
	config := ClientConfig{
		URL:    "http:auth.example.com/authorize",
		Secret: testSecret,
	}

	_, err := NewClient(config)
	if err == nil {
		t.Fatal("expected error for opaque URL")
	}
	if !strings.Contains(err.Error(), "opaque") {
		t.Fatalf("expected 'opaque' error, got %v", err)
	}
}

func TestNewClient_EmptyHostURL(t *testing.T) {
	config := ClientConfig{
		URL:    "http:///path",
		Secret: testSecret,
	}

	_, err := NewClient(config)
	if err == nil {
		t.Fatal("expected error for empty host URL")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Fatalf("expected 'host' error, got %v", err)
	}
}

func TestAuthorize_TrailingMalformedData(t *testing.T) {
	// Create test server with trailing malformed JSON
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{
			"contract_version": 1,
			"documents": [
				{
					"tenant_id": "tenant1",
					"knowledge_base_id": "kb1",
					"knowledge_id": "k1",
					"revision": "` + strings.Repeat("a", 64) + `"
				},
				{
					"tenant_id": "tenant1",
					"knowledge_base_id": "kb1",
					"knowledge_id": "k2",
					"revision": "` + strings.Repeat("b", 64) + `"
				}
			]
		}{"malformed": trailing}`

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, resp)
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for trailing malformed data")
	}
	if !strings.Contains(result.Err.Error(), "trailing data") {
		t.Fatalf("expected 'trailing data' error, got %v", result.Err)
	}
}

func TestAuthorize_SecondObjectTrailing(t *testing.T) {
	// Create test server with second valid JSON object
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{
			"contract_version": 1,
			"documents": [
				{
					"tenant_id": "tenant1",
					"knowledge_base_id": "kb1",
					"knowledge_id": "k1",
					"revision": "` + strings.Repeat("a", 64) + `"
				},
				{
					"tenant_id": "tenant1",
					"knowledge_base_id": "kb1",
					"knowledge_id": "k2",
					"revision": "` + strings.Repeat("b", 64) + `"
				}
			]
		}
		{"contract_version": 1, "documents": []}`

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, resp)
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", testDocuments())

	if result.Err == nil {
		t.Fatal("expected error for second trailing object")
	}
	if !strings.Contains(result.Err.Error(), "trailing data") {
		t.Fatalf("expected 'trailing data' error, got %v", result.Err)
	}
}

func TestAuthorize_RequestSizeBound(t *testing.T) {
	// Create test server (should never be reached)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(testResponse())
	}))
	defer server.Close()

	config := ClientConfig{
		URL:    server.URL,
		Secret: testSecret,
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// Create 1000 documents that exceed 64KB request size limit (~76KB)
	docs := make([]Document, 1000)
	for i := range docs {
		docs[i] = Document{
			TenantID:        fmt.Sprintf("tenant%d", i),
			KnowledgeBaseID: fmt.Sprintf("kb%d", i),
			KnowledgeID:     fmt.Sprintf("k%d", i),
		}
	}

	result := client.Authorize(context.Background(), "user1", "workspace1", "read", docs)

	if result.Err == nil {
		t.Fatal("expected error for oversized request")
	}
	if !strings.Contains(result.Err.Error(), "maximum size") {
		t.Fatalf("expected 'maximum size' error, got %v", result.Err)
	}
}
