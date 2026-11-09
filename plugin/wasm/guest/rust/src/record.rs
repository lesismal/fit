//! Records, rows and values in their canonical JSON (see
//! docs/decisions/036-sync.md), or in the binary form the plugin took (see
//! the module binary), read and written by hand, as the Go guest does, so
//! that a plugin needs no JSON library: a record's values stay as they
//! came, copied out of the input.

use crate::binary::{self, canonical, is_null, FORM_BINARY};

/// A JSON text that is not what was asked for.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SyntaxError;

impl core::fmt::Display for SyntaxError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        f.write_str("not the JSON expected")
    }
}

/// A record's parts: its strings read, its values (`key`, `before`,
/// `after`) left as they came, in either form, and `tx` and `position` as
/// their JSON; empty when it has none.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Record {
    pub source: String,
    pub namespace: String,
    pub op: String,
    pub key: Vec<u8>,
    pub before: Vec<u8>,
    pub after: Vec<u8>,
    pub commit_time: String,
    pub tx: Vec<u8>,
    pub position: Vec<u8>,
}

impl Record {
    /// Reads a record from its canonical JSON.
    pub fn parse(b: &[u8]) -> Result<Record, SyntaxError> {
        let mut r = Record::default();
        members(b, |name, v| {
            match name.as_str() {
                "source" => r.source = unquote(v)?,
                "namespace" => r.namespace = unquote(v)?,
                "op" => r.op = unquote(v)?,
                "commit_time" => r.commit_time = unquote(v)?,
                "key" => r.key = v.to_vec(),
                "before" => r.before = v.to_vec(),
                "after" => r.after = v.to_vec(),
                "tx" => r.tx = v.to_vec(),
                "position" => r.position = v.to_vec(),
                _ => {}
            }
            Ok(())
        })?;
        Ok(r)
    }

    /// Appends the record's canonical JSON to `b`.
    pub fn append_json(&self, b: &mut Vec<u8>) {
        b.push(b'{');
        let start = b.len();
        let field = |b: &mut Vec<u8>, name: &str| {
            if b.len() > start {
                b.push(b',');
            }
            b.push(b'"');
            b.extend_from_slice(name.as_bytes());
            b.extend_from_slice(b"\":");
        };
        for (name, v) in [("source", &self.source), ("namespace", &self.namespace), ("op", &self.op)] {
            if !v.is_empty() {
                field(b, name);
                append_quote(b, v);
            }
        }
        for (name, v) in [("key", &self.key), ("before", &self.before), ("after", &self.after)] {
            if !is_null(v) {
                field(b, name);
                binary::append_value_json(b, v);
            }
        }
        if !self.commit_time.is_empty() {
            field(b, "commit_time");
            append_quote(b, &self.commit_time);
        }
        for (name, v) in [("tx", &self.tx), ("position", &self.position)] {
            if !v.is_empty() && v.as_slice() != b"null" && v.as_slice() != b"{}" {
                field(b, name);
                b.extend_from_slice(v);
            }
        }
        b.push(b'}');
    }

    /// The record's canonical JSON.
    pub fn to_json(&self) -> Vec<u8> {
        let mut b = Vec::with_capacity(64 + self.key.len() + self.before.len() + self.after.len());
        self.append_json(&mut b);
        b
    }
}

/// Reads a batch in the form the plugin took: a JSON array of records, or
/// the binary form.
pub fn parse_batch(b: &[u8]) -> Result<Vec<Record>, SyntaxError> {
    if binary::form() == FORM_BINARY {
        return binary::parse_batch_binary(b);
    }
    parse_batch_json(b)
}

/// Reads a batch that is a JSON array of records.
pub fn parse_batch_json(b: &[u8]) -> Result<Vec<Record>, SyntaxError> {
    let mut out = Vec::new();
    elements(b, |v| {
        out.push(Record::parse(v)?);
        Ok(())
    })?;
    Ok(out)
}

/// A map value's fields, in order, each value as it came, in either form.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Row(pub Vec<(String, Vec<u8>)>);

impl Row {
    /// Reads `v`, a map value, or None for null or nothing.
    pub fn parse(v: &[u8]) -> Result<Option<Row>, SyntaxError> {
        if is_null(v) {
            return Ok(None);
        }
        let Some(v) = canonical(v) else {
            if v[0] != binary::TAG_MAP {
                return Err(SyntaxError);
            }
            return Ok(Some(Row(binary::map_fields(v)?)));
        };
        let mut inner = None;
        members(v, |name, val| {
            if name == "map" {
                inner = Some(val);
            }
            Ok(())
        })?;
        let mut row = Row::default();
        members(inner.ok_or(SyntaxError)?, |name, val| {
            row.0.push((name, val.to_vec()));
            Ok(())
        })?;
        Ok(Some(row))
    }

