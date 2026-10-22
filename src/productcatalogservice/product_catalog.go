// Copyright 2023 Google LLC
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
	"strings"
	"time"

	pb "github.com/GoogleCloudPlatform/microservices-demo/src/productcatalogservice/genproto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

type productCatalog struct {
	pb.UnimplementedProductCatalogServiceServer
	catalog  pb.ListProductsResponse
	backendC *grpc.ClientConn
	cache    *catalogCache
	metrics  *catalogMetrics
}

func (p *productCatalog) Check(ctx context.Context, req *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func (p *productCatalog) Watch(req *healthpb.HealthCheckRequest, ws healthpb.Health_WatchServer) error {
	return status.Errorf(codes.Unimplemented, "health check via Watch not implemented")
}

func (p *productCatalog) ListProducts(ctx context.Context, _ *pb.Empty) (_ *pb.ListProductsResponse, retErr error) {
	finish := p.trackRequest()
	defer func() { finish(retErr) }()
	time.Sleep(extraLatency)
	catalog, err := p.readCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return catalog, nil
}

func (p *productCatalog) GetProduct(ctx context.Context, req *pb.GetProductRequest) (_ *pb.Product, retErr error) {
	finish := p.trackRequest()
	defer func() { finish(retErr) }()
	time.Sleep(extraLatency)

	catalog, err := p.readCatalog(ctx)
	if err != nil {
		return nil, err
	}
	for _, product := range catalog.Products {
		if req.Id == product.Id {
			return product, nil
		}
	}

	return nil, status.Errorf(codes.NotFound, "no product with ID %s", req.Id)
}

func (p *productCatalog) SearchProducts(ctx context.Context, req *pb.SearchProductsRequest) (_ *pb.SearchProductsResponse, retErr error) {
	finish := p.trackRequest()
	defer func() { finish(retErr) }()
	time.Sleep(extraLatency)

	catalog, err := p.readCatalog(ctx)
	if err != nil {
		return nil, err
	}
	var ps []*pb.Product
	for _, product := range catalog.Products {
		if strings.Contains(strings.ToLower(product.Name), strings.ToLower(req.Query)) ||
			strings.Contains(strings.ToLower(product.Description), strings.ToLower(req.Query)) {
			ps = append(ps, product)
		}
	}

	return &pb.SearchProductsResponse{Results: ps}, nil
}

func (p *productCatalog) readCatalog(ctx context.Context) (*pb.ListProductsResponse, error) {
	if p.cache != nil {
		return p.cache.Get(ctx)
	}
	return &pb.ListProductsResponse{Products: p.parseCatalog()}, nil
}

func (p *productCatalog) trackRequest() func(error) {
	if p.metrics == nil {
		return func(error) {}
	}
	return p.metrics.startRequest()
}

func (p *productCatalog) parseCatalog() []*pb.Product {
	if reloadCatalog || len(p.catalog.Products) == 0 {
		err := loadCatalog(&p.catalog)
		if err != nil {
			return []*pb.Product{}
		}
	}

	return p.catalog.Products
}
