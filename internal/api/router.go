// Package api contains Gin routes, HTTP handlers, and HTTP middleware.
package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const tenantContextKey = "sentinel_tenant_id"

func NewRouter(logger *slog.Logger, deps Dependencies) *gin.Engine {
	router := gin.New()
	router.HandleMethodNotAllowed = true
	router.Use(gin.Recovery())
	router.Use(requestLogger(logger))
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "sentinel-api"})
	})
	if deps.enabled() {
		v1 := router.Group("/v1")
		v1.Use(tenantMiddleware(), bodyLimit(1<<20))
		v1.POST("/agents", createAgent(deps, logger))
		v1.GET("/agents", listAgents(deps, logger))
		v1.GET("/agents/:agent_id", getAgent(deps, logger))
		v1.GET("/agents/:agent_id/result", getAgentResult(deps, logger))
		v1.GET("/agents/:agent_id/stream", streamAgent(deps, logger))
		v1.POST("/agents/:agent_id/cancel", cancelAgent(deps, logger))
		v1.POST("/agents/:agent_id/resume", resumeAgent(deps, logger))

		v1.POST("/source/repositories", createRepository(deps, logger))
		v1.GET("/source/repositories", listRepositories(deps, logger))
		v1.GET("/source/repositories/:repository_id", getRepository(deps, logger))
		v1.GET("/source/repositories/:repository_id/versions", listVersions(deps, logger))
		v1.POST("/source/repositories/:repository_id/syncs", createSync(deps, logger))
		v1.GET("/source/syncs/:sync_id", getSync(deps, logger))
		v1.POST("/source/syncs/:sync_id/resume", resumeSync(deps, logger))
	}
	router.NoRoute(func(c *gin.Context) {
		errorJSON(c, http.StatusNotFound, "route_not_found", "route not found")
	})
	router.NoMethod(func(c *gin.Context) {
		errorJSON(c, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
	return router
}

func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		logger.InfoContext(c.Request.Context(), "HTTP request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration", time.Since(started),
		)
	}
}

func tenantMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := strings.TrimSpace(c.GetHeader("X-Tenant-ID"))
		if raw == "" {
			// EventSource cannot set request headers; the query parameter is the
			// browser-compatible fallback for SSE and simple clients.
			raw = strings.TrimSpace(c.Query("tenant_id"))
		}
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil {
			errorJSON(c, http.StatusBadRequest, "tenant_required", "X-Tenant-ID must be a nonzero UUID")
			return
		}
		c.Set(tenantContextKey, id.String())
		c.Next()
	}
}

func tenantID(c *gin.Context) string {
	return c.GetString(tenantContextKey)
}

func bodyLimit(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
