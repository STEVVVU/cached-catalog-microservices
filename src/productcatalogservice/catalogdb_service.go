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
	"strings"
	"sync/atomic"
	"time"

	pb "github.com/GoogleCloudPlatform/microservices-demo/src/productcatalogservice/genproto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type catalogDBService struct {
	pb.UnimplementedProductCatalogServiceServer
	store         *catalogStore
	backendRead   atomic.Uint64
	responseDelay time.Duration
}

func (s *catalogDBService) snapshot(ctx context.Context) (*pb.ListProductsResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.backendRead.Add(1)
	catalog, revision, err := s.store.Read()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read durable catalog: %v", err)
	}
	if s.responseDelay > 0 {
		log.Infof("catalog snapshot captured revision=%d; delaying gRPC response by %s", revision, s.responseDelay)
		timer := time.NewTimer(s.responseDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		case <-timer.C:
		}
	}
	return catalog, nil
}

func (s *catalogDBService) ListProducts(ctx context.Context, _ *pb.Empty) (*pb.ListProductsResponse, error) {
	return s.snapshot(ctx)
}

func (s *catalogDBService) GetProduct(ctx context.Context, req *pb.GetProductRequest) (*pb.Product, error) {
	catalog, err := s.snapshot(ctx)
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

func (s *catalogDBService) SearchProducts(ctx context.Context, req *pb.SearchProductsRequest) (*pb.SearchProductsResponse, error) {
	catalog, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(req.Query)
	results := make([]*pb.Product, 0)
	for _, product := range catalog.Products {
		if strings.Contains(strings.ToLower(product.Name), query) || strings.Contains(strings.ToLower(product.Description), query) {
			results = append(results, product)
		}
	}
	return &pb.SearchProductsResponse{Results: results}, nil
}

type catalogAdminService struct {
	pb.UnimplementedCatalogAdminServiceServer
	store    *catalogStore
	database *catalogDBService
}

func (s *catalogAdminService) ReplaceCatalog(ctx context.Context, req *pb.ReplaceCatalogRequest) (*pb.CatalogAdminRevision, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "replacement catalog is required")
	}
	var catalog pb.ListProductsResponse
	if err := proto.Unmarshal(req.ProductCatalog, &catalog); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode catalog snapshot: %v", err)
	}
	var expected *uint64
	if req.CheckExpectedRevision {
		expected = &req.ExpectedRevision
	}
	revision, err := s.store.Replace(&catalog, expected)
	if err != nil {
		if err == errCatalogRevisionMismatch {
			return nil, status.Error(codes.Aborted, err.Error())
		}
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	return &pb.CatalogAdminRevision{Revision: revision}, nil
}

func (s *catalogAdminService) GetStats(context.Context, *emptypb.Empty) (*pb.CatalogAdminStats, error) {
	_, revision, err := s.store.Read()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read catalog stats: %v", err)
	}
	return &pb.CatalogAdminStats{Revision: revision, BackendReads: s.database.backendRead.Load()}, nil
}

func callCatalogReplace(ctx context.Context, client pb.CatalogAdminServiceClient, catalog *pb.ListProductsResponse, expectedRevision *uint64) (uint64, error) {
	encoded, err := proto.Marshal(catalog)
	if err != nil {
		return 0, fmt.Errorf("encode catalog update: %w", err)
	}
	request := &pb.ReplaceCatalogRequest{ProductCatalog: encoded}
	if expectedRevision != nil {
		request.CheckExpectedRevision = true
		request.ExpectedRevision = *expectedRevision
	}
	revision, err := client.ReplaceCatalog(ctx, request)
	if err != nil {
		return 0, err
	}
	return revision.Revision, nil
}

func callCatalogStats(ctx context.Context, client pb.CatalogAdminServiceClient) (*pb.CatalogAdminStats, error) {
	stats, err := client.GetStats(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, err
	}
	return stats, nil
}

func callCatalogRevision(ctx context.Context, client pb.CatalogAdminServiceClient) (uint64, error) {
	stats, err := callCatalogStats(ctx, client)
	if err != nil {
		return 0, err
	}
	return stats.Revision, nil
}
