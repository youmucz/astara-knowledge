// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestPendingSourceRouteAdmission(t *testing.T) {
	for _, embedded := range []bool{true, false} {
		engine := gin.New()
		called := false
		engine.GET("/search", RequireNativeSourceRoute(), func(c *gin.Context) { called = true; c.String(200, "result") })
		req := httptest.NewRequest("GET", "/search", nil)
		if embedded {
			req = req.WithContext(embeddedContext(req))
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if embedded {
			require.False(t, called)
			require.Equal(t, 404, rec.Code)
			require.Equal(t, sourceAccessFixedDenyBodyJSON, rec.Body.String())
			require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
		} else {
			require.True(t, called)
			require.Equal(t, 200, rec.Code)
		}
	}
}
