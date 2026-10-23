# Service Mode and Deferred Work

## Service Mode → TUN Permission (done)

The Swift app planned a privileged helper service so TUN could run without
prompts. Kumo instead grants Mihomo the permission once (setuid root on
macOS, `cap_net_admin` on Linux; see
[system integration](../operations/system-integration-permissions.md#tun-permission)).
The app and CLI still start the core as the user, and every lifecycle rule
(ownership, guardian, release) stays in one place. A helper service is only
worth reconsidering if a platform stops honoring the grant.

## Deferred

- **P1 Sub-Store.** The Swift app bundled a Node.js Sub-Store backend to
  merge and convert subscriptions. It is not ported: it needs a Node
  runtime shipped with the app. Profiles it produced keep their content.
  A port would run it as a supervised sidecar owned by `Lifecycle`, with
  the same guardian cleanup.
- **P1 Update signing.** The updater is wired; the key pair and config
  entry are the maintainers' step (see [releases](../operations/release-management.md#updates)).
- **P1 Core download checks.** The managed Mihomo is installed without a
  checksum or signature check. Mihomo's releases publish no `.sha256` files
  and no signatures; the only digest is the `digest` field of each asset in
  GitHub's Releases API, which is rate limited for anonymous callers (the
  installer reads `version.txt` to avoid it). A check against it guards
  against a damaged or tampered download, not a poisoned release.
- **P2 Core versions beyond the managed core.** Only the core Kumo
  downloaded has a known version (the tag it was installed from). There is
  no rollback, no choosing a release (`--version`), no update notice, and
  no version for other cores; asking the binary (`mihomo -v`) would cover
  them. Without a version, nothing refuses to start a core too old for the
  MIPS stack.
- **P2 Windows release.** Needs the TUN permission flow and CLI link.
