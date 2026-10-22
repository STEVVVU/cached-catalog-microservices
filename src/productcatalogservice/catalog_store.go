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
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	pb "github.com/GoogleCloudPlatform/microservices-demo/src/productcatalogservice/genproto"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

var (
	catalogBucket = []byte("catalog")
	catalogKey    = []byte("snapshot")
	revisionKey   = []byte("revision")
)

type catalogStore struct {
	db *bolt.DB
}

func openCatalogStore(path, seedPath string) (*catalogStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create catalog database directory: %w", err)
	}
	_, statErr := os.Stat(path)
	databaseExisted := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect catalog database: %w", statErr)
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open catalog database: %w", err)
	}
	s := &catalogStore{db: db}
	err = db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(catalogBucket)
		if bucket != nil {
			if bucket.Get(catalogKey) == nil {
				return errors.New("existing catalog database snapshot is missing")
			}
			if _, err := readCatalogRevision(bucket); err != nil {
				return err
			}
			return nil
		}
		if databaseExisted {
			return errors.New("existing catalog database bucket is missing")
		}
		bucket, err = tx.CreateBucket(catalogBucket)
		if err != nil {
			return err
		}
		var seed pb.ListProductsResponse
		if err := loadCatalogFromPath(seedPath, &seed); err != nil {
			return fmt.Errorf("load initial catalog: %w", err)
		}
		if err := validateCatalog(&seed); err != nil {
			return fmt.Errorf("invalid initial catalog: %w", err)
		}
		encoded, err := proto.Marshal(&seed)
		if err != nil {
			return err
		}
		if err := bucket.Put(catalogKey, encoded); err != nil {
			return err
		}
		return bucket.Put(revisionKey, encodeRevision(1))
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *catalogStore) Close() error { return s.db.Close() }

func (s *catalogStore) Read() (*pb.ListProductsResponse, uint64, error) {
	var catalog pb.ListProductsResponse
	var revision uint64
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(catalogBucket)
		if bucket == nil {
			return errors.New("catalog database bucket is missing")
		}
		data := bucket.Get(catalogKey)
		if data == nil {
			return errors.New("catalog database snapshot is missing")
		}
		if err := proto.Unmarshal(data, &catalog); err != nil {
			return fmt.Errorf("decode catalog snapshot: %w", err)
		}
		storedRevision, err := readCatalogRevision(bucket)
		if err != nil {
			return err
		}
		revision = storedRevision
		return nil
	})
	return &catalog, revision, err
}

func (s *catalogStore) Replace(catalog *pb.ListProductsResponse, expectedRevision *uint64) (uint64, error) {
	if err := validateCatalog(catalog); err != nil {
		return 0, err
	}
	encoded, err := proto.Marshal(catalog)
	if err != nil {
		return 0, fmt.Errorf("encode catalog: %w", err)
	}
	var nextRevision uint64
	err = s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(catalogBucket)
		if bucket == nil {
			return errors.New("catalog database bucket is missing")
		}
		currentRevision, err := readCatalogRevision(bucket)
		if err != nil {
			return err
		}
		if expectedRevision != nil && *expectedRevision != currentRevision {
			return errCatalogRevisionMismatch
		}
		if currentRevision == math.MaxUint64 {
			return errors.New("catalog database revision is exhausted")
		}
		nextRevision = currentRevision + 1
		if err := bucket.Put(catalogKey, encoded); err != nil {
			return err
		}
		return bucket.Put(revisionKey, encodeRevision(nextRevision))
	})
	if err != nil {
		return 0, err
	}
	return nextRevision, nil
}

func encodeRevision(revision uint64) []byte {
	var encoded [8]byte
	binary.LittleEndian.PutUint64(encoded[:], revision)
	return encoded[:]
}

func decodeRevision(encoded []byte) uint64 {
	if len(encoded) != 8 {
		return 0
	}
	return binary.LittleEndian.Uint64(encoded)
}

func readCatalogRevision(bucket *bolt.Bucket) (uint64, error) {
	encoded := bucket.Get(revisionKey)
	if len(encoded) != 8 {
		return 0, errors.New("catalog database revision is missing or invalid")
	}
	revision := decodeRevision(encoded)
	if revision == 0 {
		return 0, errors.New("catalog database revision must be positive")
	}
	return revision, nil
}

var errCatalogRevisionMismatch = errors.New("catalog revision does not match expected revision")

func validateCatalog(catalog *pb.ListProductsResponse) error {
	if catalog == nil || len(catalog.Products) == 0 || len(catalog.Products) > 5000 {
		return errors.New("catalog must contain between 1 and 5000 products")
	}
	if proto.Size(catalog) > 8<<20 {
		return errors.New("catalog snapshot exceeds 8 MiB")
	}
	ids := make(map[string]struct{}, len(catalog.Products))
	for i, product := range catalog.Products {
		if product == nil || product.Id == "" || product.Name == "" {
			return fmt.Errorf("product %d must have an ID and name", i)
		}
		if len(product.Id) > 128 || len(product.Name) > 256 || len(product.Description) > 16<<10 || len(product.Picture) > 2048 || len(product.Categories) > 32 {
			return fmt.Errorf("product %q exceeds a field limit", product.Id)
		}
		if _, exists := ids[product.Id]; exists {
			return fmt.Errorf("duplicate product ID %q", product.Id)
		}
		ids[product.Id] = struct{}{}
		if product.PriceUsd == nil || product.PriceUsd.CurrencyCode != "USD" ||
			product.PriceUsd.Nanos < 0 || product.PriceUsd.Nanos > 999999999 || product.PriceUsd.Units < 0 {
			return fmt.Errorf("product %q has an invalid USD price", product.Id)
		}
		for _, category := range product.Categories {
			if category == "" || len(category) > 64 {
				return fmt.Errorf("product %q has an invalid category", product.Id)
			}
		}
	}
	return nil
}
