# Pentair Cloud API — Research Notes

_Compiled 2026-11-25 from open-source reverse-engineering projects. There is **no official, documented public API**; everything below comes from traffic captures and decompiles of the Pentair Home mobile app (`com.pentair.pentairhome`, v4.2.18 per one project). Pentair's app T&Cs prohibit reverse engineering, and the API can change without notice._

## 1. Architecture at a glance

```
email + password
   │  Cognito User Pool, SRP auth (USER_SRP_AUTH)
   ▼
IdToken / AccessToken / RefreshToken  (JWT, ~1 h; refresh via REFRESH_TOKEN_AUTH)
   │  Cognito Identity Pool: GetId → GetCredentialsForIdentity
   ▼
temporary AWS STS creds (AccessKeyId / SecretKey / SessionToken, ~1 h, independent expiry)
   │
   ├─► REST: https://api.pentair.cloud  (API Gateway, SigV4, service "execute-api", us-west-2)
   │         + header x-amz-id-token: <IdToken>
   │
   └─► WebSocket: wss://g44t970cbi.execute-api.us-west-2.amazonaws.com/prod?token=<AccessToken>
             real-time per-field deltas
```

Devices are AWS IoT things behind the scenes (`arn:aws:iot:us-west-2:...` appears in device records); clients never talk to IoT directly — everything goes through the API Gateway.

## 2. Auth constants (embedded in the app; identical across every project)

| Item | US value |
|---|---|
| Region | `us-west-2` |
| User Pool ID | `us-west-2_lbiduhSwD` |
| App Client ID (no secret) | `3de110o697faq7avdchtf07h4v` |
| Identity Pool ID | `us-west-2:6f950f85-af44-43d9-b690-a431f753e9aa` |
| Logins key | `cognito-idp.us-west-2.amazonaws.com/us-west-2_lbiduhSwD` |
| EU User Pool (from decompile, untested) | `eu-west-1_rsBGJFuRd` (region `eu-west-1`) |
| Unauth API key (pre-login only) | `p4u60la7xH7q4aat3Tzrq9BfspTOslIs6oXzIhuS` |

Notes:
- `pycognito` handles SRP; anything that speaks Cognito SRP works (Amplify, `aws-sdk`, Go `cognitosrp`).
- **Token and STS expiries are independent.** Refresh the JWT when near `exp`, and re-mint STS creds when near their `Expiration` *or* whenever the IdToken changes. Several projects had bugs from treating these as one clock.
- Expired creds return 401/403 with a JSON `message` containing "token … expired".

## 3. Request signing

Standard AWS SigV4, service `execute-api`, region `us-west-2`, plus:

| Header | Value |
|---|---|
| `x-amz-id-token` | Cognito IdToken (required) |
| `x-amz-security-token` | STS session token |
| `user-agent` | `aws-amplify/4.3.10 react-native` (mimics the app; optional) |
| `content-type` | `application/json; charset=UTF-8` (for bodies) |
| `iseuropeanuser` | `false` (sent and signed by the current app) |
| `x-pha-apptype` | `pool` for reads; empty string on device PUTs (current app) |

boto3's `SigV4Auth` / `requests_aws4auth` work fine for the older `device/` endpoints. The IntelliConnect project hand-rolls SigV4 to reproduce the app's exact signed-header set (`host;iseuropeanuser;x-amz-date;x-amz-id-token;x-amz-security-token;x-pha-apptype`) — it's unclear whether the gateway strictly requires that, but it's the safest match.

## 4. REST endpoints (base `https://api.pentair.cloud`)

| Method | Path | Purpose / body |
|---|---|---|
| POST | `/user2/user2-service/unauth/detectUser` | Pre-login. Header `x-api-key: <unauth key>`, body `{"email": "..."}` → `response.userExists` |
| GET | `/user/user-service/common/profile` | User profile |
| GET | `/user/user-service/common/profilev2` | User profile (v2) |
| GET | `/device/device-service/user/devices` | List devices (v1). `data[]` with `deviceId`, `deviceType`, `status` (`ACTIVE`…), `pname`, `productInfo.nickName` |
| GET | `/device2/device2-service/user/listdevices` | List devices (v2, used by current app). Includes `arn`, `devices` component map, etc. |
| GET | `/device/device-service/user/device/{deviceId}` | Single device state (v1) |
| POST | `/device2/device2-service/user/device` | **Bulk state snapshot.** Body `{"deviceIds": ["id1", ...]}` — must be the plural key + list; singular silently returns `data: []`. Response `response.data[]` each with `deviceId`, `online`, `alarm`, `fwVersion`, `fields{}` |
| PUT | `/device/device-service/user/device/{deviceId}` | **Write fields.** Body `{"payload": {"<field>": "<string value>", ...}}`. All values are strings. Success → `data.code == "set_device_success"` |

### Field record shape

```json
"fields": {
  "ra0": {"name": "Relay1_Manual_Schedule", "value": "2", "min": "0", "max": "4", ...},
  "s18": {"value": "1392", ...}
}
```

Field codes are **per device type** — the same code (e.g. `d1`, `p1`) means different things on different products.

## 5. WebSocket (real-time push)

- URL: `wss://g44t970cbi.execute-api.us-west-2.amazonaws.com/prod?token=<AccessToken>`
- After connect, subscribe per device:
  ```json
  {"action": "registerEvent",
   "body": {"device_ids": "<deviceId>", "active_screen": "poolScreen", "token": "<AccessToken>"}}
  ```
  `active_screen` values seen: `poolScreen` (dashboard telemetry/setpoints), `product` (device-detail fields). Pushed field sets differ by screen — run two connections to get the union.
