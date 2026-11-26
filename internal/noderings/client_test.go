package noderings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestListVirtualMachines(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-Organization-ID") != "org1" {
			t.Errorf("headers: %v", r.Header)
		}
		if r.URL.Path != "/v1/virtual-machines" || r.URL.Query().Get("pageSize") != "-1" {
			t.Errorf("request: %s", r.URL)
		}
		if n == 1 {
			http.Error(w, `{"code":14,"message":"unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"virtualMachines":[{"id":"a","cpu":"2","memory":1024,"storage":"20480","tags":["x"],"additionalIpv4":[{"ipAddress":"198.51.100.1"}]}],"totalCount":"1"}`))
	}))
	defer srv.Close()

	c, err := New(Config{Endpoint: srv.URL, Token: "tok", OrganizationID: "org1", MinBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	vms, err := c.ListVirtualMachines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) != 1 || vms[0].CPU != 2 || vms[0].Memory != 1024 || vms[0].Storage != 20480 || vms[0].AdditionalIPv4[0].IPAddress != "198.51.100.1" {
		t.Fatalf("vms = %+v", vms)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (one retry after 503)", calls.Load())
	}
}

func TestUnauthenticatedIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":16,"message":"invalid token"}`))
	}))
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "bad", MinBackoff: time.Millisecond})
	_, err := c.ListVirtualMachines(context.Background())
	if !IsUnauthenticated(err) || calls.Load() != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Config{Token: ""}); err == nil {
		t.Error("empty token accepted")
	}
	if _, err := New(Config{Endpoint: "not a url", Token: "x"}); err == nil {
		t.Error("bad URL accepted")
	}
}
