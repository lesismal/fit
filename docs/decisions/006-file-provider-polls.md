# 006: The file provider polls

[English](006-file-provider-polls.md) | [简体中文](006-file-provider-polls.zh-CN.md)

Status: accepted, in I27 (2026-10-19).

## Context

I27 applies the config file each time it changes. A watcher can ask the operating system for
events (inotify, kqueue, ReadDirectoryChangesW, usually through `fsnotify`) or look at the file
from time to time.

## Decision

- **It polls**: a goroutine of its own looks at the file's size and modification time every
  `-watch-interval` (1s), and reads it only when they change. No dependency comes in, it works the
  same on every system fib runs on, and it sees a file replaced by a rename, as editors and
  Kubernetes ConfigMaps do it, as well as one written in place, which events report differently
  everywhere.
- **A change applies once the file has stayed as it is for 500ms**, and only if its contents
  differ from what was applied last, by SHA-256: an editor's several writes make one reload, and a
  touch none. A config that fails is reported once, and the next edit is tried.
- **It is turned on with `fit run -watch`**, not in the config, since it is about where the config
  comes from; it applies through the same `admin.Configs.Apply` as SIGHUP and the Admin API.
- **The file read is bounded**, at 64 MiB.

## Consequences

- A change takes up to the interval and the debounce to apply, about 1.5s at the defaults.
- A file whose size and modification time stay the same after an edit within the file system's
  timestamp resolution is not seen until it changes again; SIGHUP still applies it.