- Client sends the literal text `keep-alive` periodically (~1 s in one implementation).
- While on a `product` screen the app PUTs `{"payload":{"appuse":"1"}}` to the device every ~60 s, which appears to keep high-rate pushes flowing.
- Incoming: `{"event_type": "device_data", "deviceId": "...", "data": {"fields": {...}}}` — **deltas only**; merge into the last REST snapshot. Keep a REST poll (~60 s) as fallback.

## 6. Device types and known field maps

| deviceType | Product | Best source |
|---|---|---|
| `IF31` | IntelliFlo 3 VSF Wi-Fi pump | pypentair, SPD13/pentair_cloud |
| `PIF0` | IntelliConnect controller | tikotzky/homeassistant-pentair-intelliconnect |
| `PLC1` | Color Sync light controller (618031) | shad0wca7/hacs-pentair |
| `SSS1`, `PPA0` | Sump-pump / battery backup products | pypentair (sensor-only) |
| `ICT1`, `WQM1` | IntelliCenter, ChemCheck | mentioned; field codes not mapped |

### IF31 — IntelliFlo 3 pump
- Status: `s14` active program (0-based), `s17` pressure (÷100 psi per SPD13; pypentair uses ÷10), `s18` power W, `s19` motor speed (÷10 %), `s20` alarm bitfield, `s25` pump enabled, `s26` flow (÷10 GPM), `s28` remaining time, `s13` RSSI.
- Config: `d2/d18` max/min speed, `d3/d19` max/min flow, `d27` nickname, `d17`/`d42` timezone. **Sensitive:** `s6` MAC, `s11` security key, `s23` SSID, `d15` password — redact.
- Programs 1–14: `zp{N}e{M}` — `e2` name, `e5` type (0 schedule / 1 interval / 2 manual), `e6` start time, `e7` duration, `e8` days, `e10` enable, `e13` exists.
- **Start program N:** PUT `{"zpNe10":"3"}`, then `{"p2":"99"}`. **Stop:** `{"zpNe10":"2"}`, then `{"p2":"<N-1>"}`. Switching programs directly (no stop first) avoids a ~30 s pump restart.

### PIF0 — IntelliConnect
- Pump relay: `ra0` state (0 off / 1 on-manual / 2 schedule-armed / 3 on-override / 4 timer-done), `ra1`/`ra2` schedule start/end (UTC seconds-of-day), `ra4` relay power W (use this for "is running"), `ras0` timer countdown, `p1` egg timer.
- Heater: `htd1` mode (write `1` = on/auto, firmware shows `3` when firing; `0` off, write `htd13` with it), `htd2` setpoint (tenths °F), `htd14` cooldown s.
- IntelliChlor: `icd1` output %, `icd2`/`icd3` boost RPM/timer (one-shot), `ics1` actual %, `ics2` salt ppm, `ics3` error, `ics9` cell temp, `ics13` boost remaining, `ics15` hours.
- Temps: `t0` water °F, `t1` air °F (integers).

### PLC1 — Color Sync
- `d13` power 0/1; `d1` show/color 0–4, 7–13 (Red, White, Magenta, Green, Blue, SAm, Party, Romance, Caribbean, American, Sunset, Royal); `d1=5` Hold, `d1=6` Recall.

## 7. Practical gotchas

- Rate-limit yourself: existing clients poll ≥60 s and debounce program starts (≥30 s).
- Writes are eventually consistent — confirm via the next WS delta/poll rather than trusting the PUT response.
- `logout()` via Cognito `GlobalSignOut` signs out **all** clients, including the phone app.
- Some PLC1 device-time fields don't decode with the known scheme; don't trust them.
- Schedules are stored in UTC; convert with the home's timezone.

## 8. Alternatives to the cloud

- **IntelliCenter:** local WebSocket on port 6680 / raw TCP on 6681 (JSON protocol) — `pyintellicenter`, `node-intellicenter`, HA core-adjacent integrations.
- **ScreenLogic:** local protocol, built into Home Assistant.
- **RS-485:** `nodejs-poolController`, ESPHome pool controller — direct bus control, no cloud.
- IntelliFlo 3 Wi-Fi and IntelliConnect appear to be cloud-only (no documented local API).

## 9. Reference implementations

| Repo | Lang | Scope |
|---|---|---|
| [natekspencer/pypentair](https://github.com/natekspencer/pypentair) | Python | Base library (auth + v1 device endpoints, IF31 field names) |
| [natekspencer/hacs-pentair](https://github.com/natekspencer/hacs-pentair) | Python | HA integration on pypentair |
| [shad0wca7/hacs-pentair](https://github.com/shad0wca7/hacs-pentair) | Python | Fork: Color Sync control, correct token/STS expiry handling |
| [SPD13/pentair_cloud](https://github.com/SPD13/pentair_cloud) | Python | IF31 pump programs start/stop, telemetry scaling |
| [tikotzky/homeassistant-pentair-intelliconnect](https://github.com/tikotzky/homeassistant-pentair-intelliconnect) | Python | Most complete: device2 endpoints, hand-rolled SigV4, WebSocket, PIF0 fields |
| [ThomasLomas/ha-pentairhome-addon](https://github.com/ThomasLomas/ha-pentairhome-addon) | Go | HA add-on → MQTT; Go client for listdevices/profile/device2 |
| [CZX6/Pentair-Color-Sync-Bridge](https://github.com/CZX6/Pentair-Color-Sync-Bridge) | — | REST bridge for Color Sync |
| [lovintexas/udi-pentair-pg3x](https://github.com/lovintexas/udi-pentair-pg3x) | — | UDI Polyglot plugin (Color Sync + VF pump) |
| `homebridge-pentair-cloud` (npm) | JS | Homebridge plugin, same Cognito SRP → SigV4 flow |
