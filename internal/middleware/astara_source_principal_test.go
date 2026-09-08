// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package middleware

import (
	"github.com/Tencent/WeKnora/internal/types"
	"testing"
)

func TestSourcePrincipalRequiresBothExternalMappings(t *testing.T) {
	for _, mode := range []string{"valid", "native-user", "native-tenant", "missing", "inactive"} {
		t.Run(mode, func(t *testing.T) {
			system, subject, workspace := "astara", "plane-user", "plane-workspace"
			user := &types.User{ID: "provider-user", IsActive: true, ExternalSystem: &system, ExternalID: &subject}
			tenant := &types.Tenant{ID: 7, Status: "active", ExternalSystem: &system, ExternalID: &workspace}
			switch mode {
			case "native-user":
				user.ExternalSystem = nil
			case "native-tenant":
				tenant.ExternalSystem = nil
			case "missing":
				tenant.ExternalID = nil
			case "inactive":
				user.IsActive = false
			}
			u, w, ok := sourcePrincipalIDs(user, tenant)
			if mode == "valid" {
				if !ok || u != subject || w != workspace {
					t.Fatal("external principal was not retained")
				}
			} else if ok || u != "" || w != "" {
				t.Fatal("untrusted principal mapping admitted")
			}
		})
	}
}
