# syntax=docker/dockerfile:1

# --- Frontend: compile the SvelteKit app to static files ---------------------
FROM node:26-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
# --ignore-scripts: no install hooks from dependencies run during the build.
RUN npm ci --ignore-scripts
COPY web/ ./
RUN npm run build

# --- Backend: build one static Go binary with the frontend embedded ----------
FROM golang:1.27-alpine AS go
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
COPY web/embed.go web/
COPY --from=web /src/web/build web/build
ARG VERSION=dev
RUN go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/platform ./cmd/platform

# --- Runtime: distroless = no shell, no package manager, runs as non-root ----
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=go /out/platform /platform
ENV HTTP_ADDR=:8080
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=3 CMD ["/platform", "healthcheck"]
ENTRYPOINT ["/platform"]
