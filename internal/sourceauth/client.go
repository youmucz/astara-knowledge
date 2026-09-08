// Copyright (c) 2024 Astara. All rights reserved.
// Use of this source code is governed by a license that can be found
// in the LICENSE file.

// Package sourceauth provides a standalone HTTP client for private Plane
// authorization. It validates document access against a remote authorization
// server using a shared secret and returns authorization snapshots.
package sourceauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

const (
	// maxBodySize is the maximum allowed response body size (512 KiB).
	maxBodySize int64 = 512 * 1024

	// maxRequestSize is the maximum allowed request body size (64 KiB).
	maxRequestSize int = 64 * 1024

	// defaultTimeout is the fixed HTTP client timeout (5 seconds).
	defaultTimeout = 5 * time.Second

	// contractVersion is the current authorization contract version.
	contractVersion = 1

	// maxDocuments is the maximum number of documents per request.
	maxDocuments = 1000
)

// hexRegex matches a 64-character hexadecimal string.
var hexRegex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ClientConfig holds the configuration for the authorization client.
type ClientConfig struct {
	// URL is the authorization server endpoint. Must be http or https,
	// must not contain credentials, fragments, or query parameters.
	URL string

	// Secret is the shared authorization secret. Must be at least 32 bytes.
	Secret string
}

// Document represents a document to authorize.
type Document struct {
	TenantID       string `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	KnowledgeID    string `json:"knowledge_id"`
}

// AuthorizationRequest is the POST body sent to the authorization server.
type AuthorizationRequest struct {
	ContractVersion int        `json:"contract_version"`
	UserID          string     `json:"user_id"`
	WorkspaceID     string     `json:"workspace_id"`
	Operation       string     `json:"operation"`
	Documents       []Document `json:"documents"`
}

// AuthorizedDocument is a document with its authorization revision.
type AuthorizedDocument struct {
	TenantID       string `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	KnowledgeID    string `json:"knowledge_id"`
	Revision       string `json:"revision"`
}

// AuthorizationResponse is the response from the authorization server.
type AuthorizationResponse struct {
	ContractVersion int                 `json:"contract_version"`
	Documents       []AuthorizedDocument `json:"documents"`
}

// AuthorizationResult contains the authorization snapshot or an error.
type AuthorizationResult struct {
	// Snapshot contains the authorized documents. Nil if authorization failed.
	Snapshot *AuthorizationResponse

	// Err is any error that occurred during authorization.
	Err error
}

// Client is the standalone authorization client.
type Client struct {
	config     ClientConfig
	httpClient *http.Client
}

// NewClient creates a new authorization client with the given configuration.
// Returns an error if the configuration is invalid.
func NewClient(config ClientConfig) (*Client, error) {
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Create HTTP client with strict settings
	httpClient := &http.Client{
		Timeout: defaultTimeout,
		Transport: &http.Transport{
			DisableKeepAlives:  true,
			DisableCompression: true,
			Proxy:              nil, // Disable proxy
		},
		// Refuse redirects
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("redirects not allowed")
		},
	}

	return &Client{
		config:     config,
		httpClient: httpClient,
	}, nil
}

// validateConfig validates the client configuration.
func validateConfig(config ClientConfig) error {
	if config.URL == "" {
		return fmt.Errorf("URL is required")
	}

	// Parse URL
	parsedURL, err := url.Parse(config.URL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Must be http or https
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("URL must use http or https scheme")
	}

	// Reject opaque URLs
	if parsedURL.Opaque != "" {
		return fmt.Errorf("URL must not be opaque")
	}

	// Require non-empty host
	if parsedURL.Hostname() == "" {
		return fmt.Errorf("URL must have a host")
	}

	// Reject credentials
	if parsedURL.User != nil {
		return fmt.Errorf("URL must not contain credentials")
	}

	// Reject fragment
	if parsedURL.Fragment != "" {
		return fmt.Errorf("URL must not contain fragment")
	}

	// Reject query parameters
	if parsedURL.RawQuery != "" || parsedURL.ForceQuery {
		return fmt.Errorf("URL must not contain query parameters")
	}

	// Validate secret length
	if len(config.Secret) < 32 {
		return fmt.Errorf("secret must be at least 32 bytes")
	}

	return nil
}

