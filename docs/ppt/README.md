# Presentations

[English](README.md) | [简体中文](README.zh-CN.md)

Slide decks that introduce fit: what it is, how it is built, how it performs and how to run it.
Sixteen slides each, 16:9, in Simplified Chinese and English.

| | |
| --- | --- |
| [fit-intro.pptx](fit-intro.pptx) | the deck, in Simplified Chinese |
| [fit-intro-en.pptx](fit-intro-en.pptx) | the same deck, in English |
| [fit-intro.univer](fit-intro.univer) | the Chinese deck as an editable Univer file |
| [fit-intro-en.univer](fit-intro-en.univer) | the English deck as an editable Univer file |

## The pages

Background (nginx, Caddy and Traefik compared), the six jobs of the data plane, the control
plane and the immutable runtime graph, why no goroutine sits on the request path, protocols from
L7 down to L4, traffic policy, the benchmark against nginx, the three config syntaxes, reloading
without a restart, observability, the WebRTC, tunnel and LLM roles, extension in Go and WASM, the
state of the milestones through 1.0, and three commands to a site over HTTPS.

## The sources

- [spec.md](spec.md): the page spec — palette, type scale and the exact copy of every page.
- [src/gen.py](src/gen.py): writes the Chinese pages, `page-01.svg` … `page-16.svg`.
- [src/en_gen.py](src/en_gen.py): writes the English pages, `en-page-01.svg` … `en-page-16.svg`,
  through the same layout system.
- [screenshots/cn/contact-slide.png](screenshots/cn/contact-slide.png) and
  [screenshots/en/contact-slide.png](screenshots/en/contact-slide.png): all sixteen pages of each
  deck in one image; the other PNGs are one file per page.

A page is authored as SVG and compiled into the deck's Univer Slide Unit, which is then exported
to pptx. The numbers on the benchmark slide come from `bench/results/plan-2026-10-30`; the rest
from [the documentation](../../README.md) itself.
