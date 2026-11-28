//! Kleiner NBT-Leser für das Minecraft-Java-Format (big-endian, unkomprimiert).
//!
//! Der Leser baut einen Baum, der auf die Eingabe zeigt (keine Kopien von
//! Zeichenketten oder Zahlenfeldern). Er ist für fremde Dateien gedacht:
//! jede Länge wird gegen die restliche Eingabe geprüft, bevor Speicher
//! reserviert wird, die Schachtelungstiefe ist begrenzt (wie im Spiel: 512),
//! und es gibt keine Panics – jeder Fehler kommt als [`Error`] zurück.
//!
//! Zeichenketten bleiben Bytes („Modified UTF-8“ von Java); für Blocknamen und
//! Eigenschaften ist das reines ASCII.

use std::fmt;

/// Größte Schachtelungstiefe (Compound/List), wie `NbtAccounter` im Spiel.
pub const MAX_DEPTH: u32 = 512;

/// Fehler beim Lesen.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Error {
    /// Die Eingabe endet mitten in einem Wert.
    UnexpectedEof,
    /// Unbekannter Tag-Typ.
    BadTagType(u8),
    /// Die Wurzel ist kein Compound.
    RootNotCompound(u8),
    /// Zu tief geschachtelt.
    TooDeep,
    /// Negative Länge eines Feldes oder einer Liste.
    NegativeLength,
    /// Hinter der Wurzel stehen noch Bytes (nur bei [`parse_exact`]).
    TrailingBytes(usize),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::UnexpectedEof => write!(f, "NBT endet unerwartet"),
            Error::BadTagType(t) => write!(f, "unbekannter NBT-Typ {t}"),
            Error::RootNotCompound(t) => write!(f, "NBT-Wurzel ist kein Compound (Typ {t})"),
            Error::TooDeep => write!(f, "NBT zu tief geschachtelt (> {MAX_DEPTH})"),
            Error::NegativeLength => write!(f, "negative Länge im NBT"),
            Error::TrailingBytes(n) => write!(f, "{n} Bytes hinter der NBT-Wurzel"),
        }
    }
}

impl std::error::Error for Error {}

pub type Result<T> = std::result::Result<T, Error>;

/// Tag-Typen.
pub mod tag {
    pub const END: u8 = 0;
    pub const BYTE: u8 = 1;
    pub const SHORT: u8 = 2;
    pub const INT: u8 = 3;
    pub const LONG: u8 = 4;
    pub const FLOAT: u8 = 5;
    pub const DOUBLE: u8 = 6;
    pub const BYTE_ARRAY: u8 = 7;
    pub const STRING: u8 = 8;
    pub const LIST: u8 = 9;
    pub const COMPOUND: u8 = 10;
    pub const INT_ARRAY: u8 = 11;
    pub const LONG_ARRAY: u8 = 12;
}

