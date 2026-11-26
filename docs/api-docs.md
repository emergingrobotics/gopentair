# Pentair Home Cloud API: Reference as Implemented

This document lists what gopentair implements and what each fact rests on.

**Status key:**

| Mark | Meaning |
|---|---|
| ✅ | verified against a live account |
| 🟡 | consistent across several independent community implementations, not yet verified by us |
| ❓ | single source or inferred |

**Nothing is ✅ yet.** Run `make test-e2e` and `pentair --debug ...` against a real account, then update this file.

## Authentication

| Item | Value | Status |
|---|---|---|
| Region | `us-west-2` | 🟡 |
| User pool | `us-west-2_lbiduhSwD` | 🟡 |
| App client (no secret) | `3de110o697faq7avdchtf07h4v` | 🟡 |
| Identity pool | `us-west-2:6f950f85-af44-43d9-b690-a431f753e9aa` | 🟡 |
| EU user pool | `eu-west-1_rsBGJFuRd` (client and identity IDs unknown) | ❓ |
| Login flow | `USER_SRP_AUTH` → `PASSWORD_VERIFIER` → `RespondToAuthChallenge` | 🟡 |
| Refresh | `REFRESH_TOKEN_AUTH` (no new refresh token returned) | 🟡 |
| AWS credentials | `GetId` + `GetCredentialsForIdentity`, Logins key `cognito-idp.us-west-2.amazonaws.com/<pool>` = IdToken | 🟡 |
| Token lifetimes | JWT ~1 h (from `exp`); refresh token likely 30 d (Cognito default); AWS credentials ~1 h | ❓ |

The SRP math is byte-identical to pycognito (`testdata/reference_vectors.json`).

## Request signing

- Signing: SigV4, service `execute-api`, region `us-west-2`. 🟡
- Required header: `x-amz-id-token: <IdToken>`. 🟡
- Session token header: `x-amz-security-token`. 🟡
- **App profile** (default): signs exactly `host;iseuropeanuser;x-amz-date;x-amz-id-token;x-amz-security-token;x-pha-apptype`. It sends `iseuropeanuser: false` and `x-pha-apptype: pool` on reads, and an empty `x-pha-apptype` on device PUTs. ❓ (from one capture of app v4.2.18)
- **Standard profile**: botocore-equivalent; signs every header except user-agent and similar. Older community clients use this successfully. 🟡 The Standard output is byte-identical to botocore (`testdata/reference_vectors.json`).
- Expired credentials come back as 401/403 whose `message` contains "expired". The client re-mints credentials and retries once. 🟡

## REST endpoints (`https://api.pentair.cloud`)

| Method | Path | Body / response | Status |
|---|---|---|---|
| GET | `/device2/device2-service/user/listdevices` | → `{"response":[device...],"allDevicesCount":n}` | 🟡 |
| GET | `/device/device-service/user/devices` | → `{"data":[device...]}` (fallback when v2 returns 400/404/405 or an unknown shape) | 🟡 |
| POST | `/device2/device2-service/user/device` | `{"deviceIds":[...]}` → `{"response":{"code","data":[state...]}}`. A singular `deviceId` key silently returns `[]`. | 🟡 |
| GET | `/device/device-service/user/device/{id}` | → `{"data":{state}}` (not used by the client) | ❓ |
| PUT | `/device/device-service/user/device/{id}` | `{"payload":{"code":"value"}}`, values always strings → `{"data":{"code":"set_device_success"}}` | 🟡 |
| GET | `/user/user-service/common/profilev2` (falls back to `/profile`) | → `{"data":{...}}` | ❓ |
| POST | `/user2/user2-service/unauth/detectUser` | `x-api-key` header, `{"email"}` (not implemented) | ❓ |

State record: `deviceId`, `deviceType`, `online`, `alarm`, `fwVersion`, and `fields: {code: {name, value, min, max}}`.

## WebSocket

