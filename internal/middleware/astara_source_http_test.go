// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/sourceauth"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSourceGuardHTTPCallbackRevocation(t *testing.T) {
	secret := strings.Repeat("s", 32)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+secret {
			w.WriteHeader(403)
			return
		}
		var request sourceauth.AuthorizationRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.UserID != testSubject || request.WorkspaceID != testWorkspace || request.Operation != "read" || len(request.Documents) != 1 {
			w.WriteHeader(400)
			return
		}
		if calls.Add(1) == 2 {
			w.WriteHeader(403)
			return
		}
		d := request.Documents[0]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sourceauth.AuthorizationResponse{ContractVersion: 1, Documents: []sourceauth.AuthorizedDocument{{TenantID: d.TenantID, KnowledgeBaseID: d.KnowledgeBaseID, KnowledgeID: d.KnowledgeID, Revision: strings.Repeat("a", 64)}}})
	}))
	defer server.Close()
	t.Setenv("ASTARA_SOURCE_AUTH_URL", server.URL)
	t.Setenv("ASTARA_SOURCE_AUTH_SECRET", secret)
	client, err := sourceauth.NewFromEnvironment()
	require.NoError(t, err)
	lookup := &stubKnowledgeLookup{knowledge: map[string]*types.Knowledge{"k-1": validKnowledge()}}
	rec := serveGuard(t, "id", lookup, client.Authorize, defaultHandler("sensitive body"))
	require.Equal(t, int32(2), calls.Load())
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NotContains(t, rec.Body.String(), "sensitive body")
}
