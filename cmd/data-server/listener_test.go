package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"testing"
)

type countedCloser struct{ closed int }

func (c *countedCloser) Close() error { c.closed++; return nil }

func TestDefaultListenAddress(t *testing.T) {
	if got := flag.Lookup("addr").DefValue; got != ":8080" {
		t.Fatalf("default address=%q", got)
	}
}

func TestTrustedInterfaceNames(t *testing.T) {
	for _, name := range []string{"lo", "usb0", "ppp0", "wg", "wg0", "wg-maintenance"} {
		if !trustedInterface(name) {
			t.Errorf("rejected %q", name)
		}
	}
	for _, name := range []string{"", "wwan0", "eth0", "end0", "wlan0", "usb1", "ppp1", "lo0", "otherwg0"} {
		if trustedInterface(name) {
			t.Errorf("trusted %q", name)
		}
	}
}

func TestListenerLifecycle(t *testing.T) {
	opened := make(map[int][]*countedCloser)
	s := listenerSet{active: make(map[int]interfaceListener), failures: make(map[int]string)}
	s.open = func(iface net.Interface) (io.Closer, error) {
		c := &countedCloser{}
		opened[iface.Index] = append(opened[iface.Index], c)
		return c, nil
	}
	first := []net.Interface{{Index: 1, Name: "lo"}, {Index: 2, Name: "wwan0"}}
	if err := s.reconcile(first); err != nil {
		t.Fatal(err)
	}
	if len(s.active) != 1 || len(opened[2]) != 0 {
		t.Fatal("untrusted listener opened")
	}
	added := append(first, net.Interface{Index: 3, Name: "usb0"}, net.Interface{Index: 4, Name: "ppp0"}, net.Interface{Index: 5, Name: "wg0"})
	if err := s.reconcile(added); err != nil {
		t.Fatal(err)
	}
	if len(s.active) != 4 {
		t.Fatal("late interfaces not added")
	}
	if err := s.reconcile(added); err != nil {
		t.Fatal(err)
	}
	if len(opened[3]) != 1 {
		t.Fatal("unchanged interface rebound")
	}
	// A replacement has the same name but a different kernel index.
	replaced := append(first, net.Interface{Index: 6, Name: "usb0"})
	if err := s.reconcile(replaced); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{3, 4, 5} {
		if opened[index][0].closed != 1 {
			t.Fatalf("old interface %d remains open", index)
		}
	}
	if len(opened[6]) != 1 {
		t.Fatal("replacement not opened")
	}
	if err := s.reconcile(append(first, net.Interface{Index: 6, Name: "wwan1"})); err != nil {
		t.Fatal(err)
	}
	if opened[6][0].closed != 1 {
		t.Fatal("renamed interface remains open")
	}
	s.close()
	if opened[1][0].closed != 1 || len(s.active) != 0 {
		t.Fatal("shutdown did not close listeners")
	}
}

func TestListenerRetriesUnavailableAddress(t *testing.T) {
	calls := 0
	s := listenerSet{active: make(map[int]interfaceListener), failures: make(map[int]string)}
	s.open = func(net.Interface) (io.Closer, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("address not available")
		}
		return &countedCloser{}, nil
	}
	interfaces := []net.Interface{{Index: 1, Name: "lo"}}
	if err := s.reconcile(interfaces); err != nil {
		t.Fatal(err)
	}
	if len(s.active) != 0 {
		t.Fatal("failed bind retained")
	}
	if err := s.reconcile(interfaces); err != nil {
		t.Fatal(err)
	}
	if len(s.active) != 1 || len(s.failures) != 0 {
		t.Fatal("address availability not retried")
	}
	s.close()
}

func TestBindingFailureNeverFallsBack(t *testing.T) {
	calls := 0
	s := listenerSet{active: make(map[int]interfaceListener), failures: make(map[int]string), open: func(net.Interface) (io.Closer, error) {
		calls++
		return nil, errInterfaceBinding
	}}
	if err := s.reconcile([]net.Interface{{Index: 1, Name: "lo"}}); !errors.Is(err, errInterfaceBinding) {
		t.Fatalf("error=%v", err)
	}
	if calls != 1 || len(s.active) != 0 {
		t.Fatal("binding failure fell back")
	}
}

func TestInvalidListenAddressFails(t *testing.T) {
	if err := serveTrusted(context.Background(), "not-an-address", http.NotFoundHandler()); err == nil {
		t.Fatal("invalid address accepted")
	}
}
