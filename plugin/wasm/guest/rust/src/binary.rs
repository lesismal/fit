//! The binary form of records (see docs/decisions/036-sync.md and package
//! fitsync's binary.go), which fit offers a plugin through
//! `fit_sync_form`: lengths and tags rather than names and quotes. With
//! the crate's feature `binary` (a default one) the plugin takes it, and
//! [`parse_batch`](crate::parse_batch), [`Row`](crate::Row) and the value
//! helpers speak it; a value is then either form, told by its first byte,
//! a tag from 0x10 to 0x1F in the binary form.

use crate::record::{append_quote, Record, SyntaxError};
use core::sync::atomic::{AtomicU32, Ordering};

/// Records in their canonical JSON.
pub const FORM_JSON: u32 = 0;
/// Records in the binary form.
pub const FORM_BINARY: u32 = 1;

static FORM: AtomicU32 = AtomicU32::new(FORM_JSON);

/// The form the plugin's records are in: [`FORM_JSON`] until fit offers
/// another.
pub fn form() -> u32 {
    FORM.load(Ordering::Relaxed)
}

/// Takes the form fit offers, the binary one when it is offered, and
/// returns it.
pub fn take_form(offered: u32) -> u32 {
    let f = if offered >= FORM_BINARY { FORM_BINARY } else { FORM_JSON };
    FORM.store(f, Ordering::Relaxed);
    f
}

/// What fit calls on an instance first, offering a form; a plugin built
/// without the feature `binary` exports none, and its records stay JSON.
#[cfg(feature = "binary")]
#[no_mangle]
pub extern "C" fn fit_sync_form(offered: u32) -> u32 {
    take_form(offered)
}

pub(crate) const TAG_NULL: u8 = 0x10;
pub(crate) const TAG_BOOL: u8 = 0x11;
pub(crate) const TAG_INT: u8 = 0x12;
pub(crate) const TAG_UINT: u8 = 0x13;
pub(crate) const TAG_FLOAT: u8 = 0x14;
pub(crate) const TAG_DECIMAL: u8 = 0x15;
pub(crate) const TAG_STRING: u8 = 0x16;
pub(crate) const TAG_BYTES: u8 = 0x17;
pub(crate) const TAG_TIME: u8 = 0x18;
pub(crate) const TAG_DATE: u8 = 0x19;
pub(crate) const TAG_JSON: u8 = 0x1A;
pub(crate) const TAG_ARRAY: u8 = 0x1B;
pub(crate) const TAG_MAP: u8 = 0x1C;
pub(crate) const TAG_CANONICAL: u8 = 0x1F;

/// Whether `v` is a value in the binary form.
pub fn is_binary(v: &[u8]) -> bool {
    matches!(v.first(), Some(0x10..=0x1F))
}

pub(crate) struct Reader<'a> {
    pub b: &'a [u8],
    pub i: usize,
}

impl<'a> Reader<'a> {
    pub fn new(b: &'a [u8], i: usize) -> Self {
        Reader { b, i }
    }

    pub fn byte(&mut self) -> Result<u8, SyntaxError> {
        let c = *self.b.get(self.i).ok_or(SyntaxError)?;
        self.i += 1;
        Ok(c)
    }

    pub fn uvarint(&mut self) -> Result<u64, SyntaxError> {
        let mut u = 0u64;
        let mut shift = 0;
        loop {
            let c = self.byte()?;
            if shift == 63 && c > 1 {
                return Err(SyntaxError);
            }
            u |= u64::from(c & 0x7f) << shift;
            if c < 0x80 {
                return Ok(u);
            }
            shift += 7;
            if shift > 63 {
                return Err(SyntaxError);
            }
        }
    }

    pub fn varint(&mut self) -> Result<i64, SyntaxError> {
        let u = self.uvarint()?;
        Ok((u >> 1) as i64 ^ -((u & 1) as i64))
    }

