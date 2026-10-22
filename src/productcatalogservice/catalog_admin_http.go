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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	pb "github.com/GoogleCloudPlatform/microservices-demo/src/productcatalogservice/genproto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

type catalogAdminHTTP struct {
	admin   pb.CatalogAdminServiceClient
	cache   *catalogCache
	metrics *catalogMetrics
	timeout time.Duration
}

func (h *catalogAdminHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/metrics":
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = io.WriteString(w, h.metrics.prometheus())
	case r.Method == http.MethodGet && r.URL.Path == "/admin/revision":
		h.writeRevision(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/stats":
		h.writeStats(w, r)
	case r.Method == http.MethodPut && r.URL.Path == "/admin/catalog":
		h.replaceCatalog(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *catalogAdminHTTP) writeRevision(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestTimeout(r, h.timeout)
	defer cancel()
	revision, err := callCatalogRevision(ctx, h.admin)
	if err != nil {
		http.Error(w, err.Error(), httpStatus(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Revision uint64 `json:"revision"`
	}{revision})
}

func (h *catalogAdminHTTP) writeStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestTimeout(r, h.timeout)
	defer cancel()
	stats, err := callCatalogStats(ctx, h.admin)
	if err != nil {
		http.Error(w, err.Error(), httpStatus(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Revision     uint64 `json:"revision"`
		BackendReads uint64 `json:"backendReads"`
	}{stats.Revision, stats.BackendReads})
}

func (h *catalogAdminHTTP) replaceCatalog(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var request struct {
		ExpectedRevision *uint64         `json:"expectedRevision"`
		Catalog          json.RawMessage `json:"catalog"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		statusCode := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			statusCode = http.StatusRequestEntityTooLarge
		}
		http.Error(w, fmt.Sprintf("invalid update JSON: %v", err), statusCode)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "update must contain one JSON object", http.StatusBadRequest)
		return
	}
	if request.Catalog == nil {
		http.Error(w, "catalog is required", http.StatusBadRequest)
		return
	}
	var catalog pb.ListProductsResponse
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(request.Catalog, &catalog); err != nil {
		http.Error(w, fmt.Sprintf("invalid catalog: %v", err), http.StatusBadRequest)
		return
	}
	ctx, cancel := requestTimeout(r, h.timeout)
	defer cancel()
	revision, err := callCatalogReplace(ctx, h.admin, &catalog, request.ExpectedRevision)
	if err != nil {
		http.Error(w, err.Error(), httpStatus(err))
		return
	}
	h.cache.Invalidate()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Revision uint64 `json:"revision"`
	}{revision})
}

func requestTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}

func httpStatus(err error) int {
	if code, ok := status.FromError(err); ok {
		switch code.Code() {
		case codes.InvalidArgument:
			return http.StatusBadRequest
		case codes.Aborted:
			return http.StatusConflict
		case codes.Unavailable, codes.DeadlineExceeded:
			return http.StatusServiceUnavailable
		}
	}
	return http.StatusInternalServerError
}

func (m *catalogMetrics) prometheus() string {
	var out strings.Builder
	fmt.Fprintf(&out, "# TYPE catalog_requests_started_total counter\ncatalog_requests_started_total %d\n", m.requestsStarted.Load())
	fmt.Fprintf(&out, "# TYPE catalog_requests_completed_total counter\ncatalog_requests_completed_total %d\n", m.requestsDone.Load())
	fmt.Fprintf(&out, "# TYPE catalog_requests_failed_total counter\ncatalog_requests_failed_total %d\n", m.requestsFailed.Load())
	fmt.Fprintf(&out, "# TYPE catalog_backend_reads_total counter\ncatalog_backend_reads_total %d\n", m.backendReads.Load())
	fmt.Fprintf(&out, "# TYPE catalog_cache_hits_total counter\ncatalog_cache_hits_total %d\n", m.cacheHits.Load())
	fmt.Fprintf(&out, "# TYPE catalog_cache_misses_total counter\ncatalog_cache_misses_total %d\n", m.cacheMisses.Load())
	fmt.Fprintf(&out, "# TYPE catalog_lease_waiters_total counter\ncatalog_lease_waiters_total %d\n", m.leaseWaiters.Load())
	fmt.Fprintf(&out, "# TYPE catalog_fenced_fills_total counter\ncatalog_fenced_fills_total %d\n", m.fencedFills.Load())
	m.latencyMu.Lock()
	defer m.latencyMu.Unlock()
	fmt.Fprint(&out, "# TYPE catalog_request_duration_seconds histogram\n")
	for i, bound := range catalogLatencyBounds {
		fmt.Fprintf(&out, "catalog_request_duration_seconds_bucket{le=%q} %d\n", strconv.FormatFloat(bound, 'g', -1, 64), m.latencyBuckets[i])
	}
	fmt.Fprintf(&out, "catalog_request_duration_seconds_bucket{le=\"+Inf\"} %d\ncatalog_request_duration_seconds_sum %g\ncatalog_request_duration_seconds_count %d\n", m.latencyCount, m.latencySum, m.latencyCount)
	return out.String()
}
