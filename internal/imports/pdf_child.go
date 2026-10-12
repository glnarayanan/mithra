package imports

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	PDFParserFD           = 3
	maxParserReplyBytes   = MaxPDFTextBytes + (256 << 10)
	maxParserRequestBytes = (MaxFileBytes * 4 / 3) + (64 << 10)
	defaultParserTimeout  = 10 * time.Second
)

// SocketPDFParser sends only bounded document bytes to the isolated parser
// identity. It never gives that process a path, database handle, or credential.
type SocketPDFParser struct {
	Path    string
	Timeout time.Duration
}

func (p SocketPDFParser) Extract(ctx context.Context, content []byte, limits Limits) ([]Fragment, error) {
	if !strings.HasPrefix(p.Path, "/") || len(content) == 0 || len(content) > MaxFileBytes {
		return nil, ErrUnreadable
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = defaultParserTimeout
	}
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "unix", p.Path)
	if err != nil {
		return nil, ErrParserUnavailable
	}
	defer connection.Close()
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	request := parserRequest{Content: content, Limits: limits}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > maxParserRequestBytes {
		return nil, ErrUnreadable
	}
	if err := writeFrame(connection, encoded); err != nil {
		return nil, ErrUnreadable
	}
	reply, err := readFrame(connection, maxParserReplyBytes)
	if err != nil {
		return nil, ErrUnreadable
	}
	var response parserResponse
	if json.Unmarshal(reply, &response) != nil {
		return nil, ErrUnreadable
	}
	if response.Error != "" {
		return nil, parserError(response.Error)
	}
	return response.Fragments, nil
}

type parserRequest struct {
	Content []byte `json:"content"`
	Limits  Limits `json:"limits"`
}
type parserResponse struct {
	Fragments []Fragment `json:"fragments,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// ServePDFParser accepts systemd-activated parser connections until the
// listener closes. Each request is independently bounded and panic-contained.
func ServePDFParser(listener net.Listener) error {
	return ServePDFParserFor(context.Background(), listener)
}

// ServePDFParserFor serves like ServePDFParser until ctx ends, then stops
// accepting and waits for in-flight requests, which are already bounded by the
// per-connection deadline. Containers use it to recycle the parser process the
// way systemd's RuntimeMaxSec does.
func ServePDFParserFor(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return ErrUnreadable
	}
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	slots := make(chan struct{}, 2)
	var inFlight sync.WaitGroup
	defer inFlight.Wait()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		slots <- struct{}{}
		inFlight.Add(1)
		go func() {
			defer inFlight.Done()
			defer func() { <-slots }()
			serveParserConnection(connection)
		}()
	}
}

// ListenPDFParser creates the parser's own Unix socket at path for platforms
// without socket activation. The parent directory must be a private directory
// of the parser identity's group; a stale socket left by a previous process in
// that directory is replaced. The socket is group read/write so only the
// application identity, which shares the group, can connect.
func ListenPDFParser(path string) (*net.UnixListener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrInvalidInput
	}
	directory, err := os.Lstat(filepath.Dir(path))
	if err != nil || !directory.IsDir() || directory.Mode()&os.ModeSymlink != 0 || directory.Mode().Perm()&0o007 != 0 {
		return nil, ErrInvalidInput
	}
	if existing, err := os.Lstat(path); err == nil {
		if existing.Mode()&os.ModeSocket == 0 {
			return nil, ErrInvalidInput
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(true)
	if err := os.Chmod(path, 0o660); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

func SystemdListener() (net.Listener, error) {
	pid, err := strconv.Atoi(os.Getenv("LISTEN_PID"))
	if err != nil || pid != os.Getpid() || os.Getenv("LISTEN_FDS") != "1" {
		return nil, ErrUnreadable
	}
	file := os.NewFile(PDFParserFD, "mithra-pdf-parser.socket")
	if file == nil {
		return nil, ErrUnreadable
	}
	listener, err := net.FileListener(file)
	_ = file.Close()
	if err != nil {
		return nil, ErrUnreadable
	}
	return listener, nil
}

func serveParserConnection(connection net.Conn) {
	defer connection.Close()
	defer func() { _ = recover() }()
	_ = connection.SetDeadline(time.Now().Add(defaultParserTimeout))
	requestBytes, err := readFrame(connection, maxParserRequestBytes)
	response := parserResponse{}
	if err != nil {
		response.Error = "unreadable"
	} else {
		var request parserRequest
		if json.Unmarshal(requestBytes, &request) != nil || len(request.Content) == 0 || len(request.Content) > MaxFileBytes || request.Limits.MaxPages < 1 || request.Limits.MaxPages > MaxPDFPages || request.Limits.MaxText < 1 || request.Limits.MaxText > MaxPDFTextBytes {
			response.Error = "unreadable"
		} else {
			parserContext, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			parserBytes := append([]byte(nil), request.Content...)
			response.Fragments, err = boundedPDF(parserContext, LocalPDFParser{}, parserBytes)
			cancel()
			if !errors.Is(err, ErrParserTimeout) {
				clear(parserBytes)
			}
			if err != nil {
				response.Error = parserErrorCode(err)
			}
		}
		clear(request.Content)
	}
	encoded, _ := json.Marshal(response)
	if len(encoded) <= maxParserReplyBytes {
		_ = writeFrame(connection, encoded)
	}
}

func writeFrame(writer io.Writer, value []byte) error {
	if len(value) == 0 || uint64(len(value)) > uint64(^uint32(0)) {
		return ErrUnreadable
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(value)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err := writer.Write(value)
	return err
}

func readFrame(reader io.Reader, limit int) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint32(header[:]))
	if size < 1 || size > limit {
		return nil, ErrOverLimit
	}
	value := make([]byte, size)
	if _, err := io.ReadFull(reader, value); err != nil {
		return nil, err
	}
	return value, nil
}

func parserErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrOverLimit):
		return "over_limit"
	case errors.Is(err, ErrScannedPDF):
		return "scanned"
	case errors.Is(err, ErrEncryptedPDF):
		return "encrypted"
	case errors.Is(err, ErrParserPanic):
		return "panic"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), errors.Is(err, ErrParserTimeout):
		return "timeout"
	default:
		return "unreadable"
	}
}
func parserError(code string) error {
	switch code {
	case "over_limit":
		return ErrOverLimit
	case "scanned":
		return ErrScannedPDF
	case "encrypted":
		return ErrEncryptedPDF
	case "panic":
		return ErrParserPanic
	case "timeout":
		return ErrParserTimeout
	default:
		return ErrUnreadable
	}
}
