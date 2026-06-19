package main

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mcmoney/platform-control-plane/internal/config"
	"github.com/mcmoney/platform-control-plane/internal/migrate"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if cfg.PostgresDSN == "" {
		log.Fatalf("PLATFORM_POSTGRES_DSN is required for platformmigrate")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("create postgres pool: %v", err)
	}
	defer pool.Close()

	if err := migrate.Run(ctx, pool); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	log.Printf("migrations applied successfully")
}
