// Package ipc speaks newline-delimited JSON over a unix socket so Hyprland
// binds (plain execs of the CLI) can drive a single running daemon.
package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// Request is one CLI → daemon command.
type Request struct {
	Cmd string `json:"cmd"` // toggle | stop | cancel | status | refresh
}

// Response is the daemon's reply.
type Response struct {
	OK    bool   `json:"ok"`
	State string `json:"state"`
	Text  string `json:"text,omitempty"`
	Err   string `json:"err,omitempty"`
}

// SocketPath returns the daemon socket inside XDG_RUNTIME_DIR.
func SocketPath() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return "", fmt.Errorf("XDG_RUNTIME_DIR ayarlı değil")
	}
	return filepath.Join(dir, "dictatego.sock"), nil
}

// Handler processes one request and may return an error shown to the CLI.
type Handler func(Request) (Response, error)

// Serve listens until lsock is closed or the process exits. Stale sockets
// left by crashed daemons are removed first.
func Serve(path string, handler Handler) error {
	if fi, err := os.Stat(path); err == nil {
		if isSocket(fi.Mode()) {
			// Only remove it ourselves if nobody is answering.
			if _, err := callAt(path, Request{Cmd: "status"}); err == nil {
				return fmt.Errorf("daemon zaten çalışıyor")
			}
		}
		_ = os.Remove(path)
	}
	lsock, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("socket açılamadı: %w", err)
	}
	defer func() {
		_ = lsock.Close()
		_ = os.Remove(path)
	}()

	for {
		conn, err := lsock.Accept()
		if err != nil {
			return err
		}
		go handleConn(conn, handler)
	}
}

func isSocket(m os.FileMode) bool { return m&os.ModeSocket != 0 }

func handleConn(conn net.Conn, handler Handler) {
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	var req Request
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		_ = writeResponse(conn, Response{OK: false, Err: "komut çözülemedi"})
		return
	}
	resp, herr := handler(req)
	if herr != nil {
		resp = Response{OK: false, Err: herr.Error()}
	}
	_ = writeResponse(conn, resp)
}

func writeResponse(conn net.Conn, resp Response) error {
	raw, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = conn.Write(raw)
	return err
}

// Call posts one command to the daemon and returns its response.
func Call(req Request) (Response, error) {
	path, err := SocketPath()
	if err != nil {
		return Response{}, err
	}
	return callAt(path, req)
}

// callAt dials an explicit socket path (tests use temp dirs).
func callAt(path string, req Request) (Response, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return Response{}, fmt.Errorf("daemon çalışmıyor: %w", err)
	}
	defer conn.Close()
	raw, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		return Response{}, err
	}
	respRaw, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return Response{}, fmt.Errorf("yanıt alınamadı: %w", err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(respRaw), &resp); err != nil {
		return Response{}, fmt.Errorf("yanıt çözülemedi: %w", err)
	}
	return resp, nil
}
