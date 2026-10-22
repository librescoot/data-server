//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"
)

func TestDeviceBoundLoopback(t *testing.T) {
	iface, err := net.InterfaceByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(address, func(t *testing.T) {
			listener, err := listenOnInterface(context.Background(), address, *iface)
			if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EAFNOSUPPORT) {
				t.Skip(err)
			}
			if err != nil {
				t.Fatal(err)
			}
			server := &deviceServer{listener: listener, server: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "trusted") })}}
			done := make(chan struct{})
			go func() { server.server.Serve(listener); close(done) }()
			t.Cleanup(func() { server.Close(); <-done })
			// Use the real loopback device even on hosts with localhost proxy routes.
			dialer := net.Dialer{Control: func(_, _ string, raw syscall.RawConn) error {
				var bindErr error
				if err := raw.Control(func(fd uintptr) {
					bindErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, "lo")
				}); err != nil {
					return err
				}
				return bindErr
			}}
			client := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext}, Timeout: time.Second}
			defer client.CloseIdleConnections()
			resp, err := client.Get("http://" + listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(body) != "trusted" {
				t.Fatalf("response=%q, err=%v", body, err)
			}
		})
	}
}

func TestInterfaceIdentityCheckedBeforeListen(t *testing.T) {
	iface, err := net.InterfaceByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	iface.Index++
	listener, err := listenOnInterface(context.Background(), "127.0.0.1:0", *iface)
	if listener != nil {
		listener.Close()
		t.Fatal("bound using a stale interface identity")
	}
	if err == nil {
		t.Fatal("interface replacement was ignored")
	}
}

func TestServeCancellationClosesListeners(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveTrusted(ctx, "127.0.0.1:0", http.NotFoundHandler()) }()
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not complete")
	}
}
