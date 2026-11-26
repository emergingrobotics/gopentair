// Command watch streams live changes for every device until interrupted.
//
//	go run ./examples/watch
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/emergingrobotics/gopentair"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	c, err := gopentair.NewClient(gopentair.WithEnvConfig(), gopentair.WithDefaultTokenFile())
	if err != nil {
		log.Fatal(err)
	}
	devs, err := c.Devices(ctx)
	if err != nil {
		log.Fatal(err)
	}
	var ids []string
	for _, d := range devs {
		ids = append(ids, d.ID)
	}
	events, err := c.Watch(ctx, ids)
	if err != nil {
		log.Fatal(err)
	}
	for ev := range events {
		for _, code := range ev.Changed {
			f := ev.State.Fields[code]
			fmt.Printf("%s %-6s %s %s=%s %s\n", ev.At.Format("15:04:05"), ev.Source, ev.DeviceID[:min(8, len(ev.DeviceID))], code, f.Value, f.Name)
		}
	}
}
