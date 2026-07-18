package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateLoopbackAddressAcceptsOnlyLiteralLoopbackAddresses(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"127.0.0.1:0", "127.23.45.67:8090", "[::1]:8090"} {
		if err := validateLoopbackAddress(address); err != nil {
			t.Errorf("validateLoopbackAddress(%q) = %v, want success", address, err)
		}
	}

	for _, address := range []string{
		":8090",
		"0.0.0.0:8090",
		"[::]:8090",
		"192.0.2.10:8090",
		"localhost:8090",
		"example.com:8090",
		"127.0.0.1",
	} {
		if err := validateLoopbackAddress(address); err == nil {
			t.Errorf("validateLoopbackAddress(%q) unexpectedly succeeded", address)
		}
	}
}

func TestListenUnixSocketRestrictsPermissionsAndCleansUp(t *testing.T) {
	path := unixSocketTestPath(t)
	listener, err := listenUnixSocket(path)
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect Unix socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode = %s, want Unix socket", info.Mode())
	}
	if permissions := info.Mode().Perm(); permissions != unixSocketMode {
		t.Errorf("socket permissions = %04o, want %04o", permissions, unixSocketMode)
	}

	if err := listener.Close(); err != nil {
		t.Fatalf("close Unix socket listener: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("closed Unix socket still exists: %v", err)
	}
}

func TestListenUnixSocketRejectsCollisionsAndUnsafePaths(t *testing.T) {
	t.Run("non-socket collision", func(t *testing.T) {
		path := unixSocketTestPath(t)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("create colliding file: %v", err)
		}

		_, err := listenUnixSocket(path)
		if err == nil || !strings.Contains(err.Error(), "not a socket") {
			t.Fatalf("non-socket collision error = %v, want rejection", err)
		}
	})

	t.Run("stale socket collision", func(t *testing.T) {
		path := unixSocketTestPath(t)
		listener, err := listenUnixSocket(path)
		if err != nil {
			t.Fatalf("create Unix socket: %v", err)
		}
		defer listener.Close()

		_, err = listenUnixSocket(path)
		if err == nil || !strings.Contains(err.Error(), "refuse to replace") {
			t.Fatalf("socket collision error = %v, want stale-socket rejection", err)
		}
	})

	t.Run("relative path", func(t *testing.T) {
		_, err := listenUnixSocket("mithra.sock")
		if err == nil || !strings.Contains(err.Error(), "must be absolute") {
			t.Fatalf("relative socket path error = %v, want rejection", err)
		}
	})

	t.Run("other-accessible parent", func(t *testing.T) {
		path := unixSocketTestPath(t)
		directory := filepath.Dir(path)
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatalf("make socket directory other-accessible: %v", err)
		}
		_, err := listenUnixSocket(path)
		if err == nil || !strings.Contains(err.Error(), "must deny all permissions to other users") {
			t.Fatalf("other-accessible socket parent error = %v, want rejection", err)
		}
	})

	t.Run("group-writable parent", func(t *testing.T) {
		path := unixSocketTestPath(t)
		directory := filepath.Dir(path)
		if err := os.Chmod(directory, 0o770); err != nil {
			t.Fatalf("make socket directory group-writable: %v", err)
		}
		_, err := listenUnixSocket(path)
		if err == nil || !strings.Contains(err.Error(), "group or world writable") {
			t.Fatalf("group-writable socket parent error = %v, want rejection", err)
		}
	})

	t.Run("world-writable parent", func(t *testing.T) {
		path := unixSocketTestPath(t)
		directory := filepath.Dir(path)
		if err := os.Chmod(directory, 0o702); err != nil {
			t.Fatalf("make socket directory world-writable: %v", err)
		}
		_, err := listenUnixSocket(path)
		if err == nil || !strings.Contains(err.Error(), "group or world writable") {
			t.Fatalf("world-writable socket parent error = %v, want rejection", err)
		}
	})

	t.Run("owner-and-proxy-group parent", func(t *testing.T) {
		path := unixSocketTestPath(t)
		directory := filepath.Dir(path)
		if err := os.Chmod(directory, 0o750); err != nil {
			t.Fatalf("make socket directory proxy-group accessible: %v", err)
		}
		listener, err := listenUnixSocket(path)
		if err != nil {
			t.Fatalf("listen with owner-and-proxy-group socket parent: %v", err)
		}
		if err := listener.Close(); err != nil {
			t.Fatalf("close owner-and-proxy-group Unix socket listener: %v", err)
		}
	})
}

func TestListenRejectsConfiguredLoopbackAndUnixSocketTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mithra.sock")
	if err := run([]string{"--addr", "127.0.0.1:0", "--socket", path}); err == nil || !strings.Contains(err.Error(), "either --addr or --socket") {
		t.Fatalf("explicit listener-mode error = %v, want rejection", err)
	}

	t.Setenv("MITHRA_ADDR", "127.0.0.1:0")
	if err := run([]string{"--socket", path}); err == nil || !strings.Contains(err.Error(), "either --addr or --socket") {
		t.Fatalf("environment listener-mode error = %v, want rejection", err)
	}
}

func TestStartupFailureLoggingRedactsSensitiveCLIAndPathText(t *testing.T) {
	t.Parallel()

	const sensitive = "--not-a-real-option=/private/mithra/household.sqlite3?token=not-for-logs"
	if err := run([]string{sensitive}); err == nil {
		t.Fatal("invalid CLI argument unexpectedly succeeded")
	}

	var logs bytes.Buffer
	logStartupFailure(&logs)
	if got, want := logs.String(), "error_code=startup_failed\n"; got != want {
		t.Fatalf("startup log = %q, want %q", got, want)
	}
	if strings.Contains(logs.String(), sensitive) || strings.Contains(logs.String(), "/private/mithra") {
		t.Fatalf("startup log leaked sensitive CLI data: %q", logs.String())
	}
}

func TestUnixSocketCleansUpAfterHTTPServerShutdown(t *testing.T) {
	path := unixSocketTestPath(t)
	listener, err := listenUnixSocket(path)
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}

	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	serveResults := make(chan error, 1)
	go func() { serveResults <- server.Serve(listener) }()

	connection, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("connect to Unix socket: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("close Unix socket connection: %v", err)
	}

	shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		t.Fatalf("shut down HTTP server: %v", err)
	}
	if err := <-serveResults; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve result = %v, want ErrServerClosed", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("shutdown Unix socket still exists: %v", err)
	}
}

func unixSocketTestPath(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "mithra-socket-")
	if err != nil {
		t.Fatalf("create short socket test directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove socket test directory: %v", err)
		}
	})
	return filepath.Join(directory, "mithra.sock")
}
