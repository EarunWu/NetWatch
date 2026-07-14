package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const maxGoogleResponseHeaderBytes = 16 * 1024

type googleProbeConfig struct {
	Host      string
	Port      int
	Path      string
	TLSConfig *tls.Config
	Nonce     func() string
}

func probeTarget(parent context.Context, target Target) Sample {
	return probeTargetOnce(parent, target)
}

func probeTargetOnce(parent context.Context, target Target) Sample {
	if target.Kind == ProbeKindProxyGoogle {
		return probeGoogleViaSOCKS5(parent, target)
	}
	sample := probeTCP(parent, target)
	sample.ProbeKind = ProbeKindDirectTCP
	sample.Stage = StageTCP
	return sample
}

func probeGoogleViaSOCKS5(parent context.Context, target Target) Sample {
	return probeGoogleViaSOCKS5WithConfig(parent, target, googleProbeConfig{
		Host: GoogleProbeHost,
		Port: GoogleProbePort,
		Path: GoogleProbePath,
		TLSConfig: &tls.Config{
			ServerName: GoogleProbeHost,
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"http/1.1"},
		},
		Nonce: probeNonce,
	})
}

func probeGoogleViaSOCKS5WithConfig(parent context.Context, target Target, config googleProbeConfig) Sample {
	ctx, cancel := context.WithTimeout(parent, time.Duration(target.TimeoutMS)*time.Millisecond)
	defer cancel()
	started := time.Now()
	sample := Sample{TargetID: target.ID, ProbeKind: ProbeKindProxyGoogle, Stage: StageLocalProxy}

	proxyAddress := net.JoinHostPort(target.ProxyHost, strconv.Itoa(target.ProxyPort))
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxyAddress)
	if err != nil {
		status := StatusLocalProxyError
		switch classifyProbeError(err) {
		case StatusTimeout:
			status = StatusLocalProxyTimeout
		case StatusRefused:
			status = StatusLocalProxyRefused
		}
		return finishNodeFailure(sample, time.Now(), status, err)
	}
	defer connection.Close()
	stopCancellation := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopCancellation()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}

	localCompleted := time.Now()
	sample.LocalProxy = durationMS(started, localCompleted)
	sample.Stage = StageSOCKS
	socksStatus, err := socks5Connect(connection, config.Host, config.Port)
	if err != nil {
		if isProbeTimeout(ctx, err) {
			socksStatus = StatusTimeout
		}
		if socksStatus == "" {
			socksStatus = StatusSOCKSProtocol
		}
		return finishNodeFailure(sample, time.Now(), socksStatus, err)
	}

	tunnelCompleted := time.Now()
	sample.Tunnel = durationMS(started, tunnelCompleted)
	sample.Stage = StageTLS
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if config.TLSConfig != nil {
		tlsConfig = config.TLSConfig.Clone()
	}
	if tlsConfig.ServerName == "" {
		tlsConfig.ServerName = config.Host
	}
	if len(tlsConfig.NextProtos) == 0 {
		tlsConfig.NextProtos = []string{"http/1.1"}
	}
	recordedConnection := &firstReadConn{
		Conn: connection,
		onFirstRead: func(completed time.Time) {
			sample.RemoteFirstByte = durationMS(started, completed)
		},
	}
	tlsConnection := tls.Client(recordedConnection, tlsConfig)
	if err := tlsConnection.HandshakeContext(ctx); err != nil {
		status := StatusTLSError
		if isProbeTimeout(ctx, err) {
			status = StatusTimeout
		} else if isCertificateError(err) {
			status = StatusTLSCertificate
		}
		return finishNodeFailure(sample, time.Now(), status, err)
	}

	tlsCompleted := time.Now()
	sample.TLS = durationMS(started, tlsCompleted)
	if !target.Google204Enabled {
		sample.TS = tlsCompleted.UnixMilli()
		sample.Latency = floatPointer(*sample.TLS)
		sample.Status = StatusSuccess
		return sample
	}

	sample.Stage = StageHTTP
	nonce := probeNonce()
	if config.Nonce != nil {
		nonce = config.Nonce()
	}
	requestPath := config.Path + "?netwatch=" + nonce
	request := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: NetWatch/0.3\r\nAccept: */*\r\nCache-Control: no-cache\r\nConnection: close\r\n\r\n",
		requestPath,
		config.Host,
	)
	if _, err := io.WriteString(tlsConnection, request); err != nil {
		status := StatusHTTPError
		if isProbeTimeout(ctx, err) {
			status = StatusTimeout
		}
		return finishNodeFailure(sample, time.Now(), status, err)
	}

	limited := io.LimitReader(tlsConnection, maxGoogleResponseHeaderBytes+1)
	reader := bufio.NewReaderSize(limited, 4096)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		status := StatusHTTPError
		if isProbeTimeout(ctx, err) {
			status = StatusTimeout
		}
		return finishNodeFailure(sample, time.Now(), status, err)
	}
	defer response.Body.Close()
	sample.HTTPStatus = response.StatusCode
	if response.StatusCode != http.StatusNoContent {
		return finishNodeFailure(
			sample,
			time.Now(),
			StatusUnexpectedHTTP,
			fmt.Errorf("Google probe returned HTTP %d instead of 204", response.StatusCode),
		)
	}

	completed := time.Now()
	googleMS := durationMS(started, completed)
	sample.TS = completed.UnixMilli()
	sample.Google = googleMS
	sample.Latency = floatPointer(*googleMS)
	sample.Status = StatusSuccess
	return sample
}

type firstReadConn struct {
	net.Conn
	once        sync.Once
	onFirstRead func(time.Time)
}

func (connection *firstReadConn) Read(buffer []byte) (int, error) {
	count, err := connection.Conn.Read(buffer)
	if count > 0 && connection.onFirstRead != nil {
		connection.once.Do(func() { connection.onFirstRead(time.Now()) })
	}
	return count, err
}

func socks5Connect(connection net.Conn, host string, port int) (string, error) {
	if len(host) == 0 || len(host) > 255 {
		return StatusSOCKSProtocol, errors.New("SOCKS5 target host must be 1 to 255 bytes")
	}
	if _, err := connection.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return StatusSOCKSProtocol, fmt.Errorf("write SOCKS5 greeting: %w", err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(connection, methodReply); err != nil {
		return StatusSOCKSProtocol, fmt.Errorf("read SOCKS5 greeting: %w", err)
	}
	if methodReply[0] != 0x05 {
		return StatusSOCKSProtocol, fmt.Errorf("unexpected SOCKS version %d", methodReply[0])
	}
	if methodReply[1] != 0x00 {
		return StatusSOCKSAuthFailed, fmt.Errorf("SOCKS5 proxy requires unsupported authentication method 0x%02x", methodReply[1])
	}

	request := make([]byte, 0, 7+len(host))
	request = append(request, 0x05, 0x01, 0x00, 0x03, byte(len(host)))
	request = append(request, host...)
	request = append(request, byte(port>>8), byte(port))
	if _, err := connection.Write(request); err != nil {
		return StatusSOCKSProtocol, fmt.Errorf("write SOCKS5 CONNECT: %w", err)
	}

	reply := make([]byte, 4)
	if _, err := io.ReadFull(connection, reply); err != nil {
		return StatusSOCKSProtocol, fmt.Errorf("read SOCKS5 CONNECT: %w", err)
	}
	if reply[0] != 0x05 || reply[2] != 0x00 {
		return StatusSOCKSProtocol, errors.New("invalid SOCKS5 CONNECT response")
	}
	if err := discardSOCKS5Address(connection, reply[3]); err != nil {
		return StatusSOCKSProtocol, err
	}
	if reply[1] != 0x00 {
		return StatusSOCKSRejected, fmt.Errorf("SOCKS5 CONNECT rejected with code 0x%02x", reply[1])
	}
	return "", nil
}

func discardSOCKS5Address(reader io.Reader, addressType byte) error {
	length := 0
	switch addressType {
	case 0x01:
		length = net.IPv4len
	case 0x04:
		length = net.IPv6len
	case 0x03:
		encodedLength := []byte{0}
		if _, err := io.ReadFull(reader, encodedLength); err != nil {
			return fmt.Errorf("read SOCKS5 bound host length: %w", err)
		}
		length = int(encodedLength[0])
	default:
		return fmt.Errorf("unsupported SOCKS5 address type 0x%02x", addressType)
	}
	if _, err := io.CopyN(io.Discard, reader, int64(length+2)); err != nil {
		return fmt.Errorf("read SOCKS5 bound address: %w", err)
	}
	return nil
}

func isProbeTimeout(ctx context.Context, err error) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded) || classifyProbeError(err) == StatusTimeout
}

func isCertificateError(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	return errors.As(err, &unknownAuthority) || errors.As(err, &hostnameError) || errors.As(err, &invalidCertificate)
}

func finishNodeFailure(sample Sample, completed time.Time, status string, err error) Sample {
	sample.TS = completed.UnixMilli()
	sample.Status = status
	if err != nil {
		sample.Message = truncateMessage(err.Error(), 240)
	}
	return sample
}

func durationMS(started, completed time.Time) *float64 {
	value := float64(completed.Sub(started).Microseconds()) / 1000
	value = float64(int64(value*1000+0.5)) / 1000
	return &value
}

func probeNonce() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buffer)
}
