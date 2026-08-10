# syntax=docker/dockerfile:1

# Build stage.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first, so a source-only change reuses the module cache layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

# CGO is off so the result is a static binary that runs on a distroless base.
# The templates and stylesheet are embedded, so this is the whole application.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/ai-account ./cmd/ai-account

# Runtime stage.
#
# distroless/static gives CA certificates (needed for Entra discovery and token
# exchange over HTTPS) and nothing else. There is no shell, no package manager,
# and no writable filesystem to speak of.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/ai-account /ai-account

# nonroot in the distroless images is uid 65532.
USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/ai-account"]
