package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

const CADDY_API_VARIABLE = "CADDY_API_URL"

type CaddyDirective struct {
	ID     string                 `json:"@id"`
	Match  []any                  `json:"match"`
	Handle []CaddyUpstreamHandler `json:"handle"`
}

type HostMatch struct {
	Host []string `json:"host"`
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
	directives map[string]CaddyDirective
	serverName string
	serverURL  string
}

type CaddyClient interface {
	CreateRoute(ctx context.Context, route RouteEntry) error
	RemoveRoute(ctx context.Context, route RouteEntry) error
}

func NewCaddyClient() (CaddyClient, error) {
	caddy := CaddyClientImpl{
		directives: make(map[string]CaddyDirective),
		serverURL:  getenv(CADDY_API_VARIABLE, "http://localhost:2019"),
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

func (caddy *CaddyClientImpl) convertToDirective(entry RouteEntry) CaddyDirective {
	match := HostMatch{
		Host: entry.Hosts,
	}

	port := entry.Port
	if port == "" {
		port = "80"
	}

	host := entry.IP
	if host == "" {
		// Use container name for dial (requires Caddy to be on the same network)
		host = entry.ContainerID[:12]
	}

	handle := CaddyUpstreamHandler{
		Handler: "reverse_proxy",
		Upstreams: []CaddyUpstream{
			{
				ID:   entry.ContainerID,
				Dial: fmt.Sprintf("%s:%s", host, port),
			},
		},
	}

	sort.Strings(match.Host)
	return CaddyDirective{
		ID:     caddy.createHash(strings.Join(match.Host, ",")),
		Match:  []any{match},
		Handle: []CaddyUpstreamHandler{handle},
	}
}

func (caddy *CaddyClientImpl) createHash(value string) string {
	hash := md5.Sum([]byte(value))
	return base64.URLEncoding.EncodeToString(hash[:])
}

func (caddy *CaddyClientImpl) CreateRoute(ctx context.Context, route RouteEntry) error {
	// TODO: A route is defined more by the matcher than the container.
	// Need a way to group routes by matcher and add/remove containers
	// as upstreams. An id will be needed for the matcher and the upstream.
	directive := caddy.convertToDirective(route)

	// TODO: Check if the directive already exists and send just the upstream instead
	if existing, exists := caddy.directives[directive.ID]; exists {
		upstreamExists := false
		for i, upstream := range existing.Handle[0].Upstreams {
			if upstream.ID == route.ContainerID {
				existing.Handle[0].Upstreams[i] = directive.Handle[0].Upstreams[0]
				upstreamExists = true
				break
			}
		}

		// If the upstream doesn't exist, add it to the list
		if !upstreamExists {
			existing.Handle[0].Upstreams = append(existing.Handle[0].Upstreams, directive.Handle[0].Upstreams[0])
		}

		// POST to /config/ to add a route
		_, err := caddy.executeHttpRequest(ctx, "PATCH", fmt.Sprintf("/id/%s", existing.ID), existing)
		if err == nil {
			slog.Info("Added route", slog.String("containerID", route.ContainerID))
			return nil
		}

		return err
	}

	// POST to /config/ to add a route
	_, err := caddy.executeHttpRequest(ctx, "POST", fmt.Sprintf("/config/apps/http/servers/%s/routes", caddy.serverName), directive)
	if err == nil {
		caddy.directives[directive.ID] = directive
		slog.Info("Added route", slog.String("containerID", route.ContainerID))
		return nil
	}

	return err
}

func (caddy *CaddyClientImpl) RemoveRoute(ctx context.Context, route RouteEntry) error {
	// DELETE to the @id endpoint
	_, err := caddy.executeHttpRequest(ctx, "DELETE", fmt.Sprintf("/id/%s", route.ContainerID), nil)
	if err == nil {
		slog.Info("Removed route", slog.String("containerID", route.ContainerID))
		return nil
	}

	return err
}
