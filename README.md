# Caddy Docker Provider

A simple Docker event listener that automatically configures Caddy reverse-proxy routes based on container labels. 
Designed to run alongside a Caddy container in the same Docker network.

## Installation

The container must be able to reach the Docker daemon: mount /var/run/docker.sock

### Environment variables
- CADDY_API (default http://caddy:2019) - URL to Caddy admin API
- CADDY_CONTAINER_LABEL (default "caddy=true") - label to find the Caddy container
- CADDY_CONTAINER_NAME - optional container name for Caddy lookup (overrides label search)

## Usage

### Label conventions
- caddy.enable=true        -- enable automatic route creation
- caddy.host=example.com   -- hostname Caddy should match
- caddy.port=80            -- target port inside the container (default 80)
- caddy.network=net_name   -- optional, preferred Docker network to use (if not given the first network is used)
- caddy.upstream.port=port   -- optional, preferred upstream port to use (if not given the first port is used)