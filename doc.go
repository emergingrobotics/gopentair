// Package gopentair is a client for the Pentair Home cloud API, the
// unofficial, reverse-engineered service behind the Pentair Home mobile
// app (IntelliFlo 3 pumps, IntelliConnect controllers, Color Sync lights).
//
// Authentication follows the app: a Cognito SRP login yields JWTs, the
// IdToken is exchanged for temporary AWS credentials through a Cognito
// identity pool, and every REST call is SigV4-signed. The client refreshes
// both on their independent expiry clocks; callers never handle tokens.
//
// Every device is a map of string-valued field codes (DeviceState). Typed
// views (DecodePump, DecodePool, DecodeLight) are pure decoders over that
// map, and writes are expressed as WritePlans that can be previewed
// (WithDryRun), debounced and confirmed against the device state.
//
//	c, _ := gopentair.NewClient(gopentair.WithEnvConfig())
//	_ = c.Login(ctx)
//	devs, _ := c.Devices(ctx)
//	st, _ := c.State(ctx, devs[0].ID)
//	fmt.Println(gopentair.DecodePump(*st).PowerW)
//
// Pentair does not publish this API and may change it at any time.
package gopentair
