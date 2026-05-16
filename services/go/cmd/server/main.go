// Package main is the entry point for the MongoDB drivers load-test HTTP
// service. It wires up the *mongo.Client, registers the four /v1 endpoints,
// and listens on :8080 (overridable via the PORT env var).
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/mongodb-labs/mongo-drivers-benchmark/services/go/internal/api"
)

const driverVersion = "v2.6.0"

func main() {
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		log.Fatalf("MONGODB_URI is required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		log.Fatalf("mongo.Connect: %v", err)
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		log.Printf("startup ping failed (continuing): %v", err)
	}

	srv := api.NewServer(client, api.Info{
		Driver:          "mongo-go-driver",
		DriverVersion:   driverVersion,
		Language:        "go",
		LanguageVersion: runtime.Version(),
		SpecVersion:     "1.0.0",
	})

	httpSrv := &http.Server{
		Addr:              net.JoinHostPort("", port),
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		log.Printf("shutting down")
		shutCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = httpSrv.Shutdown(shutCtx)
		_ = client.Disconnect(shutCtx)
	}()

	log.Printf("listening on :%s", port)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("ListenAndServe: %v", err)
	}
}
