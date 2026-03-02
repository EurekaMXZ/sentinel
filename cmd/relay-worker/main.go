package main

import "sentinel/internal/app"

func main() {
	app.Main("relay-worker", app.RunRelayWorker)
}
