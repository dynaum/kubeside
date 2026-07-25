import { useEffect, useState } from "react";
import { api, type GateView } from "./api";
import { envToken } from "./health";

// The ceremony between a developer and a destructive action.
//
// Every layer is here at once: the environment named rather than remembered,
// its colour, what the action touches, the equivalent kubectl, the typed
// confirmation, and for a break-glass environment a stated reason. None of it
// is security. RBAC is the security boundary and this dialog says so, because a
// tool that lets somebody confuse the two has taught them something dangerous.

export function Confirm({
  context, namespace, verb, resource, name, unlockOnly, onCancel, onConfirm,
}: {
  context: string;
  namespace: string;
  verb: string;
  resource: string;
  name: string;
  // unlockOnly is somebody arming an environment before deciding what to do in
  // it, rather than confirming a specific action.
  unlockOnly?: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const [view, setView] = useState<GateView | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [typed, setTyped] = useState("");
  const [reason, setReason] = useState("");

  const ask = (unlock?: string) => {
    api.gate(unlockOnly
      ? { context, namespace, verb: "", resource: "", name: "", unlock }
      : { context, namespace, verb, resource, name, unlock })
      .then(setView)
      .catch((e) => setErr(String(e.message ?? e)));
  };

  useEffect(() => {
    ask();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [context, namespace, verb, resource, name]);

  const gate = view?.gate;
  const permission = view?.permission;
  const needsTyping = gate?.require === "typed-name";
  const needsGlass = gate?.require === "break-glass";
  const typedOk = !needsTyping || typed === gate?.confirm;
  const ready = Boolean(gate?.permitted && permission?.allowed && typedOk);

  return (
    <div className="scrim" onClick={onCancel}>
      <div className="dialog" onClick={(e) => e.stopPropagation()}>
        <div className="dialog-head" data-env={gate ? envToken({ risk: gate.risk }) : "unc"}>
          {/* The environment is named, never inferred from memory. */}
          <span className="env-chip">{gate?.environment ?? "…"}</span>
          <strong style={{ fontWeight: 600 }}>
            {unlockOnly ? `arm ${gate?.environment ?? context} for writes` : `${verb} ${resource} ${name}`}
          </strong>
        </div>

        <div className="dialog-body">
          {err && <p style={{ color: "var(--err)", margin: 0 }}>{err}</p>}
          {!err && !gate && <span className="spinner" />}

          {gate && (
            <>
              <dl className="blast">
                <dt>Environment</dt>
                <dd>{gate.environment} · {gate.risk} risk · writes {gate.policy}</dd>
                {!unlockOnly && (
                  <>
                    <dt>Affects</dt>
                    <dd>{gate.blast.unknown ? "not computed" : (gate.blast.summary || `${gate.blast.pods} pods`)}</dd>
                    <dt>Equivalent</dt>
                    <dd>{gate.kubectl}</dd>
                  </>
                )}
                {gate.unlockedUntil && (
                  <>
                    <dt>Unlocked until</dt>
                    <dd>{new Date(gate.unlockedUntil).toLocaleTimeString()}</dd>
                  </>
                )}
              </dl>

              {permission && !permission.allowed && (
                <p style={{ color: "var(--err)", fontSize: 12 }}>
                  The cluster refuses this: {permission.reason}
                </p>
              )}

              {gate.reason && !gate.permitted && (
                <p style={{ color: "var(--warn)", fontSize: 12 }}>{gate.reason}</p>
              )}

              {needsGlass && (
                <>
                  <p style={{ fontSize: 12, color: "var(--fg-2)" }}>
                    Unlocking arms {gate.environment} for fifteen minutes and puts your reason on the timeline.
                  </p>
                  <input
                    className="field"
                    style={{ width: "100%" }}
                    placeholder="why this needs to happen now"
                    value={reason}
                    onChange={(e) => setReason(e.target.value)}
                  />
                  <button
                    className="btn btn-danger"
                    style={{ marginTop: "var(--s3)" }}
                    disabled={reason.trim().length === 0}
                    onClick={() => ask(reason.trim())}
                  >
                    Unlock {gate.environment}
                  </button>
                </>
              )}

              {needsTyping && !unlockOnly && (
                <>
                  <p style={{ fontSize: 12, color: "var(--fg-2)" }}>
                    Type <code className="mono">{gate.confirm}</code> to confirm.
                  </p>
                  <input
                    className="field"
                    style={{ width: "100%" }}
                    value={typed}
                    onChange={(e) => setTyped(e.target.value)}
                    placeholder={gate.confirm}
                  />
                </>
              )}

              <p style={{ color: "var(--fg-4)", fontSize: 11, marginTop: "var(--s3)", lineHeight: 1.5 }}>
                These prompts guard against acting in the wrong window. They are not security: the write
                policy lives in your own config file. RBAC is the boundary that stops anything.
              </p>
            </>
          )}
        </div>

        <div className="dialog-foot">
          <button className="btn" onClick={onCancel}>{unlockOnly ? "Close" : "Cancel"}</button>
          {!unlockOnly && (
            <button className="btn btn-danger" disabled={!ready} onClick={onConfirm}>
              {verb} {resource}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
