# Logs

One workload, every replica, one stream. No tab per pod.

## What you get

- Every pod of the app, merged by timestamp, colour-coded per pod.
- Restarts followed automatically: a container that dies and comes back keeps
  streaming into the same view.
- Sidecars filtered out by default, and switchable back on.
- `previous` for the log of the container that died, when the kubelet still has
  it.

## Availability edges

Logs have edges, and a log view that hides them lies about what it showed you.
Every one is marked in the stream:

| Edge | Means |
| --- | --- |
| `horizon` | Where this stream begins. Anything earlier is not available from the kubelet. |
| `restart` | The container restarted here; the stream reopened after it. |
| `gone` | The pod was deleted. Its logs went with it. |
| `error` | The stream failed. The reason is on the edge. |
| `ended` | The container exited and there is nothing more to come. |

A gap with no edge would read as a quiet period. A gap with an edge reads as
what it is.

## Order

Lines are merged by timestamp, not by arrival. A replica whose backlog arrives
late is placed where it belongs rather than at the bottom, within a settling
window of a few seconds; after that, late lines are marked rather than
reordered, because moving text that somebody is already reading is worse than
labelling it.

Lines longer than 64 KB are truncated and marked. A pathological log line should
not be able to stall the view.

## What is kept

Nothing on disk, ever. The session keeps a bounded ring in memory: 2000 entries
per app, 4 MB per app, 100 MB in total. When the cap is reached the oldest entry
anywhere is dropped, and the timeline says the horizon moved rather than
pretending the older lines were never there.
