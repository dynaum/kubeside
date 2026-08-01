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

/**
 * cpuCell reads in millicores, the unit kubectl top prints, so a developer
 * comparing the two screens compares the same number.
 *
 * measured is how many of the app's pods the source answered for. Zero means no
 * reading was taken, which renders as a dash. A measured zero is an idle app and
 * renders as one.
 */
export function cpuCell(measured: number, cpuMilli: number): string {
  if (measured <= 0) return "—";
  return `${Math.round(cpuMilli)}m`;
}

/** memCell picks the unit that fits, in the binary units Kubernetes uses. */
export function memCell(measured: number, memoryBytes: number): string {
  if (measured <= 0) return "—";
  const mib = memoryBytes / (1024 * 1024);
  if (mib >= 1024) return `${(mib / 1024).toFixed(1)}Gi`;
  // Round up, so a pod holding half a mebibyte is not reported as holding
  // nothing at all.
  return `${mib > 0 ? Math.max(1, Math.round(mib)) : 0}Mi`;
}

/**
 * usageNote names a total built from fewer pods than the app has. Three of four
 * replicas is not the app's usage, and the difference between a number and a
 * number you can act on is knowing which one you have.
 */
export function usageNote(measured: number, pods: number): string {
  if (measured <= 0 || measured >= pods) return "";
  return `${measured} of ${pods} pods reported`;
}
