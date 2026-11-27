package client

import "sync/atomic"

// Die Version dieses Starters geht mit Lebenszeichen und Bereitschaft an den
// Koordinator (api.HeartbeatRequest.StarterVersion), damit Admins im Reiter
// „Zustand“ sehen, wer eine ältere Version hat. Gesetzt wird sie einmal beim
// Start (App.New); leer: nicht senden.
var starterVersion atomic.Value // string

// SetStarterVersion setzt die Version, die der Client mitsendet.
func SetStarterVersion(v string) { starterVersion.Store(v) }

// StarterVersion liefert die gesetzte Version ("" ohne).
func StarterVersion() string {
	v, _ := starterVersion.Load().(string)
	return v
}
