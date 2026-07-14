package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const listenAddress = "127.0.0.1:9288"

const (
	serviceVersion  = "0.3.0"
	serviceProtocol = "netwatch-api-v1"
)

const (
	originTauriWindows = "http://tauri.localhost"
	originTauriMacOS   = "tauri://localhost"
	originDevLocalhost = "http://localhost:3000"
	originDevLoopback  = "http://127.0.0.1:3000"
)

type APIServer struct {
	monitor       *Monitor
	hub           *eventHub
	startedAt     time.Time
	web           fs.FS
	listenAddress string
	instanceID    string
}

func NewAPIServer(monitor *Monitor, hub *eventHub) (*APIServer, error) {
	return NewAPIServerAt(monitor, hub, listenAddress)
}

func NewAPIServerAt(monitor *Monitor, hub *eventHub, address string) (*APIServer, error) {
	web, err := dashboardFS()
	if err != nil {
		return nil, fmt.Errorf("open embedded dashboard: %w", err)
	}
	instanceID, err := newInstanceID()
	if err != nil {
		return nil, fmt.Errorf("create service instance id: %w", err)
	}
	return newAPIServer(monitor, hub, web, address, instanceID, time.Now()), nil
}

func newAPIServer(monitor *Monitor, hub *eventHub, web fs.FS, address, instanceID string, startedAt time.Time) *APIServer {
	return &APIServer{
		monitor:       monitor,
		hub:           hub,
		startedAt:     startedAt,
		web:           web,
		listenAddress: address,
		instanceID:    instanceID,
	}
}

func newInstanceID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (s *APIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/targets", s.listTargets)
	mux.HandleFunc("POST /api/targets", s.createTarget)
	mux.HandleFunc("PUT /api/targets/{id}", s.updateTarget)
	mux.HandleFunc("DELETE /api/targets/{id}", s.deleteTarget)
	mux.HandleFunc("GET /api/snapshot", s.getSnapshot)
	mux.HandleFunc("GET /api/events", s.events)
	// Do not let an unknown API URL fall through to the SPA index page.
	mux.HandleFunc("/api/", func(writer http.ResponseWriter, request *http.Request) {
		writeError(writer, http.StatusNotFound, "not_found", "API endpoint not found")
	})
	mux.Handle("/", s.staticHandler())
	return s.securityAndOrigin(mux)
}

func (s *APIServer) HTTPServer() *http.Server {
	server := &http.Server{
		Addr:              s.listenAddress,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	server.RegisterOnShutdown(s.hub.Close)
	return server
}

func (s *APIServer) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":         "ok",
		"version":        serviceVersion,
		"protocol":       serviceProtocol,
		"instance":       s.instanceID,
		"target_count":   s.monitor.TargetCount(),
		"uptime_seconds": int64(time.Since(s.startedAt).Seconds()),
		"time":           time.Now().UnixMilli(),
	})
}

func (s *APIServer) listTargets(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, s.monitor.ListTargets())
}

func (s *APIServer) createTarget(writer http.ResponseWriter, request *http.Request) {
	var target Target
	if err := decodeJSON(writer, request, &target); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	target, err := s.monitor.Add(target)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errMonitorClosed) {
			status = http.StatusServiceUnavailable
		}
		writeError(writer, status, "invalid_target", err.Error())
		return
	}
	writeJSON(writer, http.StatusCreated, target)
}

func (s *APIServer) updateTarget(writer http.ResponseWriter, request *http.Request) {
	var target Target
	if err := decodeJSON(writer, request, &target); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	target, err := s.monitor.Update(request.PathValue("id"), target)
	if err != nil {
		var notFound osErrNotExist
		if errors.As(err, &notFound) {
			writeError(writer, http.StatusNotFound, "not_found", err.Error())
			return
		}
		status := http.StatusBadRequest
		if errors.Is(err, errMonitorClosed) {
			status = http.StatusServiceUnavailable
		}
		writeError(writer, status, "invalid_target", err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, target)
}

func (s *APIServer) deleteTarget(writer http.ResponseWriter, request *http.Request) {
	err := s.monitor.Delete(request.PathValue("id"))
	if err != nil {
		var notFound osErrNotExist
		if errors.As(err, &notFound) {
			writeError(writer, http.StatusNotFound, "not_found", err.Error())
			return
		}
		status := http.StatusBadRequest
		if errors.Is(err, errMonitorClosed) {
			status = http.StatusServiceUnavailable
		}
		writeError(writer, status, "delete_failed", err.Error())
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *APIServer) getSnapshot(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, s.monitor.Snapshot())
}

func (s *APIServer) events(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, "stream_unavailable", "streaming is unavailable")
		return
	}
	channel, subscribed := s.hub.Subscribe()
	if !subscribed {
		writeError(writer, http.StatusServiceUnavailable, "too_many_streams", "at most 8 dashboard streams may be open")
		return
	}
	defer s.hub.Unsubscribe(channel)
	writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-cache, no-store")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")
	setSSEWriteDeadline(writer)
	if _, err := io.WriteString(writer, "retry: 3000\n\n"); err != nil {
		return
	}
	if !writeSSE(writer, "snapshot", s.monitor.Snapshot()) {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-channel:
			setSSEWriteDeadline(writer)
			if !open || !writeRawSSE(writer, event.Type, event.Data) {
				return
			}
			flusher.Flush()
		case now := <-heartbeat.C:
			setSSEWriteDeadline(writer)
			if !writeSSE(writer, "heartbeat", map[string]int64{"ts": now.UnixMilli()}) {
				return
			}
			flusher.Flush()
		}
	}
}