/// Ein Wert.
#[derive(Debug, Clone, PartialEq)]
pub enum Tag<'a> {
    Byte(i8),
    Short(i16),
    Int(i32),
    Long(i64),
    Float(f32),
    Double(f64),
    ByteArray(&'a [u8]),
    String(&'a [u8]),
    List(List<'a>),
    Compound(Compound<'a>),
    IntArray(IntArray<'a>),
    LongArray(LongArray<'a>),
}

/// Ein Compound: Schlüssel in Dateireihenfolge.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct Compound<'a> {
    entries: Vec<(&'a [u8], Tag<'a>)>,
}

/// Eine Liste. Zahlenlisten bleiben rohe Bytes.
#[derive(Debug, Clone, PartialEq)]
pub enum List<'a> {
    Empty,
    Byte(&'a [u8]),
    Short(&'a [u8]),
    Int(&'a [u8]),
    Long(&'a [u8]),
    Float(&'a [u8]),
    Double(&'a [u8]),
    ByteArray(Vec<&'a [u8]>),
    String(Vec<&'a [u8]>),
    List(Vec<List<'a>>),
    Compound(Vec<Compound<'a>>),
    IntArray(Vec<IntArray<'a>>),
    LongArray(Vec<LongArray<'a>>),
}

/// Ein Feld von Int (roh, big-endian).
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct IntArray<'a>(&'a [u8]);

/// Ein Feld von Long (roh, big-endian).
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct LongArray<'a>(&'a [u8]);

impl<'a> IntArray<'a> {
    pub fn len(&self) -> usize {
        self.0.len() / 4
    }
    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }
    pub fn get(&self, i: usize) -> Option<i32> {
        let b = self.0.get(i.checked_mul(4)?..i.checked_mul(4)?.checked_add(4)?)?;
        Some(i32::from_be_bytes([b[0], b[1], b[2], b[3]]))
    }
    pub fn iter(&self) -> impl Iterator<Item = i32> + 'a {
        self.0
            .chunks_exact(4)
            .map(|b| i32::from_be_bytes([b[0], b[1], b[2], b[3]]))
    }
}

impl<'a> LongArray<'a> {
    pub fn len(&self) -> usize {
        self.0.len() / 8
    }
    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }
    pub fn get(&self, i: usize) -> Option<i64> {
        let s = i.checked_mul(8)?;
        let b = self.0.get(s..s.checked_add(8)?)?;
        let mut a = [0u8; 8];
        a.copy_from_slice(b);
        Some(i64::from_be_bytes(a))
    }
    pub fn iter(&self) -> impl Iterator<Item = i64> + 'a {
        self.0.chunks_exact(8).map(|b| {
            let mut a = [0u8; 8];
            a.copy_from_slice(b);
            i64::from_be_bytes(a)
        })
    }
    /// Als u64-Vektor (für die Bit-Entpackung der Paletten).
    pub fn to_u64_vec(&self) -> Vec<u64> {
        self.iter().map(|v| v as u64).collect()
    }
    pub fn raw(&self) -> &'a [u8] {
        self.0
    }
}

impl<'a> Compound<'a> {
    pub fn len(&self) -> usize {
        self.entries.len()
    }
    pub fn is_empty(&self) -> bool {
        self.entries.is_empty()
    }
    pub fn iter(&self) -> impl Iterator<Item = (&'a [u8], &Tag<'a>)> {
        self.entries.iter().map(|(k, v)| (*k, v))
    }
    /// Erster Eintrag mit diesem Schlüssel (das Spiel schreibt keine doppelten).
    pub fn get(&self, key: &str) -> Option<&Tag<'a>> {
        let k = key.as_bytes();
        self.entries.iter().find(|(n, _)| *n == k).map(|(_, v)| v)
    }
    pub fn compound(&self, key: &str) -> Option<&Compound<'a>> {
        match self.get(key)? {
            Tag::Compound(c) => Some(c),
            _ => None,
        }
    }
    pub fn list(&self, key: &str) -> Option<&List<'a>> {
        match self.get(key)? {
            Tag::List(l) => Some(l),
            _ => None,
        }
    }
    pub fn long_array(&self, key: &str) -> Option<LongArray<'a>> {
        match self.get(key)? {
            Tag::LongArray(a) => Some(*a),
            _ => None,
        }
    }
    pub fn string(&self, key: &str) -> Option<&'a [u8]> {
        match self.get(key)? {
            Tag::String(s) => Some(s),
            _ => None,
        }
    }
    /// Ganzzahl aus Byte/Short/Int/Long.
    pub fn int(&self, key: &str) -> Option<i64> {
        match self.get(key)? {
            Tag::Byte(v) => Some(i64::from(*v)),
            Tag::Short(v) => Some(i64::from(*v)),
            Tag::Int(v) => Some(i64::from(*v)),
            Tag::Long(v) => Some(*v),
            _ => None,
        }
    }
}

impl<'a> List<'a> {
    pub fn len(&self) -> usize {
        match self {
            List::Empty => 0,
            List::Byte(b) => b.len(),
            List::Short(b) => b.len() / 2,
            List::Int(b) | List::Float(b) => b.len() / 4,
            List::Long(b) | List::Double(b) => b.len() / 8,
            List::ByteArray(v) | List::String(v) => v.len(),
            List::List(v) => v.len(),
            List::Compound(v) => v.len(),
            List::IntArray(v) => v.len(),
            List::LongArray(v) => v.len(),
        }
    }
    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }
    /// Die Compounds der Liste (leer, wenn es keine Compound-Liste ist).
    pub fn compounds(&self) -> &[Compound<'a>] {
        match self {
            List::Compound(v) => v,
            _ => &[],
        }
    }
    /// Die Zeichenketten der Liste (leer, wenn es keine String-Liste ist).
    pub fn strings(&self) -> &[&'a [u8]] {
        match self {
            List::String(v) => v,
            _ => &[],
        }
    }
}

/// Liest eine NBT-Wurzel (Typ, Name, Compound). Bytes dahinter werden
/// ignoriert. Liefert den Namen und den Compound.
pub fn parse_root(data: &[u8]) -> Result<(&[u8], Compound<'_>)> {
    parse_root_filtered(data, |_| true).map(|(n, c, _)| (n, c))
}

/// Wie [`parse_root`], verlangt aber, dass die Eingabe genau dort endet.
pub fn parse_exact(data: &[u8]) -> Result<(&[u8], Compound<'_>)> {
    let (n, c, used) = parse_root_filtered(data, |_| true)?;
    if used != data.len() {
        return Err(Error::TrailingBytes(data.len() - used));
    }
    Ok((n, c))
}

/// Liest eine NBT-Wurzel und behält auf der obersten Ebene nur die Schlüssel,
/// für die `keep` wahr ist. Alle anderen Werte werden nur überlesen (geprüft,
/// aber ohne Speicher zu belegen). Liefert Name, Compound und die Anzahl
/// gelesener Bytes.
pub fn parse_root_filtered<'a>(
    data: &'a [u8],
    keep: impl Fn(&[u8]) -> bool,
) -> Result<(&'a [u8], Compound<'a>, usize)> {
    let mut r = Reader { data, pos: 0 };
    let t = r.u8()?;
    if t != tag::COMPOUND {
        return Err(Error::RootNotCompound(t));
    }
    let name = r.string()?;
    let mut entries = Vec::new();
    loop {
        let t = r.u8()?;
        if t == tag::END {
            break;
        }
        let key = r.string()?;
        if keep(key) {
            let v = r.value(t, 1)?;
            entries.push((key, v));
        } else {
            r.skip(t, 1)?;
        }
    }
    Ok((name, Compound { entries }, r.pos))
}

struct Reader<'a> {
    data: &'a [u8],
    pos: usize,
}

