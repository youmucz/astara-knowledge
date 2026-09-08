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

func TestReadAuthorizedRouteRequiresServiceCredential(t *testing.T) {
	t.Setenv("ASTARA_SERVICE_AUTH_SECRET", "read-route-test-secret")
	engine := gin.New()
	RegisterAstaraReadAuthorizedRoute(engine.Group("/api/v1"), handler.NewAstaraReadAuthorizedHandler(nil))
	for _, token := range []string{"", "wrong", "user.jwt.token"} {
		req := httptest.NewRequest("POST", "/api/v1/astara/read-authorized", strings.NewReader(`{}`))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("non-service credential admitted: %d", rec.Code)
		}
	}
}
func TestReadAuthorizedRouteRefusesUnsetServiceSecret(t *testing.T) {
	t.Setenv("ASTARA_SERVICE_AUTH_SECRET", "")
	engine := gin.New()
	RegisterAstaraReadAuthorizedRoute(engine.Group("/api/v1"), handler.NewAstaraReadAuthorizedHandler(nil))
	req := httptest.NewRequest("POST", "/api/v1/astara/read-authorized", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("unconfigured service route must deny: %d", rec.Code)
	}
}

func TestReadAuthorizedRouteAbsentWithoutHandler(t *testing.T) {
	engine := gin.New()
	RegisterAstaraReadAuthorizedRoute(engine.Group("/api/v1"), nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/astara/read-authorized", nil))
	if rec.Code != 404 {
		t.Fatalf("nil handler exposed route: %d", rec.Code)
	}
}
