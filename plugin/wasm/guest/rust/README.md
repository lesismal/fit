# fit-guest (Rust)

The Rust side of fit's WASM plugin ABI for sync: the host functions of the modules `fit` and
`fit_sync` (see `plugin/wasm/sync.go` and [decision 036](../../../../docs/decisions/036-sync.md)),
wrapped, and `records`, which splits a batch into its records' canonical JSON. It has no
dependencies; parse records with the JSON library of your choice.

A plugin is a WASI reactor built as a `cdylib`:

```toml
[lib]
crate-type = ["cdylib"]

[dependencies]
fit-guest = { path = "path/to/fit/plugin/wasm/guest/rust" }
```

```rust
#[no_mangle]
pub extern "C" fn fit_sync_transform(n: u32) -> i32 {
    let batch = fit_guest::input(n);
    for (i, rec) in fit_guest::records(&batch).iter().enumerate() {
        fit_guest::emit(i, rec);
    }
    fit_guest::OK
}
```

```sh
rustup target add wasm32-wasip1
cargo build --release --target wasm32-wasip1
```

**Untested as a plugin**: the crate's own unit test runs on the host (`cargo test`), but no plugin
built from it for `wasm32-wasip1` has been run by fit's tests yet, since the machine it was written
on lacks that target (I165, pending review). The Go guest, `plugin/wasm/guest`, is the tested one.
