package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/moby/moby/api/types/events"
)

type CaddyProvider struct {
	caddy  CaddyClient
	docker DockerClient
	mutex  *sync.Mutex
	state  AppState
}

func getenv(k, d string) string {
	v := os.Getenv(k)
	if v == "" {
		return d
	}
	return v
}

func (app *CaddyProvider) handleContainerStart(ctx context.Context, containerID string) error {
	app.mutex.Lock()
	defer app.mutex.Unlock()

	routes, err := app.docker.GetContainerRoutes(ctx, containerID)
	if err != nil {
		return err
	}

	for _, entry := range routes {
		err = app.caddy.CreateRoute(ctx, entry)
		if err != nil {
			return err
		}

		app.state.AddRoute(entry)

		err = app.docker.ConnectToNetwork(ctx, entry.Network)
		if err != nil {
			return err
		}
	}

	return nil
}

func (app *CaddyProvider) handleContainerStop(ctx context.Context, containerID string) error {
	app.mutex.Lock()
	defer app.mutex.Unlock()

	route := app.state.FindByContainer(containerID)
	if route == nil {
		return nil // Do nothing (this often happens with destroy events)
	}

	err := app.caddy.RemoveRoute(ctx, *route)
	if err != nil {
		return err
	}

	app.state.RemoveRoute(*route)
	slog.Info("Removed route", slog.String("containerID", route.ContainerID))

	if app.state.HasNetworkRoutes(route.Network) {
		err = app.docker.DisconnectFromNetwork(ctx, route.Network)
		if err != nil {
			return err
		}
	}

	return nil
}

func (app *CaddyProvider) handleEvent(ctx context.Context, ev events.Message) {
	id := ev.Actor.ID
	action := ev.Action
	slog.Debug("Docker event received", slog.String("actor", id), "action", action)

	switch action {
	case "start":
		if err := app.handleContainerStart(ctx, id); err != nil {
			slog.Error(err.Error())
		}
	case "die", "destroy":
		if err := app.handleContainerStop(ctx, id); err != nil {
			slog.Error(err.Error())
		}
	default:
		// ignore
	}
}

func (app *CaddyProvider) pollContainers(ctx context.Context) {
	slog.Info("Checking running containers...")
	routes, err := app.docker.GetUpstreamContainers(ctx)
	if err != nil {
		slog.Error("Failed to get upstream containers", slog.String("error", err.Error()))
	}

	// Add any new routes to caddy and connect to networks
	if len(routes) > 0 {
		app.mutex.Lock()

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
			slog.Info("Added route",
				slog.String("hosts", strings.Join(route.Hosts, ", ")),
				slog.String("containerID", route.ContainerID),
				slog.String("networkID", route.Network),
				slog.String("ip", route.IP),
				slog.String("port", route.Port))
		}

		app.mutex.Unlock()
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
	app := CaddyProvider{
		mutex: &sync.Mutex{},
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

		go app.pollContainers(ctx)

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
