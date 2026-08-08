# syntax=docker/dockerfile:1
#
# Build context MUST be the repo root:
#   docker build -f js-service/Dockerfile -t demo-js-service:local .
#
# js-service/ and third_party/instrumentation-js/ are both copied in — the
# service depends on @akira-core/otel-nats and @akira-core/otel-flags from that
# submodule, exactly as the Go backend depends on the instrumentation-go one.
#
# The runtime image ships node_modules rather than a bundle, deliberately. The
# GO Feature Flag provider evaluates flags in-process through a WASM module it
# loads from its own package directory at runtime; a bundler that inlines JS but
# not assets produces a provider that can never initialise, which surfaces as a
# permanently dead relay connection rather than as a build error. This image is
# never pushed anywhere, so the size is not worth that failure mode.

FROM node:22-alpine AS build

RUN corepack enable

# pnpm refuses to replace an existing modules directory without a TTY and asks
# for this instead. The root .dockerignore already keeps node_modules out of the
# context; this covers every other non-interactive prompt.
ENV CI=true

WORKDIR /src

# The instrumentation packages first: they are the slowest-changing input, and
# building them is what produces the dist/ the service resolves against.
COPY third_party/instrumentation-js/ ./third_party/instrumentation-js/
RUN cd third_party/instrumentation-js \
    && pnpm install --frozen-lockfile \
    && pnpm run build

# Then the service. Its dependencies use pnpm's `link:` protocol pointing at the
# submodule paths above, which is the JS counterpart of a go.mod replace.
COPY js-service/package.json ./js-service/
COPY pnpm-workspace.yaml pnpm-lock.yaml package.json ./
COPY frontend/package.json ./frontend/
RUN pnpm install --filter demo-js-service...

COPY js-service/ ./js-service/
RUN pnpm --filter demo-js-service run build


FROM node:22-alpine AS runtime

# Never run as root; the service needs no write access to anything.
RUN addgroup -S app && adduser -S -G app app

# The WHOLE build tree, not a `pnpm deploy` output.
#
# `pnpm deploy` cannot inline a `link:` dependency: it leaves the symlink
# pointing at the original workspace path, which does not exist in the runtime
# image, so the service fails to start — and, less visibly, the GO Feature Flag
# provider's wasm-module/ never arrives either, which would surface only as a
# relay that can never initialise. Copying the tree keeps every symlink resolving
# against the same layout it was created in.
#
# The cost is the build-stage devDependencies (typescript and @types/node) riding
# along in an image that is loaded into kind and never pushed.
COPY --from=build --chown=app:app /src/ /src/

WORKDIR /src/js-service
USER app

EXPOSE 8081

CMD ["node", "dist/main.js"]
