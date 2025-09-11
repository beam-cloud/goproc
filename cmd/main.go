package main

import (
	"context"
	"log"

	goproc "github.com/beam-cloud/goproc/pkg"
)

func main() {
	configManager, err := goproc.NewConfigManager[goproc.GoProcConfig]()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	ctx := context.Background()
	cfg := configManager.GetConfig()

	goproc.InitLogger(cfg.DebugMode, cfg.PrettyLogs)

	s, err := goproc.NewGoProcServer(cfg)
	if err != nil {
		log.Fatal(err)
	}

	s.StartServer(ctx, cfg.ServerPort)
}
