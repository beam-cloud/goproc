package main

import (
	"context"
	"os"

	goproc "github.com/beam-cloud/goproc/pkg"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	configManager, err := goproc.NewConfigManager[goproc.GoProcConfig]()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load config")
	}

	ctx := context.Background()
	cfg := configManager.GetConfig()
	if cfg.PrettyLogs {
		log.Logger = log.Logger.Level(zerolog.DebugLevel)
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout})
	}

	s, err := goproc.NewGoProcServer(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create GoProc server")
	}

	s.StartServer(ctx, cfg.ServerPort)
}
