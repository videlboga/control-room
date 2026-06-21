package main

import (
	"fmt"
	"os"
)

// cr-worker is the worker daemon for control-room.
//
// Responsibilities:
//   - Register with the control plane on startup.
//   - Periodically send heartbeats (status, capacity, load).
//   - Accept dispatch requests (SSH or HTTP) and execute cr run start locally.
//   - Stream run events/logs back to the control plane.
//
// CLI:
//   cr-worker --control-plane http://control:8080 --node-id capalin
//   cr-worker run --task <task-id>        # execute one run locally (called by control-plane via SSH)
//   cr-worker status                      # print node capacity
//
// For the MVP this file is a stub; the control-plane still falls back to local execution.

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: cr-worker [run|status|serve]")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "run":
		fmt.Println("cr-worker run: not yet implemented")
	case "status":
		fmt.Println("cr-worker status: not yet implemented")
	case "serve":
		fmt.Println("cr-worker serve: not yet implemented")
	default:
		fmt.Printf("unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
