package monitoring

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Detail is the standard payload each checker returns.
type Detail map[string]any

// -------------------------
// Shared helpers
// -------------------------

var ErrProtectedFile = errors.New("file is protected and cannot be deleted")

// SafeFilename ensures a single-segment safe filename.
func SafeFilename(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return filepath.Base(name) == name
}

// -------------------------
// Server Information
// -------------------------

// ServerInfoOptions provides build/runtime context for ServerInformation.
type ServerInfoOptions struct {
	Version   string
	Revision  string
	BuiltAt   string
	DataDir   string
	StartTime time.Time
}

// ServerInformation returns basic server/build info and uptime.
func ServerInformation(ctx context.Context, opt ServerInfoOptions) (Detail, error) {
	hostname, _ := os.Hostname()
	return Detail{
		"version":   opt.Version,
		"revision":  opt.Revision,
		"builtAt":   opt.BuiltAt,
		"dataDir":   opt.DataDir,
		"pid":       os.Getpid(),
		"hostname":  hostname,
		"goVersion": runtime.Version(),
		"uptime":    time.Since(opt.StartTime).Round(time.Second).String(),
		"nowUTC":    time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// -------------------------
// Filesystem Self-Test
// -------------------------

// FilesystemSelfTest creates, reads, and deletes a temp file inside dir.
func FilesystemSelfTest(ctx context.Context, dir string) (Detail, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	name := "selftest-" + now + ".txt"
	p := filepath.Join(dir, name)

	// write
	content := "selftest at " + now
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return Detail{"step": "write", "path": p}, err
	}

	// read
	f, err := os.Open(p)
	if err != nil {
		return Detail{"step": "open", "path": p}, err
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil {
		return Detail{"step": "read", "path": p}, err
	}
	match := string(got) == content

	// delete
	delErr := os.Remove(p)

	return Detail{
		"dir":        dir,
		"file":       name,
		"writeBytes": len(content),
		"readBytes":  len(got),
		"match":      match,
		"deleteErr":  errString(delErr),
	}, nil
}

// -------------------------
// External Services (HTTP GET probes)
// -------------------------

// Services performs HTTP GET probes against provided URLs.
// Returns an error if one or more services fail or respond non-2xx.
func Services(ctx context.Context, urls []string) (Detail, error) {
	client := &httpClient // defined below to allow test injection
	results := map[string]any{}
	allOK := true

	for _, u := range urls {
		req, _ := httpNewRequestWithContext(ctx, "GET", u, nil)
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
		return Detail{"services": results}, fmt.Errorf("one or more services failing")
	}
	return Detail{"services": results}, nil
}

// Lightweight indirection for testability (no external deps here)
var httpClient = httpClientIface{Timeout: 2 * time.Second}

type httpClientIface struct {
	Timeout time.Duration
}

type httpResponse struct {
	StatusCode int
	Body       io.ReadCloser
}

func (c *httpClientIface) Do(req *httpRequest) (*httpResponse, error) {
	// Minimal standard library wrapper
	client := &httpStdClient{Timeout: c.Timeout}
	return client.Do(req)
}

type httpStdClient struct {
	Timeout time.Duration
}

func (c *httpStdClient) Do(req *httpRequest) (*httpResponse, error) {
	client := &http.Client{Timeout: c.Timeout}
	resp, err := client.Do((*http.Request)(req))
	if err != nil {
		return nil, err
	}
	return &httpResponse{StatusCode: resp.StatusCode, Body: resp.Body}, nil
}

type httpRequest http.Request

func httpNewRequestWithContext(ctx context.Context, method, url string, body io.Reader) (*httpRequest, error) {
	r, err := http.NewRequestWithContext(ctx, method, url, body)
	return (*httpRequest)(r), err
}

// -------------------------
// System Metrics
// -------------------------

// Metrics returns basic runtime/process metrics.
func Metrics(ctx context.Context) (Detail, error) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return Detail{
		"goroutines": runtime.NumGoroutine(),
		"memAlloc":   m.Alloc,
		"heapInuse":  m.HeapInuse,
		"gcCount":    m.NumGC,
	}, nil
}

// -------------------------
// Event Log (tail)
// -------------------------

// EventLogTail returns up to maxLines from the end of a text log file.
// If the file doesn't exist, returns an empty slice with no error.
func EventLogTail(ctx context.Context, logPath string, maxLines int) (Detail, error) {
	if maxLines <= 0 {
		maxLines = 100
	}
	f, err := os.Open(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Detail{"path": logPath, "lines": []string{}}, nil
		}
		return Detail{"path": logPath}, err
	}
	defer f.Close()

	// Efficient-ish tail: read blocks backwards; fallback to scan-all if small
	const block = 64 * 1024
	fi, _ := f.Stat()
	size := fi.Size()
	var (
		buf     []byte
		offset  = size
		lines   []string
		builder strings.Builder
	)

	for offset > 0 && len(lines) < maxLines+1 { // +1 to handle partial first line
		readSize := block
		if int64(readSize) > offset {
			readSize = int(offset)
		}
		offset -= int64(readSize)
		chunk := make([]byte, readSize)
		if _, err := f.ReadAt(chunk, offset); err != nil && !errors.Is(err, io.EOF) {
			return Detail{"path": logPath}, err
		}
		buf = append(chunk, buf...)
	}

	// Split lines and take the last maxLines
	sc := bufio.NewScanner(strings.NewReader(string(buf)))
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	_ = builder // reserved for future formatting

	return Detail{"path": logPath, "lines": lines}, nil
}

