//! The Rust side of fit's WASM plugin ABI for sync (see package plugin/wasm
//! and docs/decisions/036-sync.md): the host functions of the modules `fit`
//! and `fit_sync`, wrapped, and a reader of a batch's records.
//!
//! A plugin is a WASI reactor, a `cdylib` built for `wasm32-wasip1`, that
//! exports the calls it takes, each given the length of its input:
//!
//! ```ignore
//! #[no_mangle]
//! pub extern "C" fn fit_sync_transform(n: u32) -> i32 {
//!     let Ok(batch) = fit_guest::parse_batch(&fit_guest::input(n)) else {
//!         fit_guest::fail(None, "not a batch");
//!         return fit_guest::FAILED;
//!     };
//!     let mut out = fit_guest::Emitter::new();
//!     for (i, rec) in batch.iter().enumerate() {
//!         out.emit(i, rec); // as it is
//!     }
//!     fit_guest::OK
//! }
//! ```
//!
//! The exports are `fit_sync_transform`, `fit_sync_decode`,
//! `fit_sync_encode`, `fit_sync_poll` and `fit_sync_write`; each returns
//! [`OK`], [`CAUGHT_UP`] (a poll with nothing more for now) or [`FAILED`].
//! The crate exports `fit_sync_form` too, with its default feature
//! `binary`, so that records cross in the binary form; without it
//! (`default-features = false`), as their canonical JSON. Either way
//! [`parse_batch`] reads a batch into [`Record`]s, [`Row`] and the value
//! helpers ([`int`], [`string`], [`decimal`], [`as_int`], ...) read and
//! make their typed values, and [`emit_record`] and [`Emitter`] hand them
//! on, without a JSON library. [`records`] splits a batch of the JSON form
//! into its records' JSON, and [`emit`] hands one on, for a plugin built
//! without `binary` that parses records with a JSON library of its own.

mod binary;
mod record;
pub use binary::{append_value_json, form, is_binary, is_null, parse_batch_binary, take_form, value_json, FORM_BINARY, FORM_JSON};
pub use record::*;

/// What an export returns when it went well.
pub const OK: i32 = 0;
/// What a poll returns once it has caught up.
pub const CAUGHT_UP: i32 = 1;
/// What an export returns when it failed; [`fail`] says why.
pub const FAILED: i32 = 2;

#[link(wasm_import_module = "fit")]
extern "C" {
    fn request_get(field: u32, buf: *mut u8, buf_len: u32) -> i32;
    #[link_name = "log"]
    fn fit_log(level: i32, msg: *const u8, msg_len: u32);
}

#[link(wasm_import_module = "fit_sync")]
extern "C" {
    #[link_name = "input"]
    fn sync_input(buf: *mut u8, buf_len: u32) -> i32;
    fn record_emit(index: i32, rec: *const u8, rec_len: u32);
    fn records_emit(buf: *const u8, buf_len: u32);
    #[link_name = "fail"]
    fn sync_fail(index: i32, msg: *const u8, msg_len: u32);
    #[link_name = "output"]
    fn sync_output(buf: *const u8, buf_len: u32);
    #[link_name = "checkpoint"]
    fn sync_checkpoint(buf: *const u8, buf_len: u32);
    fn http_request(req: *const u8, req_len: u32) -> i32;
    fn http_response(part: u32, buf: *mut u8, buf_len: u32) -> i32;
    fn http_header(name: *const u8, name_len: u32, buf: *mut u8, buf_len: u32) -> i32;
    #[link_name = "sleep"]
    fn sync_sleep(ms: u32);
}

/// Calls `f` with a buffer, again with a larger one when what it wrote did
/// not fit, and returns what it wrote, or None for -1.
fn get(mut f: impl FnMut(*mut u8, u32) -> i32) -> Option<Vec<u8>> {
    let mut buf = vec![0u8; 1024];
    loop {
        let n = f(buf.as_mut_ptr(), buf.len() as u32);
        if n < 0 {
            return None;
        }
        let n = n as usize;
        if n <= buf.len() {
            buf.truncate(n);
            return Some(buf);
        }
        buf = vec![0u8; n];
    }
}

