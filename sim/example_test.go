// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim_test

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cB-Sigma/piwebapi-sim/sim"
)

// Serve one Data Archive with a point, and list the Data Archives.
func Example() {
	s := sim.NewPIServer("PISRV01", "demo", "demo-pw")
	defer s.Close()
	s.AddPoint("TANK1.LEVEL", "m", "tank level", sim.PIValue{Time: time.Now().Add(-time.Minute), Value: 3.2})

	req, _ := http.NewRequest(http.MethodGet, s.Base()+"/dataservers", nil)
	req.SetBasicAuth("demo", "demo-pw")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	fmt.Println(resp.StatusCode)
	// Output: 200
}
