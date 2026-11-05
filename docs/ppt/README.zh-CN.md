# 演示文稿

[English](README.md) | [简体中文](README.zh-CN.md)

介绍 fit 的幻灯片：它是什么、怎么造、有多快、怎么用。每份 16 页、16:9，分简体中文和英文两版。

| | |
| --- | --- |
| [fit-intro.pptx](fit-intro.pptx) | 中文版 |
| [fit-intro-en.pptx](fit-intro-en.pptx) | 英文版 |
| [fit-intro.univer](fit-intro.univer) | 中文版的可编辑 Univer 文件 |
| [fit-intro-en.univer](fit-intro-en.univer) | 英文版的可编辑 Univer 文件 |

## 页面

背景（nginx、Caddy、Traefik 对比）、数据面的六件工作、控制面与不可变运行时图、请求路径上
没有 goroutine、从七层到四层的协议、流量治理、与 nginx 的基准、三种配置写法、不重启换配置、
可观测性、WebRTC / tunnel / LLM 三个角色、Go 与 WASM 扩展、1.0 之前的里程碑现状，以及三条
命令搭起一个 HTTPS 站点。

## 源文件

- [spec.md](spec.md)：页面规范，包括配色、字号和每一页的最终文案。
- [src/gen.py](src/gen.py)：生成中文页 `page-01.svg` … `page-16.svg`。
- [src/en_gen.py](src/en_gen.py)：用同一套版式生成英文页 `en-page-01.svg` … `en-page-16.svg`。
- `screenshots/cn/` 与 `screenshots/en/`：渲染预览图，一页一张，另有 `contact-slide.png`
  把每份 16 页汇成一张总览图。它们在本机生成，不纳入版本库。

每一页先写成 SVG，再编译进该份演示的 Univer Slide Unit，最后导出 pptx。基准页的数字来自
`bench/results/plan-2026-10-30`，其余来自[文档](../../README.md)本身。
