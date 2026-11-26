# CLAUDE.md

Guidance for working on gopentair: a Go SDK and the `pentair` CLI for the unofficial Pentair Home cloud API.

## Key documentation

| File | Contents |
|---|---|
| `VISION.md` | Goals and non-goals |
| `docs/DESIGN.md` | Architecture, sequence diagrams, retry/confirm/watch semantics |
| `docs/api-docs.md` | The API as implemented, each item marked verified or unverified |
| `docs/research.md` | Original reverse-engineering research and source projects |

## Layout

```
client.go            Client interface + implementation (devices, states, Apply, debounce, confirm)
options.go config.go functional options; YAML/env config (Config.Merge)
device.go fields     Device, DeviceState (string fields + accessors), Merge
plan.go              WritePlan / ApplyOption / ApplyResult
pump.go              IF31 decoder + plans (zpNe10=3 start, =2 stop, p2 marker)
intelliconnect.go    PIF0 decoder + plans (ra0 state machine, heater, chlorinator)
light.go             PLC1 decoder + plans (d13 power, d1 show)
watch.go             WebSocket watcher: coalescing fan-out, reconnect, rotation, poll fallback
internal/srp         Cognito SRP (client + server math), pycognito-verified
internal/sign        SigV4 (App/Standard profiles, Verify), botocore-verified
internal/cognito     minimal Cognito IdP/Identity JSON client
internal/auth        Manager: session + AWS creds, independent refresh; MemoryStore
internal/tokenstore  0600 file store
internal/transport   signed requests, expired-token retry, read-only retries
internal/ws          WebSocket connection (registerEvent, keep-alive)
internal/api         endpoint constants, regions, wire types, field-name tables
internal/redact      log scrubbing (slog handler)
internal/testutil    fake cloud (Cognito SRP, Identity, SigV4 REST, WS) + fixtures
cmd/pentair          cobra CLI
examples/            status, watch, pump-program, prometheus
```

## Rules

- All reverse-engineered constants (pool IDs, hosts, paths) live in `internal/api`. Field codes are constants at the top of each typed-view file.
- Decoders and plan builders are pure functions. Missing fields decode to `nil`, never to zero.
- Writes go through `Apply`. Writes are never retried after an ambiguous failure (5xx or timeout after send).
- Never log or put into error strings: tokens, credentials, device IDs, ARNs or email addresses. Use `LogPath` with `{id}`, and use `internal/redact`.
- Calling `Logout` with `everywhere=true` signs out the phone app too. Keep it opt-in and behind a confirmation prompt in the CLI.

## Adding a device type or field

1. Map the fields with `pentair fields`, `pentair diff` and `pentair watch` against a real device.
2. Add names to `internal/api/fields.go` and synthetic fixtures to `internal/testutil/fixtures.go`.
3. Write a decoder and plan builders in a new `<type>.go`, with table tests in `views_test.go`.
4. Add CLI commands in `cmd/pentair/devicecmds.go` and wire them into `printStatus`.
5. Record the confirmed facts in `docs/api-docs.md`.

## Commands

```bash
make test        # all tests (fake cloud; no network)
make test-race
make build       # ./pentair
make test-e2e    # real cloud, read-only; needs PENTAIR_USERNAME/PENTAIR_PASSWORD
```

To regenerate the reference vectors, run `scripts/gen_vectors.py` (botocore + pycognito, fixed SRP private value and timestamp).
