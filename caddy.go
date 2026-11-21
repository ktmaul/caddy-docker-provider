package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
)

const CADDY_API_VARIABLE = "CADDY_API_URL"

type CaddyRoute struct {
	ID     string                 `json:"@id"`
	Match  []any                  `json:"match"`
	Handle []CaddyUpstreamHandler `json:"handle"`
}

type HostMatch struct {
	Host []string `json:"host"`
}

type PathMatch struct {
	Path []string `json:"path"`
}

type CaddyUpstreamHandler struct {
	Handler   string          `json:"handler"`
	Upstreams []CaddyUpstream `json:"upstreams"`
}

type CaddyUpstream struct {
	ID   string `json:"@id"`
	Dial string `json:"dial"`
}

type CaddyClientImpl struct {
	routes     []string
	serverName string
	serverURL  string
}

type CaddyClient interface {
	CreateRoute(ctx context.Context, route RouteEntry) error
	CreateUpstream(ctx context.Context, routeID string, upstream RouteUpstream) error
	RemoveRoute(ctx context.Context, routeID string) error
	RemoveUpstream(ctx context.Context, routeID string, upstreamID string) error
}

func NewCaddyClient() (CaddyClient, error) {
	caddy := CaddyClientImpl{
		serverURL: getenv(CADDY_API_VARIABLE, "http://localhost:2019"),
	}

	response, err := caddy.executeHttpRequest(context.Background(), "GET", "/config/apps/http/servers", nil)
	if err != nil {
		return nil, err
	}

	// Take the first server name supplied
	for k := range response {
		caddy.serverName = k
		break
	}

	if caddy.serverName == "" {
		// Possibly need to create a server?
		return nil, fmt.Errorf("No servers allocated for caddy")
	}

	slog.Debug(fmt.Sprintf("Caddy url set to %s", caddy.serverURL))
	slog.Debug(fmt.Sprintf("Caddy server set to %s", caddy.serverName))

	return &caddy, nil
}

func (caddy *CaddyClientImpl) createHttpRequest(method string, relativeURL string, requestBody any) (*http.Request, error) {
	url := strings.TrimRight(caddy.serverURL, "/") + "/" + strings.TrimLeft(relativeURL, "/")

	var body []byte
	if requestBody != nil {
		json, err := json.Marshal(requestBody)
		if err != nil {
			return nil, err
		}
		body = json
	}

	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return req, err
	}

	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (caddy *CaddyClientImpl) executeHttpRequest(ctx context.Context, method string, relativeURL string, requestBody any) (map[string]interface{}, error) {
	slog.Debug("Sending API request", slog.String("method", method), slog.String("relativeURL", relativeURL), slog.Any("requestBody", requestBody))

	req, err := caddy.createHttpRequest(method, relativeURL, requestBody)
	if err != nil {
		return nil, err
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API request failed with status %s", resp.Status)
	}

	var response map[string]interface{}
	if resp.ContentLength > 0 {
		err = json.NewDecoder(resp.Body).Decode(&response)
	}

	return response, err
}

func (caddy *CaddyClientImpl) convertRoute(route RouteEntry) CaddyRoute {
	var match any
	if len(route.Paths) > 0 {
		match = PathMatch{
			Path: route.Paths,
		}
	}

	if len(route.Hosts) > 0 {
		match = HostMatch{
			Host: route.Hosts,
		}
	}

	handle := CaddyUpstreamHandler{
		Handler:   "reverse_proxy",
		Upstreams: make([]CaddyUpstream, 0),
	}

	for _, upstream := range route.Upstreams {
		handle.Upstreams = append(handle.Upstreams, caddy.convertUpstream(route.RouteID, upstream))
	}

	return CaddyRoute{
		ID:     route.RouteID,
		Match:  []any{match},
		Handle: []CaddyUpstreamHandler{handle},
	}
}

func (caddy *CaddyClientImpl) convertUpstream(routeID string, upstream RouteUpstream) CaddyUpstream {
	return CaddyUpstream{
		ID:   fmt.Sprintf("%s-%s", routeID, upstream.ContainerID),
		Dial: fmt.Sprintf("%s:%s", upstream.IP, upstream.Port),
	}
}

func (caddy *CaddyClientImpl) CreateRoute(ctx context.Context, route RouteEntry) error {
	value := caddy.convertRoute(route)

	if slices.Contains(caddy.routes, route.RouteID) {
		_, err := caddy.executeHttpRequest(ctx, "PUT", fmt.Sprintf("/id/%s", value.ID), value)
		if err == nil {
			return nil
		}
	}

	_, err := caddy.executeHttpRequest(ctx, "POST", fmt.Sprintf("/config/apps/http/servers/%s/routes", caddy.serverName), value)
	if err == nil {
		caddy.routes = append(caddy.routes, route.RouteID)
		return nil
	}

	return err
}

func (caddy *CaddyClientImpl) CreateUpstream(ctx context.Context, routeID string, upstream RouteUpstream) error {
	value := caddy.convertUpstream(routeID, upstream)

	_, err := caddy.executeHttpRequest(ctx, "POST", fmt.Sprintf("/id/%s/handle/0/upstreams", routeID), value)
	if err == nil {
		return nil
	}

	return err
}

func (caddy *CaddyClientImpl) RemoveRoute(ctx context.Context, routeID string) error {
	_, err := caddy.executeHttpRequest(ctx, "DELETE", fmt.Sprintf("/id/%s", routeID), nil)
	if err == nil {
		return nil
	}

	return err
}

func (caddy *CaddyClientImpl) RemoveUpstream(ctx context.Context, routeID string, upstreamID string) error {
	_, err := caddy.executeHttpRequest(ctx, "DELETE", fmt.Sprintf("/id/%s", fmt.Sprintf("%s-%s", routeID, upstreamID)), nil)
	if err == nil {
		return nil
	}

	return err
}
