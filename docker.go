package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

var (
	LabelPrefix       = getenv("CADDY_LABEL_PREFIX", "caddy")
	LabelServer       = fmt.Sprintf("%s.server", LabelPrefix)
	LabelEnable       = fmt.Sprintf("%s.enable", LabelPrefix)
	LabelNetwork      = fmt.Sprintf("%s.network", LabelPrefix)
	LabelRoutes       = fmt.Sprintf("%s.routes", LabelPrefix)
	LabelHost         = fmt.Sprintf("%s.routes.%s.hosts", LabelPrefix)
	LabelUpstreamPort = fmt.Sprintf("%s.routes.%s.upstream.port", LabelPrefix)
)

var runningFilters = client.Filters{}.
	Add("status", "running"). // container.State.Status
	Add("health", string(container.Healthy), string(container.NoHealthcheck))

var serverFilters = runningFilters.Clone().
	Add("label", fmt.Sprintf("%s=true", LabelServer))

var upstreamFilters = runningFilters.Clone().
	Add("label", fmt.Sprintf("%s=true", LabelEnable))

type DockerClientImpl struct {
	cli              *client.Client
	caddyContainerID string
	caddyNetworkID   string
}

type DockerClient interface {
	ConnectToNetwork(ctx context.Context, networkId string) error
	DisconnectFromNetwork(ctx context.Context, networkId string) error
	GetContainerRoutes(ctx context.Context, containerId string) (map[string]RouteEntry, error)
	GetRoutableContainers(ctx context.Context) ([]string, error)
	Listen(ctx context.Context) (<-chan events.Message, <-chan error)
}

func NewDockerClient() (DockerClient, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("Could not connect to docker: %w", err)
	}

	docker := DockerClientImpl{
		cli: cli,
	}

	if err = docker.findCaddyContainer(); err != nil {
		return nil, err
	}

	return &docker, nil
}

func (docker *DockerClientImpl) findCaddyContainer() error {
	ctx := context.Background()

	resp, err := docker.cli.ContainerList(ctx, client.ContainerListOptions{Filters: serverFilters})
	if err != nil {
		return err
	}

	if len(resp.Items) == 0 {
		return fmt.Errorf("Caddy container not found: no containers match %v", serverFilters)
	}

	docker.caddyContainerID = resp.Items[0].ID
	return nil
}

func (docker *DockerClientImpl) isRoutableContainer(labels map[string]string) bool {
	enabled, exists := labels[LabelEnable]
	if !exists || (strings.ToLower(enabled) != "true" && enabled != "1") {
		return false
	}

	return true
}

func (docker *DockerClientImpl) ConnectToNetwork(ctx context.Context, networkID string) error {
	// If this is caddy's network, nothing to do
	if networkID == docker.caddyNetworkID {
		return nil
	}

	// Check if the network exists and if caddy is already connected
	resp, err := docker.cli.NetworkInspect(ctx, networkID, client.NetworkInspectOptions{})
	if err != nil {
		// network might not be by name (could be ID). Try list networks and match prefix
		return fmt.Errorf("Network %s not found: %w", networkID, err)
	}

	// If already connected, nothing to do
	if resp.Network.Containers != nil {
		for cont := range resp.Network.Containers {
			if cont == docker.caddyContainerID {
				slog.Debug("Caddy is already connected to network", slog.String("networkId", networkID))
				return nil
			}
		}
	}

	// Connect caddy
	_, err = docker.cli.NetworkConnect(ctx, networkID, client.NetworkConnectOptions{Container: docker.caddyContainerID})
	if err != nil {
		return err
	}

	slog.Info("Connected caddy to network", slog.String("networkId", networkID))
	return nil
}

