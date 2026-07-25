# Home Assistant add-on image for the Go tethered host (also usable via
# docker-compose). Build context = repository root (so tether/ is available):
#   docker build -f addon/Dockerfile -t zbsniff .
# HA Supervisor sets BUILD_FROM per-arch and builds with buildx (TARGET* set).

# --- build stage: compile the static Go binary ---------------------------------
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY tether/go.mod tether/go.sum ./
RUN go mod download
COPY tether/ ./
# CGO off → a fully static binary (the SQLite driver is pure Go).
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.hostVersion=1.0.0" \
    -o /zbsniff ./cmd/zbsniff

# --- runtime stage: HA base image ----------------------------------------------
ARG BUILD_FROM=ghcr.io/home-assistant/amd64-base:3.19
FROM ${BUILD_FROM}
RUN apk add --no-cache jq
COPY --from=build /zbsniff /usr/bin/zbsniff
COPY addon/run.sh /run.sh
RUN chmod +x /run.sh
EXPOSE 8080
CMD ["/run.sh"]
