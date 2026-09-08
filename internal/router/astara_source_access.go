// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/sourceauth"
	"github.com/gin-gonic/gin"
)

func (g *rbacGuards) SourceBatchRead() gin.HandlerFunc {
	client, err := sourceauth.NewFromEnvironment()
	var authorize middleware.Authorizer
	if err == nil {
		authorize = client.Authorize
	}
	return middleware.SourceBatchReadGuard(g.knowledgeService, authorize)
}

func (g *rbacGuards) SourceChunkRead(param string) gin.HandlerFunc {
	client, err := sourceauth.NewFromEnvironment()
	var authorize middleware.Authorizer
	if err == nil {
		authorize = client.Authorize
	}
	return middleware.SourceChunkReadGuard(param, g.chunkService, g.knowledgeService, authorize)
}

// SourceDocumentRead must precede KB access middleware, which can rewrite
// tenant context to the source KB. Missing configuration is deny-only for
// embedded requests; the ordinary native authentication path is unchanged.
func (g *rbacGuards) SourceDocumentRead(param string) gin.HandlerFunc {
	client, err := sourceauth.NewFromEnvironment()
	var authorize middleware.Authorizer
	if err == nil {
		authorize = client.Authorize
	}
	return middleware.SourceAccessGuard(param, g.knowledgeService, authorize)
}
