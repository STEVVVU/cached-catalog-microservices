// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/GoogleCloudPlatform/microservices-demo/src/productcatalogservice/genproto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type catalogMetrics struct {
	requestsStarted atomic.Uint64
	requestsDone    atomic.Uint64
	requestsFailed  atomic.Uint64
	backendReads    atomic.Uint64
	cacheHits       atomic.Uint64
	cacheMisses     atomic.Uint64
	leaseWaiters    atomic.Uint64
	fencedFills     atomic.Uint64
	latencyMu       sync.Mutex
	latencyBuckets  [7]uint64
	latencyCount    uint64
	latencySum      float64
}

var catalogLatencyBounds = [...]float64{0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5}

func (m *catalogMetrics) startRequest() func(error) {
	m.requestsStarted.Add(1)
	start := time.Now()
	return func(err error) {
		elapsed := time.Since(start).Seconds()
		m.requestsDone.Add(1)
		if err != nil {
			m.requestsFailed.Add(1)
		}
		m.latencyMu.Lock()
		m.latencyCount++
		m.latencySum += elapsed
		for i, bound := range catalogLatencyBounds {
			if elapsed <= bound {
				m.latencyBuckets[i]++
			}
		}
		m.latencyMu.Unlock()
	}
}

type catalogFetcher func(context.Context) (*pb.ListProductsResponse, error)

type cacheFlight struct {
	token         uint64
	done          chan struct{}
	err           error
	valid         bool
	ownerCanceled bool
}

type catalogCache struct {
	mode       string
	ttl        time.Duration
	fetch      catalogFetcher
	metrics    *catalogMetrics
	mu         sync.Mutex
	snapshot   *pb.ListProductsResponse
	expires    time.Time
	generation uint64
	flight     *cacheFlight
}

func newCatalogCache(mode string, ttl time.Duration, fetch catalogFetcher, metrics *catalogMetrics) (*catalogCache, error) {
	switch mode {
	case "none", "ttl", "lease":
	default:
		return nil, fmt.Errorf("CATALOG_CACHE_MODE must be none, ttl, or lease; got %q", mode)
	}
	if ttl <= 0 || ttl > time.Minute {
		return nil, fmt.Errorf("catalog cache TTL must be between 0 and 1m; got %s", ttl)
	}
	return &catalogCache{mode: mode, ttl: ttl, fetch: fetch, metrics: metrics}, nil
}

func (c *catalogCache) Get(ctx context.Context) (*pb.ListProductsResponse, error) {
	if c.mode == "none" {
		return c.load(ctx)
	}
	if c.mode == "ttl" {
		return c.getTTL(ctx)
	}
	return c.getLease(ctx)
}

func (c *catalogCache) getTTL(ctx context.Context) (*pb.ListProductsResponse, error) {
	c.mu.Lock()
	if c.snapshot != nil && time.Now().Before(c.expires) {
		c.metrics.cacheHits.Add(1)
		result := cloneCatalog(c.snapshot)
		c.mu.Unlock()
		return result, nil
	}
	c.metrics.cacheMisses.Add(1)
	c.mu.Unlock()
	loaded, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.snapshot = loaded
	c.expires = time.Now().Add(c.ttl)
	c.mu.Unlock()
	return cloneCatalog(loaded), nil
}

func (c *catalogCache) getLease(ctx context.Context) (*pb.ListProductsResponse, error) {
	for {
		c.mu.Lock()
		if c.snapshot != nil && time.Now().Before(c.expires) {
			c.metrics.cacheHits.Add(1)
			result := cloneCatalog(c.snapshot)
			c.mu.Unlock()
			return result, nil
		}
		if flight := c.flight; flight != nil {
			c.metrics.leaseWaiters.Add(1)
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, status.FromContextError(ctx.Err()).Err()
			case <-flight.done:
				if flight.err != nil {
					if flight.ownerCanceled {
						continue
					}
					return nil, flight.err
				}
				if flight.valid {
					c.mu.Lock()
					if c.snapshot != nil && time.Now().Before(c.expires) {
						result := cloneCatalog(c.snapshot)
						c.mu.Unlock()
						return result, nil
					}
					c.mu.Unlock()
				}
				continue
			}
		}
		c.metrics.cacheMisses.Add(1)
		flight := &cacheFlight{token: c.generation, done: make(chan struct{})}
		c.flight = flight
		c.mu.Unlock()

		loaded, err := c.load(ctx)
		c.mu.Lock()
		flight.err = err
		if err != nil && ctx.Err() != nil {
			contextCode := status.Code(status.FromContextError(ctx.Err()).Err())
			flight.ownerCanceled = errors.Is(err, ctx.Err()) ||
				(contextCode != codes.Unknown && status.Code(err) == contextCode)
		}
		if err == nil && flight.token == c.generation {
			c.snapshot = loaded
			c.expires = time.Now().Add(c.ttl)
			flight.valid = true
		} else if err == nil {
			c.metrics.fencedFills.Add(1)
		}
		c.flight = nil
		close(flight.done)
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if flight.valid {
			return cloneCatalog(loaded), nil
		}
	}
}

func (c *catalogCache) Invalidate() {
	c.mu.Lock()
	c.generation++
	c.snapshot = nil
	c.expires = time.Time{}
	c.mu.Unlock()
}

func (c *catalogCache) load(ctx context.Context) (*pb.ListProductsResponse, error) {
	ctx, span := otel.Tracer("productcatalogservice/catalog-cache").Start(ctx, "catalog.cache.load")
	defer span.End()
	span.SetAttributes(attribute.String("cache.strategy", c.mode))
	c.metrics.backendReads.Add(1)
	started := time.Now()
	catalog, err := c.fetch(ctx)
	span.SetAttributes(attribute.Int64("backend.duration_ms", time.Since(started).Milliseconds()))
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return catalog, nil
}

func cloneCatalog(catalog *pb.ListProductsResponse) *pb.ListProductsResponse {
	return proto.Clone(catalog).(*pb.ListProductsResponse)
}
