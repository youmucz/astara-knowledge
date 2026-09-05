package handler

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

const astaraServiceAuthEnv = "ASTARA_SERVICE_AUTH_SECRET"

// astaraServiceAuth authenticates private control-plane calls with the
// shared bearer secret. Fails closed when the secret is unconfigured.
func astaraServiceAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		secret := strings.TrimSpace(os.Getenv(astaraServiceAuthEnv))
		header := strings.TrimSpace(c.GetHeader("Authorization"))
		provided := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if secret == "" {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "service authentication is not configured"})
			return
		}
		if !strings.HasPrefix(header, "Bearer ") || len(provided) != len(secret) ||
			subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid service authentication"})
			return
		}
		c.Next()
	}
}

// AstaraServiceAuth lets other packages apply the shared control-plane
// service authentication without depending on a concrete handler type.
func AstaraServiceAuth(c *gin.Context) {
	astaraServiceAuth()(c)
}
