//! LOD-Pyramide: grobe Stufen werden aus den feineren abgeleitet (2×2
//! Pixel gemittelt), nie neu gerendert.
//!
//! Stufe k hat 1 Pixel je 2^k Blöcke; eine Kachel hat 256×256 Pixel und
//! deckt damit 256·2^k Blöcke. Stufe 0 ist die Farbkarte (1 Pixel je Block).
//!
//! Der Aufbau läuft rekursiv in Z-Ordnung über Quadrate aus 2^k × 2^k
//! Regionen: Ein Knoten liefert ein Bild von 512×512 Pixeln, das aus den vier
//! Kindern entsteht. So liegen nie mehr als wenige Bilder gleichzeitig im
//! Speicher (Tiefe × 4 MiB), egal wie groß die Welt ist, und die Kinder
//! laufen parallel auf Rayon.

use rayon::prelude::*;

/// Kantenlänge einer Kachel in Pixeln.
pub const TILE: usize = 256;
/// Kantenlänge eines Knotenbildes (eine Region bei Stufe 0).
pub const NODE: usize = 512;

/// Eine fertige Kachel.
pub struct Tile<'a> {
    pub level: u8,
    pub tx: i32,
    pub tz: i32,
    /// 256×256 RGBA.
    pub rgba: &'a [u8],
}

