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

// Environment risk drives the rail edge and hazard hatch. The backend has not
// yet classified environments per context, so this maps a name heuristically
// for now; it is replaced when the environment model is wired through.
export function envKey(name: string): "qa" | "stg" | "prod" | "unc" {
  const n = name.toLowerCase();
  if (/\b(prod|production|prd|live)\b|prod/.test(n)) return "prod";
  if (/stg|staging|stage|uat|preprod/.test(n)) return "stg";
  if (/qa|test|dev|sandbox|sbx/.test(n)) return "qa";
  return "unc";
}
