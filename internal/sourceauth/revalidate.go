// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package sourceauth

import "context"

// Revalidate makes a fresh request over every input document, including those
// omitted from generated references. Any changed revision rejects delivery.
func (c *Client) Revalidate(ctx context.Context, userID, workspaceID, operation string, snapshot *AuthorizationResponse) bool {
	if c == nil || snapshot == nil || snapshot.ContractVersion != contractVersion || len(snapshot.Documents) == 0 {
		return false
	}
	docs := make([]Document, len(snapshot.Documents))
	for i, doc := range snapshot.Documents {
		if !hexRegex.MatchString(doc.Revision) {
			return false
		}
		docs[i] = Document{TenantID: doc.TenantID, KnowledgeBaseID: doc.KnowledgeBaseID, KnowledgeID: doc.KnowledgeID}
	}
	result := c.Authorize(ctx, userID, workspaceID, operation, docs)
	if result.Err != nil || result.Snapshot == nil || len(result.Snapshot.Documents) != len(snapshot.Documents) {
		return false
	}
	for i, doc := range snapshot.Documents {
		if result.Snapshot.Documents[i] != doc {
			return false
		}
	}
	return true
}