// -------------------------
// File Management
// -------------------------

// ListDir lists entries in dir (non-recursive).
func ListDir(ctx context.Context, dir string) (Detail, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		info, _ := e.Info()
		out = append(out, map[string]any{
			"name":    e.Name(),
			"isDir":   e.IsDir(),
			"size":    sizeOf(info),
			"modTime": modOf(info),
			"fullPath": func() string {
				return filepath.Join(dir, e.Name())
			}(),
		})
	}
	return Detail{"dir": dir, "files": out}, nil
}

// ReadFile returns the content of a single file inside dir.
func ReadFile(ctx context.Context, dir, name string) (Detail, error) {
	if !SafeFilename(name) {
		return nil, fmt.Errorf("invalid filename")
	}
	p := filepath.Join(dir, name)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return Detail{"path": p, "bytes": len(b), "content": string(b)}, nil
}

// WriteFile writes content to a file inside dir (creates/overwrites).
func WriteFile(ctx context.Context, dir, name, content string) (Detail, error) {
	if !SafeFilename(name) {
		return nil, fmt.Errorf("invalid filename")
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return Detail{"written": p, "bytes": len(content)}, nil
}

// DeleteFile deletes a file inside dir unless protected[name] is true.
func DeleteFile(ctx context.Context, dir, name string, protected map[string]bool) (Detail, error) {
	if !SafeFilename(name) {
		return nil, fmt.Errorf("invalid filename")
	}
	if protected != nil && protected[name] {
		return nil, ErrProtectedFile
	}
	p := filepath.Join(dir, name)
	if err := os.Remove(p); err != nil {
		return nil, err
	}
	return Detail{"deleted": p}, nil
}

// -------------------------
// Database Connection
// -------------------------

// DatabaseByDSN checks a database using a full DSN with the default "pgx" driver registered by the caller.
// Example DSN: postgresql://user:pass@host:port/db?sslmode=require
func DatabaseByDSN(ctx context.Context, dsn string) (Detail, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return Detail{"mode": "dsn"}, err
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return Detail{"mode": "dsn"}, err
	}

	var now time.Time
	if err := db.QueryRowContext(ctx, "SELECT NOW()").Scan(&now); err != nil {
		return Detail{"mode": "dsn"}, err
	}

	var ver string
	_ = db.QueryRowContext(ctx, "SHOW server_version").Scan(&ver)

	return Detail{
		"mode":    "dsn",
		"nowUTC":  now.UTC().Format(time.RFC3339),
		"version": ver,
	}, nil
}

// DatabaseByTCP checks simple TCP reachability to a DB host:port.
func DatabaseByTCP(ctx context.Context, addr string) (Detail, error) {
	d := &netDialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Detail{"mode": "tcp", "addr": addr}, err
	}
	_ = conn.Close()
	return Detail{"mode": "tcp", "addr": addr, "reachable": true}, nil
}

// tiny indirection to avoid importing net here publicly
type netDialer struct{}

func (d *netDialer) DialContext(ctx context.Context, network, address string) (io.Closer, error) {
	var nd net.Dialer
	c, err := nd.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// -------------------------
// tiny helpers
// -------------------------

func sizeOf(fi os.FileInfo) int64 {
	if fi == nil {
		return 0
	}
	return fi.Size()
}

func modOf(fi os.FileInfo) string {
	if fi == nil {
		return ""
	}
	return fi.ModTime().UTC().Format(time.RFC3339)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
