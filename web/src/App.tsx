import { useEffect, useState } from "react";
import { api, type ContextView } from "./api";
import { Rail } from "./Rail";
import { AppsScreen } from "./AppsScreen";
import { envKey } from "./health";

export function App() {
  const [contexts, setContexts] = useState<ContextView[] | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    api.contexts()
      .then((cs) => {
        setContexts(cs);
        // The current context leads; select it so the developer's usual
        // workspace renders first.
        const cur = cs.find((c) => c.current) ?? cs[0];
        if (cur) setSelected(cur.name);
      })
      .catch((e) => setErr(String(e)));
  }, []);

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

  const env = selected ? envKey(selected) : "unc";

  return (
    <div className="shell" data-env={env}>
      <Rail contexts={contexts} selected={selected} onSelect={setSelected} />
      <div className="main">
        {selected ? <AppsScreen context={selected} /> : <NoSelection />}
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
