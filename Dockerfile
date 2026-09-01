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
COPY plugins/bidrl/ ./plugins/bidrl/
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY config/ ./config/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/controlcenter ./cmd/controlcenter

FROM debian:bookworm-slim
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates openssl \
		fonts-liberation libasound2 libatk-bridge2.0-0 libatk1.0-0 libatspi2.0-0 \
		libcairo2 libcups2 libdbus-1-3 libdrm2 libgbm1 libnspr4 libnss3 \
		libpango-1.0-0 libx11-6 libxcomposite1 libxdamage1 libxext6 libxfixes3 \
		libxkbcommon0 libxrandr2 \
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
# Real pages need a real browser: the fake engine serves in-process fixtures only.
# Chromium is downloaded on first start into the /data volume, which the
# unprivileged user owns, so it is fetched once and survives restarts.
ENV CC_BROWSER_ENGINE=playwright
ENV PLAYWRIGHT_BROWSERS_PATH=/data/.playwright
EXPOSE 8443
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
