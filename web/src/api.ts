// The wire types mirror internal/api/view.go. The session token lives in the
// URL kubeside opened; every request carries it.

export interface ContextView {
  name: string;
  current: boolean;
  state: string;
  hasData: boolean;
  ageSec?: number;
  error?: string;
}

export interface AppView {
  namespace: string;
  name: string;
  kind: string;
  health: string;
  reason: string;
  detail: string;
  groupedBy: string;
  managedBy?: string;
  ready: string;
  objects: number;
}

export interface MetricsInfo {
  source: string;
  available: boolean;
  reason?: string;
}

export interface AppsView {
  context: string;
  state: string;
  scope: string;
  reason?: string;
  partial?: string[];
  apps: AppView[];
  error?: string;
  metrics: MetricsInfo;
}

function token(): string {
  return new URLSearchParams(window.location.search).get("t") ?? "";
}

async function get<T>(path: string): Promise<T> {
  const sep = path.includes("?") ? "&" : "?";
  const res = await fetch(`${path}${sep}t=${encodeURIComponent(token())}`, {
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`${res.status}: ${body || res.statusText}`);
  }
  return res.json() as Promise<T>;
}

export const api = {
  contexts: () => get<ContextView[]>("/api/contexts"),
  apps: (context: string) => get<AppsView>(`/api/apps?context=${encodeURIComponent(context)}`),
};
