// Command pump-program starts an IntelliFlo 3 program by number or name,
// waits until the pump reports it running, then prints the telemetry.
//
//	go run ./examples/pump-program Cleaning          # dry run
//	go run ./examples/pump-program Cleaning -apply   # really start it
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/emergingrobotics/gopentair"
)

func main() {
	apply := flag.Bool("apply", false, "send the write (default: dry run)")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: pump-program [-apply] <program number|name>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	c, err := gopentair.NewClient(gopentair.WithEnvConfig(), gopentair.WithDefaultTokenFile())
	if err != nil {
		log.Fatal(err)
	}
	dev, err := c.ResolveDevice(ctx, "", gopentair.TypeIntelliFlo3)
	if err != nil {
		log.Fatal(err)
	}
	pump := c.Pump(dev.ID)
	plan, err := pump.StartPlan(ctx, flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(plan)
	if !*apply {
		fmt.Println("(dry run; pass -apply to send)")
		return
	}
	res, err := c.Apply(ctx, plan, gopentair.WithConfirmDefault())
	if err != nil {
		log.Fatal(err)
	}
	if res.State == nil {
		fmt.Println("write acknowledged; no state observed")
		return
	}
	st := gopentair.DecodePump(*res.State)
	fmt.Printf("confirmed=%v power=%dW speed=%.1f%%\n", res.Confirmed, deref(st.PowerW), derefF(st.SpeedPct))
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefF(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
