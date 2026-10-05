// Package httpbody reads size-limited, possibly compressed request bodies.
package httpbody

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

var (
	ErrTooLarge            = errors.New("request body too large")
	ErrUnsupportedEncoding = errors.New("unsupported content encoding")
)

// Read returns the decoded body of r. maxWire bounds the bytes read from the
// connection; maxDecoded bounds the bytes after decompression, which guards
// against compression bombs.
func Read(r *http.Request, maxWire, maxDecoded int64) ([]byte, error) {
	wire := &countingLimit{r: r.Body, n: maxWire}
	var dec io.Reader
	enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	switch enc {
	case "", "identity":
		dec = wire
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(wire)
		if err != nil {
			return nil, wireErr(wire, fmt.Errorf("gzip: %w", err))
		}
		defer zr.Close()
		dec = zr
	case "deflate":
		// RFC 9110 deflate is zlib-wrapped, but some clients send raw
		// DEFLATE; sniff the zlib header to accept both.
		br := bufio.NewReader(wire)
		head, err := br.Peek(2)
		if err != nil {
			return nil, wireErr(wire, fmt.Errorf("deflate: %w", err))
		}
		if isZlibHeader(head) {
			zr, err := zlib.NewReader(br)
			if err != nil {
				return nil, wireErr(wire, fmt.Errorf("zlib: %w", err))
			}
			defer zr.Close()
			dec = zr
		} else {
			fr := flate.NewReader(br)
			defer fr.Close()
			dec = fr
		}
	case "br":
		dec = brotli.NewReader(wire)
	case "zstd":
		zr, err := zstd.NewReader(wire, zstd.WithDecoderMaxMemory(uint64(maxDecoded)))
		if err != nil {
			return nil, fmt.Errorf("zstd: %w", err)
		}
		defer zr.Close()
		dec = zr
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedEncoding, enc)
	}

	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(dec, maxDecoded+1))
	if err != nil {
		return nil, wireErr(wire, err)
	}
	if n > maxDecoded {
		return nil, ErrTooLarge
	}
	return buf.Bytes(), nil
}

func isZlibHeader(b []byte) bool {
	return b[0]&0x0f == 8 && (uint16(b[0])<<8|uint16(b[1]))%31 == 0
}

// wireErr reports ErrTooLarge when a decode failure was caused by hitting
// the wire limit rather than by a malformed body.
func wireErr(w *countingLimit, err error) error {
	if w.exceeded {
		return ErrTooLarge
	}
	return err
}

type countingLimit struct {
	r        io.Reader
	n        int64
	exceeded bool
}

func (c *countingLimit) Read(p []byte) (int, error) {
	if c.n <= 0 {
		// Probe one byte to tell "exactly at the limit" from "over it".
		var one [1]byte
		if k, _ := c.r.Read(one[:]); k > 0 {
			c.exceeded = true
			return 0, ErrTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > c.n {
		p = p[:c.n]
	}
	k, err := c.r.Read(p)
	c.n -= int64(k)
	return k, err
}
