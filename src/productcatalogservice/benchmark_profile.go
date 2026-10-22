//go:build benchmark

package main

import (
	"os"
	"runtime"
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
	runtime.SetMutexProfileFraction(1)
	runtime.SetBlockProfileRate(1)
	time.AfterFunc(8*time.Second, func() {
		pprof.StopCPUProfile()
		if err := file.Close(); err != nil {
			panic(err)
		}
		for _, name := range []string{"mutex", "block"} {
			output, err := os.Create(path + "." + name + ".pprof")
			if err != nil {
				panic(err)
			}
			if err := pprof.Lookup(name).WriteTo(output, 0); err != nil {
				panic(err)
			}
			if err := output.Close(); err != nil {
				panic(err)
			}
		}
	})
}