    /// The field `name`'s value.
    pub fn get(&self, name: &str) -> Option<&[u8]> {
        self.0.iter().find(|(n, _)| n == name).map(|(_, v)| v.as_slice())
    }

    /// Sets the field `name` to `v`, adding it at the end when it is new.
    pub fn set(&mut self, name: &str, v: Vec<u8>) {
        match self.0.iter_mut().find(|(n, _)| n == name) {
            Some(f) => f.1 = v,
            None => self.0.push((name.to_string(), v)),
        }
    }

    /// Removes the field `name`.
    pub fn delete(&mut self, name: &str) {
        self.0.retain(|(n, _)| n != name);
    }

    /// The row as a map value, in the form the plugin took.
    pub fn value(&self) -> Vec<u8> {
        self.value_in(binary::form())
    }

    pub(crate) fn value_in(&self, form: u32) -> Vec<u8> {
        if form == FORM_BINARY {
            return binary::map_binary(self.0.iter().map(|(n, v)| (n.as_str(), v.as_slice())));
        }
        let mut b = b"{\"map\":{".to_vec();
        for (i, (name, v)) in self.0.iter().enumerate() {
            if i > 0 {
                b.push(b',');
            }
            append_quote(&mut b, name);
            b.push(b':');
            binary::append_value_json(&mut b, v);
        }
        b.extend_from_slice(b"}}");
        b
    }
}

/// `i` as a value, in the form the plugin took.
pub fn int(i: i64) -> Vec<u8> {
    int_in(binary::form(), i)
}

pub(crate) fn int_in(form: u32, i: i64) -> Vec<u8> {
    binary::make(form, binary::TAG_INT, |b| binary::append_varint(b, i), || format!("{{\"int\":{i}}}").into_bytes())
}

/// `s` as a value.
pub fn string(s: &str) -> Vec<u8> {
    string_in(binary::form(), s)
}

pub(crate) fn string_in(form: u32, s: &str) -> Vec<u8> {
    binary::make(
        form,
        binary::TAG_STRING,
        |b| binary::append_str(b, s.as_bytes()),
        || {
            let mut b = Vec::with_capacity(s.len() + 2);
            append_quote(&mut b, s);
            b
        },
    )
}

/// The decimal number written as `s`, as "-12.30", as a value.
pub fn decimal(s: &str) -> Vec<u8> {
    decimal_in(binary::form(), s)
}

pub(crate) fn decimal_in(form: u32, s: &str) -> Vec<u8> {
    binary::make(
        form,
        binary::TAG_DECIMAL,
        |b| binary::append_str(b, s.as_bytes()),
        || {
            let mut b = b"{\"decimal\":".to_vec();
            append_quote(&mut b, s);
            b.push(b'}');
            b
        },
    )
}

/// `v` as a value.
pub fn boolean(v: bool) -> Vec<u8> {
    boolean_in(binary::form(), v)
}

pub(crate) fn boolean_in(form: u32, v: bool) -> Vec<u8> {
    binary::make(form, binary::TAG_BOOL, |b| b.push(v as u8), || if v { b"true".to_vec() } else { b"false".to_vec() })
}

/// The null value.
pub fn null() -> Vec<u8> {
    binary::make(binary::form(), binary::TAG_NULL, |_| {}, || b"null".to_vec())
}

/// A value's integer, an int's or a uint's.
pub fn as_int(v: &[u8]) -> Option<i64> {
    let Some(v) = canonical(v) else {
        let mut r = binary::Reader::new(v, 1);
        return match v[0] {
            binary::TAG_INT => r.varint().ok(),
            binary::TAG_UINT => r.uvarint().ok().map(|u| u as i64),
            _ => None,
        };
    };
    let mut n = None;
    members(v, |name, val| {
        let s = core::str::from_utf8(trim(val)).ok();
        match name.as_str() {
            "int" => n = s.and_then(|s| s.parse::<i64>().ok()),
            "uint" => n = s.and_then(|s| s.parse::<u64>().ok()).map(|u| u as i64),
            _ => {}
        }
        Ok(())
    })
    .ok()?;
    n
}

/// A value's string.
pub fn as_str(v: &[u8]) -> Option<String> {
    let Some(v) = canonical(v) else {
        return binary::text(v, binary::TAG_STRING);
    };
    let v = trim(v);
    if v.first() != Some(&b'"') {
        return None;
    }
    unquote(v).ok()
}