/// The call's input, `n` bytes: the batch, the bytes to decode, or the
/// checkpoint.
pub fn input(n: u32) -> Vec<u8> {
    let mut buf = vec![0u8; n as usize];
    if n > 0 {
        unsafe { sync_input(buf.as_mut_ptr(), n) };
    }
    buf
}

/// Hands on `rec`, a record's canonical JSON, as what the input's record
/// `i` becomes (0 for a decode or a poll), in either form. Indexes never go
/// back.
pub fn emit(i: usize, rec: &[u8]) {
    unsafe { record_emit(i as i32, rec.as_ptr(), rec.len() as u32) }
}

/// Hands on `r` as what the input's record `i` becomes, as [`emit`] does,
/// in the form the plugin took. An [`Emitter`] hands on many in one go.
pub fn emit_record(i: usize, r: &Record) {
    EMIT_BUF.with(|b| {
        let mut b = b.borrow_mut();
        b.clear();
        if form() == FORM_BINARY {
            binary::append_uvarint(&mut b, i as u64);
            r.append_binary(&mut b);
            unsafe { records_emit(b.as_ptr(), b.len() as u32) };
        } else {
            r.append_json(&mut b);
            emit(i, &b);
        }
    })
}

/// Hands on records as [`emit_record`] does, but in the binary form many in
/// one go, when it holds enough or is dropped: make one at the start of an
/// export, and let it go at its end.
///
/// ```ignore
/// let mut out = fit_guest::Emitter::new();
/// for (i, r) in batch.iter().enumerate() {
///     out.emit(i, r);
/// }
/// fit_guest::OK // out goes, with the records it holds, before the export returns
/// ```
#[derive(Default)]
pub struct Emitter {
    buf: Vec<u8>,
}

impl Emitter {
    /// An emitter holding nothing.
    pub fn new() -> Self {
        Self::default()
    }

    /// Hands on `r` as what the input's record `i` becomes.
    pub fn emit(&mut self, i: usize, r: &Record) {
        if form() != FORM_BINARY {
            emit_record(i, r);
            return;
        }
        binary::append_uvarint(&mut self.buf, i as u64);
        r.append_binary(&mut self.buf);
        if self.buf.len() >= 64 << 10 {
            self.flush();
        }
    }

    /// Hands on what it holds.
    pub fn flush(&mut self) {
        if !self.buf.is_empty() {
            unsafe { records_emit(self.buf.as_ptr(), self.buf.len() as u32) };
            self.buf.clear();
        }
    }

    /// Drops what it holds, for an export that fails.
    pub fn discard(&mut self) {
        self.buf.clear();
    }
}

impl Drop for Emitter {
    fn drop(&mut self) {
        self.flush();
    }
}

std::thread_local! {
    static EMIT_BUF: std::cell::RefCell<Vec<u8>> = const { std::cell::RefCell::new(Vec::new()) };
}

/// Fails the input's record `i`, or the whole call for `None`.
pub fn fail(i: Option<usize>, msg: &str) {
    let i = i.map_or(-1, |i| i as i32);
    unsafe { sync_fail(i, msg.as_ptr(), msg.len() as u32) }
}

/// Appends bytes an encode makes.
pub fn output(b: &[u8]) {
    unsafe { sync_output(b.as_ptr(), b.len() as u32) }
}

/// Sets the checkpoint, JSON, a poll moves to.
pub fn checkpoint(cp: &[u8]) {
    unsafe { sync_checkpoint(cp.as_ptr(), cp.len() as u32) }
}

/// The plugin's config, as its params set it.
pub fn config() -> String {
    let b = get(|p, n| unsafe { request_get(5, p, n) }).unwrap_or_default();
    String::from_utf8_lossy(&b).into_owned()
}

