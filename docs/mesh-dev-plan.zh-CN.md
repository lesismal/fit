# fit 服务网格数据面开发计划

[English](mesh-dev-plan.md) | [简体中文](mesh-dev-plan.zh-CN.md)

本计划把 [服务网格数据面路线图](mesh-roadmap.zh-CN.md) 拆成小迭代，编号为 `S01` 起，与网关的 `I`、`R` 迭代
分开。迭代的大小、收尾要求和会话方式与 [网关开发计划](dev-plan.zh-CN.md#如何进行一次迭代) 相同。

- [如何进行一次迭代](#如何进行一次迭代)
- [包结构](#包结构)
- [索引](#索引)
- [S0 前提与基线](#s0-前提与基线)
- [S1 身份与 HBONE](#s1-身份与-hbone)
- [S2 节点代理](#s2-节点代理)
- [S3 Waypoint](#s3-waypoint)
- [S4 运维与韧性](#s4-运维与韧性)
- [S5 独立模式与多网络](#s5-独立模式与多网络)
- [网格 1.0](#网格-10)

## 如何进行一次迭代

按 [网关开发计划](dev-plan.zh-CN.md#如何进行一次迭代) 的做法进行，另有以下几点：

- **选一个。** 在 [索引](#索引) 中取第一个状态为 `todo`、且依赖全部为 `done` 的迭代；依赖中的 `I` 编号指网关
  开发计划中的迭代。
- **只读需要的内容。** [开发进度](progress.zh-CN.md)、本文件的索引和该迭代的小节、路线图中的
  [设计原则](mesh-roadmap.zh-CN.md#设计原则)，以及迭代列出的包和参考。
- **只在 Linux 上。** `mesh/` 下的包只为 Linux 构建，测试在 Linux 上运行（CI 或基准主机）。需要 kind 和 Istio 的
  端到端测试放在 `test/mesh` 中，只在涉及它们的改动上运行。
- **对照上游。** 涉及 Istio 协议（ZDS、WDS、CA、HBONE）的迭代，以支持范围内的 Istio 版本的源码和 proto 为准，
  在 progress 中记下对照的版本。
- **收尾。** 在两个本文件中标记状态，在两个 [开发进度](progress.zh-CN.md) 文件中按迭代记录，与网关迭代记在一起。

**会话提示词：**

> 实现 docs/mesh-dev-plan.zh-CN.md 中的迭代 SXX。先读 docs/progress.zh-CN.md、docs/mesh-dev-plan.zh-CN.md 的
> 索引和该迭代的小节，以及 docs/mesh-roadmap.zh-CN.md 中的设计原则，然后只读它列出的包。改动限制在该迭代范围内，
> 补上测试，完成后在两个 mesh-dev-plan 文件中标记为 done，并记录到两个 progress 文件中。

## 包结构

S01 建立、后续迭代逐步填充的结构。名字可以调整。

| 包 | 内容 |
| --- | --- |
| `mesh/identity` | SPIFFE ID、证书缓存与轮换、CA 客户端 |
| `mesh/hbone` | HBONE 服务端、客户端和连接池 |
| `mesh/xds` | delta ADS 客户端 |
| `mesh/wds` | 工作负载发现：资源到不可变快照的编译 |
| `mesh/zds` | 与 istio-cni 的 ZDS 协议 |
| `mesh/netns` | 进入网络命名空间创建 socket 的专用池 |
| `mesh/node` | 节点代理：出站、入站、pod 生命周期 |
| `mesh/authz` | 四层和七层授权策略 |
| `mesh/telemetry` | 网格指标与日志 |
| `mesh/waypoint` | waypoint 的 HBONE 入口与出口 |
| `mesh/meshd` | 独立模式的轻量控制面（S5） |
| `test/mesh` | kind 加 Istio 的端到端测试、网络命名空间模拟环境 |

## 索引

状态：`todo`、`doing` 或 `done`。

| ID | 迭代 | 依赖 | 状态 |
| --- | --- | --- | --- |
| **S0** | | | |
| S01 | 决定说明与包骨架 | — | todo |
| S02 | 网格测试环境 | S01 | todo |
| S03 | ztunnel 基线 | S02 | todo |
| S04 | fib：HTTP/2 客户端 CONNECT 隧道（F1） | S01 | todo |
| S05 | fib：描述符、socket 选项与原始目标地址（F2–F4） | S01 | todo |
| **S1** | | | |
| S06 | SPIFFE 身份与证书缓存 | S01 | todo |
| S07 | Istio CA 客户端 | S06、I49 | todo |
| S08 | HBONE 服务端 | S06 | todo |
| S09 | HBONE 客户端与连接池 | S04、S06 | todo |
| S10 | HBONE 与 ztunnel 互通 | S08、S09、S02 | todo |
| **S2** | | | |
| S11 | xDS 客户端 | I49 | todo |
| S12 | 工作负载发现快照 | S11 | todo |
| S13 | 网络命名空间池 | S05 | todo |
| S14 | ZDS 与 pod 生命周期 | S13 | todo |
| S15 | 出站路径 | S09、S12、S14 | todo |
| S16 | 入站路径 | S08、S14 | todo |
| S17 | 四层授权 | S12、S16 | todo |
| S18 | 四层遥测 | S15、S16 | todo |
| S19 | 节点代理退出测试 | S07、S10、S17、S18、S03 | todo |
| **S3** | | | |
| S20 | Waypoint GatewayClass 与指向 | S19、I58 | todo |
| S21 | Waypoint 数据路径 | S20、S09、S08 | todo |
| S22 | GAMMA 路由 | S21、I59 | todo |
| S23 | 七层授权与认证 | S21、I42 | todo |
| S24 | 七层遥测与追踪 | S21、I65 | todo |
| S25 | Waypoint 退出测试 | S22、S23、S24 | todo |
| **S4** | | | |
| S26 | 节点代理无中断升级 | S19 | todo |
| S27 | 过载保护与每 pod 上限 | S19 | todo |
| S28 | DNS 代理 | S19 | todo |
| S29 | 调试接口与 `fit mesh` | S19 | todo |
| S30 | 打包与安装 | S26 | todo |
| **S5** | | | |
| S31 | fit-meshd 工作负载发现 | S19 | todo |
| S32 | SPIRE 身份 | S06 | todo |
| S33 | 多网络双层 HBONE | S25 | todo |
| S34 | sidecar 模式评估 | S25 | todo |
| **网格 1.0** | | | |
| S35 | Istio 兼容矩阵 | S25、S30 | todo |
| S36 | 网格文档 | S35 | todo |

## S0 前提与基线

### S01 决定说明与包骨架

- **做什么**：决定说明：ambient 优先、支持的 Istio 版本范围、protobuf 和 gRPC 相关依赖及其只在 `mesh/` 中引入、
  waypoint 走 GAMMA 而不是 Envoy xDS，以及第三方 waypoint 在 istiod 中的支持情况（对照 Istio 源码确认）；
  只在 Linux 上构建的 `mesh/` 包骨架；`fit node` 子命令读取节点代理配置并在非 Linux 上报错。
- **完成标准**：决定说明已提交；`fit node --help` 可用；非 Linux 构建不包含 `mesh/`。
- **涉及**：`docs/decisions`、`mesh`、`cmd/fit`。

### S02 网格测试环境

- **做什么**：`test/mesh` 中两套环境：一是 CI 中的 kind 加 Istio ambient，可把 ztunnel 镜像换成 fit-node；
  二是用 `ip netns` 和 veth 模拟多个 pod 的脚本，在基准主机上使用，不需要 Kubernetes。
- **完成标准**：CI 能起 kind 集群并跑通原版 ztunnel 下的 bookinfo；模拟脚本能在主机上建出和拆掉两个 pod 的环境。
- **涉及**：`test/mesh`、CI 配置。

### S03 ztunnel 基线

- **做什么**：在基准主机的模拟环境中，用本地配置模式运行 ztunnel，测吞吐、p50/p99 延迟、每连接内存和 CPU，
  与直连对比。
- **完成标准**：结果和所用的 ztunnel 版本、配置一起提交到 `bench/results`。
- **涉及**：`bench`、`test/mesh`。

### S04 fib：HTTP/2 客户端 CONNECT 隧道（F1）

- **做什么**：在 fib 中实现：HTTP/2 客户端发起 CONNECT 和扩展 CONNECT 流，每个流作为一个 `Tunnel`，带流控和背压，
  一个连接上可有多个隧道并遵守对端的并发流上限。先写进两份 fib-api-requests，再按 fib 的标准实现和推送。
- **完成标准**：fib 的测试、race、vet、staticcheck 通过，fib CI 通过；fit 按提交哈希升级。
- **涉及**：fib `http`；fit `docs/fib-api-requests*.md`、`go.mod`。
- **参考**：fib `http/h2_tunnel.go`、`http/h2_client.go`。

### S05 fib：描述符、socket 选项与原始目标地址（F2–F4）

- **做什么**：在 fib 中实现：把已有的监听描述符作为 binding 加入、移除和排空；用调用方创建的 socket 拨号，以及
  拨号前设置 socket 选项的钩子；在 Linux 上取得接受连接的原始目标地址。先写进两份 fib-api-requests。
- **完成标准**：fib 在 linux、darwin、windows、freebsd 上的测试和静态检查通过（非 Linux 上相应能力返回明确的不支持
  错误）；fit 按提交哈希升级。
- **涉及**：fib 引擎、`binding.go`、`dial.go`；fit `docs/fib-api-requests*.md`、`go.mod`。

## S1 身份与 HBONE

### S06 SPIFFE 身份与证书缓存

- **做什么**：解析和校验 SPIFFE ID 与 trust domain；按身份缓存证书和私钥，有数量上限，过半有效期时提前申请新证书；
  申请接口抽象成接口，供 Istio CA 和 SPIRE 实现；从证书中按 URI SAN 取得对端身份。
- **完成标准**：测试覆盖轮换、上限、过期和错误的 trust domain。
- **涉及**：`mesh/identity`。

### S07 Istio CA 客户端

- **做什么**：用 fib 的 gRPC 客户端向 istiod 的证书服务申请证书，代表节点上的服务账号签发；申请在控制面执行，
  有并发上限和超时，失败时退避重试。
- **完成标准**：针对模拟 CA 的测试，以及在 kind 中从真实 istiod 取得证书。
- **涉及**：`mesh/identity`。
- **参考**：fib `grpc`；Istio 证书服务的 proto。

### S08 HBONE 服务端

- **做什么**：15008 上的 TLS 监听，要求客户端证书并取得对端身份；接受 HTTP/2 CONNECT，按目标地址连接本地目标，
  用 `Tunnel` 双向转发并背压；每连接的流数和每身份的隧道数有上限。
- **完成标准**：测试覆盖身份校验失败、慢对端、大流量、连接中途断开；race 下通过。
- **涉及**：`mesh/hbone`、`listener`。
- **参考**：fib `http/h2_tunnel.go`。

### S09 HBONE 客户端与连接池

- **做什么**：按（源身份、目标地址、目标身份）复用的 HTTP/2 mTLS 连接池；在连接上开 CONNECT 隧道；连接的流数到上限时
  开新连接；空闲回收；目标身份不符时拒绝。
- **完成标准**：测试覆盖复用、上限、身份不符、对端 GOAWAY 和断连；基准测量新隧道的开销（在基准主机上）。
- **涉及**：`mesh/hbone`。

### S10 HBONE 与 ztunnel 互通

- **做什么**：在模拟环境中让 fit 的 HBONE 客户端连 ztunnel 的入站、ztunnel 的出站连 fit 的 HBONE 服务端，并在
  kind 中重复。
- **完成标准**：两个方向都能传输数据，身份和 `Forwarded` 等头与 ztunnel 的预期一致。
- **涉及**：`test/mesh`、`mesh/hbone`。

## S2 节点代理

### S11 xDS 客户端

- **做什么**：基于 fib gRPC 双向流的 delta ADS 客户端：订阅、ACK 和 NACK、断线重连带退避、资源数和消息大小有上限；
  按资源类型把更新交给编译器。
- **完成标准**：针对模拟 xDS 服务端的测试，覆盖增量更新、删除、NACK 和重连。
- **涉及**：`mesh/xds`。

### S12 工作负载发现快照

- **做什么**：把 WDS 的 `Address`（Workload、Service）和 `Authorization` 资源编译成按 IP、服务 VIP 和端口索引的不可变
  快照，原子替换；记录每个服务的 waypoint。
- **完成标准**：测试覆盖增删改、服务与端点的对应、waypoint 指向；查找不分配内存。
- **涉及**：`mesh/wds`。

### S13 网络命名空间池

- **做什么**：一个有上限的专用池，在锁定的线程上进入指定的网络命名空间，创建监听或拨号用的 socket，再回到原命名空间；
  无法恢复时丢弃该线程。
- **完成标准**：测试证明池外的 goroutine 从不在其他命名空间中运行；池满时请求排队且有上限。
- **涉及**：`mesh/netns`。

### S14 ZDS 与 pod 生命周期

- **做什么**：在 Unix socket 上实现与 istio-cni 的 ZDS 协议，接收 pod 的网络命名空间描述符；为每个 pod 经 S13 创建
  15001、15006、15008 三个监听并交给 fib（F2）；pod 删除时排空并关闭；节点代理重启时按快照恢复。
- **完成标准**：测试覆盖添加、删除、重复消息和重启后的恢复。
- **涉及**：`mesh/zds`、`mesh/node`。

### S15 出站路径

- **做什么**：15001 上取得原始目标地址（F4），在快照中查服务和端点，用 `upstream` 的负载均衡选端点；目标在网格内时
  经 HBONE 发往目标节点或 waypoint，在网格外时透传；双向背压，连接和空闲超时。
- **完成标准**：模拟环境中两个 pod 互访经 HBONE；网格外目标透传；慢对端和断连测试。
- **涉及**：`mesh/node`、`upstream`。

### S16 入站路径

- **做什么**：15008 上终结 HBONE 后，以原始源地址（F3 的 `IP_TRANSPARENT`）连接 pod 中的应用；15006 上处理入站明文。
- **完成标准**：应用看到的源地址是调用方 pod 的地址；测试覆盖应用不在监听、慢应用和断连。
- **涉及**：`mesh/node`。

### S17 四层授权

- **做什么**：执行 WDS 下发的授权策略：按命名空间、身份、端口、IP 的 ALLOW 和 DENY，DENY 优先；在快照编译时生成
  匹配结构。
- **完成标准**：表驱动测试覆盖 Istio AuthorizationPolicy 四层语义的各个组合。
- **涉及**：`mesh/authz`。

### S18 四层遥测

- **做什么**：`istio_tcp_connections_opened_total`、`istio_tcp_sent_bytes_total` 等指标，带 Istio 标准标签；连接日志；
  标签基数有上限。
- **完成标准**：指标名字和标签与 ztunnel 一致的测试；基数上限测试。
- **涉及**：`mesh/telemetry`、`observe`。

### S19 节点代理退出测试

- **做什么**：在 kind 中用 fit-node 替换 ztunnel，跑 bookinfo 和 Istio ambient 集成测试的四层部分；在基准主机上与
  S03 的基线对比。
- **完成标准**：测试在 CI 中通过；对比结果已提交，差距大的项拆成新迭代。
- **涉及**：`test/mesh`、`bench`。

## S3 Waypoint

### S20 Waypoint GatewayClass 与指向

- **做什么**：`fit-waypoint` GatewayClass 和对应的 Gateway 控制器（复用 M4 的 Kubernetes provider）；Gateway 声明
  HBONE 监听；确认 istiod 在 WDS 中把使用它的服务指向 fit waypoint。
- **完成标准**：kind 中给服务加上 waypoint 标签后，ztunnel 或 fit-node 把流量发往 fit waypoint。
- **涉及**：`mesh/waypoint`、`provider/kubernetes`。

### S21 Waypoint 数据路径

- **做什么**：15008 上终结 HBONE，把内部流量交给 fit 的 HTTP/gRPC 处理链；出站经 HBONE 客户端发往端点，保留源身份；
  非 HTTP 的流量按四层转发。
- **完成标准**：经 waypoint 的 HTTP/1.1、h2、gRPC 和 TCP 测试；慢对端和断连测试。
- **涉及**：`mesh/waypoint`、`handler/proxy`。

### S22 GAMMA 路由

- **做什么**：以 Service 为 parentRef 的 HTTPRoute 和 GRPCRoute 转换成 waypoint 的路由；状态写回。
- **完成标准**：Gateway API 一致性测试中 GAMMA 的 mesh profile 在 CI 中通过。
- **涉及**：`provider/kubernetes`、`mesh/waypoint`。

### S23 七层授权与认证

- **做什么**：Istio AuthorizationPolicy 的七层规则（路径、方法、header、JWT claims）和 RequestAuthentication，转换成
  M3 的认证和授权中间件；源身份作为主体参与匹配。
- **完成标准**：表驱动测试覆盖 ALLOW、DENY 和 CUSTOM 之外的组合；kind 中的策略测试。
- **涉及**：`mesh/authz`、`middleware/auth`。

### S24 七层遥测与追踪

- **做什么**：`istio_requests_total`、`istio_request_duration_milliseconds` 等指标，带 Istio 标准标签；链路追踪的
  上下文传播。
- **完成标准**：指标与 Envoy waypoint 一致的测试；追踪 span 在 collector 中可见。
- **涉及**：`mesh/telemetry`、`observe`。

### S25 Waypoint 退出测试

- **做什么**：Istio waypoint 相关集成测试的选定子集；在基准主机上与 Envoy waypoint 对比。
- **完成标准**：测试在 CI 中通过；对比结果已提交。
- **涉及**：`test/mesh`、`bench`。

## S4 运维与韧性

### S26 节点代理无中断升级

- **做什么**：新实例启动后经 ZDS 重新取得各 pod 的命名空间，在其中用 `SO_REUSEPORT` 创建监听；旧实例停止接受并排空，
  到期关闭。
- **完成标准**：持续流量下升级，已建立的连接不断开，新连接不失败。
- **涉及**：`mesh/node`、`mesh/zds`。

### S27 过载保护与每 pod 上限

- **做什么**：每个 pod 的连接、隧道和待发字节上限；节点级内存水位；超限时分级动作（停止新隧道、拒绝新连接）。
- **完成标准**：一个 pod 发起大量连接时，同节点其他 pod 的延迟保持在上限之内（在基准主机的模拟环境中测）。
- **涉及**：`mesh/node`。

### S28 DNS 代理

- **做什么**：在 pod 中拦截 DNS，对 WDS 中的服务名直接应答，其余请求经异步客户端转发；缓存有上限。
- **完成标准**：测试覆盖网格内名字、外部名字、上游超时。
- **涉及**：`mesh/node`。

### S29 调试接口与 `fit mesh`

- **做什么**：节点代理的管理端口：配置快照、工作负载、证书和隧道列表；`fit mesh` 命令读取它们。
- **完成标准**：每个接口有测试；接口默认只在 localhost 上。
- **涉及**：`mesh/node`、`admin`、`cmd/fit`。

### S30 打包与安装

- **做什么**：fit-node 和 fit waypoint 镜像；替换 Istio ztunnel chart 中镜像的说明；waypoint 的部署清单。
- **完成标准**：按文档能在新的 kind 集群中装好并运行 bookinfo。
- **涉及**：`deploy`、`examples`。

## S5 独立模式与多网络

### S31 fit-meshd 工作负载发现

- **做什么**：从 Kubernetes API 生成 WDS 资源并经 xDS 下发，不需要 istiod；授权策略来自 Istio CRD 或 fit 自己的格式
  （决定说明）。
- **完成标准**：不装 istiod 时，fit-node 从 fit-meshd 取得工作负载并正常转发。
- **涉及**：`mesh/meshd`。

### S32 SPIRE 身份

- **做什么**：经 SPIFFE Workload API（Delegated Identity）为节点上的工作负载取得 X.509 SVID，作为 S06 接口的另一个实现。
- **完成标准**：kind 中用 SPIRE 签发身份，bookinfo 在 mTLS 下工作。
- **涉及**：`mesh/identity`。

### S33 多网络双层 HBONE

- **做什么**：经东西向网关的双层 HBONE：外层到网关、内层到目标；目标在其他网络时自动选择。
- **完成标准**：两个 kind 集群模拟两个网络，服务互访。
- **涉及**：`mesh/hbone`、`mesh/node`。

### S34 sidecar 模式评估

- **做什么**：评估 fit 作为 sidecar 的价值和代价（拦截、配置来源、与 ambient 共存），写决定说明；如决定做，拆成新的迭代。
- **完成标准**：决定说明已提交。
- **涉及**：`docs/decisions`。

## 网格 1.0

### S35 Istio 兼容矩阵

- **做什么**：确定支持的 Istio 版本列表；CI 定时任务按矩阵跑互通测试；协议定义按版本生成。
- **完成标准**：矩阵发布在文档中，CI 任务通过。
- **涉及**：`test/mesh`、CI 配置、`docs`。

### S36 网格文档

- **做什么**：安装、从 ztunnel 和 Envoy waypoint 迁移、排障指南，中英文各一份。
- **完成标准**：按文档能完成安装和迁移。
- **涉及**：`docs`。

路线图网格 1.0 中的外部安全审计不是一个迭代：它在 S35 完成后于代码之外安排。