impl<'a> Reader<'a> {
    fn remaining(&self) -> usize {
        self.data.len() - self.pos
    }

    fn take(&mut self, n: usize) -> Result<&'a [u8]> {
        if n > self.remaining() {
            return Err(Error::UnexpectedEof);
        }
        let s = &self.data[self.pos..self.pos + n];
        self.pos += n;
        Ok(s)
    }

    fn u8(&mut self) -> Result<u8> {
        Ok(self.take(1)?[0])
    }

    fn u16(&mut self) -> Result<u16> {
        let b = self.take(2)?;
        Ok(u16::from_be_bytes([b[0], b[1]]))
    }

    fn i32(&mut self) -> Result<i32> {
        let b = self.take(4)?;
        Ok(i32::from_be_bytes([b[0], b[1], b[2], b[3]]))
    }

    fn i64(&mut self) -> Result<i64> {
        let b = self.take(8)?;
        let mut a = [0u8; 8];
        a.copy_from_slice(b);
        Ok(i64::from_be_bytes(a))
    }

    fn string(&mut self) -> Result<&'a [u8]> {
        let n = self.u16()? as usize;
        self.take(n)
    }

    /// Länge eines Feldes/einer Liste: nicht negativ.
    fn len(&mut self) -> Result<usize> {
        let n = self.i32()?;
        usize::try_from(n).map_err(|_| Error::NegativeLength)
    }

    /// `n` Elemente zu je `size` Bytes.
    fn array(&mut self, n: usize, size: usize) -> Result<&'a [u8]> {
        let bytes = n.checked_mul(size).ok_or(Error::UnexpectedEof)?;
        self.take(bytes)
    }

    fn value(&mut self, t: u8, depth: u32) -> Result<Tag<'a>> {
        Ok(match t {
            tag::BYTE => Tag::Byte(self.u8()? as i8),
            tag::SHORT => Tag::Short(self.u16()? as i16),
            tag::INT => Tag::Int(self.i32()?),
            tag::LONG => Tag::Long(self.i64()?),
            tag::FLOAT => Tag::Float(f32::from_bits(self.i32()? as u32)),
            tag::DOUBLE => Tag::Double(f64::from_bits(self.i64()? as u64)),
            tag::BYTE_ARRAY => {
                let n = self.len()?;
                Tag::ByteArray(self.take(n)?)
            }
            tag::STRING => Tag::String(self.string()?),
            tag::LIST => Tag::List(self.list(depth)?),
            tag::COMPOUND => Tag::Compound(self.compound(depth)?),
            tag::INT_ARRAY => {
                let n = self.len()?;
                Tag::IntArray(IntArray(self.array(n, 4)?))
            }
            tag::LONG_ARRAY => {
                let n = self.len()?;
                Tag::LongArray(LongArray(self.array(n, 8)?))
            }
            other => return Err(Error::BadTagType(other)),
        })
    }

    fn compound(&mut self, depth: u32) -> Result<Compound<'a>> {
        if depth >= MAX_DEPTH {
            return Err(Error::TooDeep);
        }
        let mut entries = Vec::new();
        loop {
            let t = self.u8()?;
            if t == tag::END {
                return Ok(Compound { entries });
            }
            let key = self.string()?;
            let v = self.value(t, depth + 1)?;
            entries.push((key, v));
        }
    }

    /// Reserviert für `n` Elemente höchstens so viele, wie die restliche
    /// Eingabe bei `min_size` Bytes je Element hergeben kann.
    fn capacity(&self, n: usize, min_size: usize) -> usize {
        n.min(self.remaining() / min_size.max(1))
    }

    fn list(&mut self, depth: u32) -> Result<List<'a>> {
        if depth >= MAX_DEPTH {
            return Err(Error::TooDeep);
        }
        let t = self.u8()?;
        let n = self.len()?;
        Ok(match t {
            // Eine Liste vom Typ End trägt keine Daten (das Spiel schreibt
            // sie nur leer); die Länge wird ignoriert.
            tag::END => List::Empty,
            _ if n == 0 => {
                if t > tag::LONG_ARRAY {
                    return Err(Error::BadTagType(t));
                }
                List::Empty
            }
            tag::BYTE => List::Byte(self.array(n, 1)?),
            tag::SHORT => List::Short(self.array(n, 2)?),
            tag::INT => List::Int(self.array(n, 4)?),
            tag::LONG => List::Long(self.array(n, 8)?),
            tag::FLOAT => List::Float(self.array(n, 4)?),
            tag::DOUBLE => List::Double(self.array(n, 8)?),
            tag::BYTE_ARRAY => {
                let mut v = Vec::with_capacity(self.capacity(n, 4));
                for _ in 0..n {
                    let len = self.len()?;
                    v.push(self.take(len)?);
                }
                List::ByteArray(v)
            }
            tag::STRING => {
                let mut v = Vec::with_capacity(self.capacity(n, 2));
                for _ in 0..n {
                    v.push(self.string()?);
                }
                List::String(v)
            }
            tag::LIST => {
                let mut v = Vec::with_capacity(self.capacity(n, 5));
                for _ in 0..n {
                    v.push(self.list(depth + 1)?);
                }
                List::List(v)
            }
            tag::COMPOUND => {
                let mut v = Vec::with_capacity(self.capacity(n, 1));
                for _ in 0..n {
                    v.push(self.compound(depth + 1)?);
                }
                List::Compound(v)
            }
            tag::INT_ARRAY => {
                let mut v = Vec::with_capacity(self.capacity(n, 4));
                for _ in 0..n {
                    let len = self.len()?;
                    v.push(IntArray(self.array(len, 4)?));
                }
                List::IntArray(v)
            }
            tag::LONG_ARRAY => {
                let mut v = Vec::with_capacity(self.capacity(n, 4));
                for _ in 0..n {
                    let len = self.len()?;
                    v.push(LongArray(self.array(len, 8)?));
                }
                List::LongArray(v)
            }
            other => return Err(Error::BadTagType(other)),
        })
    }

    /// Überliest einen Wert vom Typ `t`, ohne Speicher zu belegen.
    fn skip(&mut self, t: u8, depth: u32) -> Result<()> {
        if depth >= MAX_DEPTH {
            return Err(Error::TooDeep);
        }
        match t {
            tag::BYTE => drop(self.take(1)?),
            tag::SHORT => drop(self.take(2)?),
            tag::INT | tag::FLOAT => drop(self.take(4)?),
            tag::LONG | tag::DOUBLE => drop(self.take(8)?),
            tag::BYTE_ARRAY => {
                let n = self.len()?;
                self.take(n)?;
            }
            tag::STRING => drop(self.string()?),
            tag::INT_ARRAY => {
                let n = self.len()?;
                self.array(n, 4)?;
            }
            tag::LONG_ARRAY => {
                let n = self.len()?;
                self.array(n, 8)?;
            }
            tag::LIST => {
                let et = self.u8()?;
                let n = self.len()?;
                if et == tag::END || n == 0 {
                    if et > tag::LONG_ARRAY {
                        return Err(Error::BadTagType(et));
                    }
                    return Ok(());
                }
                match et {
                    tag::BYTE => drop(self.array(n, 1)?),
                    tag::SHORT => drop(self.array(n, 2)?),
                    tag::INT | tag::FLOAT => drop(self.array(n, 4)?),
                    tag::LONG | tag::DOUBLE => drop(self.array(n, 8)?),
                    _ => {
                        for _ in 0..n {
                            self.skip(et, depth + 1)?;
                        }
                    }
                }
            }
            tag::COMPOUND => loop {
                let et = self.u8()?;
                if et == tag::END {
                    break;
                }
                self.string()?;
                self.skip(et, depth + 1)?;
            },
            other => return Err(Error::BadTagType(other)),
        }
        Ok(())
    }
}

