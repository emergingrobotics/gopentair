# gopentair

A Go SDK and CLI (`pentair`) for the **Pentair Home cloud API**: the service behind the Pentair Home mobile app. It works with IntelliFlo 3 pumps, IntelliConnect controllers and Color Sync lights, and reads raw fields from any other Pentair Home device.

> **Unofficial.** Pentair publishes no API. Everything here comes from reverse-engineering projects (see [docs/research.md](docs/research.md)). Pentair can change the API at any time, and its app terms prohibit reverse engineering. Use this to control your own equipment, gently.

## Features

- **Hands-off auth.** Cognito SRP login, then identity-pool AWS credentials, then SigV4-signed requests. The JWTs and the AWS credentials are refreshed on their independent expiry clocks, and the CLI caches the session (never the password) in a `0600` file.
- **Raw fields for every device**, plus typed views for:
  - IntelliFlo 3 (`IF31`): power, speed, flow, pressure, programs, start/stop.
  - IntelliConnect (`PIF0`): filter pump, daily schedule, heater, IntelliChlor, water and air temperature.
  - Color Sync (`PLC1`): power, 12 shows, hold and recall.
- **Safe writes.** Every change is a `WritePlan` that can be previewed with `--dry-run`. Pump program starts are debounced, and writes can wait until the device reports the change. Writes with an unknown outcome are never retried blindly.
- **Real-time updates.** `Watch` merges WebSocket pushes into full device state, reconnects with backoff, takes a REST snapshot after each reconnect, opens a new connection before the token expires, and falls back to REST polling.
- **Exploration tools** for unmapped hardware: `pentair fields`, `pentair diff` and `pentair watch`.
- **Redacted debug logging.** Tokens, credentials, device IDs, email addresses, Wi-Fi details and pump passwords never reach the logs.

## Install

```bash
go install github.com/emergingrobotics/gopentair/cmd/pentair@latest   # CLI
go get github.com/emergingrobotics/gopentair                          # library
```

## CLI quick start

```bash
export PENTAIR_USERNAME=you@example.com
pentair login                      # prompts for the password; caches the session
pentair devices list
pentair status -d "Main Pump"      # by ID, nickname or configured alias
pentair status -d lights --json    # decoded + raw fields
```

If the account has exactly one device of the right type, typed commands find it on their own:

```bash
pentair pump status
pentair pump programs
pentair pump start Cleaning --dry-run      # show the plan
pentair pump start Cleaning                # send it and wait for confirmation
pentair pump stop 2

pentair pool status
pentair pool pump on|off                   # manual override; keeps the schedule setting
pentair pool schedule --enable --start 08:00 --stop 16:00   # local time; stored as UTC
pentair pool heater heat --setpoint 84
pentair pool chlorine 40

pentair light on
pentair light show caribbean               # see: pentair light shows
pentair light hold | recall
```

Exploring a device the SDK doesn't model yet:

```bash
pentair fields -d <id> --filter s1,zp1     # every field, naturally sorted
pentair diff -d <id>                       # snapshot; change a setting in the app; press Enter
pentair watch -d <id> --ndjson             # live deltas
pentair fields set -d <id> d13=1           # raw write (asks for confirmation)
```

Global flags:

| Flag | Purpose |
|---|---|
| `--device/-d` | Target device |
| `--json`, `--csv` | Output format |
| `--dry-run` | Show write plans without sending them |
| `--yes` | Skip prompts |
| `--no-confirm` | Don't wait for the device to report the change |
| `--units f\|c\|both` | Temperature units |
| `--debug` | Redacted request logging |
| `--token-file PATH\|none` | Session cache location |
| `--config PATH` | Config file |
| `--region` | Region |
| `--sign-profile` | SigV4 header profile |

Exit codes:

| Code | Meaning |
|---|---|
| 0 | ok |
| 1 | error |
| 2 | usage or invalid value |
| 3 | auth (run `pentair login`) |
| 4 | device offline |
| 5 | write not acknowledged or not confirmed |
| 6 | rate-limited |

### Configuration

Precedence: flags, then environment, then `~/.config/pentair/config.yaml`, then defaults.

```yaml
username: you@example.com
# password: prefer PENTAIR_PASSWORD or the `pentair login` prompt
region: us
default_device: ""
token_file: ~/.config/pentair/tokens.json   # "none" disables caching
timeout: 15s
poll_interval: 60s        # Watch fallback; minimum 30s
sign_profile: app         # app | standard
devices:                  # aliases usable with --device
  pump: <device id>
  lights: <device id>
```

| Variable | Purpose |
|---|---|
| `PENTAIR_USERNAME`, `PENTAIR_PASSWORD` | Account credentials |
| `PENTAIR_REGION` | `us` (default) or `eu` |
| `PENTAIR_DEFAULT_DEVICE` | Device used when `--device` is omitted |
| `PENTAIR_CONFIG` | Config file path |
| `PENTAIR_TOKEN_FILE` | Session cache path, or `none` |
| `PENTAIR_SIGN_PROFILE` | `app` or `standard` |
| `PENTAIR_DEBUG` | Redacted request logging |

## Library quick start

```go
c, err := gopentair.NewClient(gopentair.WithEnvConfig(), gopentair.WithDefaultTokenFile())
if err != nil { log.Fatal(err) }
ctx := context.Background()
if err := c.Login(ctx); err != nil { log.Fatal(err) }

dev, _ := c.ResolveDevice(ctx, "", gopentair.TypeIntelliFlo3)
st, _ := c.State(ctx, dev.ID)
p := gopentair.DecodePump(*st)          // nil pointers mean "not reported"
if p.PowerW != nil { fmt.Println(*p.PowerW, "W") }

plan, _ := c.Pump(dev.ID).StartPlan(ctx, "Cleaning")
fmt.Println(plan)                       // zp2e10=3 ; p2=99
res, err := c.Apply(ctx, plan, gopentair.WithConfirmDefault())

events, _ := c.Watch(ctx, []string{dev.ID}, gopentair.WithFieldFilter("s18"))
for ev := range events { fmt.Println(ev.Source, ev.Changed) }
```

Examples (each run with `go run ./examples/<name>`):

| Example | What it does |
|---|---|
| `status` | Prints a one-line summary of every device |
| `watch` | Streams all changes |
| `pump-program` | Starts a program; dry run by default, `-apply` to send |
| `prometheus` | Serves a `/metrics` exporter, fed by `Watch` |

## Development

```bash
make            # list targets
make test       # unit + integration tests against the in-process fake cloud
make test-race
make test-e2e   # read-only tests against the real cloud (PENTAIR_USERNAME/PASSWORD)
```

The SRP and SigV4 code is checked against reference vectors from pycognito and botocore (`testdata/reference_vectors.json`). The fake cloud in `internal/testutil` runs real server-side SRP and verifies every SigV4 signature.

Documents:

| File | Contents |
|---|---|
| [VISION.md](VISION.md) | Why the project exists and its goals |
| [docs/DESIGN.md](docs/DESIGN.md) | Architecture and design decisions |
| [docs/api-docs.md](docs/api-docs.md) | The API as implemented, with its verification status |
| [docs/research.md](docs/research.md) | The original research |

## Known limitations

- Nothing has been checked against a live account yet. [docs/api-docs.md](docs/api-docs.md) lists the open questions.
- The EU region needs client and identity pool IDs that nobody has mapped yet.
- Cognito MFA and new-password challenges are not supported. They return `ChallengeError`.
- The client-side debounce lives in memory, so it doesn't stop two separate CLI runs from starting programs back to back.
