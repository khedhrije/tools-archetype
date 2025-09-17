package bootstrap

import (
	"fmt"
	"log"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/khedhrije/tools-archetype/internal/configuration"
	"github.com/khedhrije/tools-archetype/internal/ui/rest/router"
	"github.com/khedhrije/tools-archetype/pkg/monitoring"
)

type Bootstrap struct {
	Config *configuration.AppConfig
	Router *gin.Engine
}

func InitBootstrap() Bootstrap {
	return initBootstrap()
}

// initBootstrap sets up the application configuration, initializes services, and configures the router.
func initBootstrap() Bootstrap {
	if configuration.Config == nil {
		log.Fatal("configuration is nil")
	}

	app := Bootstrap{}
	app.Config = configuration.Config

	monitoringHandler := monitoring.New()

	// ✅ Create router
	r := router.CreateRouter(monitoringHandler)
	app.Router = r

	return app
}

func (b Bootstrap) Run() {
	dsn := fmt.Sprintf("%s:%d", b.Config.RestConfig.Host, b.Config.RestConfig.Port)
	if errRun := b.Router.Run(dsn); errRun != nil {
		slog.Error("error during service instantiation")
	}
}
