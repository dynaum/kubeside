import { useEffect, useState } from "react";
import { api, type ContextView } from "./api";
import { Rail } from "./Rail";
import { AppsScreen } from "./AppsScreen";
import { LogsScreen } from "./LogsScreen";
import { AppDetailScreen } from "./AppDetailScreen";
import { ConfigScreen } from "./ConfigScreen";
import { envToken } from "./health";
import { parseRoute, routeHash, type Route } from "./route";

export function App() {
  const [contexts, setContexts] = useState<ContextView[] | null>(null);
  const [route, setRoute] = useState<Route>(() => parseRoute(window.location.hash));
  const [err, setErr] = useState<string | null>(null);

  // The hash is the source of truth for what is on screen, so the browser's
  // back button and a pasted permalink both work without a router.
  useEffect(() => {
    const onHash = () => setRoute(parseRoute(window.location.hash));
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  useEffect(() => {
    api.contexts()
      .then((cs) => {
        setContexts(cs);
        // The current context leads; select it so the developer's usual
        // workspace renders first, unless a permalink named another.
        setRoute((r) => {
          if (r.context && cs.some((c) => c.name === r.context)) return r;
          const cur = cs.find((c) => c.current) ?? cs[0];
          return cur ? { screen: "apps", context: cur.name } : r;
        });
      })
      .catch((e) => setErr(String(e)));
  }, []);

  const go = (next: Route) => {
    window.location.hash = routeHash(next);
    setRoute(next);
  };

  if (err) {
    return (
      <div className="shell">
        <div />
        <div className="main">
          <div className="page">
            <div className="empty">
              <div className="head">Could not reach kubeside</div>
              <div className="mono">{err}</div>
            </div>
          </div>
        </div>
      </div>
    );
  }

  if (!contexts) {
    return (
      <div className="shell">
        <div className="rail" />
        <div className="main">
          <div className="page">
            <span className="spinner" /> <span style={{ color: "var(--fg-3)" }}>loading contexts…</span>
          </div>
        </div>
      </div>
    );
  }

  const current = contexts.find((c) => c.name === route.context) ?? null;

  return (
    <div className="shell" data-env={current ? envToken(current) : "unc"}>
      <Rail
        contexts={contexts}
        selected={current?.name ?? null}
        onSelect={(name) => go({ screen: "apps", context: name })}
      />
      <div className="main">
        {!current && <NoSelection />}
        {current && route.screen === "apps" && (
          <AppsScreen
            context={current}
            onOpenLogs={(namespace, workload) =>
              go({ screen: "logs", context: current.name, namespace, workload })
            }
            onOpenApp={(namespace, workload) =>
              go({ screen: "app", context: current.name, namespace, workload })
            }
          />
        )}
        {current && route.screen === "app" && (
          <AppDetailScreen
            context={current}
            namespace={route.namespace}
            workload={route.workload}
            onNavigate={(screen) =>
              go({ screen, context: current.name, namespace: route.namespace, workload: route.workload })
            }
            onBack={() => go({ screen: "apps", context: current.name })}
          />
        )}
        {current && route.screen === "config" && (
          <ConfigScreen
            context={current}
            namespace={route.namespace}
            workload={route.workload}
            onNavigate={(screen) =>
              go({ screen, context: current.name, namespace: route.namespace, workload: route.workload })
            }
            onBack={() => go({ screen: "apps", context: current.name })}
          />
        )}
        {current && route.screen === "logs" && (
          <LogsScreen
            context={current}
            namespace={route.namespace}
            workload={route.workload}
            onNavigate={(screen) =>
              go({ screen, context: current.name, namespace: route.namespace, workload: route.workload })
            }
            onBack={() => go({ screen: "apps", context: current.name })}
          />
        )}
      </div>
    </div>
  );
}

function NoSelection() {
  return (
    <div className="page">
      <div className="empty">
        <div className="head">No kubeconfig contexts found</div>
        <div>kubeside reads the kubeconfig you already have; nothing to import.</div>
      </div>
    </div>
  );
}
