// Command status prints a one-line summary of every device on the account.
//
//	PENTAIR_USERNAME=... PENTAIR_PASSWORD=... go run ./examples/status
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/emergingrobotics/gopentair"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := gopentair.NewClient(gopentair.WithEnvConfig(), gopentair.WithDefaultTokenFile())
	if err != nil {
		log.Fatal(err)
	}
	if err := c.Login(ctx); err != nil {
		log.Fatal(err)
	}
	devs, err := c.Devices(ctx)
	if err != nil {
		log.Fatal(err)
	}
	ids := make([]string, len(devs))
	names := map[string]string{}
	for i, d := range devs {
		ids[i] = d.ID
		names[d.ID] = d.Name()
	}
	states, err := c.States(ctx, ids...)
	if err != nil {
		log.Fatal(err)
	}
	for _, st := range states {
		name := names[st.DeviceID]
		switch st.Type {
		case gopentair.TypeIntelliFlo3:
			p := gopentair.DecodePump(st)
			fmt.Printf("%-20s pump   running=%v power=%s\n", name, p.Running(), show(p.PowerW))
		case gopentair.TypeIntelliConnect:
			p := gopentair.DecodePool(st)
			fmt.Printf("%-20s pool   pump=%v water=%s°F heater=%s\n", name, p.PumpRunning, show(p.WaterTempF), p.HeaterName)
		case gopentair.TypeColorSync:
			l := gopentair.DecodeLight(st)
			fmt.Printf("%-20s lights on=%v show=%s\n", name, l.On != nil && *l.On, l.ShowName)
		default:
			fmt.Printf("%-20s %s   %d fields (no typed view)\n", name, st.Type, len(st.Fields))
		}
	}
}

func show(p *int64) string {
	if p == nil {
		return "?"
	}
	return fmt.Sprint(*p)
}
