# Logging

guestweave writes two append-only log files and a size-capped rotation. Logging
never breaks a command — failures to write are silent.

## Log files

Under `<home>/logs` (follows `WEAVE_HOME`; default `~/.weave/logs`):

| File | Contents |
|------|----------|
| `weave.info.log` | informational events (commands started, lifecycle) |
| `weave.error.log` | errors and warnings |
| `weave.clipboard-audit.log` | clipboard transfer/rejection audit records (when clipboard auditing is on) |
| `*.log.old` | one rotated generation per stream (when `keepRotated` is on) |

The info/error lines are `YYYY-MM-DD HH:MM:SS.mmm [pid] message`. The clipboard
audit log is one JSON object per line (enable it with a clipboard `*-audit` flag
or `WEAVE_CLIP_AUDIT=1` — see [Clipboard](clipboard.md#audit-log)).

## Viewing logs

```sh
weave logs error              # whole error log
weave logs all --lines 100    # last 100 lines, info + error interleaved
weave logs info -f            # follow (tail -f)
```

In the run window: **File ▸ Show Logs** opens the error log in Console.

> The logs are append-only and accumulate across runs, so old entries from
> earlier sessions remain until rotated or cleared — they are not necessarily
> from the current launch.

## Size cap & rotation

A per-file size cap triggers rotation. It is configurable
([Configuration](configuration.md)):

```yaml
logging:
  maxSizeMB: 10        # cap per file; 0 = unlimited (default 10)
  keepRotated: true   # rename to .old on rotation, or truncate when false
```

```sh
weave config logging                  # show effective settings
weave config logging maxSizeMB 20     # cap at 20 MB
weave config logging maxSizeMB 0      # unlimited
weave config logging keepRotated false
```

When a file reaches the cap it is renamed to `<name>.old` (keeping one
generation) — or simply truncated when `keepRotated` is `false`. The cap is read
at process startup, so changes apply to new invocations.

## Clearing logs

Removes **all** log files — info, error, and both `.old` copies:

```sh
weave logs clear
```

In the run window: **File ▸ Clear Logs…** (with confirmation) does the same.

## Telemetry

Log records are also emitted to OpenTelemetry when an OTel exporter is
configured (the OTel service name is `com.deploymenttheory.guestweave`). This is
independent of the file logs above.
