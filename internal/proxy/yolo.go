package proxy

import "sync/atomic"

var yoloMode atomic.Bool

func SetYOLO(enabled bool) {
	yoloMode.Store(enabled)
}

func YOLOEnabled() bool {
	return yoloMode.Load()
}