// Authorize requests authorization for the given documents.
// Returns an authorization snapshot on success, or an error.
// All-or-nothing: if any validation fails, no authorization is granted.
func (c *Client) Authorize(ctx context.Context, userID, workspaceID, operation string, documents []Document) AuthorizationResult {
	// Validate inputs
	if err := validateRequest(userID, workspaceID, operation, documents); err != nil {
		return AuthorizationResult{Err: fmt.Errorf("invalid request: %w", err)}
	}

	// Build request
	req := AuthorizationRequest{
		ContractVersion: contractVersion,
		UserID:          userID,
		WorkspaceID:     workspaceID,
		Operation:       operation,
		Documents:       documents,
	}

	// Marshal request body
	body, err := json.Marshal(req)
	if err != nil {
		return AuthorizationResult{Err: fmt.Errorf("failed to marshal request: %w", err)}
	}

	// Check request size bound
	if len(body) > maxRequestSize {
		return AuthorizationResult{Err: fmt.Errorf("request body exceeds maximum size of %d bytes", maxRequestSize)}
	}

	// Create HTTP request with context
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.URL, bytes.NewReader(body))
	if err != nil {
		return AuthorizationResult{Err: fmt.Errorf("failed to create request: %w", err)}
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.config.Secret)

	// Execute request
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return AuthorizationResult{Err: fmt.Errorf("request failed: %w", err)}
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		// Read and discard body for non-200 responses
		_, _ = io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
		return AuthorizationResult{Err: fmt.Errorf("unexpected status code: %d", resp.StatusCode)}
	}

	// Read response body with size limit
	limitedBody := io.LimitReader(resp.Body, maxBodySize+1)
	bodyBytes, err := io.ReadAll(limitedBody)
	if err != nil {
		return AuthorizationResult{Err: fmt.Errorf("failed to read response body: %w", err)}
	}

	// Check if body exceeded limit
	if int64(len(bodyBytes)) > maxBodySize {
		return AuthorizationResult{Err: fmt.Errorf("response body exceeds maximum size")}
	}

	// Use strict JSON decoder to detect unknown fields and trailing data
	var authResp AuthorizationResponse
	decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&authResp); err != nil {
		return AuthorizationResult{Err: fmt.Errorf("failed to decode response: %w", err)}
	}

	// Check for trailing data - must get io.EOF after decoding the single object
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return AuthorizationResult{Err: fmt.Errorf("response contains trailing data")}
	}

	// Validate response
	if err := validateResponse(authResp, documents); err != nil {
		return AuthorizationResult{Err: fmt.Errorf("invalid response: %w", err)}
	}

	return AuthorizationResult{Snapshot: &authResp}
}

// validateRequest validates the authorization request parameters.
func validateRequest(userID, workspaceID, operation string, documents []Document) error {
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}

	if workspaceID == "" {
		return fmt.Errorf("workspace_id is required")
	}

	// Validate operation
	if operation != "query" && operation != "read" {
		return fmt.Errorf("operation must be 'query' or 'read'")
	}

	// Validate documents
	if len(documents) == 0 {
		return fmt.Errorf("documents must be non-empty")
	}

	if len(documents) > maxDocuments {
		return fmt.Errorf("documents exceeds maximum count of %d", maxDocuments)
	}

	// Check for duplicate documents
	seen := make(map[Document]bool)
	for i, doc := range documents {
		if doc.TenantID == "" {
			return fmt.Errorf("documents[%d].tenant_id is required", i)
		}
		if doc.KnowledgeBaseID == "" {
			return fmt.Errorf("documents[%d].knowledge_base_id is required", i)
		}
		if doc.KnowledgeID == "" {
			return fmt.Errorf("documents[%d].knowledge_id is required", i)
		}

		if seen[doc] {
			return fmt.Errorf("duplicate document at index %d", i)
		}
		seen[doc] = true
	}

	return nil
}

// validateResponse validates the authorization response against the request.
func validateResponse(resp AuthorizationResponse, requestDocs []Document) error {
	// Check contract version
	if resp.ContractVersion != contractVersion {
		return fmt.Errorf("unexpected contract version: %d", resp.ContractVersion)
	}

	// Check document count matches
	if len(resp.Documents) != len(requestDocs) {
		return fmt.Errorf("response document count %d does not match request count %d", len(resp.Documents), len(requestDocs))
	}

	// Validate each document matches in order
	for i, respDoc := range resp.Documents {
		reqDoc := requestDocs[i]

		// Check exact match of tenant_id, knowledge_base_id, knowledge_id
		if respDoc.TenantID != reqDoc.TenantID {
			return fmt.Errorf("documents[%d].tenant_id mismatch", i)
		}
		if respDoc.KnowledgeBaseID != reqDoc.KnowledgeBaseID {
			return fmt.Errorf("documents[%d].knowledge_base_id mismatch", i)
		}
		if respDoc.KnowledgeID != reqDoc.KnowledgeID {
			return fmt.Errorf("documents[%d].knowledge_id mismatch", i)
		}

		// Validate revision format (64 hex characters)
		if !hexRegex.MatchString(respDoc.Revision) {
			return fmt.Errorf("documents[%d].revision must be 64 hex characters", i)
		}
	}

	return nil
}
