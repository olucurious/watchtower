// Package adapter defines the contract between ingestion adapters and
// the rest of Watchtower.
//
// An adapter speaks one vendor's wire protocol well enough that the vendor's
// unmodified SDK cannot tell the difference. It authenticates, decodes and
// converts payloads into event.Event values, then hands them to a Sink.
// Grouping, storage and alerting never happen inside an adapter.
package adapter

import (
	"context"
	"errors"
	"net/http"

	"github.com/olucurious/watchtower/internal/event"
)

// Adapter is one vendor protocol adapter.
type Adapter interface {
	// Name is the stable identifier used in config, keys and metrics.
	Name() string
	// Describe documents the protocol surface and tested SDK versions.
	Describe() Description
	// Register mounts the adapter's routes.
	Register(mux *http.ServeMux, deps Deps)
}

type Description struct {
	Summary   string
	Routes    []string
	TestedSDK []string // exact SDK versions covered by captured fixtures
	// Experimental adapters rely on a reverse-engineered protocol and are
	// only enabled when listed explicitly.
	Experimental bool
}

type Deps struct {
	Keys    KeyResolver
	Sink    Sink
	Limits  Limits
	Metrics Metrics
}

type Limits struct {
	MaxBodyBytes         int64 // on the wire, before decompression
	MaxDecompressedBytes int64
	MaxEventBytes        int64 // one decoded event item
}

// Project is the tenant a credential belongs to.
type Project struct {
	ID   int64
	Slug string
}

// KeyResolver maps an adapter-specific credential to its project.
type KeyResolver interface {
	ResolveKey(ctx context.Context, adapter, key string) (Project, error)
}

// Sink durably accepts events. A nil error means the events will be
// processed even if the process dies immediately afterwards.
type Sink interface {
	Accept(ctx context.Context, events []event.Event) error
}

// Metrics counts protocol outcomes, e.g. ("sentry", "accepted").
type Metrics interface {
	Count(adapter, outcome string, n int)
}

var (
	// ErrUnknownKey: the credential does not exist or was revoked.
	ErrUnknownKey = errors.New("unknown or revoked key")
	// ErrOverloaded: the sink is shedding load; SDKs should back off.
	ErrOverloaded = errors.New("ingestion overloaded")
)
