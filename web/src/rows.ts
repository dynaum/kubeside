import { age } from "./detail";

/**
 * Cell formatters for the app list. The row answers what version is running,
 * how long it has been running, and whether it is flapping. Each cell renders a
 * dash when the reading was not taken, never a plausible-looking default.
 */

/** tagCell shows the image tag, or a dash when none was read. */
export function tagCell(tag: string | undefined): string {
  return tag ? tag : "—";
}

/**
 * revisionAge turns the moment the revision appeared into a duration. The wire
 * carries the moment so an unchanged cluster produces no patch; the subtraction
 * happens here, on every render, for free.
 */
export function revisionAge(revisionAt: string | undefined, now: number): string {
  if (!revisionAt) return "—";
  const at = Date.parse(revisionAt);
  if (Number.isNaN(at)) return "—";
  const seconds = (now - at) / 1000;
  // A laptop clock a little behind the cluster's can date a revision in the
  // future. Zero is honest; a negative duration is not.
  if (seconds <= 0) return "0s";
  return age(seconds);
}

/**
 * restartCell reads the restart total. Zero restarts across zero pods is a
 * reading nobody took, so it renders a dash: a calm zero would tell a developer
 * their app is fine when nothing was measured.
 */
export function restartCell(pods: number, restarts: number): { text: string; warn: boolean } {
  if (pods <= 0) return { text: "—", warn: false };
  return { text: String(restarts), warn: restarts > 0 };
}
