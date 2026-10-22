package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"time"

	pb "github.com/GoogleCloudPlatform/microservices-demo/src/productcatalogservice/genproto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type loadSample struct {
	Index                                                int
	ScheduledMS, StartMS, LatencyMS, RPCMS, ValidationMS float64
	Code                                                 string
	Bytes                                                int
}

func loadCatalog(args []string) error {
	flags := flag.NewFlagSet("load", flag.ContinueOnError)
	targets := flags.String("targets", "127.0.0.1:3550", "comma-separated gateway addresses, round-robin per request")
	rate := flags.Int("rate", 100, "scheduled requests per second")
	duration := flags.Duration("duration", 10*time.Second, "offered-load duration")
	concurrency := flags.Int("concurrency", 64, "maximum concurrent RPCs; excess arrivals are rejected")
	fixture := flags.String("catalog", "products.json", "exact expected catalog JSON")
	output := flags.String("out", "load.csv", "new raw-sample CSV path")
	profilePath := flags.String("cpu-profile", "", "optional separate diagnostic CPU profile path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *rate < 1 || *rate > 100000 || *duration <= 0 || *duration > 10*time.Minute || *concurrency < 1 || *concurrency > 1024 {
		return errors.New("rate must be 1..100000, duration 0..10m, concurrency 1..1024")
	}
	offered := int(duration.Seconds() * float64(*rate))
	if offered < 1 || offered > 1000000 {
		return errors.New("offer count must be 1..1000000")
	}
	data, err := os.ReadFile(*fixture)
	if err != nil {
		return err
	}
	var expected pb.ListProductsResponse
	if err := protojson.Unmarshal(data, &expected); err != nil {
		return err
	}
	if len(expected.Products) == 0 {
		return errors.New("empty expected catalog")
	}
	search := &pb.SearchProductsResponse{}
	for _, product := range expected.Products {
		if strings.Contains(strings.ToLower(product.Name), "benchmark-needle") || strings.Contains(strings.ToLower(product.Description), "benchmark-needle") {
			search.Results = append(search.Results, product)
		}
	}
	var clients []pb.ProductCatalogServiceClient
	for _, target := range strings.Split(*targets, ",") {
		conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(8<<20)))
		if err != nil {
			return err
		}
		defer conn.Close()
		clients = append(clients, pb.NewProductCatalogServiceClient(conn))
	}
	for _, client := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		actual, err := client.ListProducts(ctx, &pb.Empty{})
		cancel()
		if err != nil {
			return fmt.Errorf("warmup: %w", err)
		}
		if !proto.Equal(actual, &expected) {
			return errors.New("warmup catalog differs from fixture")
		}
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if *profilePath != "" {
		profile, err := os.OpenFile(*profilePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if err := pprof.StartCPUProfile(profile); err != nil {
			profile.Close()
			return err
		}
		defer func() { pprof.StopCPUProfile(); profile.Close() }()
	}
	samples := make([]loadSample, offered)
	jobs := make(chan int, *concurrency)
	var workers sync.WaitGroup
	started := time.Now()
	for range *concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				scheduled := started.Add(time.Duration(float64(index) * float64(time.Second) / float64(*rate)))
				rpcStart := time.Now()
				ctx, cancel := context.WithDeadline(context.Background(), scheduled.Add(3*time.Second))
				client := clients[index%len(clients)]
				var actual, want proto.Message
				var callErr error
				switch index % 10 {
				case 0, 1, 2, 3, 4:
					actual, callErr = client.ListProducts(ctx, &pb.Empty{})
					want = &expected
				case 5, 6, 7, 8:
					product := expected.Products[(index/10)%len(expected.Products)]
					actual, callErr = client.GetProduct(ctx, &pb.GetProductRequest{Id: product.Id})
					want = product
				default:
					actual, callErr = client.SearchProducts(ctx, &pb.SearchProductsRequest{Query: "benchmark-needle"})
					want = search
				}
				finished := time.Now()
				cancel()
				code := status.Code(callErr).String()
				bytes := 0
				if callErr == nil {
					bytes = proto.Size(actual)
					if !proto.Equal(actual, want) {
						code = "CORRECTNESS_ERROR"
					}
				}
				samples[index] = loadSample{index, scheduled.Sub(started).Seconds() * 1000, rpcStart.Sub(started).Seconds() * 1000, finished.Sub(scheduled).Seconds() * 1000, finished.Sub(rpcStart).Seconds() * 1000, time.Since(finished).Seconds() * 1000, code, bytes}
			}
		}()
	}
	for index := range offered {
		scheduled := started.Add(time.Duration(float64(index) * float64(time.Second) / float64(*rate)))
		if wait := time.Until(scheduled); wait > 0 {
			time.Sleep(wait)
		}
		select {
		case jobs <- index:
		default:
			samples[index] = loadSample{Index: index, ScheduledMS: scheduled.Sub(started).Seconds() * 1000, StartMS: time.Since(started).Seconds() * 1000, Code: "REJECTED"}
		}
	}
	close(jobs)
	if wait := time.Until(started.Add(*duration)); wait > 0 {
		time.Sleep(wait)
	}
	workers.Wait()
	wall := time.Since(started).Seconds()
	writer := csv.NewWriter(file)
	if err := writer.Write([]string{"index", "scheduled_ms", "start_ms", "latency_from_schedule_ms", "rpc_ms", "validation_ms", "code", "response_bytes"}); err != nil {
		return err
	}
	counts := map[string]int{}
	var success, all, lag, validation []float64
	for _, sample := range samples {
		counts[sample.Code]++
		lag = append(lag, sample.StartMS-sample.ScheduledMS)
		if sample.Code != "REJECTED" {
			all = append(all, sample.LatencyMS)
			validation = append(validation, sample.ValidationMS)
		}
		if sample.Code == codes.OK.String() {
			success = append(success, sample.LatencyMS)
		}
		if err := writer.Write([]string{fmt.Sprint(sample.Index), fmt.Sprint(sample.ScheduledMS), fmt.Sprint(sample.StartMS), fmt.Sprint(sample.LatencyMS), fmt.Sprint(sample.RPCMS), fmt.Sprint(sample.ValidationMS), sample.Code, fmt.Sprint(sample.Bytes)}); err != nil {
			return err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	for _, values := range [][]float64{success, all, lag, validation} {
		sort.Float64s(values)
	}
	result := map[string]any{"offered": offered, "successful": len(success), "rejected": counts["REJECTED"], "timeouts": counts[codes.DeadlineExceeded.String()], "correctness_errors": counts["CORRECTNESS_ERROR"], "codes": counts, "wall_s": wall, "offered_rate": *rate, "successful_per_s": float64(len(success)) / wall, "p50_ms": percentile(success, .5), "p95_ms": percentile(success, .95), "p99_ms": percentile(success, .99), "all_completed_p99_ms": percentile(all, .99), "generator_lag_p99_ms": percentile(lag, .99), "validation_p99_ms": percentile(validation, .99), "catalog_products": len(expected.Products), "targets": *targets, "concurrency": *concurrency, "duration_s": duration.Seconds(), "mix": "50% list, 40% get, 10% search; round-robin replica selection"}
	result["failed"] = offered - len(success) - counts["REJECTED"]
	result["attempted"] = offered - counts["REJECTED"]
	result["queue_capacity"] = *concurrency
	result["warmup_requests"] = len(clients)
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return err
	}
	if counts["CORRECTNESS_ERROR"] != 0 || counts[""] != 0 {
		return errors.New("incorrect or missing load results")
	}
	return nil
}