    pub fn bytes(&mut self) -> Result<&'a [u8], SyntaxError> {
        let n = self.uvarint()?;
        if n > (self.b.len() - self.i) as u64 {
            return Err(SyntaxError);
        }
        let s = &self.b[self.i..self.i + n as usize];
        self.i += n as usize;
        Ok(s)
    }

    pub fn string(&mut self) -> Result<String, SyntaxError> {
        // Go strings need not be UTF-8; what is not becomes U+FFFD, as JSON has it.
        Ok(String::from_utf8_lossy(self.bytes()?).into_owned())
    }

    /// Reads an array's or a map's length, after its tag, and returns
    /// where it ends.
    pub fn enter(&mut self) -> Result<usize, SyntaxError> {
        let l = self.b.get(self.i..self.i + 4).ok_or(SyntaxError)?;
        let n = u32::from_le_bytes([l[0], l[1], l[2], l[3]]) as usize;
        self.i += 4;
        if n > self.b.len() - self.i {
            return Err(SyntaxError);
        }
        Ok(self.i + n)
    }

    /// Passes over the value at the reader and returns it.
    pub fn skip(&mut self) -> Result<&'a [u8], SyntaxError> {
        let start = self.i;
        match self.byte()? {
            TAG_NULL => {}
            TAG_BOOL => {
                self.byte()?;
            }
            TAG_INT | TAG_UINT => {
                self.uvarint()?;
            }
            TAG_FLOAT => {
                if self.i + 8 > self.b.len() {
                    return Err(SyntaxError);
                }
                self.i += 8;
            }
            TAG_DECIMAL | TAG_STRING | TAG_BYTES | TAG_JSON | TAG_CANONICAL => {
                self.bytes()?;
            }
            TAG_TIME => {
                self.uvarint()?;
                self.uvarint()?;
                self.uvarint()?;
            }
            TAG_DATE => {
                self.uvarint()?;
                self.byte()?;
                self.byte()?;
            }
            TAG_ARRAY | TAG_MAP => self.i = self.enter()?,
            _ => return Err(SyntaxError),
        }
        Ok(&self.b[start..self.i])
    }
}

pub(crate) fn append_uvarint(b: &mut Vec<u8>, mut u: u64) {
    while u >= 0x80 {
        b.push(u as u8 | 0x80);
        u >>= 7;
    }
    b.push(u as u8);
}

pub(crate) fn append_varint(b: &mut Vec<u8>, i: i64) {
    append_uvarint(b, ((i << 1) ^ (i >> 63)) as u64);
}

pub(crate) fn append_str(b: &mut Vec<u8>, s: &[u8]) {
    append_uvarint(b, s.len() as u64);
    b.extend_from_slice(s);
}

fn trim(v: &[u8]) -> &[u8] {
    let is_space = |c: &u8| matches!(c, b' ' | b'\t' | b'\n' | b'\r');
    let s = v.iter().position(|c| !is_space(c)).unwrap_or(v.len());
    let e = v.iter().rposition(|c| !is_space(c)).map_or(s, |e| e + 1);
    &v[s..e]
}

/// Whether `v` is null, or none, in either form.
pub fn is_null(v: &[u8]) -> bool {
    if is_binary(v) {
        return v[0] == TAG_NULL;
    }
    let t = trim(v);
    t.is_empty() || t == b"null"
}

/// `v` as JSON: itself, or what the tag 0x1F holds; None for another
/// binary value.
pub(crate) fn canonical(v: &[u8]) -> Option<&[u8]> {
    if !is_binary(v) {
        return Some(v);
    }
    if v[0] == TAG_CANONICAL {
        return Reader::new(v, 1).bytes().ok();
    }
    None
}

/// Appends `v` in the binary form: as it is, or, for JSON, under the tag
/// 0x1F.
pub(crate) fn append_value_binary(b: &mut Vec<u8>, v: &[u8]) {
    if is_binary(v) {
        b.extend_from_slice(v);
    } else if is_null(v) {
        b.push(TAG_NULL);
    } else {
        b.push(TAG_CANONICAL);
        append_str(b, trim(v));
    }
}

/// `v`'s canonical JSON, in either form; null for a binary value cut
/// short.
pub fn value_json(v: &[u8]) -> Vec<u8> {
    let mut b = Vec::with_capacity(v.len() + 16);
    append_value_json(&mut b, v);
    b
}

/// Appends `v`'s canonical JSON, as [`value_json`] makes it.
pub fn append_value_json(b: &mut Vec<u8>, v: &[u8]) {
    if let Some(doc) = canonical(v) {
        let doc = trim(doc);
        b.extend_from_slice(if doc.is_empty() { b"null" } else { doc });
        return;
    }
    let start = b.len();
    if write_json(b, &mut Reader::new(v, 0), 0).is_err() {
        b.truncate(start);
        b.extend_from_slice(b"null");
    }
}

