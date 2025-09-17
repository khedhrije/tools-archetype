// internal/ui/rest/router/functional.go
package router

import (
	"github.com/gin-gonic/gin"
	"github.com/khedhrije/tools-archetype/internal/ui/rest/handlers"
)

// RegisterFunctionalRoutes wires "business/functional" API endpoints
// under the /api group. Keep tech/ops endpoints in technical.go.
func RegisterFunctionalRoutes(api *gin.RouterGroup) {
	// Example functional endpoint (you can add your domain routes here, e.g. /tasks, /users, etc.)
	// NOTE: This keeps the original Ping behavior at GET /api/
	api.GET("/", handlers.Ping())

}
