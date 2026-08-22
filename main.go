package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/moby/moby/api/types/events"
)

type CaddyManager struct {
	caddy  CaddyClient
	docker DockerClient
	state  StateBag
}

func getenv(k, d string) string {
	v := os.Getenv(k)
	if v == "" {
		return d
	}
	return v
}

func (manager *CaddyManager) handleContainerStart(ctx context.Context, containerId string) error {
	routes, err := manager.docker.GetContainerRoutes(ctx, containerId)
	if err != nil {
		return err
	}

	for _, entry := range routes {
		err = manager.caddy.CreateRoute(ctx, entry)
		if err != nil {
			return err
		}

		err = manager.docker.ConnectToNetwork(ctx, entry.Network)
		if err != nil {
			return err
		}

		manager.state.AddRoute(entry)
	}

	return nil
}

func (manager *CaddyManager) handleContainerStop(ctx context.Context, containerId string) error {
	route := manager.state.FindByContainer(containerId)
	if route == nil {
		return nil // Do nothing (this often happens with destroy events)
	}

	manager.state.RemoveRoute(*route)

	err := manager.caddy.RemoveRoute(ctx, *route)
	if err != nil {
		return err
	}

	if manager.state.HasNetworkRoutes(route.Network) {
		err = manager.docker.DisconnectFromNetwork(ctx, route.Network)
		if err != nil {
			return err
		}
	}

	return nil
}

func (manager *CaddyManager) handleEvent(ctx context.Context, ev events.Message) {
	id := ev.Actor.ID
	action := ev.Action
	slog.Debug("Docker event received", slog.String("actor", id), "action", action)

	switch action {
	case "start":
		if err := manager.handleContainerStart(ctx, id); err != nil {
			slog.Error(err.Error())
		}
	case "die", "destroy":
		if err := manager.handleContainerStop(ctx, id); err != nil {
			slog.Error(err.Error())
		}
	default:
		// ignore
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	// Set log level
	var level slog.Level
	err := level.UnmarshalText([]byte(os.Getenv("LOG_LEVEL")))
	if err == nil {
		slog.SetLogLoggerLevel(level)
	}

	// Handle shutdown signals
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigc
		slog.Info("Shutting down gracefully...")
		cancel()
	}()

	// Initialize our application state
	app := CaddyManager{
		state: NewStateBag(),
	}

	duration, _ := time.ParseDuration("30s")

	for {
		if app.caddy == nil {
			caddy, err := NewCaddyClient()
			if err != nil {
				slog.Error("Failed to connect to caddy", slog.String("error", err.Error()))
			}

			app.caddy = caddy
		}

		if app.docker == nil {
			docker, err := NewDockerClient()
			if err != nil {
				slog.Error("Failed to connect to docker", slog.String("error", err.Error()))
			}

			app.docker = docker
		}

		slog.Info("Getting upstream containers...")
		routes, err := app.docker.GetUpstreamContainers(ctx)
		if err != nil {
			slog.Error("Failed to get upstream containers", slog.String("error", err.Error()))
		}

		// Add any new routes to caddy and connect to networks
		if len(routes) > 0 {
			for _, route := range routes {
				err = app.caddy.CreateRoute(ctx, route)
				if err != nil {
					slog.Error("Failed to create route in caddy", slog.String("error", err.Error()))
					break
				}

				err = app.docker.ConnectToNetwork(ctx, route.Network)
				if err != nil {
					slog.Error("Failed to connect to network", slog.String("error", err.Error()))
					break
				}

				app.state.AddRoute(route)
			}
		}

	ListenEvents:
		for {
			eventChan, errChan := app.docker.Listen(ctx)

			select {
			case event := <-eventChan:
				go app.handleEvent(ctx, event)
			case err := <-errChan:
				if !errors.Is(err, context.Canceled) {
					slog.Error("Error listening to events", slog.String("error", err.Error()))
				}
				break ListenEvents
			}
		}

		select {
		case <-time.After(duration):
			slog.Debug("Attempting to reconnect after timeout", slog.String("duration", duration.String()))
		case <-ctx.Done():
			return
		}
	}
}
