// internal/ui/rest/router/technical.go
package router

import (
	"github.com/gin-gonic/gin"
	"github.com/khedhrije/tools-archetype/pkg/monitoring"
)

// RegisterTechnicalRoutes wires health, diagnostics, and data-management endpoints
// under the /api group (technical / ops-focused).
func RegisterTechnicalRoutes(api *gin.RouterGroup, checksHandler monitoring.Handler) {
	// Basic health/info
	api.GET("/livez", checksHandler.Livez())
	api.GET("/readyz", checksHandler.Readyz())
	api.GET("/healthz", checksHandler.Healthz())
	api.GET("/version", checksHandler.Version())

	// Server information
	api.GET("/server", checksHandler.ServerInfo())

	// Check endpoints
	checks := api.Group("/check")
	{
		checks.GET("/database", checksHandler.Check())
		checks.GET("/services", checksHandler.Services())
		checks.GET("/metrics", checksHandler.Metrics())

		// Alias for fs selftest under /check for consistency
		checks.POST("/fs/selftest", checksHandler.FilesystemSelfTest())
	}

	// Data / file management
	data := api.Group("/data")
	{
		data.POST("/selftest", checksHandler.FilesystemSelfTest())
		data.GET("/list", checksHandler.DataList())
		data.GET("/read", checksHandler.DataReadText())
		// Support both /api/data and /api/data/ for DELETE
		data.DELETE("", checksHandler.DataDelete())
		data.DELETE("/", checksHandler.DataDelete())
	}
}
