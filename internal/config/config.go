// Package config memuat konfigurasi server dari environment (dan berkas .env opsional).
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	DataDir     string // direktori penyimpanan objek
	DBPath      string // berkas SQLite metadata
	S3Addr      string // alamat listen API S3
	ConsoleAddr string // alamat listen console + admin API
	Region      string // region yang dilaporkan ke klien
	Domain      string // opsional: domain untuk virtual-host style (bucket.domain)
	PublicURL   string // opsional: URL publik endpoint S3 (dipakai console untuk contoh konfigurasi)

	RootAccessKey string
	RootSecretKey string
}

// LoadEnvFile membaca KEY=VALUE dari berkas dan memasangnya ke environment
// bila belum ada. Berkas yang tidak ada diabaikan.
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
	return sc.Err()
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func FromEnv() (*Config, error) {
	c := &Config{
		DataDir:       env("JASSTORE_DATA_DIR", "./data"),
		S3Addr:        env("JASSTORE_S3_ADDR", ":9000"),
		ConsoleAddr:   env("JASSTORE_CONSOLE_ADDR", ":9001"),
		Region:        env("JASSTORE_REGION", "us-east-1"),
		Domain:        env("JASSTORE_DOMAIN", ""),
		PublicURL:     env("JASSTORE_PUBLIC_URL", ""),
		RootAccessKey: os.Getenv("JASSTORE_ROOT_ACCESS_KEY"),
		RootSecretKey: os.Getenv("JASSTORE_ROOT_SECRET_KEY"),
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return nil, err
	}
	c.DataDir = abs
	c.DBPath = env("JASSTORE_DB_PATH", filepath.Join(c.DataDir, "jasstore.db"))

	if len(c.RootAccessKey) < 3 {
		return nil, fmt.Errorf("JASSTORE_ROOT_ACCESS_KEY wajib diisi (minimal 3 karakter)")
	}
	if len(c.RootSecretKey) < 8 {
		return nil, fmt.Errorf("JASSTORE_ROOT_SECRET_KEY wajib diisi (minimal 8 karakter)")
	}
	return c, nil
}
