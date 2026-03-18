// Package portalloc finds available TCP ports on the local host.
// It is used by platform adapters to resolve `host: 0` or `hostRange: "start-end"`
// in a stack manifest's PortSpec at deployment time.
package portalloc

import (
	"fmt"
	"net"
	"sync"
)

var mu sync.Mutex

// FindAvailable returns the first available port starting at preferred.
// If preferred is 0 the OS picks a random ephemeral port.
func FindAvailable(preferred int) (int, error) {
	mu.Lock()
	defer mu.Unlock()
	if preferred == 0 {
		return randomPort()
	}
	for port := preferred; port < preferred+200; port++ {
		if available(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port found near %d", preferred)
}

// FindInRange returns the first available port in [start, end].
func FindInRange(start, end int) (int, error) {
	mu.Lock()
	defer mu.Unlock()
	for port := start; port <= end; port++ {
		if available(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port in range %d-%d", start, end)
}

func available(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func randomPort() (int, error) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}
