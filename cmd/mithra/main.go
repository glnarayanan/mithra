// Command mithra serves the Mithra application over loopback TCP or a
// permissioned Unix-domain socket.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/glnarayanan/mithra/internal/app"
	"github.com/glnarayanan/mithra/internal/auth"
	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/household"
	"github.com/glnarayanan/mithra/internal/imports"
	"github.com/glnarayanan/mithra/internal/providers"
)

const shutdownTimeout = 20 * time.Second

// Set by the release build with -X main.buildVersion=... . The command-level
// version interface is added with the public CLI contract.
var buildVersion = "dev"

const unixSocketMode = 0o660
const maxCredentialBytes = 16 << 10

func main() {
	if err := runWithOutput(os.Args[1:], os.Stdout); err != nil {
		logStartupFailure(os.Stderr, err)
		os.Exit(1)
	}
}

type startupError struct {
	stage string
	err   error
}

func (e startupError) Error() string { return e.err.Error() }
func (e startupError) Unwrap() error { return e.err }

func failStartup(stage string, err error) error {
	return startupError{stage: stage, err: err}
}

func logStartupFailure(output io.Writer, err error) {
	var failure startupError
	if errors.As(err, &failure) {
		fmt.Fprintf(output, "error_code=startup_failed stage=%s\n", failure.stage)
		return
	}
	fmt.Fprintln(output, "error_code=startup_failed stage=command")
}

func run(args []string) error {
	return runWithOutput(args, io.Discard)
}

func runWithOutput(args []string, output io.Writer) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		if len(args) != 1 {
			return errors.New("help does not accept arguments")
		}
		fmt.Fprintln(output, "Usage: mithra [serve] [OPTIONS]\n\nCommands:\n  serve          run the Mithra application (the default)\n  recover-owner  recover an allowlisted household owner\n  help           show this help\n  version        print the Mithra version")
		return nil
	}
	if len(args) > 0 && args[0] == "version" {
		if len(args) != 1 {
			return errors.New("version does not accept arguments")
		}
		fmt.Fprintln(output, buildVersion)
		return nil
	}
	if len(args) > 0 && args[0] == "pdf-parser" {
		if len(args) != 1 {
			return errors.New("pdf-parser does not accept arguments")
		}
		listener, err := imports.SystemdListener()
		if err != nil {
			return errors.New("open PDF parser socket")
		}
		defer listener.Close()
		return imports.ServePDFParser(listener)
	}
	if len(args) > 0 && args[0] == "recover-owner" {
		return runRecoverOwner(args[1:])
	}
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}

	flags := flag.NewFlagSet("mithra", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	address := flags.String("addr", environmentDefault("MITHRA_ADDR", "127.0.0.1:8090"), "loopback address to listen on")
	socketPath := flags.String("socket", environmentDefault("MITHRA_SOCKET", ""), "absolute Unix socket path to listen on")
	databasePath := flags.String("db", environmentDefault("MITHRA_DB", "data/mithra.sqlite3"), "SQLite database path")
	sourceRoot := flags.String("source-dir", environmentDefault("MITHRA_SOURCE_DIR", "data/sources"), "encrypted source directory")
	allowedRaw := flags.String("allowed-emails", environmentDefault("ALLOWED_EMAILS", ""), "comma-separated allowed adult emails")
	originRaw := flags.String("origin", environmentDefault("MITHRA_CANONICAL_ORIGIN", ""), "canonical browser origin")
	plunkKeyFile := flags.String("plunk-key-file", environmentDefault("MITHRA_PLUNK_KEY_FILE", ""), "Plunk credential file")
	plunkFrom := flags.String("plunk-from", environmentDefault("MITHRA_PLUNK_FROM", ""), "Plunk sender identity")
	masterKeyFile := flags.String("master-key-file", environmentDefault("MITHRA_MASTER_KEY_FILE", ""), "Mithra master-key credential file")
	pdfParserSocket := flags.String("pdf-parser-socket", environmentDefault("MITHRA_PDF_PARSER_SOCKET", defaultPDFParser(*databasePath)), "isolated PDF parser Unix socket")
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
		return failStartup("listener", err)
	}
	defer listener.Close()

	allowedEmails, err := parseAllowedEmails(*allowedRaw)
	if err != nil {
		return failStartup("allowlist", err)
	}
	secureCookies, err := secureCookiesForOrigin(*originRaw)
	if err != nil {
		return failStartup("origin", err)
	}
	credential, err := readCredentialFile(*plunkKeyFile)
	if err != nil {
		return failStartup("plunk_credential", err)
	}
	mailer, err := providers.NewPlunk(providers.PlunkConfig{APIKey: credential, From: strings.TrimSpace(*plunkFrom)})
	if err != nil {
		return failStartup("plunk_config", errors.New("Plunk configuration is invalid"))
	}
	masterCredential, err := readCredentialFile(*masterKeyFile)
	if err != nil {
		return failStartup("master_credential", err)
	}
	masterKey, err := decodeMasterKey(masterCredential)
	if err != nil {
		return failStartup("master_key", err)
	}
	application, err := app.New(context.Background(), app.Config{
		DatabasePath:    *databasePath,
		AllowedEmails:   allowedEmails,
		CanonicalOrigin: strings.TrimSpace(*originRaw),
		SecureCookies:   secureCookies,
		TrustedProxy:    strings.TrimSpace(*socketPath) != "",
		Mailer:          mailer,
		MasterKey:       masterKey,
		SourceRoot:      *sourceRoot,
		ImportPDF:       configuredPDFParser(*pdfParserSocket),
	})
	if err != nil {
		return failStartup("application", fmt.Errorf("initialize Mithra: %w", err))
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
		return failStartup("server", fmt.Errorf("serve Mithra: %w", err))
	case <-stop.Done():
		shutdown, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutdownCancel()
		if err := server.Shutdown(shutdown); err != nil {
			return failStartup("shutdown", fmt.Errorf("shut down Mithra: %w", err))
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return failStartup("shutdown", fmt.Errorf("serve Mithra during shutdown: %w", err))
		}
		return nil
	}
}

