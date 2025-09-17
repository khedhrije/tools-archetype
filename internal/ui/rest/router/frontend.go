// internal/ui/rest/router/frontend.go
package router

import (
	"github.com/gin-gonic/gin"
	"github.com/khedhrije/tools-archetype/pkg/monitoring"
)

// RegisterFrontendRoutes mounts SPAs or static front-end bundles.
// These are NOT under /api.
func RegisterFrontendRoutes(r *gin.Engine) {
	// Monitoring SPA at /monitoring
	monitoring.AttachMonitoring(r,
		monitoring.MonitoringUIOptions{
			StaticDir:  "./static",
			IndexFile:  "monitoring.html",
			BasePath:   "/monitoring",
			AssetsPath: "",
		})
}