/// Schreibt NBT (nur für Tests und Testdaten; der Leser ist das Ziel).
pub mod write {
    use super::tag;

    /// Ein zu schreibender Wert (besitzt seine Daten).
    #[derive(Debug, Clone)]
    pub enum Val {
        Byte(i8),
        Int(i32),
        Long(i64),
        Str(String),
        LongArray(Vec<i64>),
        List(u8, Vec<Val>),
        Compound(Vec<(String, Val)>),
    }

    impl Val {
        fn tag(&self) -> u8 {
            match self {
                Val::Byte(_) => tag::BYTE,
                Val::Int(_) => tag::INT,
                Val::Long(_) => tag::LONG,
                Val::Str(_) => tag::STRING,
                Val::LongArray(_) => tag::LONG_ARRAY,
                Val::List(..) => tag::LIST,
                Val::Compound(_) => tag::COMPOUND,
            }
        }
    }

    fn put_str(out: &mut Vec<u8>, s: &str) {
        out.extend_from_slice(&(s.len() as u16).to_be_bytes());
        out.extend_from_slice(s.as_bytes());
    }

    fn put(out: &mut Vec<u8>, v: &Val) {
        match v {
            Val::Byte(b) => out.push(*b as u8),
            Val::Int(i) => out.extend_from_slice(&i.to_be_bytes()),
            Val::Long(l) => out.extend_from_slice(&l.to_be_bytes()),
            Val::Str(s) => put_str(out, s),
            Val::LongArray(a) => {
                out.extend_from_slice(&(a.len() as i32).to_be_bytes());
                for l in a {
                    out.extend_from_slice(&l.to_be_bytes());
                }
            }
            Val::List(t, items) => {
                out.push(*t);
                out.extend_from_slice(&(items.len() as i32).to_be_bytes());
                for it in items {
                    put(out, it);
                }
            }
            Val::Compound(entries) => {
                for (k, v) in entries {
                    out.push(v.tag());
                    put_str(out, k);
                    put(out, v);
                }
                out.push(tag::END);
            }
        }
    }

