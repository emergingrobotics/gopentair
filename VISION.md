# gopentair — Vision

A Go SDK and CLI for the **Pentair Home cloud API**: the unofficial, reverse-engineered service behind the Pentair Home mobile app. It covers IntelliFlo 3 pumps, IntelliConnect controllers, Color Sync lights and the other products the app supports.

The project follows the structure and conventions of the sibling project [`goaquatemp`](../goaquatemp): an importable root package, internals hidden under `internal/`, a cobra-based CLI under `cmd/`, a fake server for tests, and a written record of the API kept in `docs/api-docs.md`. See [docs/research.md](docs/research.md) for the research this is based on.

---

## Why

- Pentair has **no public API**. The working knowledge is spread across Python Home Assistant integrations, a Go Home Assistant add-on and a Homebridge plugin. Each covers one device type, and each has made its own mistakes with token handling.
- No Go library exists that handles the full auth chain correctly: Cognito SRP, then Cognito Identity, then STS, then SigV4, each with its own expiry.
- People want to script their pool: check the pump wattage from cron, start a cleaning program before a party, log salt levels to a time-series database, or wire the lights to other automations. Today that requires running Home Assistant.

## What it is

1. **A library** (`github.com/emergingrobotics/gopentair`). It can be imported by anything: a home-automation bridge, an exporter, a Matter or MQTT gateway.
2. **A CLI** (`pentair`). It is useful on day one for inspection, control and live monitoring, and it is also how we explore the API and confirm what we learn.
3. **A living API reference** (`docs/api-docs.md`). Every endpoint and field code we confirm against a live account goes here, with captured request and response shapes. This document is the project's most durable output.

## What it is not

- Not a local or LAN controller. IntelliCenter's WebSocket on port 6680, ScreenLogic and RS-485 are already well served by other projects.
- Not a Home Assistant integration. It can be the engine underneath one.
- Not a proxy or server. Bridges such as MQTT or Prometheus belong in separate binaries or `examples/`, not in the core.

---

## Goals

| Goal | What it means here |
|---|---|
| **Correct auth, invisible to the caller** | SRP login once, then the client silently refreshes the JWT and the STS credentials on their *independent* expiry clocks, and retries once on an "expired token" 401/403. Callers never see a token. |
| **Typed where we know, raw where we don't** | Typed views for confirmed device types (`IF31`, `PIF0`, `PLC1`). Every device also exposes its raw `Fields` map, and the API offers a raw `SetFields`, so new hardware is usable before anyone models it. |
| **Real-time first-class** | A `Watch` API over the WebSocket that merges field deltas into a consistent snapshot, with a REST poll fallback and reconnect with backoff. |
| **Safe writes** | Mutations are explicit, debounced and confirmed. The CLI supports `--dry-run` and never writes as a side effect of reading. |
| **Polite to Pentair's cloud** | Minimum poll interval (60 s by default), at most one auth flow at a time, a cached refresh token so the CLI doesn't run SRP on every invocation, and an honest `User-Agent` option. |
| **Testable without an account** | `internal/testutil` provides a fake server that mimics the real wire format, including SigV4 checks, the `deviceIds` body quirk, `set_device_success` and expired-token responses. End-to-end tests run only when credentials are present. |

---

## Shape of the SDK

```
gopentair/
├── client.go          # Client interface + implementation
├── options.go         # WithUsername, WithPassword, WithRegion, WithTokenStore, WithTimeout, WithDebug, ...
├── config.go          # Config, env + YAML loading (~/.config/pentair/config.yaml)
├── device.go          # Device, DeviceState, Field, DeviceType constants
├── pump.go            # IF31 typed view: telemetry, programs, start/stop
├── intelliconnect.go  # PIF0 typed view: relay/pump, heater, IntelliChlor, temps, schedule
├── light.go           # PLC1 typed view: power, color/show, hold/recall
├── watch.go           # WebSocket subscription, delta merge, Event stream
├── errors.go          # AuthError, APIError, sentinels (ErrDeviceNotFound, ErrNotAcknowledged, ...)
├── doc.go
├── internal/
│   ├── auth/          # Cognito SRP, refresh, Cognito Identity → STS, TokenStore
│   ├── sign/          # SigV4 (execute-api, us-west-2) with Pentair's header set
│   ├── api/           # endpoints, request/response wire types, field-code tables
│   ├── transport/     # signed HTTP with retry/backoff + expired-token retry
│   ├── ws/            # WebSocket client: registerEvent, keep-alive, appuse ping
│   └── testutil/      # fake Pentair cloud (REST + WS)
├── cmd/pentair/       # CLI
├── examples/          # status, watch, pump-program, prometheus-exporter
└── docs/              # api-docs.md, DESIGN.md, plan.md
```

### Core interface (sketch)

```go
type Client interface {
    Login(ctx context.Context) error            // SRP, or resume from TokenStore
    Close() error                               // local only; never GlobalSignOut
    SignOutEverywhere(ctx context.Context) error // explicit, documented as signing out the phone app too

    Profile(ctx context.Context) (*Profile, error)
    Devices(ctx context.Context) ([]Device, error)
    Device(ctx context.Context, id string) (*Device, error)
    States(ctx context.Context, ids ...string) ([]DeviceState, error) // POST device2 {"deviceIds":[...]}

    SetFields(ctx context.Context, id string, fields map[string]string) error // raw escape hatch

    Watch(ctx context.Context, ids []string, opts ...WatchOption) (<-chan Event, error)

    // Typed helpers return views over DeviceState and wrap SetFields.
    Pump(id string) *Pump                     // IF31
    IntelliConnect(id string) *IntelliConnect // PIF0
    Light(id string) *Light                   // PLC1
}
```

