package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestManagedServeEmitsReadyAndStopsOnCommandOrEOF(t *testing.T) {
	tests := []struct {
		name string
		stop func(*io.PipeWriter) error
	}{
		{
			name: "shutdown command",
			stop: func(writer *io.PipeWriter) error {
				_, err := io.WriteString(writer, "ignored\nshutdown\n")
				return err
			},
		},
		{
			name: "stdin EOF",
			stop: func(writer *io.PipeWriter) error {
				return writer.Close()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			server := &http.Server{
				Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					writer.WriteHeader(http.StatusNoContent)
				}),
			}
			stdinReader, stdinWriter := io.Pipe()
			stdoutReader, stdoutWriter := io.Pipe()
			t.Cleanup(func() {
				_ = stdinReader.Close()
				_ = stdinWriter.Close()
				_ = stdoutReader.Close()
				_ = stdoutWriter.Close()
			})

			expected := readyMessage{
				Type:     "ready",
				Version:  serviceVersion,
				Protocol: serviceProtocol,
				Instance: "test-instance",
				Address:  address,
				URL:      "http://" + address,
			}
			done := make(chan error, 1)
			go func() {
				done <- serveHTTP(server, listener, serveOptions{
					managed:         true,
					stdin:           stdinReader,
					stdout:          stdoutWriter,
					signals:         make(chan os.Signal),
					ready:           expected,
					shutdownTimeout: time.Second,
				})
			}()

			var received readyMessage
			if err := json.NewDecoder(stdoutReader).Decode(&received); err != nil {
				t.Fatal(err)
			}
			if received != expected {
				t.Fatalf("unexpected ready message: %#v", received)
			}

			client := &http.Client{Timeout: time.Second}
			response, err := client.Get(expected.URL)
			if err != nil {
				t.Fatalf("ready emitted before HTTP became reachable: %v", err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("unexpected HTTP status: %d", response.StatusCode)
			}

			if err := test.stop(stdinWriter); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("managed server did not shut down")
			}
		})
	}
}
