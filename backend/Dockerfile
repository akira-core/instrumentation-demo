# syntax=docker/dockerfile:1
#
# Build context MUST be the repo root:
#   docker build -f backend/Dockerfile -t demo-backend:local .
#
# Only backend/ and third_party/instrumentation-go/otel-nats/ are copied in
# (not the whole third_party/ tree) to keep the build context reasonable.
#
# NOTE ON go.work: the repo-root go.work is not copied in. This stage
# generates its own inside the image with `go work init`, so otel-nats
# resolves to the copied-in submodule path exactly as it does for local dev,
# without the image depending on the layout of a file outside its context.
# That resolution is what matters: backend/go.mod's `require` line names
# v0.7.0, the newest published tag, while the submodule is 0.8.0 — the
# workspace is what makes the built binary the submodule's code.

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
