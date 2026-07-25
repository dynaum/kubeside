// Health maps to the status channel: shape carries meaning, colour reinforces.
// A theme toggle can strip colour and the glyph still reads.
export function healthClass(health: string): string {
  switch (health) {
    case "healthy": return "st-ok";
    case "progressing": return "st-prog";
    case "degraded": return "st-warn";
    case "failed": return "st-err";
    default: return "st-unknown";
  }
}

export type EnvToken = "qa" | "stg" | "prod" | "unc";

// Environment colour drives the rail edge, the topbar, and the hazard hatch.
// The backend resolves it from the config file when one binds the context, and
// from the context name otherwise, so this maps a decision rather than making
// one.
//
// A colour the design system has no token for falls back to risk, and anything
// unknown lands on unclassified, which carries prod-strength styling. An
// environment nobody classified is never rendered as safe.
const BY_COLOR: Record<string, EnvToken> = {
  green: "qa", amber: "stg", red: "prod", violet: "unc",
};
const BY_RISK: Record<string, EnvToken> = {
  low: "qa", medium: "stg", high: "prod",
};

export function envToken(env: { color?: string; risk?: string }): EnvToken {
  return BY_COLOR[env.color ?? ""] ?? BY_RISK[env.risk ?? ""] ?? "unc";
}
