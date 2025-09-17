package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/khedhrije/tools-archetype/internal/configuration"
)

// protectedFiles are never deletable from DataDelete.
var protectedFiles = map[string]bool{
	".first-mount.sh": true,
	"lost+found":      true,
}

// ====== Public surface ======

type Handler interface {
	// Basic
	Livez() gin.HandlerFunc
	Readyz() gin.HandlerFunc
	Healthz() gin.HandlerFunc
	Version() gin.HandlerFunc
	ServerInfo() gin.HandlerFunc

	// Checks
	Check() gin.HandlerFunc    // database
	Services() gin.HandlerFunc // external services
	Metrics() gin.HandlerFunc  // system metrics
	FilesystemSelfTest() gin.HandlerFunc

	// Data / files
	DataList() gin.HandlerFunc
	DataReadText() gin.HandlerFunc
	DataDelete() gin.HandlerFunc
}

// New constructs a Handler with the provided configuration.
func New() Handler {
	return &handler{}
}

// ====== Implementation ======

type handler struct{}

// --- shared runner to unify JSON output like your runCheck in main ---
func (h *handler) run(c *gin.Context, name string, timeout time.Duration, fn func(ctx context.Context) (Detail, error)) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	detail, err := fn(ctx)
	lat := time.Since(start).Milliseconds()

	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":    "error",
			"name":      name,
			"latencyMs": lat,
			"error":     err.Error(),
			"detail":    detail,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"name":      name,
		"latencyMs": lat,
		"detail":    detail,
	})
}

// --- basic health/info ---

func (h *handler) Livez() gin.HandlerFunc {
	return func(c *gin.Context) { c.Status(http.StatusOK) }
}

func (h *handler) Readyz() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
			"checks": gin.H{"static": "ok", "dataDir": configuration.Config.AppDataDir},
		})
	}
}

func (h *handler) Healthz() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":   "ok",
			"version":  configuration.Config.AppVersion,
			"revision": configuration.Config.AppRevision,
			"builtAt":  configuration.Config.AppBuiltAt,
		})
	}
}

func (h *handler) Version() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Type", "application/json")
		_ = json.NewEncoder(c.Writer).Encode(gin.H{
			"version":  configuration.Config.AppVersion,
			"revision": configuration.Config.AppRevision,
			"builtAt":  configuration.Config.AppBuiltAt,
		})
	}
}

func (h *handler) ServerInfo() gin.HandlerFunc {
	return func(c *gin.Context) {
		h.run(c, "server-info", 800*time.Millisecond, func(ctx context.Context) (Detail, error) {
			return ServerInformation(ctx, ServerInfoOptions{
				Version:   configuration.Config.AppVersion,
				Revision:  configuration.Config.AppRevision,
				BuiltAt:   configuration.Config.AppBuiltAt,
				DataDir:   configuration.Config.AppDataDir,
				StartTime: time.Now(),
			})
		})
	}
}

// --- /api/check/database ---

func (h *handler) Check() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Prefer full DSN built from env (ConfigMap + Secret)
		if dsn, ok := buildPostgresDSN(); ok {
			h.run(c, "database", 2500*time.Millisecond, func(ctx context.Context) (Detail, error) {
				return DatabaseByDSN(ctx, dsn)
			})
			return
		}
		// Fallback: plain TCP reachability if only APP_DB_ADDR is set
		addr := configuration.Config.DatabaseConfig.Addr
		h.run(c, "database", 1500*time.Millisecond, func(ctx context.Context) (Detail, error) {
			return DatabaseByTCP(ctx, addr)
		})
	}
}

// --- /api/check/services ---

func (h *handler) Services() gin.HandlerFunc {
	return func(c *gin.Context) {
		urlsEnv := os.Getenv("APP_CHECK_SERVICE_URLS")
		var urls []string
		for _, u := range strings.Split(urlsEnv, ",") {
			if s := strings.TrimSpace(u); s != "" {
				urls = append(urls, s)
			}
		}
		if len(urls) == 0 {
			urls = []string{"https://api.github.com"} // sensible default
		}
		h.run(c, "services", 2500*time.Millisecond, func(ctx context.Context) (Detail, error) {
			return Services(ctx, urls)
		})
	}
}

// --- /api/check/metrics ---

func (h *handler) Metrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		h.run(c, "metrics", 800*time.Millisecond, Metrics)
	}
}

// --- Filesystem self-test (exposed under /api/check/fs/selftest and /api/data/selftest) ---

func (h *handler) FilesystemSelfTest() gin.HandlerFunc {
	return func(c *gin.Context) {
		h.run(c, "fs-selftest", 1500*time.Millisecond, func(ctx context.Context) (Detail, error) {
			return FilesystemSelfTest(ctx, configuration.Config.AppDataDir)
		})
	}
}

// --- Data / files ---

func (h *handler) DataList() gin.HandlerFunc {
	return func(c *gin.Context) {
		h.run(c, "data-list", 1500*time.Millisecond, func(ctx context.Context) (Detail, error) {
			return ListDir(c, configuration.Config.AppDataDir)
		})
	}
}

// Returns raw text (not JSON) because the frontend expects plain content.
func (h *handler) DataReadText() gin.HandlerFunc {
	return func(c *gin.Context) {
		name := strings.TrimSpace(c.Query("file"))
		if name == "" {
			c.String(http.StatusBadRequest, "missing 'file' query param")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 1500*time.Millisecond)
		defer cancel()

		detail, err := ReadFile(ctx, configuration.Config.AppDataDir, name)
		if err != nil {
			c.String(http.StatusNotFound, err.Error())
			return
		}
		content, _ := detail["content"].(string)
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(content))
	}
}

func (h *handler) DataDelete() gin.HandlerFunc {
	return func(c *gin.Context) {
		name := strings.TrimSpace(c.Query("file"))
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing 'file' query param"})
			return
		}
		// Enforce protected files
		if protectedFiles[name] {
			c.JSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("%q is protected and cannot be deleted", name)})
			return
		}

		h.run(c, "data-delete", 1500*time.Millisecond, func(ctx context.Context) (Detail, error) {
			return DeleteFile(ctx, configuration.Config.AppDataDir, name, protectedFiles)
		})
	}
}

// ====== helpers kept inside package ======

// buildPostgresDSN composes a DSN from env vars (ConfigMap + Secret).
// Env: DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASSWORD, DB_SSLMODE (default "require")
func buildPostgresDSN() (string, bool) {
	host := configuration.Config.DatabaseConfig.Host
	port := configuration.Config.DatabaseConfig.Port
	db := configuration.Config.DatabaseConfig.Name
	user := configuration.Config.DatabaseConfig.Username
	pass := configuration.Config.DatabaseConfig.Password
	ssl := configuration.Config.DatabaseConfig.SSL
	if ssl == "" {
		ssl = "require"
	}
	if host == "" || port == 0 || db == "" || user == "" || pass == "" {
		return "", false
	}
	// Example: postgresql://user:pass@host:port/db?sslmode=require
	return fmt.Sprintf("postgresql://%s:%s@%s:%d/%s?sslmode=%s",
		user, pass, host, port, db, ssl,
	), true
}
