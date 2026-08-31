# syntax=docker/dockerfile:1

# Control Center: frontend + daemon in one image. Bind-mount /data to keep the
# database, blobs, master key, and first-run password across restarts.

FROM node:22-bookworm AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-bookworm AS go
WORKDIR /src
ENV GOTOOLCHAIN=local
COPY go.work go.work.sum go.mod go.sum ./
COPY host/ ./host/
COPY plugins/hello/ ./plugins/hello/
COPY plugins/pagewatch/ ./plugins/pagewatch/
COPY plugins/tid/ ./plugins/tid/
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY config/ ./config/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/controlcenter ./cmd/controlcenter

FROM debian:bookworm-slim
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates openssl \
	&& rm -rf /var/lib/apt/lists/* \
	&& useradd --system --home /data --uid 10001 cc
COPY --from=go /out/controlcenter /usr/local/bin/controlcenter
COPY --from=web /src/web/dist /app/web
COPY config/config.example.yaml /app/config/config.yaml
COPY config/models.yaml /app/config/models.yaml
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN sed -i 's/\r$//' /usr/local/bin/docker-entrypoint.sh \
	&& chmod 0755 /usr/local/bin/docker-entrypoint.sh \
	&& mkdir -p /data \
	&& chown cc:cc /data
USER cc
WORKDIR /app
ENV CC_DATA_DIR=/data
EXPOSE 8443
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
