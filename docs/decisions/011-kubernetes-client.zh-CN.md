# 011：基于 net/http 的自有 Kubernetes 客户端，以及解析 kubeconfig 的 YAML 库

[English](011-kubernetes-client.md) | [简体中文](011-kubernetes-client.zh-CN.md)

状态：已采纳，见 I58（2026-10-22）。

## 背景

I58 的 Kubernetes provider 要监视 GatewayClass、Gateway 和 HTTPRoute，以及它们引用的 Service、EndpointSlice、
Namespace 和 ReferenceGrant。常规做法是用 client-go 的 informer，加上 `sigs.k8s.io/gateway-api` 中 Gateway API 的
Go 类型。另一种做法是 fit 自己的客户端：API server 的 REST API 是 HTTPS 上的 JSON，watch 是 JSON 事件流。

## 决定

- **不用 client-go。** client-go、apimachinery 和 k8s.io/api，再加 Gateway API 的类型，会带来约四十个模块
  （protobuf、gnostic、klog、structured-merge-diff、OpenAPI、Gateway API 自己生成的客户端）和几十 MB 体积，而这个
  二进制目前只有四个依赖，许多部署根本不会启用这个 provider。fit 需要的很少：分页列出资源，从列表的 resourceVersion
  开始 watch，watch 返回 410 Gone 时重新列出，并解码七种资源的少量字段。`provider/kubernetes` 用 `net/http` 实现这些：
  先 list，再带 bookmark 的 watch，每种资源保存在各自的 store 中，类型只含读取的字段。测试针对假的 API server 运行。
- **按 kubectl 的方式找凭据，但不支持 exec 插件。** 在 pod 中：service account 的 token（轮换时重新读取）、CA 和
  `KUBERNETES_SERVICE_HOST`。在集群外：kubeconfig 文件，当前或指定的 context，支持 CA、客户端证书和私钥（文件或
  data）、token 或 token 文件，以及 `insecure-skip-tls-verify`。云集群用于短期凭据的 `exec` 和 `auth-provider` 插件
  会被拒绝并给出说明；在那里请在集群内运行 fit，或使用 token。
- **用 `go.yaml.in/yaml/v3` 读取 kubeconfig**，因为它是 YAML。它是 YAML 组织维护的 `gopkg.in/yaml.v3` 后继版本，纯 Go，
  自身没有依赖；kind、kubeadm 和各家云写出的 kubeconfig 都需要真正的 YAML 解析器（块与流两种风格、带引号和不带引号的
  标量）。fit 将来的 YAML 配置适配器（见路线图）也可以使用它。
- **有界，且在控制面。** provider 在自己的 goroutine 上运行。每种资源最多保存 `max_objects` 个对象（10000）；watch 以外的
  请求限时 `request_timeout`；watch 每五分钟结束并重新建立；list 失败后每隔 1s 到 30s 重试。资源未安装（404，例如缺少
  Gateway API 的 CRD）时视为空，并会再次查找。

## 影响

- 新增一个依赖：`go.yaml.in/yaml/v3`。
- 回写资源状态（I59）使用同一个客户端：对 status 子资源发 JSON merge patch，并写明它基于的 resourceVersion，
  这样基于旧对象的写入会被拒绝（409），而不会覆盖更新的对象。
- 以后需要的资源种类，只需一个自己的类型，并在监视列表中加一行。资源种类可以写明服务器回答 404 时依次退回的旧版本
  （I59 的 TLSRoute：v1、v1alpha3、v1alpha2），以及 field selector（I59 的 Secret 只取类型为 `kubernetes.io/tls`
  的，其他 Secret 既不读取也不保存）。
