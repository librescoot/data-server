package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

var errInterfaceBinding = errors.New("cannot enforce interface binding")

func trustedInterface(name string) bool {
	return name == "lo" || name == "usb0" || name == "ppp0" || strings.HasPrefix(name, "wg")
}

type interfaceListener struct {
	name string
	io.Closer
}

type listenerSet struct {
	active   map[int]interfaceListener
	failures map[int]string
	open     func(net.Interface) (io.Closer, error)
}

func (s *listenerSet) close() {
	for index, listener := range s.active {
		listener.Close()
		delete(s.active, index)
	}
}

func (s *listenerSet) reconcile(interfaces []net.Interface) error {
	wanted := make(map[int]string)
	for _, iface := range interfaces {
		if trustedInterface(iface.Name) {
			wanted[iface.Index] = iface.Name
		}
	}
	for index, listener := range s.active {
		if wanted[index] != listener.name {
			listener.Close()
			delete(s.active, index)
		}
	}
	for index := range s.failures {
		if _, ok := wanted[index]; !ok {
			delete(s.failures, index)
		}
	}
	for _, iface := range interfaces {
		if !trustedInterface(iface.Name) {
			continue
		}
		if _, ok := s.active[iface.Index]; ok {
			continue
		}
		listener, err := s.open(iface)
		if err != nil {
			if errors.Is(err, errInterfaceBinding) {
				return err
			}
			if s.failures[iface.Index] != err.Error() {
				log.Printf("waiting for listener on %s: %v", iface.Name, err)
				s.failures[iface.Index] = err.Error()
			}
			continue
		}
		delete(s.failures, iface.Index)
		s.active[iface.Index] = interfaceListener{name: iface.Name, Closer: listener}
	}
	return nil
}

type deviceServer struct {
	listener net.Listener
	server   *http.Server
}

func (s *deviceServer) Close() error {
	// Close the socket even if Serve has not registered it yet.
	s.listener.Close()
	return s.server.Close()
}

func serveTrusted(ctx context.Context, address string, handler http.Handler) error {
	if !interfaceBindingSupported {
		return fmt.Errorf("%w: Linux is required", errInterfaceBinding)
	}
	resolved, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return err
	}
	address = resolved.String()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type serveFailure struct {
		index  int
		server *deviceServer
		err    error
	}
	failed := make(chan serveFailure)
	s := listenerSet{active: make(map[int]interfaceListener), failures: make(map[int]string)}
	s.open = func(iface net.Interface) (io.Closer, error) {
		listener, err := listenOnInterface(ctx, address, iface)
		if err != nil {
			return nil, err
		}
		server := &deviceServer{listener: listener, server: &http.Server{Handler: handler}}
		log.Printf("listening on %s via %s", listener.Addr(), iface.Name)
		go func() {
			err := server.server.Serve(listener)
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			select {
			case failed <- serveFailure{iface.Index, server, err}:
			case <-ctx.Done():
			}
		}()
		return server, nil
	}
	defer s.close()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		interfaces, err := net.Interfaces()
		if err != nil {
			// An unknown interface inventory must not retain stale access.
			s.close()
			log.Printf("cannot enumerate interfaces: %v", err)
		} else if err := s.reconcile(interfaces); err != nil {
			return fmt.Errorf("restricted listener: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case failure := <-failed:
			if active, ok := s.active[failure.index]; ok && active.Closer == failure.server {
				failure.server.Close()
				delete(s.active, failure.index)
				log.Printf("listener on %s stopped: %v", active.name, failure.err)
			}
		case <-ticker.C:
		}
	}
}
