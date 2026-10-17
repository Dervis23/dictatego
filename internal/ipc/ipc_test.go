package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitForSocket blocks until the daemon's socket appears or times out.
func waitForSocket(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket oluşmadı: %s", path)
}

func TestServeAndCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sock")
	handler := func(req Request) (Response, error) {
		if req.Cmd != "status" {
			return Response{}, fmt.Errorf("test")
		}
		return Response{OK: true, State: "recording", Text: "merhaba"}, nil
	}
	done := make(chan error, 1)
	go func() { done <- Serve(path, handler) }()
	waitForSocket(t, path)

	resp, err := callAt(path, Request{Cmd: "status"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !resp.OK || resp.State != "recording" || resp.Text != "merhaba" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	// Unknown command yields an error in the response.
	resp, err = callAt(path, Request{Cmd: "x"})
	if err != nil {
		t.Fatalf("unknown cmd call: %v", err)
	}
	if resp.OK || resp.Err == "" {
		t.Fatalf("expected failure response: %+v", resp)
	}
}

// Double-start is refused while another daemon answers.
func TestServeTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t2.sock")
	handler := func(req Request) (Response, error) {
		return Response{OK: true, State: req.Cmd}, nil
	}
	go Serve(path, handler)
	waitForSocket(t, path)
	if _, err := callAt(path, Request{Cmd: "ok"}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := Serve(path, handler); err == nil {
		t.Fatal("second Serve must fail")
	}
}

