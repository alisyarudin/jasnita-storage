// jasstore: server object storage S3-compatible dengan web console.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alisyarudin/jasnita-storage/internal/blob"
	"github.com/alisyarudin/jasnita-storage/internal/config"
	"github.com/alisyarudin/jasnita-storage/internal/console"
	"github.com/alisyarudin/jasnita-storage/internal/meta"
	"github.com/alisyarudin/jasnita-storage/internal/s3api"
)

var version = "dev"

func main() {
	envFile := flag.String("env", ".env", "berkas environment (opsional)")
	showVersion := flag.Bool("version", false, "tampilkan versi")
	flag.Parse()
	if *showVersion {
		fmt.Println("jasstore", version)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := config.LoadEnvFile(*envFile); err != nil {
		log.Error("baca env", "err", err)
		os.Exit(1)
	}
	cfg, err := config.FromEnv()
	if err != nil {
		log.Error("konfigurasi", "err", err)
		os.Exit(1)
	}

	store, err := blob.New(cfg.DataDir)
	if err != nil {
		log.Error("data dir", "err", err)
		os.Exit(1)
	}
	db, err := meta.Open(cfg.DBPath)
	if err != nil {
		log.Error("buka database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()
	// Root key dari env selalu disinkron sebagai admin.
	if err := db.UpsertAccessKey(ctx, meta.AccessKey{AccessKey: cfg.RootAccessKey, SecretKey: cfg.RootSecretKey, Name: "root", Admin: true, Enabled: true, CreatedAt: time.Now()}); err != nil {
		log.Error("root key", "err", err)
		os.Exit(1)
	}

	s3 := &s3api.Server{Meta: db, Blob: store, Region: cfg.Region, Domain: cfg.Domain, Log: log}
	con := console.New(s3, cfg, log, version)

	s3Srv := &http.Server{Addr: cfg.S3Addr, Handler: s3, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}
	conSrv := &http.Server{Addr: cfg.ConsoleAddr, Handler: con, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}

	go func() {
		log.Info("API S3 mendengarkan", "addr", cfg.S3Addr, "data", cfg.DataDir, "region", cfg.Region)
		if err := s3Srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server s3", "err", err)
			os.Exit(1)
		}
	}()
	go func() {
		log.Info("Console mendengarkan", "addr", cfg.ConsoleAddr)
		if err := conSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server console", "err", err)
			os.Exit(1)
		}
	}()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			if n := s3.CleanStaleUploads(ctx, 7*24*time.Hour); n > 0 {
				log.Info("upload multipart basi dibersihkan", "n", n)
			}
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Info("berhenti...")
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s3Srv.Shutdown(shutdownCtx)
	conSrv.Shutdown(shutdownCtx)
}