/// Writes `msg` to fit's log at `level`, as Go's log/slog numbers them: -4
/// debug, 0 info, 4 warn, 8 error.
pub fn log(level: i32, msg: &str) {
    unsafe { fit_log(level, msg.as_ptr(), msg.len() as u32) }
}

/// Sends an HTTP request, as JSON `{"method", "url", "headers", "body"}`,
/// through fit's client to a host the connector's allow_hosts names, and
/// returns its status and body, or the error that kept it from one. A host
/// not allowed fails the whole call.
pub fn http(request_json: &str) -> Result<(u16, Vec<u8>), String> {
    let status = unsafe { http_request(request_json.as_ptr(), request_json.len() as u32) };
    if status < 0 {
        let msg = get(|p, n| unsafe { http_response(1, p, n) }).unwrap_or_default();
        return Err(String::from_utf8_lossy(&msg).into_owned());
    }
    let body = get(|p, n| unsafe { http_response(0, p, n) }).unwrap_or_default();
    Ok((status as u16, body))
}

/// Sends a request, as [`http`] does, made of its parts: `method` (GET
/// when empty), `url`, header fields and a text body.
pub fn request(method: &str, url: &str, headers: &[(&str, &str)], body: &str) -> Result<(u16, Vec<u8>), String> {
    let mut b = Vec::with_capacity(64 + url.len() + body.len());
    b.extend_from_slice(b"{\"url\":");
    append_quote(&mut b, url);
    if !method.is_empty() {
        b.extend_from_slice(b",\"method\":");
        append_quote(&mut b, method);
    }
    if !headers.is_empty() {
        b.extend_from_slice(b",\"headers\":{");
        for (i, (k, v)) in headers.iter().enumerate() {
            if i > 0 {
                b.push(b',');
            }
            append_quote(&mut b, k);
            b.push(b':');
            append_quote(&mut b, v);
        }
        b.push(b'}');
    }
    if !body.is_empty() {
        b.extend_from_slice(b",\"body\":");
        append_quote(&mut b, body);
    }
    b.push(b'}');
    // Every part was a str, quoted: the request is UTF-8.
    http(&String::from_utf8_lossy(&b))
}

/// The header field `name` of the last response [`http`] had.
pub fn response_header(name: &str) -> Option<String> {
    let b = get(|p, n| unsafe { http_header(name.as_ptr(), name.len() as u32, p, n) })?;
    Some(String::from_utf8_lossy(&b).into_owned())
}

/// Waits `ms` milliseconds, for a source or a sink, within its call's
/// timeout.
pub fn sleep(ms: u32) {
    unsafe { sync_sleep(ms) }
}

/// The records of a batch in the JSON form, a JSON array of objects, each
/// its canonical JSON as a slice of `batch`.
pub fn records(batch: &[u8]) -> Vec<&[u8]> {
    let mut out = Vec::new();
    let (mut depth, mut start, mut in_str, mut esc) = (0usize, 0usize, false, false);
    for (i, &c) in batch.iter().enumerate() {
        if in_str {
            match (esc, c) {
                (true, _) => esc = false,
                (false, b'\\') => esc = true,
                (false, b'"') => in_str = false,
                _ => {}
            }
            continue;
        }
        match c {
            b'"' => in_str = true,
            b'{' | b'[' => {
                if depth == 1 {
                    start = i;
                }
                depth += 1;
            }
            b'}' | b']' => {
                depth = depth.saturating_sub(1);
                if depth == 1 {
                    out.push(&batch[start..=i]);
                }
            }
            _ => {}
        }
    }
    out
}

#[cfg(test)]
mod tests {
    #[test]
    fn records() {
        let b = br#"[{"namespace":"a}","after":{"map":{"x":[1]}}}, {"op":"insert"}]"#;
        let r = super::records(b);
        assert_eq!(r.len(), 2);
        assert_eq!(r[1], br#"{"op":"insert"}"#);
    }
}
