package main

import (
	"sync"
)

type RouteEntry struct {
	ContainerID string
	Hosts       []string
	Network     string
	Port        string
	IP          string
}

type AppStateImpl struct {
	caddyContainerID string
	caddyNetworkID   string
	routes           []RouteEntry
	mutex            *sync.Mutex
}

type AppState interface {
	AddRoute(entry RouteEntry)
	FindByContainer(containerId string) *RouteEntry
	HasNetworkRoutes(network string) bool
	RemoveRoute(entry RouteEntry)
}

func NewStateBag() AppState {
	return &AppStateImpl{
		routes: []RouteEntry{},
		mutex:  &sync.Mutex{},
	}
}

func (state *AppStateImpl) AddRoute(entry RouteEntry) {
	state.mutex.Lock()
	state.routes = append(state.routes, entry)
	state.mutex.Unlock()
}

func (state *AppStateImpl) FindByContainer(containerID string) *RouteEntry {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	for _, item := range state.routes {
		if item.ContainerID == containerID {
			return &item
		}
	}

	return nil
}

func (state *AppStateImpl) HasNetworkRoutes(networkID string) bool {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	for _, item := range state.routes {
		if item.Network == networkID {
			return true
		}
	}

	return false
}

func (state *AppStateImpl) RemoveRoute(entry RouteEntry) {
	state.mutex.Lock()

	for i, item := range state.routes {
		if item.ContainerID == entry.ContainerID {
			state.routes = append(state.routes[:i], state.routes[i+1:]...)
		}
	}

	state.mutex.Unlock()
}
