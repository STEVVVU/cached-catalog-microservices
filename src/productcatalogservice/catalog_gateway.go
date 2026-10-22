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
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	pb "github.com/GoogleCloudPlatform/microservices-demo/src/productcatalogservice/genproto"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func newProductCatalogService() (*productCatalog, error) {
	address := os.Getenv("CATALOG_BACKEND_ADDR")
	if address == "" {
		if os.Getenv("CATALOG_CACHE_MODE") != "" {
			return nil, fmt.Errorf("CATALOG_CACHE_MODE requires CATALOG_BACKEND_ADDR")
		}
		return &productCatalog{}, nil
	}

	timeout, err := parseBoundedDuration("CATALOG_BACKEND_TIMEOUT", "2s", time.Millisecond, 30*time.Second)
	if err != nil {
		return nil, err
	}
	ttl, err := parseBoundedDuration("CATALOG_CACHE_TTL", "2s", time.Millisecond, time.Minute)
	if err != nil {
		return nil, err
	}
	mode := envOr("CATALOG_CACHE_MODE", "none")
	conn, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(8<<20), grpc.MaxCallSendMsgSize(8<<20)),
	)
	if err != nil {
		return nil, fmt.Errorf("create catalog backend gRPC client: %w", err)
	}
	metrics := &catalogMetrics{}
	client := pb.NewProductCatalogServiceClient(conn)
	cache, err := newCatalogCache(mode, ttl, func(ctx context.Context) (*pb.ListProductsResponse, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return client.ListProducts(ctx, &pb.Empty{})
	}, metrics)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &productCatalog{backendC: conn, cache: cache, metrics: metrics}, nil
}

func parseBoundedDuration(name, defaultValue string, minimum, maximum time.Duration) (time.Duration, error) {
	value := envOr(name, defaultValue)
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if duration < minimum || duration > maximum {
		return 0, fmt.Errorf("%s must be between %s and %s", name, minimum, maximum)
	}
	return duration, nil
}

func startCatalogAdminHTTP(service *productCatalog) error {
	portValue := envOr("CATALOG_ADMIN_PORT", "3552")
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("CATALOG_ADMIN_PORT must be a valid TCP port")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", portValue))
	if err != nil {
		return fmt.Errorf("listen for loopback catalog admin HTTP: %w", err)
	}
	server := &http.Server{Handler: &catalogAdminHTTP{admin: pb.NewCatalogAdminServiceClient(service.backendC), cache: service.cache, metrics: service.metrics, timeout: 2 * time.Second}}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Errorf("catalog admin HTTP stopped: %v", err)
		}
	}()
	log.Infof("catalog admin and metrics listening on %s", listener.Addr())
	return nil
}
