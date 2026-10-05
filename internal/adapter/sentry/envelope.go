package sentry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Envelope is the parsed form of Sentry's envelope format:
// https://develop.sentry.dev/sdk/data-model/envelopes/
type Envelope struct {
	Header EnvelopeHeader
	Items  []Item
}

type EnvelopeHeader struct {
	EventID string `json:"event_id"`
	DSN     string `json:"dsn"`
}

type Item struct {
	Type    string
	Payload []byte
}

var errMalformed = errors.New("malformed envelope")

// ParseEnvelope splits raw into its header and items. An item's payload is
// either exactly "length" bytes or, without a length, the rest of the line.
func ParseEnvelope(raw []byte) (*Envelope, error) {
	line, rest, _ := bytes.Cut(raw, []byte{'\n'})
	env := &Envelope{}
	if len(bytes.TrimSpace(line)) > 0 {
		if err := json.Unmarshal(line, &env.Header); err != nil {
			return nil, fmt.Errorf("%w: header: %w", errMalformed, err)
		}
	}
	for len(bytes.TrimSpace(rest)) > 0 {
		var hdrLine []byte
		hdrLine, rest, _ = bytes.Cut(rest, []byte{'\n'})
		if len(bytes.TrimSpace(hdrLine)) == 0 {
			continue
		}
		var hdr struct {
			Type   string `json:"type"`
			Length *int   `json:"length"`
		}
		if err := json.Unmarshal(hdrLine, &hdr); err != nil {
			return nil, fmt.Errorf("%w: item header: %w", errMalformed, err)
		}
		var payload []byte
		if hdr.Length != nil {
			n := *hdr.Length
			if n < 0 || n > len(rest) {
				return nil, fmt.Errorf("%w: item length %d exceeds remaining %d bytes", errMalformed, n, len(rest))
			}
			payload, rest = rest[:n], rest[n:]
			rest = bytes.TrimPrefix(rest, []byte{'\n'})
		} else {
			payload, rest, _ = bytes.Cut(rest, []byte{'\n'})
		}
		env.Items = append(env.Items, Item{Type: hdr.Type, Payload: payload})
	}
	return env, nil
}
