//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
)

const interfaceBindingSupported = true

func listenOnInterface(ctx context.Context, address string, iface net.Interface) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var bindErr error
		if err := raw.Control(func(fd uintptr) {
			bindErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, iface.Name)
		}); err != nil {
			return err
		}
		if bindErr != nil {
			if errors.Is(bindErr, syscall.ENODEV) {
				return bindErr
			}
			return fmt.Errorf("%w on %s: %w", errInterfaceBinding, iface.Name, bindErr)
		}
		// Names may be reused while an interface is being recreated.
		current, err := net.InterfaceByName(iface.Name)
		if err != nil {
			return err
		}
		if current.Index != iface.Index {
			return fmt.Errorf("interface %s changed during bind", iface.Name)
		}
		return nil
	}}
	return lc.Listen(ctx, "tcp", address)
}