fn write_json(b: &mut Vec<u8>, r: &mut Reader, depth: usize) -> Result<(), SyntaxError> {
    if depth > 1000 {
        return Err(SyntaxError);
    }
    match r.byte()? {
        TAG_NULL => b.extend_from_slice(b"null"),
        TAG_BOOL => b.extend_from_slice(if r.byte()? == 1 { b"true" } else { b"false" }),
        TAG_INT => b.extend_from_slice(format!("{{\"int\":{}}}", r.varint()?).as_bytes()),
        TAG_UINT => b.extend_from_slice(format!("{{\"uint\":{}}}", r.uvarint()?).as_bytes()),
        TAG_FLOAT => {
            let s = r.b.get(r.i..r.i + 8).ok_or(SyntaxError)?;
            let f = f64::from_bits(u64::from_le_bytes(s.try_into().map_err(|_| SyntaxError)?));
            r.i += 8;
            let text = if f.is_nan() {
                "\"NaN\"".to_string()
            } else if f.is_infinite() {
                (if f > 0.0 { "\"+Inf\"" } else { "\"-Inf\"" }).to_string()
            } else {
                format!("{f:?}")
            };
            b.extend_from_slice(format!("{{\"float\":{text}}}").as_bytes());
        }
        TAG_DECIMAL => {
            b.extend_from_slice(b"{\"decimal\":");
            append_quote(b, &r.string()?);
            b.push(b'}');
        }
        TAG_STRING => append_quote(b, &r.string()?),
        TAG_BYTES => {
            b.extend_from_slice(b"{\"bytes\":\"");
            base64(b, r.bytes()?);
            b.extend_from_slice(b"\"}");
        }
        TAG_JSON => {
            b.extend_from_slice(b"{\"json\":");
            b.extend_from_slice(r.bytes()?);
            b.push(b'}');
        }
        TAG_CANONICAL => b.extend_from_slice(r.bytes()?),
        TAG_TIME => {
            let (sec, nsec, off) = (r.varint()?, r.uvarint()?, r.varint()?);
            b.extend_from_slice(b"{\"time\":\"");
            rfc3339(b, sec, nsec as u32, off);
            b.extend_from_slice(b"\"}");
        }
        TAG_DATE => {
            let y = r.varint()?;
            let (m, d) = (r.byte()?, r.byte()?);
            b.extend_from_slice(format!("{{\"date\":\"{y:04}-{m:02}-{d:02}\"}}").as_bytes());
        }
        TAG_ARRAY => {
            r.enter()?;
            b.push(b'[');
            for i in 0..r.uvarint()? {
                if i > 0 {
                    b.push(b',');
                }
                write_json(b, r, depth + 1)?;
            }
            b.push(b']');
        }
        TAG_MAP => {
            r.enter()?;
            b.extend_from_slice(b"{\"map\":{");
            for i in 0..r.uvarint()? {
                if i > 0 {
                    b.push(b',');
                }
                append_quote(b, &r.string()?);
                b.push(b':');
                write_json(b, r, depth + 1)?;
            }
            b.extend_from_slice(b"}}");
        }
        _ => return Err(SyntaxError),
    }
    Ok(())
}

/// Appends an instant as RFC 3339 with nanoseconds, its trailing zeros
/// dropped, at `off` seconds east of UTC, as Go writes it.
fn rfc3339(b: &mut Vec<u8>, sec: i64, nsec: u32, off: i64) {
    let local = sec + off;
    let (y, m, d) = civil(local.div_euclid(86400));
    let s = local.rem_euclid(86400);
    b.extend_from_slice(format!("{y:04}-{m:02}-{d:02}T{:02}:{:02}:{:02}", s / 3600, s / 60 % 60, s % 60).as_bytes());
    if nsec > 0 {
        let frac = format!("{nsec:09}");
        b.push(b'.');
        b.extend_from_slice(frac.trim_end_matches('0').as_bytes());
    }
    if off == 0 {
        b.push(b'Z');
    } else {
        let a = off.abs();
        b.extend_from_slice(format!("{}{:02}:{:02}", if off < 0 { '-' } else { '+' }, a / 3600, a / 60 % 60).as_bytes());
    }
}