/// The text of a value's decimal, as "12.30".
pub fn as_decimal(v: &[u8]) -> Option<String> {
    let Some(v) = canonical(v) else {
        return binary::text(v, binary::TAG_DECIMAL);
    };
    let mut s = None;
    members(v, |name, val| {
        if name == "decimal" {
            s = unquote(val).ok();
        }
        Ok(())
    })
    .ok()?;
    s
}

/// A value's boolean.
pub fn as_bool(v: &[u8]) -> Option<bool> {
    let Some(v) = canonical(v) else {
        return match v {
            [binary::TAG_BOOL, c] => Some(*c == 1),
            _ => None,
        };
    };
    match trim(v) {
        b"true" => Some(true),
        b"false" => Some(false),
        _ => None,
    }
}

fn is_space(c: u8) -> bool {
    matches!(c, b' ' | b'\t' | b'\n' | b'\r')
}

fn skip_space(b: &[u8], mut i: usize) -> usize {
    while i < b.len() && is_space(b[i]) {
        i += 1;
    }
    i
}

fn trim(b: &[u8]) -> &[u8] {
    let s = skip_space(b, 0);
    let mut e = b.len();
    while e > s && is_space(b[e - 1]) {
        e -= 1;
    }
    &b[s..e]
}

/// Where the JSON value at `b[i]` ends.
fn skip_value(b: &[u8], mut i: usize) -> Result<usize, SyntaxError> {
    match b.get(i) {
        None => Err(SyntaxError),
        Some(b'"') => {
            i += 1;
            while i < b.len() {
                match b[i] {
                    b'\\' => i += 1,
                    b'"' => return Ok(i + 1),
                    _ => {}
                }
                i += 1;
            }
            Err(SyntaxError)
        }
        Some(b'{') | Some(b'[') => {
            let mut depth = 0usize;
            while i < b.len() {
                match b[i] {
                    b'{' | b'[' => depth += 1,
                    b'}' | b']' => {
                        depth -= 1;
                        if depth == 0 {
                            return Ok(i + 1);
                        }
                    }
                    b'"' => i = skip_value(b, i)? - 1,
                    _ => {}
                }
                i += 1;
            }
            Err(SyntaxError)
        }
        Some(_) => {
            while i < b.len() && !matches!(b[i], b' ' | b'\t' | b'\r' | b'\n' | b',' | b']' | b'}' | b':') {
                i += 1;
            }
            Ok(i)
        }
    }
}

/// Calls `f` with each member of the JSON object `b`, its value a slice of
/// `b`.
pub fn members<'a>(
    b: &'a [u8],
    mut f: impl FnMut(String, &'a [u8]) -> Result<(), SyntaxError>,
) -> Result<(), SyntaxError> {
    let mut i = skip_space(b, 0);
    if b.get(i) != Some(&b'{') {
        return Err(SyntaxError);
    }
    i = skip_space(b, i + 1);
    if b.get(i) == Some(&b'}') {
        return Ok(());
    }
    loop {
        if b.get(i) != Some(&b'"') {
            return Err(SyntaxError);
        }
        let kend = skip_value(b, i)?;
        let name = unquote(&b[i..kend])?;
        i = skip_space(b, kend);
        if b.get(i) != Some(&b':') {
            return Err(SyntaxError);
        }
        i = skip_space(b, i + 1);
        let end = skip_value(b, i)?;
        f(name, &b[i..end])?;
        i = skip_space(b, end);
        match b.get(i) {
            Some(b'}') => return Ok(()),
            Some(b',') => i = skip_space(b, i + 1),
            _ => return Err(SyntaxError),
        }
    }
}

/// Calls `f` with each element of the JSON array `b`, a slice of `b`.
pub fn elements<'a>(
    b: &'a [u8],
    mut f: impl FnMut(&'a [u8]) -> Result<(), SyntaxError>,
) -> Result<(), SyntaxError> {
    let mut i = skip_space(b, 0);
    if b.get(i) != Some(&b'[') {
        return Err(SyntaxError);
    }
    i = skip_space(b, i + 1);
    if b.get(i) == Some(&b']') {
        return Ok(());
    }
    loop {
        let end = skip_value(b, i)?;
        f(&b[i..end])?;
        i = skip_space(b, end);
        match b.get(i) {
            Some(b']') => return Ok(()),
            Some(b',') => i = skip_space(b, i + 1),
            _ => return Err(SyntaxError),
        }
    }
}

