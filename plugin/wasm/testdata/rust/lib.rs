//! The sync modules' test plugin in Rust: what testdata/sync, the Go one,
//! does, on the fit-guest crate, so that the same tests run it — a
//! transform, a codec of a line format of its own, a source reading a paged
//! HTTP API and a sink posting to one, each chosen by its config, and
//! misbehaving on purpose.

use fit_guest::{self as fit, Record, Row};
use std::cell::RefCell;

#[derive(Default)]
struct Settings {
    mode: String,
    url: String,
    fail: i64,
}

thread_local! {
    static CONF: RefCell<Option<std::rc::Rc<Settings>>> = const { RefCell::new(None) };
    // What grow allocates, kept.
    static HOLD: RefCell<Vec<Vec<u8>>> = const { RefCell::new(Vec::new()) };
}

fn cfg() -> std::rc::Rc<Settings> {
    CONF.with(|c| {
        c.borrow_mut()
            .get_or_insert_with(|| {
                let raw = fit::config();
                let mut s = Settings::default();
                let _ = fit::members(raw.as_bytes(), |name, v| {
                    match name.as_str() {
                        "mode" => s.mode = fit::unquote(v)?,
                        "url" => s.url = fit::unquote(v)?,
                        "fail" => s.fail = number(v).unwrap_or(0),
                        _ => {}
                    }
                    Ok(())
                });
                std::rc::Rc::new(s)
            })
            .clone()
    })
}

/// A plain JSON number's integer.
fn number(v: &[u8]) -> Option<i64> {
    std::str::from_utf8(v).ok()?.trim().parse().ok()
}

fn id(r: &Record) -> i64 {
    Row::parse(&r.key)
        .ok()
        .flatten()
        .and_then(|k| k.get("id").and_then(fit::as_int))
        .unwrap_or(0)
}

/// Fails the whole call for `msg`.
fn failed(msg: &str) -> i32 {
    fit::fail(None, msg);
    fit::FAILED
}

#[no_mangle]
pub extern "C" fn fit_sync_transform(n: u32) -> i32 {
    let c = cfg();
    let mut batch = match fit::parse_batch(&fit::input(n)) {
        Ok(b) => b,
        Err(e) => return failed(&e.to_string()),
    };
    // What the call emits, handed on in one go as it returns.
    let mut out = fit::Emitter::new();
    for (i, r) in batch.iter_mut().enumerate() {
        if c.mode == "none" {
            continue;
        }
        if c.mode == "route" {
            // The same work as the built-in route with a map.
            if r.namespace == "shop.orders" {
                r.namespace = "orders".to_string();
            }
            out.emit(i, r);
            continue;
        }
        let n = id(r);
        if n == c.fail {
            match c.mode.as_str() {
                "loop" => loop {
                    std::hint::spin_loop();
                },
                "grow" => HOLD.with(|h| h.borrow_mut().push(vec![0u8; 64 << 20])),
                "trap" => panic!("on purpose"),
                "http" => {
                    if let Err(e) = fit::request("", "http://127.0.0.1:1/", &[], "") {
                        out.discard();
                        return failed(&e);
                    }
                }
                "refuse" => {
                    fit::fail(Some(i), &format!("record {n} refused"));
                    continue;
                }
                _ => {}
            }
        }
        match n % 5 {
            0 => {} // dropped
            1 => {
                // split
                out.emit(i, r);
                r.namespace.push_str(".copy");
                out.emit(i, r);
            }
            _ => {
                // changed
                match Row::parse(&r.after) {
                    Err(e) => {
                        out.discard();
                        return failed(&e.to_string());
                    }
                    Ok(Some(mut row)) => {
                        row.set("seen", fit::boolean(true));
                        r.after = row.value();
                    }
                    Ok(None) => {}
                }
                out.emit(i, r);
            }
        }
    }
    fit::OK
}

// The line format: id|name|price, a line a record.

#[no_mangle]
pub extern "C" fn fit_sync_decode(n: u32) -> i32 {
    let data = fit::input(n);
    let text = String::from_utf8_lossy(&data);
    for line in text.strip_suffix('\n').unwrap_or(&text).split('\n') {
        let parts: Vec<&str> = line.split('|').collect();
        if parts.len() != 3 {
            return failed(&format!("{line:?} is not id|name|price"));
        }
        let Ok(n) = parts[0].parse::<i64>() else {
            return failed(&format!("{:?} is not a number", parts[0]));
        };
        // The price written as canonical JSON by hand, which goes in either
        // form.
        let price = format!("{{\"decimal\":\"{}\"}}", parts[2]).into_bytes();
        let r = Record {
            namespace: "lines".into(),
            op: "insert".into(),
            key: Row(vec![("id".into(), fit::int(n))]).value(),
            after: Row(vec![("id".into(), fit::int(n)), ("name".into(), fit::string(parts[1])), ("price".into(), price)]).value(),
            ..Default::default()
        };
        fit::emit_record(0, &r);
    }
    fit::OK
}

