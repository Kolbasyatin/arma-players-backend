package main

import (
	"armaplayers/internal/config"
	"fmt"
	"log"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	fmt.Printf("%+v\n", cfg)
	fmt.Printf("observer starting on %s, poll every %s\n", cfg.HTTPAddr, cfg.PollInterval)
}
