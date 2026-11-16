package main

import (
	"context"
	"embed"
	"errors"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"locationbot/internal/catalog"
	"locationbot/internal/syncer"
	"locationbot/internal/telegram"
)

//go:embed config/seed.json
var embedded embed.FS

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if value, err := strconv.ParseBool(v); err == nil {
			return value
		}
	}
	return def
}
func syncNow(ctx context.Context, service *syncer.Syncer) {
	syncCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	r, err := service.Run(syncCtx)
	if err != nil {
		log.Printf("Yandex Disk: %v (keeping existing catalog)", err)
		return
	}
	log.Printf("Yandex Disk synced: total=%d downloaded=%d unchanged=%d errors=%d", r.Total, r.Downloaded, r.Skipped, len(r.Failures))
	for _, msg := range r.Failures {
		log.Printf("Yandex Disk file: %s", msg)
	}
}
func main() {
	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" {
		log.Fatal("BOT_TOKEN must be provided in server .env")
	}
	dataDir := env("DATA_DIR", "./runtime")
	overrides := env("OVERRIDES_PATH", "./config/overrides.json")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatal(err)
	}
	seed, err := embedded.ReadFile("config/seed.json")
	if err != nil {
		log.Fatal(err)
	}
	seedPath := filepath.Join(dataDir, "seed.json")
	if err := os.WriteFile(seedPath, seed, 0644); err != nil {
		log.Fatal(err)
	}
	store, err := catalog.New(dataDir, seedPath, overrides)
	if err != nil {
		log.Fatalf("Catalog initialization: %v", err)
	}
	diskURL := env("YANDEX_DISK_PUBLIC_URL", "https://disk.yandex.ru/d/75ss-NdSgD-gZQ")
	syncService := syncer.New(store, diskURL)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if envBool("SYNC_ON_START", true) {
		go syncNow(ctx, syncService)
	}
	if rawInterval := env("SYNC_INTERVAL", "12h"); rawInterval != "0" {
		interval, err := time.ParseDuration(rawInterval)
		if err != nil || interval < time.Minute {
			log.Fatalf("SYNC_INTERVAL must be >= 1m or 0, got %q", rawInterval)
		}
		go func() {
			timer := time.NewTicker(interval)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					syncNow(ctx, syncService)
				}
			}
		}()
	}
	bot := telegram.New(token, env("MAP_CONSTRUCTOR_ID", "4e091ad95afb487039d07dc0667d6d596da173fe30cff48c7f2e234b7150d7f0"), dataDir, store, syncService, os.Getenv("ADMIN_IDS"))
	if err := bot.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
