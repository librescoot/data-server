//go:build !linux

package main

import (
	"context"
	"net"
)

const interfaceBindingSupported = false

func listenOnInterface(context.Context, string, net.Interface) (net.Listener, error) {
	return nil, errInterfaceBinding
}
