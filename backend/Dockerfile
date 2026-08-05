# syntax=docker/dockerfile:1
#
# Build context MUST be the repo root:
#   docker build -f backend/Dockerfile -t demo-backend:local .
#
# backend/, otel-nats/, and otel-flags/ are copied in (otel-nats requires
# otel-flags; both come from the instrumentation-go submodule).
#
# NOTE ON go.work: the repo-root go.work is not copied in. This stage
# generates its own inside the image with `go work init`, so local modules
# resolve to the copied-in submodule paths exactly as they do for local dev,
# without the image depending on the layout of a file outside its context.

FROM golang:1.26-alpine AS build

WORKDIR /src

COPY backend/ ./backend/
COPY third_party/instrumentation-go/otel-nats/ ./third_party/instrumentation-go/otel-nats/
COPY third_party/instrumentation-go/otel-flags/ ./third_party/instrumentation-go/otel-flags/

RUN go work init \
	./backend \
	./third_party/instrumentation-go/otel-flags \
	./third_party/instrumentation-go/otel-nats

WORKDIR /src/backend
RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/demo-backend ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot AS final

COPY --from=build /out/demo-backend /usr/local/bin/demo-backend

ENV PORT=8080
EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/demo-backend"]
