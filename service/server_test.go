package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) (*Monitor, *APIServer) {
	t.Helper()
	hub := newEventHub()
	monitor, err := NewMonitor([]Target{{ID: "one", Name: "One", Host: "127.0.0.1", Port: 443, IntervalMS: 1000, TimeoutMS: 500, Enabled: false}}, nil, hub, 10)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewAPIServer(monitor, hub)
	if err != nil {
		monitor.Close()
		t.Fatal(err)
	}
	return monitor, server
}

func localRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.Host = listenAddress
	return request
}

func TestNetworkInterfacesAPI(t *testing.T) {
	monitor, server := testServer(t)
	defer monitor.Close()
	server.listInterfaces = func() ([]NetworkInterfaceInfo, error) {
		return []NetworkInterfaceInfo{{
			ID: "adapter-one", Name: "Ethernet", Addresses: []string{"192.0.2.10"}, Families: []string{"ipv4"}, IsDefault: true,
		}}, nil
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, localRequest(http.MethodGet, "/api/network-interfaces", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"adapter-one"`) || !strings.Contains(response.Body.String(), `"is_default":true`) {
		t.Fatalf("unexpected interface response: %d %s", response.Code, response.Body.String())
	}

	server.listInterfaces = func() ([]NetworkInterfaceInfo, error) { return nil, errors.New("unavailable") }
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, localRequest(http.MethodGet, "/api/network-interfaces", nil))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "interface_discovery_failed") {
		t.Fatalf("unexpected discovery error response: %d %s", response.Code, response.Body.String())
	}
}

func TestAPIOriginStaticFallbackAndCRUD(t *testing.T) {
	monitor, server := testServer(t)
	defer monitor.Close()
	handler := server.Handler()

	request := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("untrusted Host accepted: %d %s", response.Code, response.Body.String())
	}

	request = localRequest(http.MethodGet, "/api/health", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("unexpected health response: %d %s", response.Code, response.Body.String())
	}
	var health struct {
		Status   string `json:"status"`
		Version  string `json:"version"`
		Protocol string `json:"protocol"`
		Instance string `json:"instance"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != "ok" || health.Version != serviceVersion || health.Protocol != serviceProtocol || health.Instance == "" {
		t.Fatalf("health identity is incomplete: %#v", health)
	}
	ready := server.readyMessage(listenAddress)
	if ready.Type != "ready" || ready.Version != health.Version || ready.Protocol != health.Protocol || ready.Instance != health.Instance {
		t.Fatalf("ready and health identities differ: ready=%#v health=%#v", ready, health)
	}
	firstInstance := health.Instance
	request = localRequest(http.MethodGet, "/api/health", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil || health.Instance != firstInstance {
		t.Fatalf("health instance changed within one server: %#v %v", health, err)
	}
	request = localRequest(http.MethodGet, "/api/health", nil)
	request.Host = "localhost:9288"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("localhost Host rejected: %d %s", response.Code, response.Body.String())
	}

	request = localRequest(http.MethodGet, "/api/snapshot", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"estimated_loss_rate":null`) {
		t.Fatalf("undefined estimated loss was not JSON null: %d %s", response.Code, response.Body.String())
	}

	request = localRequest(http.MethodGet, "/api/health", nil)
	request.Header.Set("Origin", "https://evil.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("external origin accepted: %d", response.Code)
	}

	request = localRequest(http.MethodGet, "/api/does-not-exist", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unknown API did not return JSON 404: %d %s", response.Code, response.Body.String())
	}

	request = localRequest(http.MethodGet, "/dashboard/route", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(strings.ToLower(response.Body.String()), "<!doctype html") {
		t.Fatalf("SPA fallback failed: %d %s", response.Code, response.Body.String())
	}

	created := Target{Name: "Two", Host: "127.0.0.1", Port: 8443, BypassTUN: true, BypassInterfaceID: "adapter-one", IntervalMS: 1000, TimeoutMS: 500, Enabled: false}
	payload, _ := json.Marshal(created)
	request = localRequest(http.MethodPost, "/api/targets", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", originDevLocalhost)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("Access-Control-Allow-Origin") != originDevLocalhost {
		t.Fatalf("create failed: %d %s", response.Code, response.Body.String())
	}
	var target Target
	if err := json.Unmarshal(response.Body.Bytes(), &target); err != nil || target.ID == "" || !target.BypassTUN || target.BypassInterfaceID != "adapter-one" {
		t.Fatalf("invalid created target: %#v %v", target, err)
	}

	target.Name = "Renamed"
	payload, _ = json.Marshal(target)
	request = localRequest(http.MethodPut, "/api/targets/"+target.ID, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("update failed: %d %s", response.Code, response.Body.String())
	}
	var updated Target
	if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil || !updated.BypassTUN || updated.BypassInterfaceID != "adapter-one" {
		t.Fatalf("update lost bypass settings: %#v %v", updated, err)
	}

	request = localRequest(http.MethodDelete, "/api/targets/"+target.ID, nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete failed: %d %s", response.Code, response.Body.String())
	}
}

func TestAPIAllowsOnlyApprovedDesktopDevelopmentAndBrowserOrigins(t *testing.T) {
	monitor, server := testServer(t)
	defer monitor.Close()
	handler := server.Handler()

	approved := []string{
		originTauriWindows,
		originTauriMacOS,
		originDevLocalhost,
		originDevLoopback,
		"http://localhost:9288",
		"http://127.0.0.1:9288",
	}
	for _, origin := range approved {
		t.Run("allow_"+strings.NewReplacer(":", "_", "/", "_").Replace(origin), func(t *testing.T) {
			request := localRequest(http.MethodGet, "/api/health", nil)
			request.Header.Set("Origin", origin)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Header().Get("Access-Control-Allow-Origin") != origin {
				t.Fatalf("approved origin rejected: %d %s", response.Code, response.Body.String())
			}
		})
	}

	rejected := []string{
		"http://tauri.localhost.evil.example",
		"https://tauri.localhost",
		"tauri://evil.example",
		"http://localhost:3001",
		"http://127.0.0.1:5173",
		"http://127.0.0.2:9288",
	}
	for _, origin := range rejected {
		t.Run("reject_"+strings.NewReplacer(":", "_", "/", "_").Replace(origin), func(t *testing.T) {
			request := localRequest(http.MethodGet, "/api/health", nil)
			request.Header.Set("Origin", origin)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("unapproved origin accepted: %d %s", response.Code, response.Body.String())
			}
		})
	}

	request := localRequest(http.MethodOptions, "/api/snapshot", nil)
	request.Header.Set("Origin", originTauriWindows)
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != originTauriWindows {
		t.Fatalf("Tauri preflight failed: %d %s", response.Code, response.Body.String())
	}
}

func TestAPIRejectsUnknownJSONField(t *testing.T) {
	monitor, server := testServer(t)
	defer monitor.Close()
	request := localRequest(http.MethodPost, "/api/targets", strings.NewReader(`{"name":"bad","host":"127.0.0.1","port":80,"interval_ms":1000,"timeout_ms":500,"enabled":false,"extra":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown JSON field accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestAPICreatesGoogleNodeProbe(t *testing.T) {
	monitor, server := testServer(t)
	defer monitor.Close()
	target := Target{
		Name: "Current Proxy Node", Kind: ProbeKindProxyGoogle,
		Host: "ignored.example", Port: 80,
		ProxyHost: "127.0.0.1", ProxyPort: 10808,
		Google204Enabled: true,
		IntervalMS:       5000, TimeoutMS: 8000, Enabled: false,
	}
	payload, _ := json.Marshal(target)
	request := localRequest(http.MethodPost, "/api/targets", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create node probe failed: %d %s", response.Code, response.Body.String())
	}
	var created Target
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Kind != ProbeKindProxyGoogle || created.Host != GoogleProbeHost || created.Port != GoogleProbePort || created.ProxyPort != 10808 || !created.Google204Enabled {
		t.Fatalf("node probe was not normalized: %#v", created)
	}
}

func TestSSEStartsWithSnapshot(t *testing.T) {
	monitor, server := testServer(t)
	defer monitor.Close()
	testHTTP := httptest.NewServer(server.Handler())
	defer testHTTP.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	request, err := http.NewRequest(http.MethodGet, testHTTP.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = listenAddress
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	var received strings.Builder
	for !strings.Contains(received.String(), "event: snapshot") || !strings.Contains(received.String(), "\ndata: {") {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			t.Fatal(err)
		}
		received.WriteString(line)
		if received.Len() > 64*1024 || err == io.EOF {
			break
		}
	}
	if !strings.Contains(received.String(), "event: snapshot") || !strings.Contains(received.String(), `"generated_at"`) {
		t.Fatalf("initial SSE snapshot missing: %q", received.String())
	}
}

func TestHTTPServerGracefulShutdownDisconnectsSSE(t *testing.T) {
	hub := newEventHub()
	monitor, err := NewMonitor([]Target{{ID: "one", Name: "One", Host: "127.0.0.1", Port: 443, IntervalMS: 1000, TimeoutMS: 500, Enabled: false}}, nil, hub, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewAPIServerAt(monitor, hub, listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	server := api.HTTPServer()
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()

	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("graceful shutdown waited on SSE: %v", err)
	}
	select {
	case err := <-serveErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP Serve did not return after shutdown")
	}
}
