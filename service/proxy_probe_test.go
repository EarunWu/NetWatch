package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGoogleProbeViaSOCKS5RecordsAllPhases(t *testing.T) {
	requestSeen := make(chan *http.Request, 1)
	google := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestSeen <- request.Clone(context.Background())
		writer.WriteHeader(http.StatusNoContent)
	}))
	google.EnableHTTP2 = false
	google.StartTLS()
	defer google.Close()

	proxyHost, proxyPort := startForwardingSOCKS5(t, google.Listener.Addr().String())
	clientTLS := google.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	clientTLS.InsecureSkipVerify = true // Test server certificate only; production never disables verification.
	clientTLS.NextProtos = []string{"http/1.1"}
	target := Target{
		ID: "node-one", Kind: ProbeKindProxyGoogle,
		ProxyHost: proxyHost, ProxyPort: proxyPort,
		TimeoutMS: 2000, Google204Enabled: true,
	}
	sample := probeGoogleViaSOCKS5WithConfig(context.Background(), target, googleProbeConfig{
		Host: "www.google.com", Port: GoogleProbePort, Path: GoogleProbePath,
		TLSConfig: clientTLS, Nonce: func() string { return "fixed-nonce" },
	})
	if sample.Status != StatusSuccess || sample.HTTPStatus != http.StatusNoContent {
		t.Fatalf("Google probe failed: %#v", sample)
	}
	if sample.LocalProxy == nil || sample.Tunnel == nil || sample.RemoteFirstByte == nil || sample.TLS == nil || sample.Google == nil || sample.Latency == nil {
		t.Fatalf("phase timing is incomplete: %#v", sample)
	}
	if *sample.LocalProxy > *sample.Tunnel || *sample.Tunnel > *sample.RemoteFirstByte || *sample.RemoteFirstByte > *sample.TLS || *sample.TLS > *sample.Google || *sample.Google != *sample.Latency {
		t.Fatalf("phase timings are not cumulative: %#v", sample)
	}
	select {
	case request := <-requestSeen:
		if request.URL.Path != GoogleProbePath || request.URL.Query().Get("netwatch") != "fixed-nonce" {
			t.Fatalf("unexpected request URL: %s", request.URL.String())
		}
		if request.Header.Get("Cache-Control") != "no-cache" || !request.Close {
			t.Fatalf("probe request may be cached or reused: %#v", request.Header)
		}
	case <-time.After(time.Second):
		t.Fatal("Google test endpoint did not receive a request")
	}
}

func TestGoogleProbeDefaultsToTLSOnly(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	google := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestSeen <- struct{}{}
		writer.WriteHeader(http.StatusNoContent)
	}))
	google.EnableHTTP2 = false
	google.StartTLS()
	defer google.Close()

	proxyHost, proxyPort := startForwardingSOCKS5(t, google.Listener.Addr().String())
	clientTLS := google.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	clientTLS.InsecureSkipVerify = true // Test server certificate only; production never disables verification.
	clientTLS.NextProtos = []string{"http/1.1"}
	target := Target{
		ID: "tls-only", Kind: ProbeKindProxyGoogle,
		ProxyHost: proxyHost, ProxyPort: proxyPort,
		TimeoutMS: 2000,
	}
	sample := probeGoogleViaSOCKS5WithConfig(context.Background(), target, googleProbeConfig{
		Host: "www.google.com", Port: GoogleProbePort, Path: GoogleProbePath,
		TLSConfig: clientTLS,
	})
	if sample.Status != StatusSuccess || sample.Stage != StageTLS || sample.HTTPStatus != 0 {
		t.Fatalf("TLS-only probe failed: %#v", sample)
	}
	if sample.TLS == nil || sample.Latency == nil || sample.Google != nil || *sample.TLS != *sample.Latency {
		t.Fatalf("TLS-only terminal timing is incorrect: %#v", sample)
	}
	select {
	case <-requestSeen:
		t.Fatal("TLS-only probe unexpectedly sent an HTTP request")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestGoogleProbeUsesConfiguredSOCKSEndpoint(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{name: "domain", host: "status.example"},
		{name: "IPv4", host: "192.0.2.10"},
		{name: "IPv6", host: "2001:db8::10"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proxyHost, proxyPort := startSOCKS5Responder(t, func(connection net.Conn) {
				readSOCKS5Request(t, connection, test.host, 8443)
				_, _ = connection.Write([]byte{0x05, 0x05, 0x00, 0x01, 127, 0, 0, 1, 0, 0})
			})
			target := Target{ID: "rejected", Kind: ProbeKindProxyGoogle, Host: test.host, Port: 8443, ProxyHost: proxyHost, ProxyPort: proxyPort, TimeoutMS: 1000}
			sample := probeGoogleViaSOCKS5(context.Background(), target)
			if sample.Status != StatusSOCKSRejected || sample.Stage != StageSOCKS || sample.LocalProxy == nil || sample.Tunnel != nil {
				t.Fatalf("unexpected rejected sample: %#v", sample)
			}
		})
	}
}

