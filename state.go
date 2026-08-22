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

type State struct {
	routes []RouteEntry
	mutex  sync.Mutex
}

type StateBag interface {
	AddRoute(entry RouteEntry)
	FindByContainer(containerId string) *RouteEntry
	HasNetworkRoutes(network string) bool
	RemoveRoute(entry RouteEntry)
}

func NewStateBag() StateBag {
	return &State{routes: []RouteEntry{}}
}

func (state *State) AddRoute(entry RouteEntry) {
	state.mutex.Lock()
	state.routes = append(state.routes, entry)
	state.mutex.Unlock()
}

func (state *State) FindByContainer(containerId string) *RouteEntry {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	for _, item := range state.routes {
		if item.ContainerID == containerId {
			return &item
		}
	}

	return nil
}

func (state *State) HasNetworkRoutes(network string) bool {
	state.mutex.Lock()
	defer state.mutex.Unlock()

	for _, item := range state.routes {
		if item.Network == network {
			return true
		}
	}

	return false
}

func (state *State) RemoveRoute(entry RouteEntry) {
	state.mutex.Lock()

	for i, item := range state.routes {
		if item.ContainerID == entry.ContainerID {
			state.routes = append(state.routes[:i], state.routes[i+1:]...)
		}
	}

	state.mutex.Unlock()
}
