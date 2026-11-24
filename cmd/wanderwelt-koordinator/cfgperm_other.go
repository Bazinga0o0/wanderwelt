//go:build !unix

package main

// checkConfigFile: Besitzer und Rechte prüft „mitglied“ nur unter Unix
// (dort läuft es als root auf dem Server).
func checkConfigFile(string) error { return nil }