func configuredPDFParser(socketPath string) imports.PDFParser {
	if strings.TrimSpace(socketPath) == "local" {
		return imports.LocalPDFParser{}
	}
	return imports.SocketPDFParser{Path: strings.TrimSpace(socketPath)}
}

func defaultPDFParser(databasePath string) string {
	if !filepath.IsAbs(strings.TrimSpace(databasePath)) {
		return "local"
	}
	return "/run/mithra/pdf-parser.sock"
}

func runRecoverOwner(args []string) error {
	flags := flag.NewFlagSet("mithra recover-owner", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databasePath := flags.String("db", environmentDefault("MITHRA_DB", "data/mithra.sqlite3"), "SQLite database path")
	allowedRaw := flags.String("allowed-emails", environmentDefault("ALLOWED_EMAILS", ""), "comma-separated allowed adult emails")
	householdID := flags.String("household", "", "closed household identifier")
	email := flags.String("email", "", "allowlisted replacement owner email")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*householdID) == "" || strings.TrimSpace(*email) == "" {
		return errors.New("recover-owner requires --household and --email")
	}
	allowedEmails, err := parseAllowedEmails(*allowedRaw)
	if err != nil {
		return err
	}
	db, err := database.Open(context.Background(), *databasePath)
	if err != nil {
		return errors.New("open Mithra database for recovery")
	}
	defer db.Close()
	service := auth.New(db, auth.Config{})
	if err := service.SynchronizeAllowlist(context.Background(), allowedEmails); err != nil {
		return errors.New("synchronize allowlist for recovery")
	}
	if err := service.RecoverOwner(context.Background(), strings.TrimSpace(*householdID), strings.TrimSpace(*email)); err != nil {
		return errors.New("recover Mithra household owner")
	}
	return nil
}

func decodeMasterKey(encoded string) ([]byte, error) {
	value, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(value) != 32 {
		return nil, errors.New("Mithra master-key credential is invalid")
	}
	return value, nil
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

func parseAllowedEmails(raw string) ([]string, error) {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == '\n' })
	if len(parts) == 0 {
		return nil, errors.New("allowed email configuration is required")
	}
	seen := make(map[string]struct{}, len(parts))
	emails := make([]string, 0, len(parts))
	for _, value := range parts {
		email, err := household.NormalizeEmail(value)
		if err != nil {
			return nil, errors.New("allowed email configuration is invalid")
		}
		if _, exists := seen[email]; exists {
			continue
		}
		seen[email] = struct{}{}
		emails = append(emails, email)
	}
	return emails, nil
}

func secureCookiesForOrigin(raw string) (bool, error) {
	origin, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false, errors.New("canonical origin configuration is invalid")
	}
	if origin.Scheme == "https" {
		return true, nil
	}
	if origin.Scheme == "http" {
		ip := net.ParseIP(origin.Hostname())
		if ip != nil && ip.IsLoopback() {
			return false, nil
		}
	}
	return false, errors.New("canonical origin must use HTTPS or literal loopback HTTP")
}

func readCredentialFile(path string) (string, error) {
	if !filepath.IsAbs(strings.TrimSpace(path)) {
		return "", errors.New("credential file is invalid")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("credential file is invalid")
	}
	if !systemdCredentialPath(path) {
		stat, ok := pathInfo.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || pathInfo.Mode().Perm()&0o077 != 0 {
			return "", errors.New("credential file is invalid")
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("credential file is invalid")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, openedInfo) {
		return "", errors.New("credential file is invalid")
	}
	value, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil || len(value) > maxCredentialBytes || strings.TrimSpace(string(value)) == "" {
		return "", errors.New("credential file is invalid")
	}
	return strings.TrimSpace(string(value)), nil
}

func systemdCredentialPath(path string) bool {
	directory := strings.TrimSpace(os.Getenv("CREDENTIALS_DIRECTORY"))
	return filepath.IsAbs(directory) && filepath.Clean(directory) == directory && filepath.Dir(filepath.Clean(path)) == directory
}
