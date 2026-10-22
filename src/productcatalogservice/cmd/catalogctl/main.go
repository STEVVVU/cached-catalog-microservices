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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	pb "github.com/GoogleCloudPlatform/microservices-demo/src/productcatalogservice/genproto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: catalogctl list|get ID|search QUERY|bench REQUESTS CONCURRENCY|revision|stats|metrics|update FILE [EXPECTED_REVISION]")
	}
	switch args[0] {
	case "load":
		return loadCatalog(args[1:])
	case "list":
		return callCatalog(args, func(ctx context.Context, client pb.ProductCatalogServiceClient) (proto.Message, error) {
			return client.ListProducts(ctx, &pb.Empty{})
		})
	case "get":
		if len(args) != 2 {
			return errors.New("usage: catalogctl get ID")
		}
		return callCatalog(args[1:], func(ctx context.Context, client pb.ProductCatalogServiceClient) (proto.Message, error) {
			return client.GetProduct(ctx, &pb.GetProductRequest{Id: args[1]})
		})
	case "search":
		if len(args) != 2 {
			return errors.New("usage: catalogctl search QUERY")
		}
		return callCatalog(args[1:], func(ctx context.Context, client pb.ProductCatalogServiceClient) (proto.Message, error) {
			return client.SearchProducts(ctx, &pb.SearchProductsRequest{Query: args[1]})
		})
	case "bench":
		if len(args) != 3 {
			return errors.New("usage: catalogctl bench REQUESTS CONCURRENCY")
		}
		return benchmark(args[1], args[2])
	case "revision":
		return adminRequest("GET", "/admin/revision", nil)
	case "stats":
		return adminRequest("GET", "/admin/stats", nil)
	case "metrics":
		return adminRequest("GET", "/metrics", nil)
	case "update":
		if len(args) < 2 || len(args) > 3 {
			return errors.New("usage: catalogctl update FILE [EXPECTED_REVISION]")
		}
		return updateCatalog(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func callCatalog(_ []string, call func(context.Context, pb.ProductCatalogServiceClient) (proto.Message, error)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(envOr("CATALOGCTL_GRPC_ADDR", "127.0.0.1:3550"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	message, err := call(ctx, pb.NewProductCatalogServiceClient(conn))
	if err != nil {
		return err
	}
	encoded, err := (protojson.MarshalOptions{Multiline: true, Indent: "  "}).Marshal(message)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(encoded, '\n'))
	return err
}

func benchmark(requestText, concurrencyText string) error {
	requests, err := strconv.Atoi(requestText)
	if err != nil || requests < 1 || requests > 100000 {
		return errors.New("requests must be between 1 and 100000")
	}
	concurrency, err := strconv.Atoi(concurrencyText)
	if err != nil || concurrency < 1 || concurrency > requests || concurrency > 1000 {
		return errors.New("concurrency must be between 1 and min(requests, 1000)")
	}
	conn, err := grpc.NewClient(envOr("CATALOGCTL_GRPC_ADDR", "127.0.0.1:3550"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := pb.NewProductCatalogServiceClient(conn)
	latencies := make([]float64, 0, requests)
	var mu sync.Mutex
	var wg sync.WaitGroup
	jobs := make(chan struct{})
	var completed, failed int
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				started := time.Now()
				_, err := client.ListProducts(ctx, &pb.Empty{})
				elapsed := time.Since(started).Seconds() * 1000
				cancel()
				mu.Lock()
				latencies = append(latencies, elapsed)
				if err != nil {
					failed++
				} else {
					completed++
				}
				mu.Unlock()
			}
		}()
	}
	for range requests {
		jobs <- struct{}{}
	}
	close(jobs)
	wg.Wait()
	sort.Float64s(latencies)
	result := struct {
		Offered   int     `json:"offered"`
		Completed int     `json:"completed"`
		Failed    int     `json:"failed"`
		P50Millis float64 `json:"p50_ms"`
		P95Millis float64 `json:"p95_ms"`
		P99Millis float64 `json:"p99_ms"`
	}{requests, completed, failed, percentile(latencies, .50), percentile(latencies, .95), percentile(latencies, .99)}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func percentile(values []float64, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * fraction)
	return values[index]
}

func adminRequest(method, suffix string, body io.Reader) error {
	url := envOr("CATALOGCTL_ADMIN_URL", "http://127.0.0.1:3552") + suffix
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 9<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("admin endpoint returned %s: %s", response.Status, data)
	}
	_, err = os.Stdout.Write(data)
	return err
}

func updateCatalog(args []string) error {
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var catalog pb.ListProductsResponse
	if err := protojson.Unmarshal(data, &catalog); err != nil {
		return fmt.Errorf("read catalog JSON: %w", err)
	}
	catalogJSON, err := protojson.Marshal(&catalog)
	if err != nil {
		return err
	}
	var expected *uint64
	if len(args) == 2 {
		revision, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("expected revision: %w", err)
		}
		expected = &revision
	}
	var body []byte
	if expected == nil {
		body, err = json.Marshal(struct {
			Catalog json.RawMessage `json:"catalog"`
		}{catalogJSON})
	} else {
		body, err = json.Marshal(struct {
			Catalog          json.RawMessage `json:"catalog"`
			ExpectedRevision uint64          `json:"expectedRevision"`
		}{catalogJSON, *expected})
	}
	if err != nil {
		return err
	}
	return adminRequest("PUT", "/admin/catalog", bytes.NewReader(body))
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
