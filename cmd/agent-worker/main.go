package main

import "sentinel/internal/app"

func main() {
	app.Main("agent-worker", app.RunAgentWorker)
}