/// The civil date of days since 1970-01-01.
fn civil(days: i64) -> (i64, u32, u32) {
    let z = days + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    let y = yoe + era * 400;
    (if m <= 2 { y + 1 } else { y }, m, d)
}

fn base64(b: &mut Vec<u8>, data: &[u8]) {
    const A: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    for c in data.chunks(3) {
        let n = (u32::from(c[0]) << 16) | (u32::from(*c.get(1).unwrap_or(&0)) << 8) | u32::from(*c.get(2).unwrap_or(&0));
        b.push(A[(n >> 18) as usize & 63]);
        b.push(A[(n >> 12) as usize & 63]);
        b.push(if c.len() > 1 { A[(n >> 6) as usize & 63] } else { b'=' });
        b.push(if c.len() > 2 { A[n as usize & 63] } else { b'=' });
    }
}

const OPS: [&str; 6] = ["", "insert", "update", "delete", "ddl", "message"];

const TX_FIRST: u8 = 1;
const TX_LAST: u8 = 2;

/// Reads a batch in the binary form.
pub fn parse_batch_binary(b: &[u8]) -> Result<Vec<Record>, SyntaxError> {
    let mut r = Reader::new(b, 0);
    let n = r.uvarint()?;
    if n > (b.len() / 10) as u64 {
        return Err(SyntaxError);
    }
    let mut out = Vec::with_capacity(n as usize);
    for _ in 0..n {
        out.push(read_record(&mut r)?);
    }
    if r.i != b.len() {
        return Err(SyntaxError);
    }
    Ok(out)
}

fn none(v: &[u8]) -> Vec<u8> {
    if v == [TAG_NULL] {
        Vec::new()
    } else {
        v.to_vec()
    }
}

fn read_record(r: &mut Reader) -> Result<Record, SyntaxError> {
    let op = *OPS.get(r.byte()? as usize).ok_or(SyntaxError)?;
    let flags = r.byte()?;
    let mut rec = Record { op: op.to_string(), source: r.string()?, namespace: r.string()?, ..Default::default() };
    rec.key = none(r.skip()?);
    rec.before = none(r.skip()?);
    rec.after = none(r.skip()?);
    rec.commit_time = r.string()?;
    let id = r.string()?;
    if !id.is_empty() || flags != 0 {
        let mut tx = b"{".to_vec();
        if !id.is_empty() {
            tx.extend_from_slice(b"\"id\":");
            append_quote(&mut tx, &id);
        }
        for (bit, name) in [(TX_FIRST, "first"), (TX_LAST, "last")] {
            if flags & bit != 0 {
                if tx.len() > 1 {
                    tx.push(b',');
                }
                tx.extend_from_slice(format!("\"{name}\":true").as_bytes());
            }
        }
        tx.push(b'}');
        rec.tx = tx;
    }
    rec.position = r.bytes()?.to_vec();
    Ok(rec)
}

impl Record {
    /// Appends the record in the binary form.
    pub fn append_binary(&self, b: &mut Vec<u8>) {
        let op = OPS.iter().position(|o| *o == self.op).map_or(0xFF, |i| i as u8);
        let (mut flags, mut id) = (0u8, String::new());
        if !is_null(&self.tx) {
            let _ = crate::record::members(&self.tx, |name, v| {
                match name.as_str() {
                    "id" => id = crate::record::unquote(v)?,
                    "first" if trim(v) == b"true" => flags |= TX_FIRST,
                    "last" if trim(v) == b"true" => flags |= TX_LAST,
                    _ => {}
                }
                Ok(())
            });
        }
        b.push(op);
        b.push(flags);
        append_str(b, self.source.as_bytes());
        append_str(b, self.namespace.as_bytes());
        for v in [&self.key, &self.before, &self.after] {
            append_value_binary(b, v);
        }
        append_str(b, self.commit_time.as_bytes());
        append_str(b, id.as_bytes());
        let p = trim(&self.position);
        append_str(b, if p == b"null" { b"" } else { p });
    }
}

/// Makes a value in `form`.
pub(crate) fn make(form: u32, tag: u8, body: impl FnOnce(&mut Vec<u8>), json: impl FnOnce() -> Vec<u8>) -> Vec<u8> {
    if form != FORM_BINARY {
        return json();
    }
    let mut b = vec![tag];
    body(&mut b);
    b
}