/// Nimmt fertige Kacheln entgegen (wird aus mehreren Threads gerufen).
pub trait TileSink: Sync {
    fn put(&self, tile: &Tile<'_>);
}

/// Halbiert ein Bild (`w`×`w`, RGBA) mit Alpha-gewichtetem Mittel.
pub fn downsample2(src: &[u8], w: usize, out: &mut [u8]) {
    let ow = w / 2;
    for y in 0..ow {
        for x in 0..ow {
            let (mut r, mut g, mut b, mut a) = (0u32, 0u32, 0u32, 0u32);
            for (dx, dy) in [(0, 0), (1, 0), (0, 1), (1, 1)] {
                let o = ((2 * y + dy) * w + 2 * x + dx) * 4;
                let al = u32::from(src[o + 3]);
                r += u32::from(src[o]) * al;
                g += u32::from(src[o + 1]) * al;
                b += u32::from(src[o + 2]) * al;
                a += al;
            }
            let o = (y * ow + x) * 4;
            if a == 0 {
                out[o..o + 4].copy_from_slice(&[0, 0, 0, 0]);
            } else {
                out[o] = ((r + a / 2) / a) as u8;
                out[o + 1] = ((g + a / 2) / a) as u8;
                out[o + 2] = ((b + a / 2) / a) as u8;
                out[o + 3] = ((a + 2) / 4) as u8;
            }
        }
    }
}

fn is_empty(rgba: &[u8]) -> bool {
    rgba.chunks_exact(4).all(|p| p[3] == 0)
}

/// Gibt die vier Kacheln eines Knotenbildes an den Sink. Kacheln ohne Inhalt
/// werden nicht ausgegeben.
fn emit(sink: &dyn TileSink, level: u8, x0: i32, z0: i32, img: &[u8]) {
    let mut buf = vec![0u8; TILE * TILE * 4];
    for j in 0..2usize {
        for i in 0..2usize {
            for y in 0..TILE {
                let s = ((j * TILE + y) * NODE + i * TILE) * 4;
                buf[y * TILE * 4..(y + 1) * TILE * 4].copy_from_slice(&img[s..s + TILE * 4]);
            }
            if is_empty(&buf) {
                continue;
            }
            // x0 ist ein Vielfaches von 2^level (Regionen), also exakt teilbar.
            let tx = x0 * 2 / (1 << level) + i as i32;
            let tz = z0 * 2 / (1 << level) + j as i32;
            sink.put(&Tile { level, tx, tz, rgba: &buf });
        }
    }
}

/// Baut die Pyramide für alle Regionen in `regions` (rx, rz). `render`
/// liefert das 512×512-RGBA-Bild einer Region (`None`: leer). Stufen
/// 0..=`max_level` gehen an `sink`. Rückgabe: Anzahl gerenderter Regionen.
pub fn build_pyramid(
    regions: &[(i32, i32)],
    max_level: u8,
    render: &(dyn Fn(i32, i32) -> Option<Vec<u8>> + Sync),
    sink: &dyn TileSink,
) -> usize {
    if regions.is_empty() {
        return 0;
    }
    let set: std::collections::HashSet<(i32, i32)> = regions.iter().copied().collect();
    // Alle Regionen liegen in einem Quadrat der Kantenlänge 2^k.
    let k = max_level;
    let side = 1i32 << k;
    // Wurzeln: alle Quadrate der Größe 2^k, die Regionen enthalten.
    let mut roots: Vec<(i32, i32)> = regions
        .iter()
        .map(|&(x, z)| (x.div_euclid(side) * side, z.div_euclid(side) * side))
        .collect();
    roots.sort_unstable();
    roots.dedup();
    let counts: usize = roots
        .par_iter()
        .map(|&(x0, z0)| {
            let mut n = 0;
            node(k, x0, z0, &set, render, sink, &mut n);
            n
        })
        .sum();
    counts
}

fn node(
    k: u8,
    x0: i32,
    z0: i32,
    set: &std::collections::HashSet<(i32, i32)>,
    render: &(dyn Fn(i32, i32) -> Option<Vec<u8>> + Sync),
    sink: &dyn TileSink,
    rendered: &mut usize,
) -> Option<Vec<u8>> {
    // Gibt es im Quadrat überhaupt Regionen?
    let side = 1i32 << k;
    let any = if side as usize * side as usize <= set.len() {
        (0..side).any(|dz| (0..side).any(|dx| set.contains(&(x0 + dx, z0 + dz))))
    } else {
        set.iter().any(|&(x, z)| x >= x0 && x < x0 + side && z >= z0 && z < z0 + side)
    };
    if !any {
        return None;
    }
    let img = if k == 0 {
        *rendered += 1;
        render(x0, z0)?
    } else {
        let half = 1i32 << (k - 1);
        let children: Vec<(Option<Vec<u8>>, usize)> = [(0, 0), (1, 0), (0, 1), (1, 1)]
            .into_par_iter()
            .map(|(i, j)| {
                let mut n = 0;
                let r = node(k - 1, x0 + i * half, z0 + j * half, set, render, sink, &mut n);
                (r, n)
            })
            .collect();
        let mut big = vec![0u8; 2 * NODE * 2 * NODE * 4];
        let mut have = false;
        for (idx, (c, n)) in children.into_iter().enumerate() {
            *rendered += n;
            let Some(c) = c else { continue };
            have = true;
            let (i, j) = (idx % 2, idx / 2);
            for y in 0..NODE {
                let d = ((j * NODE + y) * 2 * NODE + i * NODE) * 4;
                big[d..d + NODE * 4].copy_from_slice(&c[y * NODE * 4..(y + 1) * NODE * 4]);
            }
        }
        if !have {
            return None;
        }
        let mut out = vec![0u8; NODE * NODE * 4];
        downsample2(&big, 2 * NODE, &mut out);
        out
    };
    emit(sink, k, x0, z0, &img);
    Some(img)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Mutex;

    struct Collect(Mutex<Vec<(u8, i32, i32, u8)>>);
    impl TileSink for Collect {
        fn put(&self, t: &Tile<'_>) {
            self.0.lock().unwrap().push((t.level, t.tx, t.tz, t.rgba[0]));
        }
    }

    #[test]
    fn halbieren_mittelt_mit_alpha() {
        let mut src = vec![0u8; 4 * 4 * 4];
        // Oben links 2×2: ein voller roter Pixel, Rest durchsichtig.
        src[0..4].copy_from_slice(&[200, 0, 0, 255]);
        let mut out = vec![9u8; 2 * 2 * 4];
        downsample2(&src, 4, &mut out);
        assert_eq!(&out[0..4], &[200, 0, 0, 64]); // Farbe bleibt, Alpha geviertelt
        assert_eq!(&out[4..8], &[0, 0, 0, 0]);
    }

    #[test]
    fn pyramide_koordinaten_und_leere_kacheln() {
        // Zwei Regionen: (0,0) rot und (-1,0) grün-ish; Rest leer.
        let render = |x: i32, _z: i32| -> Option<Vec<u8>> {
            let mut v = vec![0u8; NODE * NODE * 4];
            // Nur die obere linke Kachel (256×256) füllen.
            for y in 0..TILE {
                for xx in 0..TILE {
                    let o = (y * NODE + xx) * 4;
                    v[o..o + 4].copy_from_slice(&[if x == 0 { 100 } else { 50 }, 0, 0, 255]);
                }
            }
            Some(v)
        };
        let sink = Collect(Mutex::new(Vec::new()));
        let n = build_pyramid(&[(0, 0), (-1, 0)], 2, &render, &sink);
        assert_eq!(n, 2);
        let mut got = sink.0.lock().unwrap().clone();
        got.sort();
        // Stufe 0: je Region nur die obere linke Kachel: tx = 2·rx.
        assert!(got.contains(&(0, 0, 0, 100)));
        assert!(got.contains(&(0, -2, 0, 50)));
        assert_eq!(got.iter().filter(|t| t.0 == 0).count(), 2);
        // Stufe 1: je Region eine Kachel (tx = rx), Inhalt in der oberen linken Ecke.
        assert!(got.iter().any(|t| t.0 == 1 && t.1 == 0 && t.2 == 0));
        assert!(got.iter().any(|t| t.0 == 1 && t.1 == -1 && t.2 == 0));
        // Stufe 2: beide Regionen liegen in verschiedenen Quadraten (−4..0 und 0..4).
        assert!(got.iter().any(|t| t.0 == 2 && t.1 == 0 && t.2 == 0));
        assert!(got.iter().any(|t| t.0 == 2 && t.1 == -1 && t.2 == 0));
    }
}
