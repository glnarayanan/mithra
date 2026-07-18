// Command mithra serves the Mithra application over loopback TCP or a
// permissioned Unix-domain socket.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/glnarayanan/mithra/internal/app"
)

const shutdownTimeout = 20 * time.Second

const unixSocketMode = 0o660

func main() {
	if err := run(os.Args[1:]); err != nil {
		logStartupFailure(os.Stderr)
		os.Exit(1)
	}
}

func logStartupFailure(output io.Writer) {
	fmt.Fprintln(output, "error_code=startup_failed")
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}

	flags := flag.NewFlagSet("mithra", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	address := flags.String("addr", environmentDefault("MITHRA_ADDR", "127.0.0.1:8090"), "loopback address to listen on")
	socketPath := flags.String("socket", environmentDefault("MITHRA_SOCKET", ""), "absolute Unix socket path to listen on")
	databasePath := flags.String("db", environmentDefault("MITHRA_DB", "data/mithra.sqlite3"), "SQLite database path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	addressConfigured := strings.TrimSpace(os.Getenv("MITHRA_ADDR")) != ""
	flags.Visit(func(current *flag.Flag) {
		if current.Name == "addr" {
			addressConfigured = true
		}
	})

	listener, err := listen(*address, *socketPath, addressConfigured)
	if err != nil {
		return err
	}
	defer listener.Close()

	application, err := app.New(context.Background(), app.Config{DatabasePath: *databasePath})
	if err != nil {
		return fmt.Errorf("initialize Mithra: %w", err)
	}
	defer application.Close()

	server := &http.Server{
		Handler:           application.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()

	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve Mithra: %w", err)
	case <-stop.Done():
		shutdown, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutdownCancel()
		if err := server.Shutdown(shutdown); err != nil {
			return fmt.Errorf("shut down Mithra: %w", err)
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve Mithra during shutdown: %w", err)
		}
		return nil
	}
}

func listen(address, socketPath string, addressConfigured bool) (net.Listener, error) {
	if strings.TrimSpace(socketPath) != "" {
		if addressConfigured {
			return nil, errors.New("Mithra accepts either --addr or --socket, not both")
		}
		return listenUnixSocket(socketPath)
	}
	return listenLoopback(address)
}

func listenLoopback(address string) (net.Listener, error) {
	if err := validateLoopbackAddress(address); err != nil {
		return nil, err
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen on %q: %w", address, err)
	}
	return listener, nil
}

func listenUnixSocket(path string) (*net.UnixListener, error) {
	if err := validateUnixSocketPath(path); err != nil {
		return nil, err
	}
	if err := rejectUnixSocketCollision(path); err != nil {
		return nil, err
	}

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on Unix socket %q: %w", path, err)
	}
	listener.SetUnlinkOnClose(true)
	if err := os.Chmod(path, unixSocketMode); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("restrict Unix socket %q permissions: %w", path, err)
	}
	return listener, nil
}

func validateUnixSocketPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("Mithra Unix socket path is required")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("Mithra Unix socket path must be absolute, got %q", path)
	}

	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect Unix socket directory %q: %w", directory, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Unix socket parent %q must be a real directory", directory)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("Unix socket parent directory %q is group or world writable", directory)
	}
	if info.Mode().Perm()&0o007 != 0 {
		return fmt.Errorf("Unix socket parent directory %q must deny all permissions to other users", directory)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("Unix socket parent directory ownership is unavailable")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("Unix socket parent directory %q is not owned by the service user", directory)
	}
	return nil
}

func rejectUnixSocketCollision(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Unix socket path %q: %w", path, err)
	}
	if info.Mode()&os.ModeSocket != 0 {
		return fmt.Errorf("Unix socket path %q already exists; refuse to replace a stale socket", path)
	}
	return fmt.Errorf("Unix socket path %q already exists and is not a socket", path)
}

func validateLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", address, err)
	}
	if host == "" {
		return errors.New("Mithra requires an explicit loopback host, not a wildcard address")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("Mithra only accepts literal loopback addresses, got %q", address)
	}
	return nil
}

func environmentDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
