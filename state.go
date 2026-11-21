package main

import (
	"sync"
)

type RouteEntry struct {
	RouteID   string
	Hosts     []string
	Paths     []string
	Upstreams map[string]RouteUpstream
}

type RouteUpstream struct {
	ContainerID string
	Network     string
	IP          string
	Port        string
}

type AppStateImpl struct {
	caddyContainerID string
	caddyNetworkID   string
	routes           map[string]RouteEntry
	mutex            *sync.Mutex
}

type AppState interface {
	AddRoute(route RouteEntry)
	Find(routeID string) (*RouteEntry, bool)
	FindByContainer(containerID string) []RouteEntry
	HasNetworkRoutes(network string) bool
	RemoveRoute(routeID string)
}

func NewStateBag() AppState {
	return &AppStateImpl{
		routes: make(map[string]RouteEntry),
		mutex:  &sync.Mutex{},
	}
}

func (route *RouteEntry) TryAddUpstream(upstream RouteUpstream) bool {
	if route.Upstreams == nil {
		route.Upstreams = make(map[string]RouteUpstream)
	}

	_, exists := route.Upstreams[upstream.ContainerID]
	if exists {
		return false
	}

	route.Upstreams[upstream.ContainerID] = upstream
	return true
}

func (state *AppStateImpl) AddRoute(route RouteEntry) {
	state.mutex.Lock()
	state.routes[route.RouteID] = route
	state.mutex.Unlock()
}

func (state *AppStateImpl) Find(routeID string) (*RouteEntry, bool) {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	route, exists := state.routes[routeID]
	return &route, exists
}

func (state *AppStateImpl) FindByContainer(containerID string) []RouteEntry {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	routes := make([]RouteEntry, 0)
	for _, route := range state.routes {
		for _, upstream := range route.Upstreams {
			if upstream.ContainerID == containerID {
				routes = append(routes, route)
			}
		}
	}

	return routes
}

func (state *AppStateImpl) HasNetworkRoutes(networkID string) bool {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	for _, item := range state.routes {
		for _, upstream := range item.Upstreams {
			if upstream.Network == networkID {
				return true
			}
		}
	}

	return false
}

func (state *AppStateImpl) RemoveRoute(routeID string) {
	state.mutex.Lock()
	delete(state.routes, routeID)
	state.mutex.Unlock()
}
