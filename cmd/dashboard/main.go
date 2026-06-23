package main

import (
	"log/slog"
	"net/http"
	"os"

	"control-room/internal/config"
	"control-room/internal/dashboard"
	"control-room/internal/store"
)

func main() {
	root := os.Getenv("CONTROL_ROOM_WORKSPACE")
	if root == "" {
		root = config.DefaultWorkspace()
	}
	if err := dashboard.LoadTemplates(); err != nil {
		slog.Error("failed to load templates", "err", err)
		os.Exit(1)
	}
	st := store.New(root)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}
	mux := dashboard.New(st)
	slog.Info("control-room dashboard listening", "addr", "http://localhost:"+port, "workspace", root)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}
