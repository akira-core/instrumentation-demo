/**
 * Runtime configuration for the trace-propagation demo frontend.
 *
 * All values are supplied at build time via Vite's `import.meta.env`
 * (VITE_-prefixed env vars). Every value has a sane localhost default so the
 * app also runs standalone with `pnpm dev` / `pnpm build` and no `.env` file.
 */
export interface DemoConfig {
  /** Base URL of the demo-backend HTTP API (no trailing slash). */
  readonly backendUrl: string;
  /** OTLP/HTTP traces endpoint the frontend's span exporter sends to. */
  readonly otlpEndpoint: string;
  /** Base URL used to build the "View in Grafana" deep link. */
  readonly grafanaBaseUrl: string;
}

/** Strips a single trailing slash so URL concatenation never double-slashes. */
function stripTrailingSlash(url: string): string {
  return url.endsWith("/") ? url.slice(0, -1) : url;
}

export const config: DemoConfig = {
  backendUrl: stripTrailingSlash(
    import.meta.env.VITE_BACKEND_URL ?? "http://localhost:8080",
  ),
  otlpEndpoint: stripTrailingSlash(
    import.meta.env.VITE_OTLP_ENDPOINT ?? "http://localhost:4318",
  ),
  grafanaBaseUrl: stripTrailingSlash(
    import.meta.env.VITE_GRAFANA_BASE_URL ?? "http://localhost:3000",
  ),
};