/// A map value of `fields`, in the binary form.
pub(crate) fn map_binary<'a>(fields: impl ExactSizeIterator<Item = (&'a str, &'a [u8])>) -> Vec<u8> {
    let mut b = vec![TAG_MAP, 0, 0, 0, 0];
    append_uvarint(&mut b, fields.len() as u64);
    for (name, v) in fields {
        append_str(&mut b, name.as_bytes());
        append_value_binary(&mut b, v);
    }
    let n = (b.len() - 5) as u32;
    b[1..5].copy_from_slice(&n.to_le_bytes());
    b
}

/// A binary map's fields, each value a slice of `v`.
pub(crate) fn map_fields(v: &[u8]) -> Result<Vec<(String, Vec<u8>)>, SyntaxError> {
    let mut r = Reader::new(v, 1);
    let end = r.enter()?;
    let n = r.uvarint()?;
    if n > v.len() as u64 / 2 {
        return Err(SyntaxError);
    }
    let mut out = Vec::with_capacity(n as usize);
    for _ in 0..n {
        let name = r.string()?;
        out.push((name, r.skip()?.to_vec()));
    }
    if r.i != end || end != v.len() {
        return Err(SyntaxError);
    }
    Ok(out)
}

/// The text a binary value of `tag` holds.
pub(crate) fn text(v: &[u8], tag: u8) -> Option<String> {
    if v.first() != Some(&tag) {
        return None;
    }
    Reader::new(v, 1).string().ok()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::record::{boolean_in, int_in, string_in, Row};

    /// A record of every kind, as fitsync's AppendBinary writes it.
    fn every_kind() -> Vec<u8> {
        let mut after = vec![TAG_MAP, 0, 0, 0, 0];
        let fields: Vec<(&str, Vec<u8>)> = vec![
            ("i", int_in(FORM_BINARY, -3)),
            ("u", {
                let mut v = vec![TAG_UINT];
                append_uvarint(&mut v, 300);
                v
            }),
            ("f", {
                let mut v = vec![TAG_FLOAT];
                v.extend_from_slice(&1.5f64.to_bits().to_le_bytes());
                v
            }),
            ("nan", {
                let mut v = vec![TAG_FLOAT];
                v.extend_from_slice(&f64::NAN.to_bits().to_le_bytes());
                v
            }),
            ("inf", {
                let mut v = vec![TAG_FLOAT];
                v.extend_from_slice(&f64::NEG_INFINITY.to_bits().to_le_bytes());
                v
            }),
            ("d", decimal_in(FORM_BINARY, "1.20")),
            ("s", string_in(FORM_BINARY, "a\"b")),
            ("b", {
                let mut v = vec![TAG_BYTES];
                append_str(&mut v, &[1, 2, 255, 0]);
                v
            }),
            ("t", {
                let mut v = vec![TAG_TIME];
                append_varint(&mut v, 1_700_000_000);
                append_uvarint(&mut v, 500_000_000);
                append_varint(&mut v, 8 * 3600);
                v
            }),
            ("tz", {
                let mut v = vec![TAG_TIME];
                append_varint(&mut v, -1);
                append_uvarint(&mut v, 0);
                append_varint(&mut v, -(5 * 3600 + 30 * 60));
                v
            }),
            ("day", {
                let mut v = vec![TAG_DATE];
                append_varint(&mut v, 2026);
                v.extend_from_slice(&[2, 28]);
                v
            }),
            ("doc", {
                let mut v = vec![TAG_JSON];
                append_str(&mut v, br#"{"n":1}"#);
                v
            }),
            ("arr", {
                let mut v = vec![TAG_ARRAY, 0, 0, 0, 0];
                append_uvarint(&mut v, 2);
                v.extend_from_slice(&boolean_in(FORM_BINARY, true));
                v.push(TAG_NULL);
                let n = (v.len() - 5) as u32;
                v[1..5].copy_from_slice(&n.to_le_bytes());
                v
            }),
            ("raw", {
                let mut v = vec![TAG_CANONICAL];
                append_str(&mut v, br#"{"uint":7}"#);
                v
            }),
        ];
        append_uvarint(&mut after, fields.len() as u64);
        for (n, v) in &fields {
            append_str(&mut after, n.as_bytes());
            after.extend_from_slice(v);
        }
        let n = (after.len() - 5) as u32;
        after[1..5].copy_from_slice(&n.to_le_bytes());

        let mut b = Vec::new();
        append_uvarint(&mut b, 1);
        b.extend_from_slice(&[1, TX_FIRST | TX_LAST]);
        append_str(&mut b, b"src");
        append_str(&mut b, b"ns");
        b.extend_from_slice(&map_binary([("id", &int_in(FORM_BINARY, 7)[..])].into_iter()));
        b.push(TAG_NULL);
        b.extend_from_slice(&after);
        append_str(&mut b, b"2026-11-09T10:00:00Z");
        append_str(&mut b, b"t1");
        append_str(&mut b, br#"{"n":7}"#);
        b
    }

    use crate::record::decimal_in;

    #[test]
    fn records_in_the_binary_form() {
        let b = every_kind();
        let recs = parse_batch_binary(&b).unwrap();
        assert_eq!(recs.len(), 1);
        let r = &recs[0];
        assert_eq!((r.op.as_str(), r.source.as_str(), r.namespace.as_str()), ("insert", "src", "ns"));
        assert!(r.before.is_empty());
        assert_eq!(r.tx, br#"{"id":"t1","first":true,"last":true}"#);
        assert_eq!(r.position, br#"{"n":7}"#);
        let json = String::from_utf8(r.to_json()).unwrap();
        assert_eq!(
            json,
            concat!(
                r#"{"source":"src","namespace":"ns","op":"insert","key":{"map":{"id":{"int":7}}},"#,
                r#""after":{"map":{"i":{"int":-3},"u":{"uint":300},"f":{"float":1.5},"nan":{"float":"NaN"},"#,
                r#""inf":{"float":"-Inf"},"d":{"decimal":"1.20"},"s":"a\"b","b":{"bytes":"AQL/AA=="},"#,
                r#""t":{"time":"2023-11-15T06:13:20.5+08:00"},"tz":{"time":"1969-12-31T18:29:59-05:30"},"#,
                r#""day":{"date":"2026-02-28"},"doc":{"json":{"n":1}},"arr":[true,null],"raw":{"uint":7}}},"#,
                r#""commit_time":"2026-11-09T10:00:00Z","tx":{"id":"t1","first":true,"last":true},"position":{"n":7}}"#
            )
        );
        // Written back, it is what it was.
        let mut again = Vec::new();
        append_uvarint(&mut again, 1);
        r.append_binary(&mut again);
        assert_eq!(again, b);

        let row = Row::parse(&r.after).unwrap().unwrap();
        assert_eq!(crate::as_int(row.get("i").unwrap()), Some(-3));
        assert_eq!(crate::as_int(row.get("u").unwrap()), Some(300));
        assert_eq!(crate::as_int(row.get("raw").unwrap()), Some(7));
        assert_eq!(crate::as_str(row.get("s").unwrap()).as_deref(), Some("a\"b"));
        assert_eq!(crate::as_decimal(row.get("d").unwrap()).as_deref(), Some("1.20"));
        assert_eq!(crate::as_bool(&boolean_in(FORM_BINARY, false)), Some(false));
        assert_eq!(crate::as_int(&string_in(FORM_BINARY, "x")), None);

        for cut in 1..b.len() {
            assert!(parse_batch_binary(&b[..cut]).is_err(), "cut at {cut}");
        }
        assert!(parse_batch_binary(&[0x80]).is_err());
        assert_eq!(value_json(&[TAG_INT]), b"null");
        assert_eq!(value_json(&[0x1E]), b"null");
        assert!(is_null(b" null "));
        assert!(is_null(&[TAG_NULL]));
    }

    #[test]
    fn values_written_as_json_go_in_the_binary_form() {
        let mut b = Vec::new();
        append_value_binary(&mut b, br#" {"decimal":"2"} "#);
        append_value_binary(&mut b, b"null");
        append_value_binary(&mut b, b"");
        let mut want = vec![TAG_CANONICAL];
        append_str(&mut want, br#"{"decimal":"2"}"#);
        want.extend_from_slice(&[TAG_NULL, TAG_NULL]);
        assert_eq!(b, want);
        let rec = Record { op: "odd".into(), tx: b"null".to_vec(), position: b"null".to_vec(), ..Default::default() };
        let mut b = Vec::new();
        rec.append_binary(&mut b);
        assert_eq!(b, [0xFF, 0, 0, 0, TAG_NULL, TAG_NULL, TAG_NULL, 0, 0, 0]);
    }
}
