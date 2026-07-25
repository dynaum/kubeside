// Routing lives in the URL hash, so every screen has a permalink and the
// browser's own back button works without a router library.

export type Route =
  | { screen: "apps"; context?: string }
  | { screen: "logs"; context: string; namespace: string; workload: string };

export function parseRoute(hash: string): Route {
  const parts = hash.replace(/^#\/?/, "").split("/").filter(Boolean).map(decodeURIComponent);
  if (parts[0] === "logs" && parts.length >= 4) {
    return { screen: "logs", context: parts[1], namespace: parts[2], workload: parts[3] };
  }
  if (parts[0] === "apps" && parts.length >= 2) {
    return { screen: "apps", context: parts[1] };
  }
  return { screen: "apps" };
}

export function routeHash(r: Route): string {
  const enc = encodeURIComponent;
  if (r.screen === "logs") return `#logs/${enc(r.context)}/${enc(r.namespace)}/${enc(r.workload)}`;
  return r.context ? `#apps/${enc(r.context)}` : "#apps";
}
