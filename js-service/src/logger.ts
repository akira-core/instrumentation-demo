/**
 * Structured JSON logging on one line per record, matching the shape the Go
 * backend's `slog` JSON handler emits so `kubectl logs` reads the same for both
 * services.
 */

export type Fields = Record<string, unknown>;

export interface Logger {
  info(message: string, fields?: Fields): void;
  warn(message: string, fields?: Fields): void;
  error(message: string, fields?: Fields): void;
}

function emit(level: string, message: string, fields?: Fields): void {
  const record = { level, msg: message, ...fields };
  // A timestamp is deliberately omitted: the container runtime stamps every line,
  // and a second one only invites the two to disagree.
  process.stdout.write(`${JSON.stringify(record)}\n`);
}

export const logger: Logger = {
  info: (message, fields) => emit("INFO", message, fields),
  warn: (message, fields) => emit("WARN", message, fields),
  error: (message, fields) => emit("ERROR", message, fields),
};
