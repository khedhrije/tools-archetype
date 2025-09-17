// internal/ui/rest/router/router.go
package router

import (
	"github.com/gin-gonic/gin"
	"github.com/khedhrije/tools-archetype/pkg/monitoring"
)

type Options struct {
	TrustedProxies []string
	// You can add more global router options here later (CORS, logger, etc.)
}

// CreateRouter builds the Gin engine and delegates route registration
// to the technical, functional, and frontend registrars.
func CreateRouter(checksHandler monitoring.Handler, opts ...Options) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(gin.Recovery())

	// Apply options (if any)
	if len(opts) > 0 && len(opts[0].TrustedProxies) > 0 {
		_ = r.SetTrustedProxies(opts[0].TrustedProxies) // ignore error => falls back to default
	}

	// Group all backend routes under /api
	api := r.Group("/api")

	// Register endpoint families
	RegisterTechnicalRoutes(api, checksHandler)
	RegisterFunctionalRoutes(api)
	RegisterFrontendRoutes(r)

	return r
}
