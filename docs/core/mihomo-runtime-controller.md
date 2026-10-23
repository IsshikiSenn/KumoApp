# Mihomo Runtime Controller

`application.Lifecycle` owns the core process. The runtime record
(`state.json`, guarded by `.state.lock`) is shared by every Kumo process, so
the app, the CLI and the guardian agree on what runs.

## Start

1. Refuse when a core already runs (`ErrAlreadyRunning`).
2. Resolve the core, first found wins: `--core`, the chosen path, the
   managed core (`cores/mihomo`), `KUMO_MIHOMO_PATH`, one bundled with the
   app, `PATH`, then common install directories.
3. Check ports. The mixed port and the controller must be free; a port held
   by another program is a `PortConflict` naming the holder (pid, process)
   and a suggested free port. Nothing is killed. The caller decides: the app
   shows a dialog, the CLI prints the conflict, and `resolvePorts` moves to
   the suggested ports.
4. Build the runtime config (profiles were validated when imported).
5. Launch detached (`-d work -f work/config.yaml -ext-ctl … -secret …`),
   with proxy variables cleared, record pid, owner and `processLaunched`.
6. Ready when the controller answers **and** `/proxies` lists groups;
   otherwise fail with the last error line of `logs/core.log`.
7. Apply the system proxy when it is enabled.

## Managed Core and Updates

`SettingsService.InstallCore` (the Core page's *Download*/*Update*, `kumo core
install`) installs the latest Mihomo to `cores/mihomo`:

1. Read the latest tag from the release's `version.txt`. When it equals the
   version recorded in `cores/managed.json` and the file is there, stop:
   nothing is downloaded or restarted. A core with no record (installed
   before versions were kept) is installed again.
2. Download and unpack the build. A failure here touches nothing. The core
   keeps running meanwhile: when it is up, the version check and the
   download go through its mixed port, since GitHub may only be reachable
   through the proxy it provides. If that proxy does not answer within 20 s,
   the request is made directly.
3. When the running core is the managed one, stop it. A running executable
   cannot be replaced everywhere, and a replaced one keeps running the old
   release.
4. Replace the file, then record the tag and time in `managed.json`.
5. Give TUN permission to the new file again when the old one had it by
   setuid or capability (see [system integration](../operations/system-integration-permissions.md#tun-permission)).
6. Start the core again when it was running. A refused grant does not stop
   this: Mihomo only logs that its TUN device could not be created and
   keeps serving the other listeners. The outcome says `needs-grant`.

The chosen core only changes to the managed one when it is the first
install, when it is already the choice, or when nothing was chosen. A core
the user picked, or one that runs from another file, is left alone.

The version is the tag the release announced when it was downloaded; Kumo
does not run the binary to ask, so only the managed core has one, and a file
replaced by hand leaves the record stale. Kumo does not check the download
against a checksum (see [deferred work](../roadmap/service-mode-roadmap.md#deferred)).

## Reload, Mode and Settings

Profile switches, override changes and DNS/sniffer/geo edits rebuild the
config and `PUT /configs?force=true` without restarting. A rejected config
leaves the running core as it was and reports the error. Turning TUN on, or
changing the controller, restarts. `mode` patches `/configs`.

## Stop and Shutdown

`Stop` releases the system proxy first, then terminates the core
(SIGINT → SIGTERM → SIGKILL), waits for its ports and records the stop.
`Shutdown` (every app quit) does the same for whatever core runs and
always stops the PAC server.

## Monitoring and Adoption

`Monitor` refreshes the state every 3 s: a core that died is recorded as
`failed` with its exit and a `CoreExited` event; a takeover of the system
proxy is detected (see [system integration](../operations/system-integration-permissions.md)).
A core started by the CLI is adopted by the app, and vice versa. A stale
pid file never kills a process unless it is Mihomo (`process.IsCore`).

## Guardian

The app spawns a copy of itself with `KUMO_GUARDIAN_PARENT=<pid>`. It waits
for the app to exit. If the app quit properly there is nothing left to do;
after a crash or SIGKILL it shuts the app's core down and releases the
system proxy Kumo set. On the next launch `Recover` adopts a surviving
core or releases a proxy left behind.

## Verification

- `internal/application/lifecycle_sim_test.go` runs real Mihomo
  (`KUMO_TEST_MIHOMO=<path> go test ./internal/application`) through start,
  selection, stop, proxy ownership, takeover and reclaim, port conflicts,
  crashes, adoption, stale pids, broken profiles, profile switches,
  concurrent starts and shutdown. `core_update_test.go` installs from a
  fake release (fresh, already latest, no record, new release, another
  chosen core, offline, TUN regrant outcomes) and, with a real Mihomo,
  updates a running core and leaves a different running core alone.
- `scripts/e2e/lifecycle.sh <mihomo>` drives the built app (see
  [testing](../quality/testing-quality.md)).
