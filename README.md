# Caddy Docker Provider

A simple Docker event listener that automatically configures Caddy reverse-proxy routes based on container labels. 
Designed to run alongside a Caddy container in the same Docker network.

## Installation

The container must be able to reach the Docker daemon: mount /var/run/docker.sock

### Environment variables
| Variable | Default | Notes |
| -------- | ------- | ----- |
| CADDY_API | http://localhost:2019 | URL to Caddy admin API |
| CADDY_LABEL_PREFIX | caddy | The label prefix used for route discovery |
| LOG_LEVEL | INFO | The log verbosity. Valid values are ERROR, WARN, INFO, and DEBUG |

## Usage

### Label conventions
| Label | Notes |
| ----- | ----- |
| caddy.enable | Set to true to enable automatic route creation for the container |
| caddy.network | Optional. The Docker network to use when directing traffic to the container |
| caddy.server | Optional. Identifies the container running caddy to enable auto-attaching to networks specified by the `caddy.network` label  |
| caddy.routes.`{id}`.host | The host matcher used to direct traffic to this container |
| caddy.routes.`{id}`.upstream.port | Optional. The port to use. Defaults to `80` |

### Example
```yaml
services:
  caddy:
    image: caddy
    labels:
      caddy.server: true
    ports:
      - 80:80/tcp
      - 443:443/tcp
      - 443:443/udp
  provider:
    image: ktmaul/caddy-docker-provider
    depends_on:
      - caddy
    environment:
      LOG_LEVEL: DEBUG
    network_mode: service:caddy # This allows us to use localhost and avoid the needlessly complex setup for exposing the caddy API
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
  services:
    image: busybox
    command: httpd -f -v -p 8080
    deploy:
      replicas: 2
    labels:
      caddy.enable: "true"
      caddy.routes.busybox.host: "example.com"
      caddy.routes.busybox.upstream.port: "8080"
```