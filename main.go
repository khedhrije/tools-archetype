package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib" // Postgres driver via pgx stdlib
)

// Default metadata (can be overridden by env variables or -ldflags)
var (
	version  = "dev"
	revision = "unknown"
	builtAt  = "unknown"
)

type Status struct {
	Status   string            `json:"status,omitempty"`
	Checks   map[string]string `json:"checks,omitempty"`
	Version  string            `json:"version,omitempty"`
	Revision string            `json:"revision,omitempty"`
	BuiltAt  string            `json:"builtAt,omitempty"`
}

// ---- Real check runner (replaces simulateCheck) ----
type CheckDetail map[string]any

// add near other helpers
var protectedFiles = map[string]bool{
	".first-mount.sh": true,
	"lost+found":      true,
}

// runCheck executes a check function with a timeout, measures latency, and
// writes a consistent JSON response to the Gin context.
func runCheck(c *gin.Context, name string, timeout time.Duration, fn func(ctx context.Context) (CheckDetail, error)) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	detail, err := fn(ctx)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":    "error",
			"name":      name,
			"latencyMs": latency,
			"error":     err.Error(),
			"detail":    detail,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"name":      name,
		"latencyMs": latency,
		"detail":    detail,
	})
}

// Backwards-compatible wrapper if existing code still calls simulateCheck.
// You can safely delete this once all callers are migrated to runCheck.
func simulateCheck(c *gin.Context, successMsg, _ string, _ float64) {
	runCheck(c, "legacy", 1500*time.Millisecond, func(ctx context.Context) (CheckDetail, error) {
		return CheckDetail{"message": successMsg}, nil
	})
}

