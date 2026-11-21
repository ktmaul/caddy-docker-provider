package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
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

func (app *CaddyProvider) handleContainerStart(ctx context.Context, containerID string) {
	app.mutex.Lock()
	defer app.mutex.Unlock()

	routes, err := app.docker.GetContainerRoutes(ctx, containerID)
	if err != nil {
		slog.Error(err.Error())
		return
	}

	for _, pending := range routes {
		route, exists := app.state.Find(pending.RouteID)

		// If the route doesn't exist, create it and add it to the state
		if !exists {
			app.caddy.CreateRoute(ctx, pending)
			app.state.AddRoute(pending)
			slog.Info("Added route", slog.String("routeID", pending.RouteID), slog.Any("hosts", pending.Hosts), slog.Any("upstreams", pending.Upstreams))
			continue
		}

		// Otherwise, add any new upstreams to the existing route and connect to the network
		for _, upstream := range pending.Upstreams {
			if !route.TryAddUpstream(upstream) {
				continue
			}

			err = app.caddy.CreateUpstream(ctx, route.RouteID, upstream)
			if err != nil {
				slog.Error(err.Error())
				continue
			}

			err = app.docker.ConnectToNetwork(ctx, upstream.Network)
			if err != nil {
				slog.Error(err.Error())
				continue
			}

			slog.Info("Added upstream",
				slog.String("routeID", route.RouteID),
				slog.String("containerID", upstream.ContainerID),
				slog.String("network", upstream.Network),
				slog.String("ip", upstream.IP),
				slog.String("port", upstream.Port))
		}
	}
}

func (app *CaddyProvider) handleContainerStop(ctx context.Context, containerID string) {
	app.mutex.Lock()
	defer app.mutex.Unlock()

	routes := app.state.FindByContainer(containerID)
	if len(routes) == 0 {
		return // Do nothing (this often happens with destroy events)
	}

	var networks []string
	for _, route := range routes {
		// Remove the upstreams for this container
		for upstreamID, upstream := range route.Upstreams {
			if upstream.ContainerID != containerID {
				continue
			}

			err := app.caddy.RemoveUpstream(ctx, route.RouteID, upstreamID)
			if err != nil {
				slog.Error(err.Error())
				continue
			}

			networks = append(networks, upstream.Network)
			delete(route.Upstreams, upstreamID)
			slog.Info("Removed upstream", slog.String("routeID", route.RouteID), slog.String("containerID", upstream.ContainerID))
		}

		// If there are no more upstreams for this route, remove the route from caddy and the state
		if len(route.Upstreams) == 0 {
			err := app.caddy.RemoveRoute(ctx, route.RouteID)
			if err != nil {
				slog.Error(err.Error())
			}

			app.state.RemoveRoute(route.RouteID)
			slog.Info("Removed route", slog.String("routeID", route.RouteID))
		}
	}

	for _, network := range networks {
		if !app.state.HasNetworkRoutes(network) {
			err := app.docker.DisconnectFromNetwork(ctx, network)
			if err != nil {
				slog.Error(err.Error())
			}
		}
	}
}

func (app *CaddyProvider) handleEvent(ctx context.Context, ev events.Message) {
	id := ev.Actor.ID
	action := ev.Action
	slog.Debug("Docker event received", slog.String("actor", id), "action", action)

	switch action {
	case "start":
		app.handleContainerStart(ctx, id)
	case "die", "destroy":
		app.handleContainerStop(ctx, id)
	default:
		// ignore
	}
}

func (app *CaddyProvider) pollContainers(ctx context.Context) {
	slog.Info("Checking running containers...")
	containerIDs, err := app.docker.GetRoutableContainers(ctx)
	if err != nil {
		slog.Error("Failed to get routable containers", slog.String("error", err.Error()))
	}

	// Add any new routes to caddy and connect to networks
	if len(containerIDs) > 0 {
		for _, containerID := range containerIDs {
			app.handleContainerStart(ctx, containerID)
		}
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

		if app.caddy == nil || app.docker == nil {
			select {
			case <-time.After(duration):
				slog.Debug("Attempting to reconnect after timeout", slog.String("duration", duration.String()))
				continue
			case <-ctx.Done():
				return
			}
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
			case <-ctx.Done():
				return
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
