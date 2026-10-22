//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func startCatalogReloadListener() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range sigs {
			log.Printf("Received signal: %s", sig)
			if sig == syscall.SIGUSR1 {
				reloadCatalog = true
				log.Infof("Enable catalog reloading")
			} else {
				reloadCatalog = false
				log.Infof("Disable catalog reloading")
			}
		}
	}()
}
