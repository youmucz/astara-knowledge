// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package handler

import (
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
)

// writeEmbeddedSourceFile avoids Gin Stream/Flush so the route's source guard
// can perform fresh authorization before committing any file bytes.
func writeEmbeddedSourceFile(c *gin.Context, file io.Reader) bool {
	embedded, _ := c.Request.Context().Value(types.EmbeddedSessionContextKey).(bool)
	if !embedded {
		return false
	}
	const limit = 2 << 20
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(body) > limit {
		for key := range c.Writer.Header() {
			c.Writer.Header().Del(key)
		}
		c.Header("Cache-Control", "private, no-store")
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"success": false, "error": gin.H{"code": "NOT_FOUND", "message": "Resource not found"}})
		return true
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Pragma", "no-cache")
	_, _ = c.Writer.Write(body)
	return true
}
