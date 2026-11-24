//go:build !unix

package config

import "os"

// copyOwner: außerhalb von Unix gibt es keinen Besitzer zu übernehmen.
func copyOwner(string, os.FileInfo) error { return nil }