/// The string a JSON string holds.
pub fn unquote(b: &[u8]) -> Result<String, SyntaxError> {
    let b = trim(b);
    if b.len() < 2 || b[0] != b'"' || b[b.len() - 1] != b'"' {
        return Err(SyntaxError);
    }
    let b = &b[1..b.len() - 1];
    if !b.contains(&b'\\') {
        return String::from_utf8(b.to_vec()).map_err(|_| SyntaxError);
    }
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        let c = b[i];
        i += 1;
        if c != b'\\' {
            out.push(c);
            continue;
        }
        let e = *b.get(i).ok_or(SyntaxError)?;
        i += 1;
        let simple = match e {
            b'"' => b'"',
            b'\\' => b'\\',
            b'/' => b'/',
            b'b' => 8,
            b'f' => 12,
            b'n' => b'\n',
            b'r' => b'\r',
            b't' => b'\t',
            b'u' => 0,
            _ => return Err(SyntaxError),
        };
        if e != b'u' {
            out.push(simple);
            continue;
        }
        let mut cp = hex4(b, i)?;
        i += 4;
        if (0xD800..0xDC00).contains(&cp) && b.get(i) == Some(&b'\\') && b.get(i + 1) == Some(&b'u') {
            let lo = hex4(b, i + 2)?;
            if (0xDC00..0xE000).contains(&lo) {
                cp = 0x10000 + ((cp - 0xD800) << 10) + (lo - 0xDC00);
                i += 6;
            }
        }
        let ch = char::from_u32(cp).unwrap_or('\u{FFFD}');
        let mut buf = [0u8; 4];
        out.extend_from_slice(ch.encode_utf8(&mut buf).as_bytes());
    }
    String::from_utf8(out).map_err(|_| SyntaxError)
}

fn hex4(b: &[u8], i: usize) -> Result<u32, SyntaxError> {
    let s = b.get(i..i + 4).ok_or(SyntaxError)?;
    let s = core::str::from_utf8(s).map_err(|_| SyntaxError)?;
    u32::from_str_radix(s, 16).map_err(|_| SyntaxError)
}

/// Appends `s` as a JSON string.
pub fn append_quote(b: &mut Vec<u8>, s: &str) {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    b.push(b'"');
    for &c in s.as_bytes() {
        match c {
            b'"' | b'\\' => b.extend_from_slice(&[b'\\', c]),
            0..=0x1f => b.extend_from_slice(&[b'\\', b'u', b'0', b'0', HEX[(c >> 4) as usize], HEX[(c & 15) as usize]]),
            _ => b.push(c),
        }
    }
    b.push(b'"');
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn records_round_trip() {
        let rest = r#"\"x\"","op":"insert","key":{"map":{"id":{"int":7}}},"after":{"map":{"id":{"int":7},"name":"g"}},"tx":{"id":"t","first":true},"position":{"n":7}}"#;
        let b = format!(r#"[{{"namespace":"shop.{}{rest}, {{"op":"delete","before":null}}]"#, esc("00f6"));
        let recs = parse_batch(b.as_bytes()).unwrap();
        assert_eq!(recs.len(), 2);
        assert_eq!(recs[0].namespace, "shop.\u{f6}\"x\"");
        assert_eq!(String::from_utf8(recs[0].to_json()).unwrap(), format!(r#"{{"namespace":"shop.{}{rest}"#, '\u{f6}'));
        assert_eq!(recs[1].to_json(), br#"{"op":"delete"}"#);
        assert!(parse_batch(b"[{").is_err());
        assert!(parse_batch(b"{}").is_err());
        assert_eq!(parse_batch(b" [ ] ").unwrap().len(), 0);
    }

    #[test]
    fn rows_and_values() {
        let mut row = Row::parse(br#"{"map":{"id":{"int":2},"name":"a\nb"}}"#).unwrap().unwrap();
        assert_eq!(as_int(row.get("id").unwrap()), Some(2));
        assert_eq!(as_str(row.get("name").unwrap()).as_deref(), Some("a\nb"));
        row.set("seen", boolean(true));
        row.set("id", int(-3));
        row.delete("name");
        assert_eq!(row.value(), br#"{"map":{"id":{"int":-3},"seen":true}}"#);
        assert_eq!(as_bool(row.get("seen").unwrap()), Some(true));
        assert_eq!(as_int(br#"{"uint":18446744073709551615}"#), Some(-1));
        assert_eq!(Row::parse(b"null").unwrap(), None);
        assert!(Row::parse(b"[1]").is_err());
        assert_eq!(string("\u{1}\""), br#""\u0001\"""#);
        let pair = format!("\"{}{}\\t\"", esc("d83d"), esc("de00"));
        assert_eq!(unquote(pair.as_bytes()).unwrap(), "\u{1F600}\t");
        assert_eq!(null(), b"null");
    }

    /// A JSON escape of the code unit `hex`, spelled out.
    fn esc(hex: &str) -> String {
        let mut s = String::from("\\");
        s.push('u');
        s.push_str(hex);
        s
    }
}
