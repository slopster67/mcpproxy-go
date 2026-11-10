package server

import (
	"fmt"
)

// PortInUseError indicates that the requested listen address is already occupied.
type PortInUseError struct {
	Address string
	Err     error
}

func (e *PortInUseError) Error() string {
	return fmt.Sprintf("port %s is already in use", e.Address)
}

func (e *PortInUseError) Unwrap() error {
	return e.Err
}

// isAddrInUseError is implemented in platform-specific files (port_unix.go, port_windows.go)
