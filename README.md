caddy-event-watcher

What this does
- Listens for Docker container events (start/stop/destroy).
- Reads labels on containers to build reverse-proxy routes for Caddy.
- Connects/disconnects the Caddy container to the container's Docker networks so Caddy can reach the target container.
- Pushes an HTTP JSON config to the Caddy Admin API.

Label convention (defaults)
- caddy.enable=true        -- enable automatic route creation
- caddy.host=example.com   -- hostname Caddy should match
- caddy.port=80            -- target port inside the container (default 80)
- caddy.network=net_name   -- optional, preferred Docker network to use (if not given the first network is used)

Environment variables
- CADDY_API (default http://caddy:2019) - URL to Caddy admin API
- CADDY_CONTAINER_LABEL (default "caddy=true") - label to find the Caddy container
- CADDY_CONTAINER_NAME - optional container name for Caddy lookup (overrides label search)

How to run locally (developer)
Build:
  docker build -t caddy-watcher:local .

Run (needs docker socket):
  docker run -v /var/run/docker.sock:/var/run/docker.sock \
    -e CADDY_API="http://caddy:2019" \
    -e CADDY_CONTAINER_LABEL="caddy=true" \
    caddy-watcher:local

Notes and caveats
- The container must be able to reach the Docker daemon: mount /var/run/docker.sock.
- The program connects/disconnects the Caddy container to networks using Docker NetworkConnect/NetworkDisconnect — inspired by the approach in obeone/traefik_network_connector.
- The program pushes a full minimal HTTP app config JSON to the Caddy Admin API at /config/ (PUT). The JSON shape is compatible with Caddy's apps/http servers configuration (simple route list).
- You may want to add retries, error handling, or preserve existing Caddy configuration (this example replaces the server config each time).
- For production use: secure the Docker socket and the Caddy admin endpoint; consider using the Docker API over TLS, and use Caddy's admin local socket or TLS-protected admin API.