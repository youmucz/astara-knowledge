// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package sourceauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRevalidateChecksEveryInputRevision(t *testing.T) {
	snapshot := &AuthorizationResponse{ContractVersion: 1, Documents: []AuthorizedDocument{
		{TenantID: "7", KnowledgeBaseID: "kb", KnowledgeID: "one", Revision: strings.Repeat("a", 64)},
		{TenantID: "7", KnowledgeBaseID: "kb", KnowledgeID: "omitted", Revision: strings.Repeat("b", 64)},
	}}
	changed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req AuthorizationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Documents) != 2 {
			t.Error("incomplete revalidation request")
			w.WriteHeader(400)
			return
		}
		response := *snapshot
		response.Documents = append([]AuthorizedDocument(nil), snapshot.Documents...)
		if changed {
			response.Documents[1].Revision = strings.Repeat("c", 64)
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{URL: server.URL, Secret: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	if !client.Revalidate(context.Background(), "user", "workspace", "query", snapshot) {
		t.Fatal("stable snapshot rejected")
	}
	changed = true
	if client.Revalidate(context.Background(), "user", "workspace", "query", snapshot) {
		t.Fatal("omitted document revision drift accepted")
	}
	if client.Revalidate(context.Background(), "user", "workspace", "query", nil) {
		t.Fatal("missing snapshot accepted")
	}
}
