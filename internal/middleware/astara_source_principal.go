// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package middleware

import (
	"github.com/Tencent/WeKnora/internal/types"
	"strings"
)

// sourcePrincipalIDs accepts only server-resolved external identities. Request
// headers, effective shared-KB tenants, and provider user IDs are not Plane IDs.
func sourcePrincipalIDs(user *types.User, tenant *types.Tenant) (string, string, bool) {
	if user == nil || !user.IsActive || tenant == nil || tenant.Status != "active" ||
		user.ExternalSystem == nil || *user.ExternalSystem != "astara" ||
		tenant.ExternalSystem == nil || *tenant.ExternalSystem != "astara" ||
		user.ExternalID == nil || tenant.ExternalID == nil {
		return "", "", false
	}
	subject, workspace := *user.ExternalID, *tenant.ExternalID
	if subject == "" || workspace == "" || strings.TrimSpace(subject) != subject || strings.TrimSpace(workspace) != workspace {
		return "", "", false
	}
	return subject, workspace, true
}
