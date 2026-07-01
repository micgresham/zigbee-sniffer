# Home Assistant add-on image (also usable via docker-compose).
# Build context = repository root (so host/ and frontend/dist are available).
#   docker build -f addon/Dockerfile -t zbsniff .
ARG BUILD_FROM=ghcr.io/home-assistant/amd64-base-python:3.12
FROM ${BUILD_FROM}

ENV LANG=C.UTF-8 PYTHONUNBUFFERED=1

# Backend
COPY host/ /app/host/
RUN pip install --no-cache-dir -e "/app/host[host]"

# Built React UI (optional; served at / when present). Build it first with:
#   cd frontend && npm ci && npm run build
COPY frontend/dist/ /app/frontend/dist/

# Add-on entrypoint reads HA options and starts the server.
COPY addon/run.sh /run.sh
RUN chmod +x /run.sh

EXPOSE 8080
CMD ["/run.sh"]
