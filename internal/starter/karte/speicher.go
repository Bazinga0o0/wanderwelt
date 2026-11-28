package karte

import "container/list"

// lru ist ein Zwischenspeicher fester Größe: Wird er voll, fliegt der am
// längsten nicht benutzte Eintrag raus. Nicht für mehrere Goroutinen
// gedacht; der Aufrufer sperrt.
type lru[K comparable, V any] struct {
	grenze int
	liste  *list.List // vorn: zuletzt benutzt
	index  map[K]*list.Element
}

type lruEintrag[K comparable, V any] struct {
	k K
	v V
}

func neuerLRU[K comparable, V any](grenze int) *lru[K, V] {
	return &lru[K, V]{grenze: max(grenze, 1), liste: list.New(), index: map[K]*list.Element{}}
}

// holen liefert den Eintrag und macht ihn zum jüngsten.
func (c *lru[K, V]) holen(k K) (V, bool) {
	e, ok := c.index[k]
	if !ok {
		var v V
		return v, false
	}
	c.liste.MoveToFront(e)
	return e.Value.(*lruEintrag[K, V]).v, true
}

// setzen legt den Eintrag an oder ersetzt ihn; über die Grenze hinaus
// verdrängt es die ältesten.
func (c *lru[K, V]) setzen(k K, v V) {
	if e, ok := c.index[k]; ok {
		e.Value.(*lruEintrag[K, V]).v = v
		c.liste.MoveToFront(e)
		return
	}
	c.index[k] = c.liste.PushFront(&lruEintrag[K, V]{k, v})
	for c.liste.Len() > c.grenze {
		hinten := c.liste.Back()
		delete(c.index, hinten.Value.(*lruEintrag[K, V]).k)
		c.liste.Remove(hinten)
	}
}

func (c *lru[K, V]) loeschen(k K) {
	if e, ok := c.index[k]; ok {
		c.liste.Remove(e)
		delete(c.index, k)
	}
}

func (c *lru[K, V]) laenge() int { return c.liste.Len() }
