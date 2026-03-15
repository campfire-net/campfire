package http

import (
	"net"
)

// listenTCP creates a TCP listener on the given address.
func listenTCP(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
