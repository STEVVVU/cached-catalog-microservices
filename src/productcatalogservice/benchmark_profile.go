//go:build benchmark

package main

import (
	"os"
	"runtime/pprof"
	"time"
)

func init() {
	path := os.Getenv("CATALOG_BENCH_PROFILE")
	if path == "" {
		return
	}
	file, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	if err := pprof.StartCPUProfile(file); err != nil {
		panic(err)
	}
	time.AfterFunc(8*time.Second, func() {
		pprof.StopCPUProfile()
		if err := file.Close(); err != nil {
			panic(err)
		}
	})
}