func (docker *DockerClientImpl) DisconnectFromNetwork(ctx context.Context, networkID string) error {
	// Don't disconnect from caddy's original network
	if networkID == docker.caddyNetworkID {
		return nil
	}

	resp, err := docker.cli.NetworkInspect(ctx, networkID, client.NetworkInspectOptions{})
	if err != nil {
		return fmt.Errorf("Network %s not found: %w", networkID, err)
	}

	// Only disconnect if already connected
	if resp.Network.Containers != nil {
		found := false
		for cont := range resp.Network.Containers {
			if cont == docker.caddyContainerID {
				found = true
			}
		}
		if !found {
			slog.Debug("Caddy already disconnected from network", slog.String("networkId", networkID))
			return nil
		}
	}

	// Force disconnect (true)
	_, err = docker.cli.NetworkDisconnect(ctx, networkID, client.NetworkDisconnectOptions{Container: docker.caddyContainerID, Force: true})
	if err != nil {
		return err
	}

	slog.Info("Disconnected caddy from network", slog.String("networkId", networkID))
	return nil
}

func (docker *DockerClientImpl) GetContainerRoutes(ctx context.Context, containerID string) (map[string]RouteEntry, error) {
	resp, err := docker.cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{Size: false})
	if err != nil {
		return nil, err
	}

	// Make sure this container is enabled
	labels := resp.Container.Config.Labels
	if !docker.isRoutableContainer(labels) {
		return nil, fmt.Errorf("Container %s is not enabled %w", containerID, labels)
	}

	// Get the name of the network to use
	networkName := labels[LabelNetwork]
	if networkName == "" {
		// Get the first network in the list
		for name := range resp.Container.NetworkSettings.Networks {
			networkName = name
			break
		}
	}

	// Get the IP on the target network
	ip := containerID
	network, exists := resp.Container.NetworkSettings.Networks[networkName]
	if exists {
		ip = network.IPAddress.String()
	}

	port := "80"
	// Get the first exposed port
	for p := range resp.Container.Config.ExposedPorts {
		port = p.Port()
		break
	}

	routes := make(map[string]RouteEntry)
	for label, value := range labels {
		suffix, exists := strings.CutPrefix(label, LabelRoutes+".")
		if !exists {
			continue
		}

		parts := strings.Split(suffix, ".")
		routeID := parts[0]

		route, exists := routes[routeID]
		if !exists {
			route = RouteEntry{
				RouteID:   routeID,
				Upstreams: make(map[string]RouteUpstream),
			}
		}

		upstream, exists := route.Upstreams[resp.Container.ID]
		if !exists {
			upstream = RouteUpstream{
				ContainerID: resp.Container.ID,
				Network:     networkName,
				IP:          ip,
				Port:        port,
			}
		}

		// Apply the label value to the appropriate construct
		subSection := parts[1]
		switch subSection {
		case "hosts":
			route.Hosts = strings.Split(value, " ")
		case "paths":
			route.Paths = strings.Split(value, " ")
		case "upstream":
			upstream.Port = value
		}

		// Replace the values (as these are not references)
		route.Upstreams[resp.Container.ID] = upstream
		routes[routeID] = route
	}

	slog.Debug("Parsed container routes", slog.Any("routes", routes))
	return routes, nil
}

func (docker *DockerClientImpl) GetRoutableContainers(ctx context.Context) ([]string, error) {
	var containerIDs []string
	resp, err := docker.cli.ContainerList(ctx, client.ContainerListOptions{Filters: upstreamFilters})
	if err != nil {
		return containerIDs, err
	}

	if len(resp.Items) == 0 {
		slog.Debug("No routes found on existing containers")
		return containerIDs, nil
	}

	for _, item := range resp.Items {
		if !docker.isRoutableContainer(item.Labels) {
			slog.Debug("Container is not routable, skipping", slog.String("containerID", item.ID))
			continue
		}

		containerIDs = append(containerIDs, item.ID)
	}

	return containerIDs, nil
}

func (docker *DockerClientImpl) Listen(ctx context.Context) (<-chan events.Message, <-chan error) {
	// defer docker.cli.Close()

	// Listen container events
	filters := client.Filters{}
	filters.Add("type", "container")

	// Listen for start and die/destroy
	filters.Add("event", "start")
	filters.Add("event", "die")
	filters.Add("event", "destroy")

	options := client.EventsListOptions{Filters: filters}
	events := docker.cli.Events(ctx, options)

	return events.Messages, events.Err
}
