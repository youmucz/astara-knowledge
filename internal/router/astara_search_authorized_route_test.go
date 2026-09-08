// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchAuthorizedRouteAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, secret, token string
		want                int
	}{
		{"unset", "", "anything", 503},
		{"missing", "search-test-secret", "", 401},
		{"wrong", "search-test-secret", "user.jwt", 401},
		{"service", "search-test-secret", "search-test-secret", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ASTARA_SERVICE_AUTH_SECRET", tc.secret)
			engine := gin.New()
			RegisterAstaraSearchAuthorizedRoute(engine.Group("/api/v1"), handler.NewAstaraSearchAuthorizedHandler(nil, nil))
			req := httptest.NewRequest("POST", "/api/v1/astara/search-authorized", strings.NewReader(`{}`))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d", rec.Code, tc.want)
			}
		})
	}
}
