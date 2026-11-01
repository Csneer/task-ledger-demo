// taskledgerd serves the personal task ledger's HTTP binding.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Csneer/task-ledger-demo/internal/application"
	"github.com/Csneer/task-ledger-demo/internal/core"
	"github.com/Csneer/task-ledger-demo/internal/jsonvalue"
	"github.com/Csneer/task-ledger-demo/internal/storage"
	"github.com/Csneer/task-ledger-demo/internal/transport"
	"github.com/Csneer/task-ledger-demo/web"
)

type config struct {
	Authority   string                 `json:"authority"`
	Database    string                 `json:"database"`
	Listen      string                 `json:"listen"`
	Operators   []core.ActorRef        `json:"operators"`
	Credentials []transport.Credential `json:"credentials"`
	TLSCert     string                 `json:"tls_cert,omitempty"`
	TLSKey      string                 `json:"tls_key,omitempty"`
}

func loadConfig(path string) (config, error) {
	var c config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return c, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return c, errors.New("config must be a regular file readable only by its owner (mode 0600)")
	}
	raw, err := io.ReadAll(io.LimitReader(f, application.MaxRequestBytes+1))
	if err != nil {
		return c, err
	}
	if len(raw) > application.MaxRequestBytes {
		return c, errors.New("config exceeds 1 MiB")
	}
	if _, err := jsonvalue.Parse(raw); err != nil {
		return c, errors.New("config is not strict JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, fmt.Errorf("config shape: %w", err)
	}
	if c.Authority == "" || utf8.RuneCountInString(c.Authority) > 256 || c.Database == "" || len(c.Credentials) == 0 {
		return c, errors.New("authority (1..256 characters), database and credentials are required")
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8090"
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return c, errors.New("listen must be an IP address and port")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return c, errors.New("listen must use an IP literal")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return c, errors.New("tls_cert and tls_key must be supplied together")
	}
	if !ip.IsLoopback() && c.TLSCert == "" {
		return c, errors.New("non-loopback listening requires TLS")
	}
	// Resolve file references against the configuration, not the caller's cwd.
	for _, field := range []*string{&c.Database, &c.TLSCert, &c.TLSKey} {
		if *field != "" && !filepath.IsAbs(*field) {
			*field = filepath.Join(filepath.Dir(path), *field)
		}
	}
	return c, nil
}

func serve(ctx context.Context, c config, initialize bool) error {
	for _, path := range []string{c.Database, c.Database + "-wal", c.Database + "-shm"} {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("database and sidecars must be owner-private regular files")
		}
	}
	db, err := storage.Open(c.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	svc := application.New(db, c.Authority)
	handler, err := transport.New(svc, c.Credentials)
	if err != nil {
		return err
	}
	if initialize {
		return svc.Initialize(ctx, c.Operators)
	}
	if err := db.View(ctx, func(tx *storage.Tx) error {
		v, err := tx.Get("settings", "authority")
		if errors.Is(err, storage.ErrNotFound) {
			return errors.New("database is not initialized; run with -init first")
		}
		if err != nil {
			return err
		}
		var authority string
		if err := json.Unmarshal(v, &authority); err != nil {
			return err
		}
		if authority != c.Authority {
			return errors.New("configured authority does not match database")
		}
		return nil
	}); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	scheme := "http"
	if c.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
		if err != nil {
			return err
		}
		listener = tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
		scheme = "https"
	}
	server := &http.Server{Handler: web.Handler(handler), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	log.Printf("taskledgerd ready address=%s://%s build=%s", scheme, listener.Addr(), buildID())
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		drain, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(drain); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

func buildID() string {
	value := version
	if value == "" {
		value = "development"
	}
	sourceCommit := commit
	if info, ok := debug.ReadBuildInfo(); ok {
		modified := false
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && sourceCommit == "" {
				sourceCommit = setting.Value
			}
			if setting.Key == "vcs.modified" && setting.Value == "true" {
				modified = true
			}
		}
		if modified {
			value += "+modified"
		}
	}
	if sourceCommit != "" {
		value += " (" + sourceCommit
		if buildDate != "" {
			value += " " + buildDate
		}
		value += ")"
	}
	return value
}

// These values are replaced by the release build; local builds remain explicit.
var (
	version   = "development"
	commit    string
	buildDate string
)

func main() {
	// The database includes private receipts and cursor signing material. Apply
	// this before opening SQLite so its main file and sidecars start private.
	syscall.Umask(0077)
	configPath := flag.String("config", "", "owner-readable JSON configuration file")
	initialize := flag.Bool("init", false, "initialize the authority and operators, then exit")
	version := flag.Bool("version", false, "print build identity and exit")
	flag.Parse()
	if *version {
		fmt.Println(buildID())
		return
	}
	if *configPath == "" || flag.NArg() != 0 {
		log.Fatal("usage: taskledgerd -config <file> [-init]")
	}
	c, err := loadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := serve(ctx, c, *initialize); err != nil {
		log.Fatal(err)
	}
}