| Item | Value | Status |
|---|---|---|
| URL | `wss://g44t970cbi.execute-api.us-west-2.amazonaws.com/prod?token=<AccessToken>` | ❓ |
| Subscribe | `{"action":"registerEvent","body":{"device_ids":"<id>","active_screen":"poolScreen"\|"product","token":"<AccessToken>"}}` | ❓ |
| Keep-alive | text frame `keep-alive`. The app sends one about every 1 s; we send one every 10 s. | ❓ |
| Presence | PUT `{"appuse":"1"}` every 60 s while on `product` | ❓ |
| Push | `{"event_type":"device_data","deviceId","screen_type","data":{"fields":{...}}}`. Pushes are deltas only. | ❓ |

## Device types and fields

### IF31: IntelliFlo 3

Status fields:

| Field | Meaning | Status |
|---|---|---|
| `s18` | Power, W | 🟡 |
| `s19` | Speed, ÷10 % | 🟡 |
| `s26` | Flow, ÷10 GPM | 🟡 |
| `s17` | Pressure, ÷100 psi (pypentair uses ÷10) | ❓ |
| `s14` | Active program, 0-based | 🟡 |
| `s20` | Alarm bits | 🟡 |
| `s13` | RSSI | 🟡 |

Programs: `zp{N}e{M}` for N = 1–14.

| Element | Meaning |
|---|---|
| `e2` | Name |
| `e5` | Type: 0 schedule, 1 interval, 2 manual |
| `e10` | Enable |
| `e13` | Exists |

Program writes: 🟡

| Action | Writes |
|---|---|
| Start | `zpNe10=3`, then `p2=99` |
| Stop | `zpNe10=2`, then `p2=N-1` |

Switching programs directly, without stopping the running one first, avoids a restart of about 30 s. ❓

Sensitive fields (always redacted): `s6`, `s11`, `s23`, `d15`.

### PIF0: IntelliConnect

The pump relay state lives in `ra0`. 🟡

| `ra0` | Meaning |
|---|---|
| 0 | off |
| 1 | manual on |
| 2 | schedule enabled |
| 3 | on, schedule enabled |
| 4 | schedule enabled, timer done |

Writes to `ra0` preserve the other bit (schedule or override).

Other fields:

| Field | Meaning | Status |
|---|---|---|
| `ra4` | Relay power, W. Shows whether the pump is actually running. | 🟡 |
| `ra1`, `ra2` | Schedule start and stop, UTC seconds of day | 🟡 |
| `htd1` | Heater mode: 0 off, 1 auto/idle, 3 heating. Write 1 to turn on. Write 0 together with `htd13=0` to turn off. | 🟡 |
| `htd2` | Heater setpoint, tenths of °F | 🟡 |
| `htd14` | Heater cooldown, s | 🟡 |
| `icd1` | Chlorinator output setpoint | 🟡 |
| `ics1` | Chlorinator actual output | 🟡 |
| `ics2` | Salt, ppm | 🟡 |
| `ics13` | Boost remaining | 🟡 |
| `t0` | Water temperature, whole °F | 🟡 |
| `t1` | Air temperature, whole °F | 🟡 |
| `ifs3`, `ifs4`, `ifs6` | Variable-speed pump power, speed and flow | ❓ |

### PLC1: Color Sync

| Field | Meaning | Status |
|---|---|---|
| `d13` | Power, 0/1 | 🟡 |
| `d1` | Show: 0 red, 1 white, 2 magenta, 3 green, 4 blue, 7 SAm, 8 party, 9 romance, 10 Caribbean, 11 American, 12 sunset, 13 royal | 🟡 |
| `d1` = 5 / 6 | Hold / recall commands | 🟡 |

## Open questions

1. Is the App signing profile required, or is Standard accepted everywhere?
2. What are the real token, refresh-token and credential lifetimes?
3. Does an open WebSocket survive access-token expiry? (The client rotates connections before expiry either way.)
4. What is the minimum keep-alive cadence?
5. Are the v1 and v2 device lists equivalent?
6. IF31 `s17` scale: ÷100 or ÷10?
7. Is there a rate limit, and what does a 429 look like?
8. What are the EU client and identity pool IDs?
9. Does `set_device_success` mean the device applied the change, or only that the cloud queued it?
10. What units do the IF31 program fields `e6`, `e7`, `e8` and `s28` use?