func TestGoogleProbeClassifiesSOCKSTimeout(t *testing.T) {
	host, port := startSOCKS5Responder(t, func(connection net.Conn) {
		buffer := make([]byte, 3)
		_, _ = io.ReadFull(connection, buffer)
		_, _ = io.Copy(io.Discard, connection)
	})
	target := Target{ID: "timeout", Kind: ProbeKindProxyGoogle, ProxyHost: host, ProxyPort: port, TimeoutMS: 100}
	sample := probeGoogleViaSOCKS5(context.Background(), target)
	if sample.Status != StatusTimeout || sample.Stage != StageSOCKS || sample.LocalProxy == nil {
		t.Fatalf("unexpected timeout sample: %#v", sample)
	}
}

func TestGoogleProbeClassifiesLocalProxyRefusal(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	target := Target{ID: "down", Kind: ProbeKindProxyGoogle, ProxyHost: "127.0.0.1", ProxyPort: port, TimeoutMS: 500}
	sample := probeGoogleViaSOCKS5(context.Background(), target)
	if sample.Status != StatusLocalProxyRefused || sample.Stage != StageLocalProxy || sample.LocalProxy != nil {
		t.Fatalf("unexpected local proxy failure: %#v", sample)
	}
}

func startForwardingSOCKS5(t *testing.T, upstreamAddress string) (string, int) {
	t.Helper()
	return startSOCKS5Responder(t, func(client net.Conn) {
		readSOCKS5Request(t, client, GoogleProbeHost, GoogleProbePort)
		upstream, err := net.Dial("tcp", upstreamAddress)
		if err != nil {
			t.Errorf("dial test endpoint: %v", err)
			return
		}
		defer upstream.Close()
		if _, err := client.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0}); err != nil {
			t.Errorf("write SOCKS5 success: %v", err)
			return
		}
		go func() {
			_, _ = io.Copy(upstream, client)
		}()
		_, _ = io.Copy(client, upstream)
	})
}

func startSOCKS5Responder(t *testing.T, responder func(net.Conn)) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		responder(connection)
	}()
	address := listener.Addr().(*net.TCPAddr)
	return address.IP.String(), address.Port
}

func readSOCKS5Request(t *testing.T, connection net.Conn, expectedHost string, expectedPort int) string {
	t.Helper()
	greeting := make([]byte, 3)
	if _, err := io.ReadFull(connection, greeting); err != nil {
		t.Errorf("read SOCKS5 greeting: %v", err)
		return ""
	}
	if string(greeting) != string([]byte{0x05, 0x01, 0x00}) {
		t.Errorf("unexpected SOCKS5 greeting: %v", greeting)
		return ""
	}
	if _, err := connection.Write([]byte{0x05, 0x00}); err != nil {
		t.Errorf("write SOCKS5 method: %v", err)
		return ""
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		t.Errorf("read SOCKS5 request: %v", err)
		return ""
	}
	if header[0] != 0x05 || header[1] != 0x01 {
		t.Errorf("unexpected SOCKS5 CONNECT header: %v", header)
		return ""
	}
	var host string
	switch header[3] {
	case 0x01:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(connection, address); err != nil {
			t.Errorf("read SOCKS5 IPv4 target: %v", err)
			return ""
		}
		host = net.IP(address).String()
	case 0x04:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(connection, address); err != nil {
			t.Errorf("read SOCKS5 IPv6 target: %v", err)
			return ""
		}
		host = net.IP(address).String()
	case 0x03:
		length := []byte{0}
		if _, err := io.ReadFull(connection, length); err != nil {
			t.Errorf("read SOCKS5 target host length: %v", err)
			return ""
		}
		address := make([]byte, int(length[0]))
		if _, err := io.ReadFull(connection, address); err != nil {
			t.Errorf("read SOCKS5 target host: %v", err)
			return ""
		}
		host = string(address)
	default:
		t.Errorf("unexpected SOCKS5 address type: 0x%02x", header[3])
		return ""
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(connection, port); err != nil {
		t.Errorf("read SOCKS5 target port: %v", err)
		return ""
	}
	if host != expectedHost {
		t.Errorf("unexpected SOCKS5 target: %s", host)
	}
	if int(port[0])<<8|int(port[1]) != expectedPort {
		t.Errorf("unexpected SOCKS5 target port: %s", strconv.Itoa(int(port[0])<<8|int(port[1])))
	}
	return strings.TrimSpace(host)
}
