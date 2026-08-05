/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Base URL of the demo-backend HTTP API. Default: http://localhost:8080 */
  readonly VITE_BACKEND_URL?: string;
  /** OTLP/HTTP traces endpoint the frontend exporter sends to. Default: http://localhost:4318 */
  readonly VITE_OTLP_ENDPOINT?: string;
  /** Base URL used to build the "View in Grafana" deep link. Default: http://localhost:3000 */
  readonly VITE_GRAFANA_BASE_URL?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