Typed views decode fields with the correct scaling. For example, IF31 `s17` pressure is ÷100 psi, `s19` speed and `s26` flow are ÷10, and PIF0 `htd2` is tenths of °F. The views also put state-machine knowledge in one place. "Is the pump running?" reads `ra4 > 0`, not the intent field `ra0`. Starting an IF31 program writes `zpNe10=3` and then `p2=99`.

### Auth design notes

- **SRP:** an internal `srp` package (a port of pycognito's `aws_srp`, tested with known vectors). Cognito Identity Provider and Cognito Identity are called as plain JSON POSTs with `X-Amz-Target` headers, with no AWS SDK. See `docs/DESIGN.md` § Authentication.
- **SigV4:** an internal `sign` package (standard library only, tested against the AWS SigV4 test suite) with two header profiles: `app` (the app's exact signed-header set, the default) and `standard`. See `docs/DESIGN.md` § Request Signing.
- **TokenStore** interface with these implementations:
  - in-memory (the default for library use)
  - file (the CLI default: `~/.config/pentair/tokens.json`, mode `0600`)
  - none

  Only the refresh token and IdToken/AccessToken are stored, never the password.
- **Regions:** US (`us-west-2`) is the default. EU (`eu-west-1_rsBGJFuRd`) is selectable but marked experimental until someone confirms it.

---

## The CLI

`pentair` keeps the same habits as `aquatemp`:
- Every listing command offers human-readable tables, `--json` and `--csv`.
- Without `--device`, commands use the default device: the one from config, or the account's only device.
- Temperatures are shown in °F and °C.

```bash
export PENTAIR_USERNAME=you@example.com PENTAIR_PASSWORD=...

pentair login                      # SRP once, cache refresh token
pentair whoami                     # profile
pentair devices list               # id, type, nickname, online, status
pentair status [--device ID] [--json]   # typed summary for known types, raw for others

# Raw field access: the exploration toolkit
pentair fields [--device ID] [--filter s1] [--names]   # every field: code, name, value, min/max
pentair fields set --device ID ra0=1 --dry-run
pentair watch [--device ID] [--screen poolScreen|product] [--fields s18,s19]  # live deltas
pentair diff  --device ID          # snapshot, wait for Enter, snapshot again, print changed fields

# IntelliFlo 3 (IF31)
pentair pump status
pentair pump programs
pentair pump start <program#|name>
pentair pump stop  <program#|name>

# IntelliConnect (PIF0)
pentair pool status                # pump watts, water/air temp, heater, salt, chlorine
pentair pool pump on|off
pentair pool schedule [--enable|--disable] [--start 08:00 --stop 16:00]  # local time → UTC seconds
pentair pool heater off|heat [--setpoint 84]
pentair pool chlorine 40

# Color Sync (PLC1)
pentair light on|off
pentair light show caribbean       # red, white, magenta, ..., royal
pentair light hold|recall

pentair logout                     # delete cached tokens (local)
pentair logout --everywhere        # Cognito GlobalSignOut, with a warning prompt
```

`fields`, `watch` and `diff` matter as much as the typed commands. They are how a user with an unsupported device, such as ChemCheck or IntelliCenter-via-cloud, maps its fields and contributes them back to `docs/api-docs.md`.

### Configuration

| Env var | Purpose |
|---|---|
| `PENTAIR_USERNAME` / `PENTAIR_PASSWORD` | Account credentials |
| `PENTAIR_REGION` | `us` (default) or `eu` |
| `PENTAIR_DEFAULT_DEVICE` | Device ID used when `--device` is omitted |
| `PENTAIR_CONFIG` | Config file path (default `~/.config/pentair/config.yaml`) |
| `PENTAIR_TOKEN_FILE` | Token cache path, or `none` to disable |
| `PENTAIR_DEBUG` | Log requests and responses with secrets and identifiers **redacted** |

Redaction always applies: device IDs, ARNs, email, MAC (`s6`), security key (`s11`), SSID (`s23`) and pump password (`d15`) never appear in debug output.

---

## Milestones

1. **Auth + read:** SRP login, token cache, STS, signed GET/POST; `login`, `devices list`, `fields`, `status --json` (raw). Confirm against a live account.
2. **Writes + raw control:** `SetFields` with `set_device_success` check, `fields set --dry-run`, expired-token retry, fake-server tests.
3. **Typed devices:** IF31 pump view and programs; PIF0 pool view; PLC1 light view. Each field confirmed live and documented in `docs/api-docs.md`.
4. **Real-time:** `Watch` over the WebSocket with delta merge, reconnect and poll fallback; `pentair watch` and `pentair diff`.
5. **Ecosystem examples:** Prometheus exporter, MQTT bridge (Home Assistant discovery), cron-friendly `status --csv`.

## Success looks like

- `go install .../cmd/pentair@latest && pentair status` works within a minute of setup, and never asks for the password again until the refresh token expires (about 30 days).
- A week-long `pentair watch` runs without a re-login error, a dropped stream or a leaked credential in the logs.
- Someone with a device we've never seen can map its fields with `fields`, `diff` and `watch`, then send a PR that adds a typed view.
- `docs/api-docs.md` becomes the reference other projects link to.

## Risks and principles

- **Unofficial and fragile.** Pentair can change endpoints, headers or the app client ID at any time. All constants live in `internal/api`, are confirmed by an e2e smoke test, and failures produce clear errors rather than silent zeros.
- **Terms of use.** Pentair's app terms prohibit reverse engineering. The project documents only what's needed to control the user's own equipment, makes no attempt to bypass server-side limits, and stays a polite client.
- **Physical consequences.** These are pumps, heaters and chlorinators. Defaults are conservative: no write without an explicit command, debounced program starts, and confirm-then-report rather than optimistic success.