    /// Wurzel mit leerem Namen.
    pub fn root(entries: Vec<(String, Val)>) -> Vec<u8> {
        let mut out = vec![tag::COMPOUND];
        put_str(&mut out, "");
        put(&mut out, &Val::Compound(entries));
        out
    }
}

#[cfg(test)]
mod tests {
    use super::write::{Val, root};
    use super::*;

    fn sample() -> Vec<u8> {
        root(vec![
            ("DataVersion".into(), Val::Int(4800)),
            ("Status".into(), Val::Str("minecraft:full".into())),
            (
                "sections".into(),
                Val::List(
                    tag::COMPOUND,
                    vec![Val::Compound(vec![
                        ("Y".into(), Val::Byte(-4)),
                        ("data".into(), Val::LongArray(vec![1, -2, 3])),
                    ])],
                ),
            ),
            ("LastUpdate".into(), Val::Long(123_456_789_012)),
        ])
    }

    #[test]
    fn liest_beispiel() {
        let data = sample();
        let (name, c) = parse_exact(&data).unwrap();
        assert_eq!(name, b"");
        assert_eq!(c.int("DataVersion"), Some(4800));
        assert_eq!(c.string("Status"), Some(&b"minecraft:full"[..]));
        assert_eq!(c.int("LastUpdate"), Some(123_456_789_012));
        let secs = c.list("sections").unwrap().compounds();
        assert_eq!(secs.len(), 1);
        assert_eq!(secs[0].int("Y"), Some(-4));
        let a = secs[0].long_array("data").unwrap();
        assert_eq!(a.len(), 3);
        assert_eq!(a.get(1), Some(-2));
        assert_eq!(a.get(3), None);
        assert_eq!(a.iter().collect::<Vec<_>>(), vec![1, -2, 3]);
    }

