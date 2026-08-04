/**
 * Logic stranded inside FleetScreen.tsx, extracted so it can be tested without
 * a DOM. web/vite.config.ts's vitest include does not match .tsx, and there is
 * no jsdom in this package, so none of a component's inline logic is
 * reachable from a test until it lives in a plain .ts module.
 */

/**
 * notPresentLabel names a row that never ran the app: it did not answer, it
 * refused to let us look, or it is still asking. A state this map has never
 * heard of surfaces as itself rather than going blank, because a blank cell
 * on this screen reads as "checked and found nothing", which is a claim
 * nobody earned about a state nobody recognizes.
 */
const NOT_PRESENT_LABEL: Record<string, string> = {
  unreachable: "no answer",
  denied: "no access",
  pending: "asking",
};

export function notPresentLabel(state: string): string {
  return NOT_PRESENT_LABEL[state] ?? state;
}

/** readySplit is where the Ratio cell's "/" lives, so the whole and the
 * denominator can be styled apart. A value with no separator, or none at all,
 * renders as one piece rather than losing text to a slice that assumed a "/"
 * that was not there. */
export function readySplit(ready: string): { whole: string; of: string | null } {
  const i = ready.indexOf("/");
  if (i === -1) return { whole: ready, of: null };
  return { whole: ready.slice(0, i), of: ready.slice(i) };
}

/**
 * verdictBadge picks the one flag a present row shows, in precedence order. A
 * mutable tag is the worse fact: it means two clusters claiming this version
 * are not running the same code, and "behind" undersells that as a mere
 * schedule. Showing both would suggest they are independent problems, so
 * mutableTag suppresses behind rather than sitting beside it.
 */
export function verdictBadge(r: { mutableTag?: boolean; behind?: boolean }): "mutable-tag" | "behind" | null {
  if (r.mutableTag) return "mutable-tag";
  if (r.behind) return "behind";
  return null;
}
