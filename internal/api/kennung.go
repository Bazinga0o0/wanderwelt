package api

import (
	"cmp"
	"strings"
)

// Vergleiche über die pseudonyme Kennung (Datenschutz: fremde E-Mail-Adressen
// bekommt kein Starter mehr). Ältere Koordinatoren kennen die Kennung nicht
// und liefern die Adresse; dann gilt weiter sie.

// IstIch meldet, ob h der Host ist, der me ist: nach Kennung (beide
// vorhanden), sonst nach E-Mail-Adresse (älterer Koordinator, oder: Der
// Koordinator nennt die Adresse nur dem Host selbst, ein Fremder sieht sie
// leer, und leer ist nie gleich).
func (h *HostInfo) IstIch(me *Me) bool {
	if h == nil || me == nil {
		return false
	}
	if h.ID != "" && me.ID != "" {
		return h.ID == me.ID
	}
	return h.Email != "" && me.Email != "" && strings.EqualFold(h.Email, me.Email)
}

// vergleiche vergleicht a und b nach Kennung, sonst nach E-Mail-Adresse,
// sonst nach Minecraft-Namen; ok false, wenn sich nichts vergleichen lässt.
func vergleiche(a, b HostInfo) (gleich, ok bool) {
	switch {
	case a.ID != "" && b.ID != "":
		return a.ID == b.ID, true
	case a.Email != "" && b.Email != "":
		return strings.EqualFold(a.Email, b.Email), true
	case a.MinecraftName != "" && b.MinecraftName != "":
		return strings.EqualFold(a.MinecraftName, b.MinecraftName), true
	}
	return false, false
}

// GleicherHost meldet, ob a und b dasselbe Mitglied beschreiben (Beobachten
// des Hosts: „noch derselbe?“). Lässt sich nichts vergleichen, gilt es als
// gleich.
func GleicherHost(a, b HostInfo) bool {
	g, ok := vergleiche(a, b)
	return g || !ok
}

// IstDasselbe meldet, ob a und b sicher dasselbe Mitglied beschreiben (Ziel
// einer Übergabe); ohne Vergleichsmöglichkeit false.
func IstDasselbe(a, b HostInfo) bool {
	g, ok := vergleiche(a, b)
	return g && ok
}

// Schluessel ist ein stabiler Vergleichswert für h: die Kennung, sonst die
// kleingeschriebene E-Mail-Adresse, sonst "" (zum Erkennen von Wechseln).
func (h *HostInfo) Schluessel() string {
	if h == nil {
		return ""
	}
	return cmp.Or(h.ID, strings.ToLower(h.Email))
}

// Key ist der Schlüssel des Mitglieds für Aktionen (Rolle, Minecraft-Name):
// die Kennung, bei einem älteren Koordinator die Adresse.
func (m MemberInfo) Key() string { return cmp.Or(m.ID, m.Email) }

// IstIch meldet, ob m das Mitglied me ist.
func (m MemberInfo) IstIch(me *Me) bool {
	if me == nil {
		return false
	}
	if m.ID != "" && me.ID != "" {
		return m.ID == me.ID
	}
	return m.Email != "" && me.Email != "" && strings.EqualFold(m.Email, me.Email)
}

// Key ist der Schlüssel der Anfrage für Annehmen und Ablehnen: die Kennung,
// bei einem älteren Koordinator die Adresse.
func (r JoinRequestInfo) Key() string { return cmp.Or(r.ID, r.Email) }