func main() {
	// Seed random number generator (still used elsewhere)
	rand.Seed(time.Now().UnixNano())

	// Allow env to override build-time defaults
	if v := os.Getenv("VERSION"); v != "" {
		version = v
	}
	if v := os.Getenv("REVISION"); v != "" {
		revision = v
	}
	if v := os.Getenv("BUILT_AT"); v != "" {
		builtAt = v
	}

	// DATA_DIR for mounted volume
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		// If running in Kubernetes, default to /data; else use ./data
		if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
			dataDir = "/data"
		} else {
			dataDir = "./data"
		}
	}
	if err := ensureDir(dataDir); err != nil {
		log.Fatalf("cannot ensure data dir %q: %v", dataDir, err)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	_ = r.SetTrustedProxies(nil)

	// ---- Health endpoints ----
	r.GET("/livez", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	r.GET("/readyz", func(c *gin.Context) {
		c.JSON(http.StatusOK, Status{
			Status: "ok",
			Checks: map[string]string{"static": "ok", "dataDir": dataDir},
		})
	})
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, Status{
			Status:   "ok",
			Version:  version,
			Revision: revision,
			BuiltAt:  builtAt,
		})
	})
	r.GET("/version", func(c *gin.Context) {
		c.Header("Content-Type", "application/json")
		_ = json.NewEncoder(c.Writer).Encode(Status{
			Version:  version,
			Revision: revision,
			BuiltAt:  builtAt,
		})
	})

	// ---- Real System Check Endpoints ----
	checks := r.Group("/check")
	{
		// Real database reachability (TCP dial). Configure DB_ADDR like "postgres:5432" or "db.prod.internal:3306".
		checks.GET("/database", func(c *gin.Context) {
			// Prefer full DSN built from env (ConfigMap + Secret)
			if dsn, ok := buildPostgresDSN(); ok {
				runCheck(c, "database", 2500*time.Millisecond, func(ctx context.Context) (CheckDetail, error) {
					db, err := sql.Open("pgx", dsn)
					if err != nil {
						return CheckDetail{"mode": "dsn"}, err
					}
					defer db.Close()

					if err := db.PingContext(ctx); err != nil {
						return CheckDetail{"mode": "dsn"}, err
					}

					var now time.Time
					if err := db.QueryRowContext(ctx, "SELECT NOW()").Scan(&now); err != nil {
						return CheckDetail{"mode": "dsn"}, err
					}

					var ver string
					_ = db.QueryRowContext(ctx, "SHOW server_version").Scan(&ver)

					return CheckDetail{
						"mode":    "dsn",
						"nowUTC":  now.UTC().Format(time.RFC3339),
						"version": ver,
					}, nil
				})
				return
			}

			// Fallback: plain TCP reachability if only DB_ADDR is set
			addr := os.Getenv("DB_ADDR")
			if addr == "" {
				addr = "localhost:5432"
			}
			runCheck(c, "database", 1500*time.Millisecond, func(ctx context.Context) (CheckDetail, error) {
				d := &net.Dialer{}
				conn, err := d.DialContext(ctx, "tcp", addr)
				if err != nil {
					return CheckDetail{"mode": "tcp", "addr": addr}, err
				}
				_ = conn.Close()
				return CheckDetail{"mode": "tcp", "addr": addr, "reachable": true}, nil
			})
		})

		// Real HTTP probes for external services. Set CHECK_SERVICE_URLS=comma,separated,urls
		checks.GET("/services", func(c *gin.Context) {
			urlsEnv := os.Getenv("CHECK_SERVICE_URLS")
			var urls []string
			for _, u := range strings.Split(urlsEnv, ",") {
				if s := strings.TrimSpace(u); s != "" {
					urls = append(urls, s)
				}
			}
			if len(urls) == 0 {
				urls = []string{"https://api.github.com"} // sensible default
			}

			runCheck(c, "services", 2500*time.Millisecond, func(ctx context.Context) (CheckDetail, error) {
				client := &http.Client{Timeout: 2 * time.Second}
				results := map[string]any{}
				allOK := true

				for _, u := range urls {
					req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
					resp, err := client.Do(req)
					if err != nil {
						results[u] = map[string]any{"status": "error", "error": err.Error()}
						allOK = false
						continue
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()

					if resp.StatusCode >= 200 && resp.StatusCode < 300 {
						results[u] = map[string]any{"status": "ok", "code": resp.StatusCode}
					} else {
						results[u] = map[string]any{"status": "degraded", "code": resp.StatusCode}
						allOK = false
					}
				}

				if !allOK {
					return CheckDetail{"services": results}, fmt.Errorf("one or more services failing")
				}
				return CheckDetail{"services": results}, nil
			})
		})

		// Real process/runtime metrics (no randomness)
		checks.GET("/metrics", func(c *gin.Context) {
			runCheck(c, "metrics", 800*time.Millisecond, func(ctx context.Context) (CheckDetail, error) {
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				return CheckDetail{
					"goroutines": runtime.NumGoroutine(),
					"memAlloc":   m.Alloc,
					"heapInuse":  m.HeapInuse,
					"gcCount":    m.NumGC,
				}, nil
			})
		})
	}

	// ---- Volume test endpoints ----
	api := r.Group("/data")
	{
		// List files
		api.GET("/list", func(c *gin.Context) {
			entries, err := os.ReadDir(dataDir)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			out := make([]gin.H, 0, len(entries))
			for _, e := range entries {
				info, _ := e.Info()
				out = append(out, gin.H{
					"name":  e.Name(),
					"isDir": e.IsDir(),
					"size": func() int64 {
						if info != nil {
							return info.Size()
						}
						return 0
					}(),
					"modTime": func() string {
						if info != nil {
							return info.ModTime().UTC().Format(time.RFC3339)
						}
						return ""
					}(),
					"fullPath": filepath.Join(dataDir, e.Name()),
				})
			}
			c.JSON(http.StatusOK, gin.H{"dir": dataDir, "files": out})
		})

		// Write a small text file
		api.POST("/write", func(c *gin.Context) {
			var req struct {
				File    string `json:"file"`
				Content string `json:"content"`
			}
			if err := c.ShouldBindJSON(&req); err != nil || req.File == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "provide JSON {file, content}"})
				return
			}
			if !safeFilename(req.File) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file name"})
				return
			}
			p := filepath.Join(dataDir, req.File)
			if err := os.WriteFile(p, []byte(req.Content), 0o644); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"written": p, "bytes": len(req.Content)})
		})

		// Read a file
		api.GET("/read", func(c *gin.Context) {
			name := c.Query("file")
			if name == "" || !safeFilename(name) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "missing or invalid file query param"})
				return
			}
			p := filepath.Join(dataDir, name)
			b, err := os.ReadFile(p)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				} else {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				}
				return
			}
			c.Data(http.StatusOK, "text/plain; charset=utf-8", b)
		})

		// Delete a file
		// Delete a file
		api.DELETE("/", func(c *gin.Context) {
			name := c.Query("file")
			if name == "" || !safeFilename(name) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "missing or invalid file query param"})
				return
			}

			// NEW: protect special files
			if protectedFiles[name] {
				c.JSON(http.StatusForbidden, gin.H{"error": "file is protected and cannot be deleted"})
				return
			}

			p := filepath.Join(dataDir, name)
			if err := os.Remove(p); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"deleted": p})
		})

		// Upload file (multipart/form-data, field "file")
		api.POST("/upload", func(c *gin.Context) {
			fh, err := c.FormFile("file")
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "expected form file field 'file'"})
				return
			}
			if !safeFilename(fh.Filename) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
				return
			}
			dst := filepath.Join(dataDir, fh.Filename)
			if err := c.SaveUploadedFile(fh, dst); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"saved": dst, "size": fh.Size})
		})

		// Self-test: create -> read -> delete a temp file
		api.POST("/selftest", func(c *gin.Context) {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			name := "selftest-" + now + ".txt"
			p := filepath.Join(dataDir, name)

			// write
			content := "selftest at " + now
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"step": "write", "error": err.Error(), "path": p})
				return
			}

			// read
			f, err := os.Open(p)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"step": "open", "error": err.Error(), "path": p})
				return
			}
			defer f.Close()
			got, err := io.ReadAll(f)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"step": "read", "error": err.Error(), "path": p})
				return
			}
			ok := string(got) == content

			// delete
			delErr := os.Remove(p)

			c.JSON(http.StatusOK, gin.H{
				"dir":        dataDir,
				"file":       name,
				"writeBytes": len(content),
				"readBytes":  len(got),
				"match":      ok,
				"deleteErr":  errString(delErr),
			})
		})
	}

	// ---- Static files & SPA ----
	// Serve from ./static, which can be created by a build process
	r.Static("/static", "./static")
	r.GET("/", func(c *gin.Context) {
		c.File("./static/index.html")
	})
	r.NoRoute(func(c *gin.Context) {
		c.File("./static/index.html")
	})

	// ---- HTTP server with graceful shutdown ----
	port := os.Getenv("PORT")
	if port == "" {
		port = os.Getenv("APP_PORT")
		if port == "" {
			port = "8080"
		}
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("archetype %s (%s) built %s listening on :%s; DATA_DIR=%s",
			version, revision, builtAt, port, dataDir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}
	log.Println("Server exiting")
}

func ensureDir(path string) error {
	if path == "" {
		return errors.New("empty path")
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	return nil
}

func safeFilename(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if filepath.Base(name) != name {
		return false
	}
	return true
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func buildPostgresDSN() (string, bool) {
	host := os.Getenv("DB_HOST")
	port := os.Getenv("DB_PORT")
	db := os.Getenv("DB_NAME")
	user := os.Getenv("DB_USER")
	pass := os.Getenv("DB_PASSWORD")
	ssl := os.Getenv("DB_SSLMODE")
	if ssl == "" {
		ssl = "require"
	}

	if host == "" || port == "" || db == "" || user == "" || pass == "" {
		return "", false
	}
	// Example: postgresql://user:pass@host:port/db?sslmode=require
	return fmt.Sprintf("postgresql://%s:%s@%s:%s/%s?sslmode=%s",
		user,
		pass,
		host,
		port,
		db,
		ssl,
	), true
}