#[no_mangle]
pub extern "C" fn fit_sync_encode(n: u32) -> i32 {
    let batch = match fit::parse_batch(&fit::input(n)) {
        Ok(b) => b,
        Err(e) => return failed(&e.to_string()),
    };
    let mut out = String::new();
    for r in &batch {
        let row = match Row::parse(&r.after) {
            Ok(row) => row.unwrap_or_default(),
            Err(e) => return failed(&e.to_string()),
        };
        let id = row.get("id").and_then(fit::as_int).unwrap_or(0);
        let name = row.get("name").and_then(fit::as_str).unwrap_or_default();
        let price = row.get("price").and_then(fit::as_decimal).unwrap_or_default();
        out.push_str(&format!("{id}|{name}|{price}\n"));
    }
    if !out.is_empty() {
        fit::output(out.as_bytes());
    }
    fit::OK
}

// The API: GET url?after=N gives {"items": [{"id", "name"}...], "more"},
// the items after id N, a page of them.

#[no_mangle]
pub extern "C" fn fit_sync_poll(n: u32) -> i32 {
    let checkpoint = fit::input(n);
    let mut after = 0i64;
    if !checkpoint.is_empty() {
        if let Err(e) = fit::members(&checkpoint, |k, v| {
            if k == "after" {
                after = number(v).ok_or(fit::SyntaxError)?;
            }
            Ok(())
        }) {
            return failed(&e.to_string());
        }
    }
    let c = cfg();
    let (status, body) = match fit::request("", &format!("{}?after={after}", c.url), &[], "") {
        Ok(r) => r,
        Err(e) => return failed(&e),
    };
    if status != 200 {
        return failed(&format!("the API answered {status}"));
    }
    let mut items: Vec<(i64, String)> = Vec::new();
    let mut more = false;
    let parsed = fit::members(&body, |k, v| {
        match k.as_str() {
            "items" => fit::elements(v, |item| {
                let (mut id, mut name) = (0, String::new());
                fit::members(item, |k, v| {
                    match k.as_str() {
                        "id" => id = number(v).ok_or(fit::SyntaxError)?,
                        "name" => name = fit::unquote(v)?,
                        _ => {}
                    }
                    Ok(())
                })?;
                items.push((id, name));
                Ok(())
            })?,
            "more" => more = fit::as_bool(v).unwrap_or(false),
            _ => {}
        }
        Ok(())
    });
    if let Err(e) = parsed {
        return failed(&e.to_string());
    }
    let Some(last) = items.last().map(|it| it.0) else {
        return fit::CAUGHT_UP;
    };
    for (id, name) in &items {
        let r = Record {
            namespace: "api.items".into(),
            op: "insert".into(),
            key: Row(vec![("id".into(), fit::int(*id))]).value(),
            after: Row(vec![("id".into(), fit::int(*id)), ("name".into(), fit::string(name))]).value(),
            ..Default::default()
        };
        fit::emit_record(0, &r);
    }
    fit::checkpoint(format!("{{\"after\":{last}}}").as_bytes());
    if more {
        fit::OK
    } else {
        fit::CAUGHT_UP
    }
}

#[no_mangle]
pub extern "C" fn fit_sync_write(n: u32) -> i32 {
    let c = cfg();
    let batch = match fit::parse_batch(&fit::input(n)) {
        Ok(b) => b,
        Err(e) => return failed(&e.to_string()),
    };
    for (i, r) in batch.iter().enumerate() {
        if c.fail != 0 && id(r) == c.fail {
            fit::fail(Some(i), "refused");
            return fit::FAILED;
        }
    }
    let mut body = vec![b'['];
    for (i, r) in batch.iter().enumerate() {
        if i > 0 {
            body.push(b',');
        }
        r.append_json(&mut body);
    }
    body.push(b']');
    let body = String::from_utf8_lossy(&body);
    match fit::request("POST", &c.url, &[("Content-Type", "application/json")], &body) {
        Err(e) => failed(&e),
        Ok((200, _)) => fit::OK,
        Ok((status, _)) => failed(&format!("the API answered {status}")),
    }
}
