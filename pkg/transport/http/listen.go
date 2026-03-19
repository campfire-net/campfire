package http

import (
	"crypto/tls"
	"net"
)

// listenTCP creates a TCP listener on the given address.
func listenTCP(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

// listenTLS creates a TLS listener on the given address using the provided certificate and key files.
// certFile and keyFile must be PEM-encoded. Use this instead of listenTCP when TLS is required.
func listenTLS(addr, certFile, keyFile string) (net.Listener, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	return tls.Listen("tcp", addr, cfg)
}

// TLSConfig holds optional TLS configuration for the transport server.
type TLSConfig struct {
	CertFile string
	KeyFile  string
}
