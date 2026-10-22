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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/GoogleCloudPlatform/microservices-demo/src/productcatalogservice/genproto"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

func TestCatalogStorePersistsReplacementAndRejectsInvalidWrites(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "products.json")
	if err := os.WriteFile(seed, []byte(`{"products":[{"id":"a","name":"Initial","priceUsd":{"currencyCode":"USD","units":1}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "catalog.db")
	store, err := openCatalogStore(dbPath, seed)
	if err != nil {
		t.Fatal(err)
	}
	_, revision, err := store.Read()
	if err != nil || revision != 1 {
		t.Fatalf("seed revision = %d, err = %v; want 1 and nil", revision, err)
	}
	expected := uint64(1)
	updated := testCatalog("Updated")
	revision, err = store.Replace(updated, &expected)
	if err != nil || revision != 2 {
		t.Fatalf("replacement revision = %d, err = %v; want 2 and nil", revision, err)
	}
	if _, err := store.Replace(testCatalog("stale writer"), &expected); !errors.Is(err, errCatalogRevisionMismatch) {
		t.Fatalf("stale revision error = %v; want revision conflict", err)
	}
	if _, err := store.Replace(&pb.ListProductsResponse{Products: []*pb.Product{{Id: "dup", Name: "one", PriceUsd: validTestPrice()}, {Id: "dup", Name: "two", PriceUsd: validTestPrice()}}}, nil); err == nil {
		t.Fatal("duplicate product IDs were accepted")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = openCatalogStore(dbPath, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	catalog, revision, err := store.Read()
	if err != nil || revision != 2 || len(catalog.Products) != 1 || catalog.Products[0].Name != "Updated" {
		t.Fatalf("reopened catalog = %#v, revision = %d, err = %v", catalog, revision, err)
	}
}

func TestCatalogStoreRejectsMalformedRevisionWithoutOverwritingDatabase(t *testing.T) {
	for _, badRevision := range [][]byte{nil, {1}, {1, 2, 3, 4}, make([]byte, 8), {1, 2, 3, 4, 5, 6, 7, 8, 9}} {
		t.Run(fmt.Sprintf("revision-length-%d", len(badRevision)), func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "catalog.db")
			seed := filepath.Join(dir, "products.json")
			if err := os.WriteFile(seed, []byte(`{"products":[{"id":"seed","name":"Seed","priceUsd":{"currencyCode":"USD","units":1}}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			originalSnapshot, err := proto.Marshal(testCatalog("must survive corrupt revision"))
			if err != nil {
				t.Fatal(err)
			}
			db, err := bolt.Open(dbPath, 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Update(func(tx *bolt.Tx) error {
				bucket, err := tx.CreateBucket(catalogBucket)
				if err != nil {
					return err
				}
				if err := bucket.Put(catalogKey, originalSnapshot); err != nil {
					return err
				}
				return bucket.Put(revisionKey, badRevision)
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			db, err = bolt.Open(dbPath, 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			directStore := &catalogStore{db: db}
			_, _, readErr := directStore.Read()
			_, replaceErr := directStore.Replace(testCatalog("must not overwrite"), nil)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if readErr == nil {
				t.Fatal("Read accepted malformed revision bytes")
			}
			if replaceErr == nil {
				t.Fatal("Replace accepted malformed revision bytes")
			}

			store, openErr := openCatalogStore(dbPath, seed)
			if openErr == nil {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				t.Fatal("open accepted malformed revision bytes")
			}

			db, err = bolt.Open(dbPath, 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.View(func(tx *bolt.Tx) error {
				bucket := tx.Bucket(catalogBucket)
				if !bytes.Equal(bucket.Get(catalogKey), originalSnapshot) {
					t.Error("malformed database snapshot was overwritten")
				}
				if !bytes.Equal(bucket.Get(revisionKey), badRevision) {
					t.Error("malformed revision bytes were changed")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCatalogStoreDoesNotReseedExistingMalformedDatabase(t *testing.T) {
	tests := []struct {
		name   string
		create func(*bolt.Tx) error
		check  func(*testing.T, *bolt.Tx)
	}{
		{
			name: "snapshot missing but revision remains",
			create: func(tx *bolt.Tx) error {
				bucket, err := tx.CreateBucket(catalogBucket)
				if err != nil {
					return err
				}
				return bucket.Put(revisionKey, encodeRevision(7))
			},
			check: func(t *testing.T, tx *bolt.Tx) {
				bucket := tx.Bucket(catalogBucket)
				if bucket.Get(catalogKey) != nil {
					t.Error("missing snapshot was reseeded")
				}
				if got := decodeRevision(bucket.Get(revisionKey)); got != 7 {
					t.Errorf("revision changed to %d; want original revision 7", got)
				}
			},
		},
		{
			name: "catalog bucket missing from existing database",
			create: func(tx *bolt.Tx) error {
				bucket, err := tx.CreateBucket([]byte("unrelated"))
				if err != nil {
					return err
				}
				return bucket.Put([]byte("sentinel"), []byte("preserve"))
			},
			check: func(t *testing.T, tx *bolt.Tx) {
				if tx.Bucket(catalogBucket) != nil {
					t.Error("missing catalog bucket was created on an existing database")
				}
				if got := tx.Bucket([]byte("unrelated")).Get([]byte("sentinel")); !bytes.Equal(got, []byte("preserve")) {
					t.Errorf("unrelated existing data changed to %q", got)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "catalog.db")
			seed := filepath.Join(dir, "products.json")
			if err := os.WriteFile(seed, []byte(`{"products":[{"id":"seed","name":"Seed","priceUsd":{"currencyCode":"USD","units":1}}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			db, err := bolt.Open(dbPath, 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Update(test.create); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			store, err := openCatalogStore(dbPath, seed)
			if err == nil {
				if closeErr := store.Close(); closeErr != nil {
					t.Fatal(closeErr)
				}
				t.Fatal("existing malformed database was reseeded instead of rejected")
			}

			db, err = bolt.Open(dbPath, 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.View(func(tx *bolt.Tx) error {
				test.check(t, tx)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLeaseCacheCoalescesColdReads(t *testing.T) {
	const readers = 12
	started := make(chan struct{}, readers)
	release := make(chan struct{})
	var calls atomic.Int32
	metrics := &catalogMetrics{}
	cache, err := newCatalogCache("lease", time.Minute, func(context.Context) (*pb.ListProductsResponse, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return testCatalog("warm"), nil
	}, metrics)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, readers)
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			catalog, err := cache.Get(context.Background())
			if err == nil && catalog.Products[0].Name != "warm" {
				err = errors.New("cache returned an unexpected snapshot")
			}
			errCh <- err
		}()
	}
	<-started
	deadline := time.After(time.Second)
	for metrics.leaseWaiters.Load() < readers-1 {
		select {
		case <-deadline:
			t.Fatalf("only %d callers waited for the lease", metrics.leaseWaiters.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("backend reads = %d; want one coalesced read", got)
	}
}

func TestCacheReturnedCopiesRemainIsolatedDuringInvalidation(t *testing.T) {
	for _, mode := range []string{"ttl", "lease"} {
		t.Run(mode, func(t *testing.T) {
			cache, err := newCatalogCache(mode, time.Minute, func(context.Context) (*pb.ListProductsResponse, error) {
				catalog := testCatalog("original")
				catalog.Products[0].Categories = []string{"original"}
				return catalog, nil
			}, &catalogMetrics{})
			if err != nil {
				t.Fatal(err)
			}
			mutate := func() {
				catalog, err := cache.Get(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				product := catalog.Products[0]
				if product.Name != "original" || product.PriceUsd.Units != 1 || product.Categories[0] != "original" {
					t.Error("a returned copy mutated another response or cached snapshot")
				}
				product.Name = "caller mutation"
				product.PriceUsd.Units = 42
				product.Categories[0] = "caller mutation"
			}
			mutate()
			mutate()
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range 16 {
						mutate()
					}
				}()
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 32 {
					cache.Invalidate()
				}
			}()
			wg.Wait()
			mutate()
		})
	}
}

func TestLeaseCacheCanceledLeaderDoesNotFailLiveWaiter(t *testing.T) {
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	var calls atomic.Int32
	metrics := &catalogMetrics{}
	cache, err := newCatalogCache("lease", time.Minute, func(ctx context.Context) (*pb.ListProductsResponse, error) {
		switch calls.Add(1) {
		case 1:
			close(firstStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		default:
			close(secondStarted)
			<-releaseSecond
			return testCatalog("survived"), nil
		}
	}, metrics)
	if err != nil {
		t.Fatal(err)
	}

	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	ownerResult := make(chan error, 1)
	go func() {
		_, err := cache.Get(ownerCtx)
		ownerResult <- err
	}()
	<-firstStarted

	waiterResult := make(chan error, 1)
	go func() {
		catalog, err := cache.Get(context.Background())
		if err == nil && catalog.Products[0].Name != "survived" {
			err = errors.New("waiter received an unexpected catalog")
		}
		waiterResult <- err
	}()
	deadline := time.After(time.Second)
	for metrics.leaseWaiters.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("live waiter did not join the leader's flight")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	cancelOwner()
	if err := <-ownerResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled owner error = %v; want context canceled", err)
	}
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("live waiter did not elect a replacement fetch after leader cancellation")
	}
	close(releaseSecond)
	if err := <-waiterResult; err != nil {
		t.Fatalf("live waiter failed with canceled leader: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("backend reads = %d; want canceled owner plus one replacement", got)
	}
}

func TestLeaseCacheDiscardsInflightFillAfterInvalidation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	cache, err := newCatalogCache("lease", time.Minute, func(context.Context) (*pb.ListProductsResponse, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return testCatalog("old"), nil
		}
		return testCatalog("new"), nil
	}, &catalogMetrics{})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan *pb.ListProductsResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		catalog, err := cache.Get(context.Background())
		result <- catalog
		errCh <- err
	}()
	<-started
	cache.Invalidate()
	close(release)
	catalog := <-result
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if catalog.Products[0].Name != "new" {
		t.Fatalf("read after invalidation returned %q; want new snapshot", catalog.Products[0].Name)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("backend reads = %d; want old fill plus refetch", got)
	}
	if got := cache.metrics.fencedFills.Load(); got != 1 {
		t.Fatalf("fenced fills = %d; want one discarded old fill", got)
	}
}

func testCatalog(name string) *pb.ListProductsResponse {
	return &pb.ListProductsResponse{Products: []*pb.Product{{Id: "a", Name: name, PriceUsd: validTestPrice()}}}
}

func validTestPrice() *pb.Money { return &pb.Money{CurrencyCode: "USD", Units: 1} }
