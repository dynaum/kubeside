// Routing lives in the URL hash, so every screen has a permalink and the
// browser's own back button works without a router library.

export type Route =
  | { screen: "apps"; context?: string }
  | { screen: "promotion" }
  | { screen: "app"; context: string; namespace: string; workload: string }
  | { screen: "config"; context: string; namespace: string; workload: string }
  | { screen: "exec"; context: string; namespace: string; workload: string }
  | { screen: "diff"; context: string; namespace: string; workload: string; other?: string }
  | { screen: "logs"; context: string; namespace: string; workload: string };

export function parseRoute(hash: string): Route {
  const parts = hash.replace(/^#\/?/, "").split("/").filter(Boolean).map(decodeURIComponent);
  if (parts[0] === "promotion") return { screen: "promotion" };
  if (parts[0] === "diff" && parts.length >= 4) {
    return { screen: "diff", context: parts[1], namespace: parts[2], workload: parts[3], other: parts[4] };
  }
  if ((parts[0] === "logs" || parts[0] === "app" || parts[0] === "config" || parts[0] === "exec") && parts.length >= 4) {
    return {
      screen: parts[0] as "logs" | "app" | "config" | "exec",
      context: parts[1], namespace: parts[2], workload: parts[3],
    };
  }
  if (parts[0] === "apps" && parts.length >= 2) {
    return { screen: "apps", context: parts[1] };
  }
  return { screen: "apps" };
}

export function routeHash(r: Route): string {
  const enc = encodeURIComponent;
  if (r.screen === "diff") {
    const base = `#diff/${enc(r.context)}/${enc(r.namespace)}/${enc(r.workload)}`;
    return r.other ? `${base}/${enc(r.other)}` : base;
  }
  if (r.screen === "logs" || r.screen === "app" || r.screen === "config" || r.screen === "exec") {
    return `#${r.screen}/${enc(r.context)}/${enc(r.namespace)}/${enc(r.workload)}`;
  }
  if (r.screen === "promotion") return "#promotion";
  return r.context ? `#apps/${enc(r.context)}` : "#apps";
}