func setSSEWriteDeadline(writer http.ResponseWriter) {
	_ = http.NewResponseController(writer).SetWriteDeadline(time.Now().Add(5 * time.Second))
}

func writeSSE(writer io.Writer, eventType string, value any) bool {
	data, err := json.Marshal(value)
	if err != nil {
		return false
	}
	return writeRawSSE(writer, eventType, data)
}

func writeRawSSE(writer io.Writer, eventType string, data []byte) bool {
	if _, err := fmt.Fprintf(writer, "event: %s\ndata: ", eventType); err != nil {
		return false
	}
	if _, err := writer.Write(data); err != nil {
		return false
	}
	_, err := io.WriteString(writer, "\n\n")
	return err == nil
}

func (s *APIServer) staticHandler() http.Handler {
	fileServer := http.FileServer(http.FS(s.web))
	index, indexErr := fs.ReadFile(s.web, "index.html")
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "only GET and HEAD are allowed")
			return
		}

		clean := path.Clean("/" + request.URL.Path)
		name := strings.TrimPrefix(clean, "/")
		if name == "" {
			name = "index.html"
		}
		info, err := fs.Stat(s.web, name)
		if err == nil && (!info.IsDir() || fileHasIndex(s.web, name)) {
			if strings.Contains(name, "/_next/static/") || strings.HasPrefix(name, "_next/static/") || strings.HasPrefix(name, "assets/") {
				writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(writer, request)
			return
		}
		if indexErr != nil {
			writeError(writer, http.StatusNotFound, "not_found", "dashboard not found")
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(writer, request, "index.html", time.Time{}, bytes.NewReader(index))
	})
}

func fileHasIndex(files fs.FS, directory string) bool {
	_, err := fs.Stat(files, path.Join(directory, "index.html"))
	return err == nil
}

func (s *APIServer) securityAndOrigin(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.listenAddress)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		writer.Header().Set("Content-Security-Policy", fmt.Sprintf("default-src 'self'; base-uri 'none'; connect-src 'self' http://127.0.0.1:%s http://localhost:%s; frame-ancestors 'none'; form-action 'self'; img-src 'self' data:; object-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'", port, port))
		if !validRequestHost(request.Host, s.listenAddress) {
			writeError(writer, http.StatusForbidden, "host_forbidden", fmt.Sprintf("Host must use a loopback name or address on port %s", port))
			return
		}

		origin := request.Header.Get("Origin")
		if origin != "" {
			if !validRequestOrigin(origin, s.listenAddress) {
				writeError(writer, http.StatusForbidden, "origin_forbidden", "request origin is not an approved NetWatch origin")
				return
			}
			writer.Header().Set("Access-Control-Allow-Origin", origin)
			writer.Header().Add("Vary", "Origin")
		}
		if request.Method == http.MethodOptions {
			if !strings.HasPrefix(request.URL.Path, "/api/") {
				writer.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			writer.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			writer.Header().Set("Access-Control-Max-Age", "600")
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func validRequestHost(value, address string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	_, allowedPort, err := net.SplitHostPort(address)
	if err != nil || port != allowedPort {
		return false
	}
	return isLoopbackHost(host)
}

func validRequestOrigin(origin, address string) bool {
	switch origin {
	case originTauriWindows, originTauriMacOS, originDevLocalhost, originDevLoopback:
		return true
	}

	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	_, allowedPort, err := net.SplitHostPort(address)
	if err != nil || parsed.Port() != allowedPort || !isLoopbackHost(parsed.Hostname()) {
		return false
	}
	return true
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback))
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 32*1024)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("request must contain exactly one JSON value")
	}
	return err
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