    #[test]
    fn filter_ueberliest_andere_schluessel() {
        let data = sample();
        let (_, c, used) = parse_root_filtered(&data, |k| k == b"LastUpdate").unwrap();
        assert_eq!(used, data.len());
        assert_eq!(c.len(), 1);
        assert_eq!(c.int("LastUpdate"), Some(123_456_789_012));
        assert!(c.get("sections").is_none());
    }

    #[test]
    fn jedes_abgeschnittene_ende_ist_ein_fehler() {
        let data = sample();
        for cut in 0..data.len() {
            assert!(parse_root(&data[..cut]).is_err(), "Schnitt bei {cut}");
            assert!(
                parse_root_filtered(&data[..cut], |_| false).is_err(),
                "Schnitt bei {cut} (überlesen)"
            );
        }
    }

    #[test]
    fn falsche_wurzel_und_typen() {
        assert_eq!(parse_root(&[8, 0, 0]), Err(Error::RootNotCompound(8)));
        // Compound mit Eintrag vom Typ 13.
        assert_eq!(parse_root(&[10, 0, 0, 13, 0, 0]), Err(Error::BadTagType(13)));
        assert_eq!(
            parse_root_filtered(&[10, 0, 0, 13, 0, 0], |_| false).map(|_| ()),
            Err(Error::BadTagType(13))
        );
        // Long-Array mit negativer Länge.
        assert_eq!(
            parse_root(&[10, 0, 0, 12, 0, 1, b'a', 0xff, 0xff, 0xff, 0xff]),
            Err(Error::NegativeLength)
        );
        assert_eq!(parse_exact(&[10, 0, 0, 0, 7]), Err(Error::TrailingBytes(1)));
    }

    #[test]
    fn riesige_laenge_reserviert_nichts() {
        // Liste von 2^31-1 Compounds ohne Daten: Fehler statt Speicherfresser.
        let data = [10, 0, 0, 9, 0, 1, b'l', 10, 0x7f, 0xff, 0xff, 0xff];
        assert_eq!(parse_root(&data).map(|_| ()), Err(Error::UnexpectedEof));
        // Long-Array mit 2^31-1 Elementen.
        let data = [10, 0, 0, 12, 0, 1, b'a', 0x7f, 0xff, 0xff, 0xff];
        assert_eq!(parse_root(&data).map(|_| ()), Err(Error::UnexpectedEof));
    }

    #[test]
    fn tiefe_ist_begrenzt() {
        // 600 verschachtelte Listen von Listen.
        let mut data = vec![10, 0, 0, 9, 0, 1, b'l'];
        for _ in 0..600 {
            data.extend_from_slice(&[9, 0, 0, 0, 1]);
        }
        assert_eq!(parse_root(&data).map(|_| ()), Err(Error::TooDeep));
        assert_eq!(
            parse_root_filtered(&data, |_| false).map(|_| ()),
            Err(Error::TooDeep)
        );
        // Genau an der Grenze (511 Ebenen unter der Wurzel) geht es noch.
        let mut ok = vec![10, 0, 0];
        for _ in 0..(MAX_DEPTH - 1) {
            ok.extend_from_slice(&[10, 0, 0]);
        }
        ok.extend(std::iter::repeat_n(0u8, MAX_DEPTH as usize));
        assert!(parse_exact(&ok).is_ok());
        let mut zu_tief = vec![10, 0, 0];
        for _ in 0..MAX_DEPTH {
            zu_tief.extend_from_slice(&[10, 0, 0]);
        }
        zu_tief.extend(std::iter::repeat_n(0u8, MAX_DEPTH as usize + 1));
        assert_eq!(parse_exact(&zu_tief).map(|_| ()), Err(Error::TooDeep));
    }

    #[test]
    fn zufaellige_bytes_paniken_nicht() {
        // Einfacher Zufallsgenerator (xorshift), damit der Test ohne Crates läuft.
        let mut s: u64 = 0x9e37_79b9_7f4a_7c15;
        let base = sample();
        for _ in 0..20_000 {
            s ^= s << 13;
            s ^= s >> 7;
            s ^= s << 17;
            let mut d = base.clone();
            let i = (s as usize) % d.len();
            d[i] = (s >> 32) as u8;
            let _ = parse_root(&d);
            let _ = parse_root_filtered(&d, |k| k.len() % 2 == 0);
        }
    }
}
