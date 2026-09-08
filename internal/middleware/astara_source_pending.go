// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package middleware

import "github.com/gin-gonic/gin"

// RequireNativeSourceRoute denies embedded access to routes whose document
// scope has not been integrated with Plane source authorization. This is an
// admission closure, not proof that the feature is implemented or accepted.
func RequireNativeSourceRoute() gin.HandlerFunc {
	return func(c *gin.Context) {
		if isEmbeddedSession(c.Request.Context()) {
			writeSourceDeny404(c)
			return
		}
		c.Next()
	}
}
