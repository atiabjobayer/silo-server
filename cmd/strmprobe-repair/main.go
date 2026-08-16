package main
// Command strmprobe-repair bulk-repairs placeholder metadata for .strm media
// files by probing the remote URLs they point at and persisting the real
// track metadata. It is the maintenance counterpart of the per-playback repair
// in PlaybackProbeEnsurer: run it once against a deployment whose scan-time
// remote probes failed (slow or throttled origins) so the whole library gains
// accurate audio/subtitle tracks without a rescan or playing every file.
//
// Usage (inside the silo container, where DATABASE_URL is already set):
//
//	strmprobe-repair -ffprobe /usr/bin/ffprobe -workers 8
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	workers := flag.Int("workers", 8, "concurrent remote probes")
	ffprobe := flag.String("ffprobe", "ffprobe", "path to the ffprobe binary")
	limit := flag.Int("limit", 0, "cap on files to repair (0 = all)")
	progressEvery := flag.Int("progress", 50, "log a progress line every N scanned files")
	flag.Parse()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fatal("DATABASE_URL is not set")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		fatal(fmt.Sprintf("creating database pool: %v", err))
	}
	defer pool.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	repo := scanner.NewFileRepository(pool)
	stats, err := scanner.RepairStrmPlaceholderProbes(ctx, repo, *ffprobe, *workers, *limit, func(s scanner.RepairStrmProbeStats) {
		if s.Scanned%*progressEvery == 0 {
			slog.Info("strm probe repair progress",
				"scanned", s.Scanned, "repaired", s.Repaired, "failed", s.Failed)
		}
	})
	if err != nil {
		fatal(fmt.Sprintf("strm probe repair failed: %v", err))
	}

	slog.Info("strm probe repair finished",
		"scanned", stats.Scanned, "repaired", stats.Repaired, "failed", stats.Failed)
}

func fatal(message string) {
	slog.Error("strmprobe-repair", "error", message)
	os.Exit(1)
}
