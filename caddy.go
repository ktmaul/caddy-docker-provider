package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

const CADDY_API_VARIABLE = "CADDY_API_URL"

type CaddyClientImpl struct {
	serverName string
	serverUrl  string
}

type CaddyClient interface {
	CreateRoute(ctx context.Context, route RouteEntry) error
	RemoveRoute(ctx context.Context, route RouteEntry) error
}

func NewCaddyClient() (CaddyClient, error) {
	caddy := CaddyClientImpl{}
	caddy.serverUrl = os.Getenv(CADDY_API_VARIABLE)

	if caddy.serverUrl == "" {
		return nil, fmt.Errorf("%s is not set", CADDY_API_VARIABLE)
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

	slog.Debug(fmt.Sprintf("Caddy url set to %s", caddy.serverUrl))
	slog.Debug(fmt.Sprintf("Caddy server set to %s", caddy.serverName))

	return &caddy, nil
}

func (caddy *CaddyClientImpl) createHttpRequest(method string, relativeUrl string, requestBody map[string]interface{}) (*http.Request, error) {
	url := strings.TrimRight(caddy.serverUrl, "/") + "/" + strings.TrimLeft(relativeUrl, "/")

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

func (caddy *CaddyClientImpl) executeHttpRequest(ctx context.Context, method string, relativeUrl string, requestBody map[string]interface{}) (map[string]interface{}, error) {
	req, err := caddy.createHttpRequest(method, relativeUrl, requestBody)
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

func (caddy *CaddyClientImpl) convertToJson(entry RouteEntry) map[string]interface{} {
	match := map[string]interface{}{
		"host": entry.Hosts,
	}

	upstream := map[string]interface{}{}
	port := entry.Port
	if port == "" {
		port = "80"
	}

	dial := ""
	if entry.IP != "" {
		dial = fmt.Sprintf("%s:%s", entry.IP, port)
	} else {
		// Use container name for dial (requires Caddy to be on the same network)
		dial = fmt.Sprintf("%s:%s", entry.ContainerID[:12], port)
	}
	upstream["dial"] = dial

	handle := map[string]interface{}{
		"handler": "reverse_proxy",
		"upstreams": []map[string]interface{}{
			upstream,
		},
	}

	route := map[string]interface{}{
		"@id":    entry.ContainerID,
		"match":  []map[string]interface{}{match},
		"handle": []map[string]interface{}{handle},
	}

	return route
}

func (caddy *CaddyClientImpl) CreateRoute(ctx context.Context, route RouteEntry) error {
	body := caddy.convertToJson(route)

	slog.Debug("Sending route to caddy", slog.Any("json", body))

	// POST to /config/ to add a route
	_, err := caddy.executeHttpRequest(ctx, "POST", fmt.Sprintf("/config/apps/http/servers/%s/routes", caddy.serverName), body)
	if err == nil {
		slog.Info("Added route", slog.String("containerId", route.ContainerID))
		return nil
	}

	return err
}

func (caddy *CaddyClientImpl) RemoveRoute(ctx context.Context, route RouteEntry) error {
	// DELETE to the @id endpoint
	_, err := caddy.executeHttpRequest(ctx, "DELETE", fmt.Sprintf("/id/%s", route.ContainerID), nil)
	if err == nil {
		slog.Info("Removed route", slog.String("containerId", route.ContainerID))
		return nil
	}

	return err
}
