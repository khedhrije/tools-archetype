// pkg/checkers/ui.go
package monitoring

import (
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// MonitoringUIOptions controls how the monitoring SPA is mounted.
type MonitoringUIOptions struct {
	// Directory that contains index.html and built assets.
	StaticDir string
	// The main SPA file (usually "index.html") inside StaticDir.
	IndexFile string
	// Base path for the SPA shell (default: "/monitoring").
	BasePath string
	// Optional public URL for static assets. If empty, nothing is mounted here.
	// Use this ONLY if you are not already mounting assets elsewhere.
	// Example: "/assets" or "/static".
	AssetsPath string
}

// AttachMonitoring mounts the monitoring SPA at /monitoring without using a wildcard route.
// We rely on a NoRoute handler to serve index.html for any unknown path under /monitoring,
// which avoids Gin's "catch-all conflicts with existing path segment" panic.
func AttachMonitoring(r *gin.Engine, opts MonitoringUIOptions) {
	if r == nil {
		return
	}
	if opts.StaticDir == "" || opts.IndexFile == "" {
		return
	}

	base := opts.BasePath
	if base == "" {
		base = "/monitoring"
	}
	// normalize: single leading slash, no trailing slash
	base = "/" + strings.Trim(strings.TrimSpace(base), "/")

	indexPath := filepath.Join(opts.StaticDir, opts.IndexFile)

	// OPTIONAL: mount assets if requested (SKIP if you already mount /static elsewhere)
	if p := strings.TrimSpace(opts.AssetsPath); p != "" {
		assets := "/" + strings.Trim(p, "/")
		r.Static(assets, opts.StaticDir)
	}

	// Entry route: GET /monitoring -> index.html
	r.GET(base, func(c *gin.Context) {
		c.File(indexPath)
	})

	// Catch-all via NoRoute: if the *unmatched* path begins with /monitoring, serve index.html.
	// This avoids registering "/monitoring/*path" and therefore avoids wildcard conflicts.
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		// match /monitoring or /monitoring/... (but only when nothing else handled it)
		if p == base || strings.HasPrefix(p, base+"/") {
			c.File(indexPath)
			return
		}
		// fall through to the next NoRoute (default 404) if any
	})
}
