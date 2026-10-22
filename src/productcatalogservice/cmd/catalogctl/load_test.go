package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	pb "github.com/GoogleCloudPlatform/microservices-demo/src/productcatalogservice/genproto"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestLoadRejectsInvalidWorkload(t *testing.T) {
	for _, args := range [][]string{{"-rate", "0"}, {"-rate", "100001"}, {"-duration", "0s"}, {"-duration", "11m"}, {"-concurrency", "0"}, {"-duration", "10m", "-rate", "100000"}} {
		if err := loadCatalog(args); err == nil {
			t.Fatalf("accepted invalid workload %v", args)
		}
	}
}

type loadFixtureServer struct {
	pb.UnimplementedProductCatalogServiceServer
	catalog *pb.ListProductsResponse
	calls   atomic.Int32
	corrupt bool
}

func (s *loadFixtureServer) ListProducts(context.Context, *pb.Empty) (*pb.ListProductsResponse, error) {
	if s.calls.Add(1) > 1 && s.corrupt {
		return &pb.ListProductsResponse{}, nil
	}
	return s.catalog, nil
}

func (s *loadFixtureServer) GetProduct(context.Context, *pb.GetProductRequest) (*pb.Product, error) {
	return s.catalog.Products[0], nil
}

func (s *loadFixtureServer) SearchProducts(context.Context, *pb.SearchProductsRequest) (*pb.SearchProductsResponse, error) {
	return &pb.SearchProductsResponse{Results: s.catalog.Products}, nil
}

func TestLoadDrivesGRPCAndRejectsCorruptResponses(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "correct", true: "corrupt"}[corrupt], func(t *testing.T) {
			catalog := &pb.ListProductsResponse{Products: []*pb.Product{{Id: "one", Name: "benchmark-needle"}}}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer()
			pb.RegisterProductCatalogServiceServer(server, &loadFixtureServer{catalog: catalog, corrupt: corrupt})
			go server.Serve(listener)
			defer server.Stop()
			directory := t.TempDir()
			fixture := filepath.Join(directory, "catalog.json")
			data, err := protojson.Marshal(catalog)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture, data, 0600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(directory, "raw.csv")
			err = loadCatalog([]string{"-targets", listener.Addr().String(), "-catalog", fixture, "-rate", "100", "-duration", "100ms", "-concurrency", "2", "-out", out})
			if (err != nil) != corrupt {
				t.Fatalf("corrupt=%v error=%v", corrupt, err)
			}
			raw, readErr := os.ReadFile(out)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Count(string(raw), "\n") != 11 {
				t.Fatalf("missing scheduled arrivals: %s", raw)
			}
			if strings.Contains(string(raw), "CORRECTNESS_ERROR") != corrupt {
				t.Fatalf("corruption was miscounted: %s", raw)
			}
		})
	}
}
