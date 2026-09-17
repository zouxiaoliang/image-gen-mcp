package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zouxiaoliang/image-gen-mcp/internal/imagegen"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	flags := flag.NewFlagSet("image-gen-mcp", flag.ContinueOnError)
	configPath := flags.String("config", "config.yaml", "YAML configuration path")
	transport := flags.String("transport", "stdio", "stdio or http")
	version := flags.Bool("version", false, "print version and exit")
	check := flags.Bool("check", false, "validate configuration without starting or making network calls")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *version {
		fmt.Println(imagegen.Version)
		return nil
	}
	if *transport != "stdio" && *transport != "http" {
		return fmt.Errorf("transport must be stdio or http")
	}
	cfg, err := imagegen.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	if *transport == "http" {
		if err = cfg.ValidateHTTP(); err != nil {
			return err
		}
	}
	if *check {
		slog.Info("configuration valid")
		return nil
	}
	// Claim the HTTP endpoint before starting workers. A configuration or bind
	// failure must not resume paid generation jobs from an existing database.
	var listener net.Listener
	if *transport == "http" {
		listener, err = net.Listen("tcp", cfg.HTTP.Listen)
		if err != nil {
			return err
		}
		defer listener.Close()
	}
	engine, err := imagegen.NewEngine(cfg)
	if err != nil {
		return err
	}
	defer engine.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *transport == "stdio" {
		err = imagegen.NewMCPServer(engine, "stdio").Run(ctx, &mcp.StdioTransport{})
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	handler, err := imagegen.NewHTTPHandler(engine)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: cfg.HTTP.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() {
		slog.Info("HTTP MCP listening", "address", cfg.HTTP.Listen, "endpoint", "/mcp")
		done <- srv.Serve(listener)
	}()
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		err = srv.Shutdown(shutdownCtx)
		if err != nil {
			_ = srv.Close()
		}
		return err
	}
}
