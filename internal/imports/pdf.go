package imports

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// LocalPDFParser is intentionally a small seam: production may replace it
// with an isolated parser without letting callers receive a file path or state.
type LocalPDFParser struct{}

func (LocalPDFParser) Extract(ctx context.Context, content []byte, limits Limits) ([]Fragment, error) {
	if bytes.Contains(content, []byte("/Encrypt")) {
		return nil, ErrEncryptedPDF
	}
	reader, err := pdf.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, ErrUnreadable
	}
	pages := reader.NumPage()
	if pages < 1 {
		return nil, ErrUnreadable
	}
	if pages > limits.MaxPages {
		return nil, ErrOverLimit
	}
	var fragments []Fragment
	total := 0
	for page := 1; page <= pages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text, err := pageText(reader, page)
		if err != nil {
			return nil, err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		total += len(text)
		if total > limits.MaxText {
			return nil, ErrOverLimit
		}
		fragments = append(fragments, Fragment{Locator: Locator{Kind: "page", Value: fmt.Sprintf("page:%d", page)}, Text: text})
	}
	if len(fragments) == 0 {
		return nil, ErrScannedPDF
	}
	return fragments, nil
}

func pageText(reader *pdf.Reader, page int) (text string, err error) {
	defer func() {
		if recover() != nil {
			err = ErrParserPanic
		}
	}()
	return reader.Page(page).GetPlainText(nil)
}

func isPDFContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
