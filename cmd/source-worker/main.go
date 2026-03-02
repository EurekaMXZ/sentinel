package main

import "sentinel/internal/app"

func main() {
	app.Main("source-worker", app.RunSourceWorker)
}
