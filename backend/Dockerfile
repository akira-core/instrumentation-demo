# syntax=docker/dockerfile:1
#
# Build context MUST be the repo root:
#   docker build -f backend/Dockerfile -t demo-backend:local .
#
# Only backend/ and third_party/instrumentation-go/otel-nats/ are copied in
# (not the whole third_party/ tree) to keep the build context reasonable.
#
# NOTE ON go.work: the repo-root go.work also lists ./relay-proxy as a
# workspace member. This image's build context intentionally does NOT
# include relay-proxy/ (it isn't a backend dependency), so the root go.work
# can't be copied in verbatim — `go build` would fail trying to load a
# workspace member directory that doesn't exist in the image. Instead this
# stage generates its own two-member go.work (backend + otel-nats only)
# inside the image with `go work init`, which resolves otel-nats from the
# copied-in submodule path exactly like the repo-root workspace does for
# local dev, without needing relay-proxy/ present.

FROM golang:1.26-alpine AS build

WORKDIR /src

# Copy just the two modules this image needs.
COPY backend/ ./backend/
COPY third_party/instrumentation-go/otel-nats/ ./third_party/instrumentation-go/otel-nats/

# Recreate a minimal workspace (backend + otel-nats only — see note above)
# so backend's import of github.com/akira-core/instrumentation-go/otel-nats/otelnats
# resolves to the local submodule copy instead of the network.
RUN go work init ./backend ./third_party/instrumentation-go/otel-nats

WORKDIR /src/backend
RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/demo-backend ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot AS final

COPY --from=build /out/demo-backend /usr/local/bin/demo-backend

ENV PORT=8080
EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/demo-backend"]
