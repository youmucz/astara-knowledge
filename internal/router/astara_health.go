package router

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/astara"
	"github.com/Tencent/WeKnora/internal/database"
)

func registerAstaraHealthRoutes(r *gin.Engine, db *gorm.DB, redisClient *redis.Client, profile astara.Profile) {
	live := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"live": true}) }
	r.GET("/health", live) // retained for existing container probes
	r.GET("/health/live", live)
	r.GET("/health/ready", func(c *gin.Context) {
		ready := profile.Valid && dependenciesReady(c, db, redisClient)
		status := http.StatusOK
		if !ready {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, gin.H{"ready": ready, "identity": astara.ReleaseIdentity(profile)})
	})
}

func dependenciesReady(parent context.Context, db *gorm.DB, redisClient *redis.Client) bool {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	if db == nil {
		return false
	}
	sqlDB, err := db.DB()
	if err != nil || sqlDB.PingContext(ctx) != nil {
		return false
	}
	if redisClient == nil || redisClient.Ping(ctx).Err() != nil {
		return false
	}
	_, dirty, known := database.CachedMigrationVersion()
	return known && !dirty && database.CachedMigrationError() == ""
}
