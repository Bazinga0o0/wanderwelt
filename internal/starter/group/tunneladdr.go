package group

import (
	"net"
	"strings"
)

// DefaultTunnelPort ist der Steuerport des Tunnel-Servers (tunneld), wenn die
// Adresse in der Gruppendatei keinen Port nennt.
const DefaultTunnelPort = "7443"

// TunnelAddr liefert die Adresse des Tunnel-Servers als host:port. Der
// tunnel-Client verlangt einen Port („missing port in address“); fehlt er
// (zum Beispiel weil in der Einrichtung nur „twebl.de“ eingegeben wurde),
// kommt DefaultTunnelPort dazu. Leere Adressen bleiben leer.
func TunnelAddr(server string) string {
	s := strings.TrimSpace(server)
	if s == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(s); err == nil {
		return s
	}
	return net.JoinHostPort(s, DefaultTunnelPort)
}
