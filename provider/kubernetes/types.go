package kubernetes

// The fields the provider reads of the kinds it watches, as the API server
// writes them in JSON; everything else is left out.

type meta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	ResourceVersion   string            `json:"resourceVersion,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
}

// key is how a store keys an object: its namespace and name.
func (m *meta) key() string {
	if m.Namespace == "" {
		return m.Name
	}
	return m.Namespace + "/" + m.Name
}

type listMeta struct {
	ResourceVersion string `json:"resourceVersion"`
	Continue        string `json:"continue,omitempty"`
}

// Namespace is a core/v1 Namespace, for its labels.
type Namespace struct {
	Metadata meta `json:"metadata"`
}

// GatewayClass is a gateway.networking.k8s.io/v1 GatewayClass.
type GatewayClass struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		ControllerName string `json:"controllerName"`
	} `json:"spec"`
}

// Gateway is a gateway.networking.k8s.io/v1 Gateway.
type Gateway struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		GatewayClassName string     `json:"gatewayClassName"`
		Listeners        []Listener `json:"listeners"`
	} `json:"spec"`
}

// Listener is one of a Gateway's listeners.
type Listener struct {
	Name          string         `json:"name"`
	Hostname      *string        `json:"hostname,omitempty"`
	Port          int            `json:"port"`
	Protocol      string         `json:"protocol"`
	AllowedRoutes *AllowedRoutes `json:"allowedRoutes,omitempty"`
}

// AllowedRoutes says which routes may attach to a listener.
type AllowedRoutes struct {
	Namespaces *struct {
		From     string         `json:"from,omitempty"`
		Selector *LabelSelector `json:"selector,omitempty"`
	} `json:"namespaces,omitempty"`
	Kinds []struct {
		Group *string `json:"group,omitempty"`
		Kind  string  `json:"kind"`
	} `json:"kinds,omitempty"`
}

// LabelSelector is a metav1.LabelSelector.
type LabelSelector struct {
	MatchLabels      map[string]string `json:"matchLabels,omitempty"`
	MatchExpressions []struct {
		Key      string   `json:"key"`
		Operator string   `json:"operator"`
		Values   []string `json:"values,omitempty"`
	} `json:"matchExpressions,omitempty"`
}

// HTTPRoute is a gateway.networking.k8s.io/v1 HTTPRoute.
type HTTPRoute struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		ParentRefs []ParentRef     `json:"parentRefs,omitempty"`
		Hostnames  []string        `json:"hostnames,omitempty"`
		Rules      []HTTPRouteRule `json:"rules,omitempty"`
	} `json:"spec"`
}

// ParentRef names the Gateway, and maybe the listener, a route attaches to.
type ParentRef struct {
	Group       *string `json:"group,omitempty"`
	Kind        *string `json:"kind,omitempty"`
	Namespace   *string `json:"namespace,omitempty"`
	Name        string  `json:"name"`
	SectionName *string `json:"sectionName,omitempty"`
	Port        *int    `json:"port,omitempty"`
}

// HTTPRouteRule is one of an HTTPRoute's rules.
type HTTPRouteRule struct {
	Matches     []HTTPRouteMatch  `json:"matches,omitempty"`
	Filters     []HTTPRouteFilter `json:"filters,omitempty"`
	BackendRefs []HTTPBackendRef  `json:"backendRefs,omitempty"`
}

// HTTPRouteMatch is what a request must match.
type HTTPRouteMatch struct {
	Path *struct {
		Type  *string `json:"type,omitempty"`
		Value *string `json:"value,omitempty"`
	} `json:"path,omitempty"`
	Headers     []HTTPMatchValue `json:"headers,omitempty"`
	QueryParams []HTTPMatchValue `json:"queryParams,omitempty"`
	Method      *string          `json:"method,omitempty"`
}

// HTTPMatchValue matches a header or a query parameter.
type HTTPMatchValue struct {
	Type  *string `json:"type,omitempty"`
	Name  string  `json:"name"`
	Value string  `json:"value"`
}

// HTTPRouteFilter changes requests or responses.
type HTTPRouteFilter struct {
	Type                   string               `json:"type"`
	RequestHeaderModifier  *HTTPHeaderFilter    `json:"requestHeaderModifier,omitempty"`
	ResponseHeaderModifier *HTTPHeaderFilter    `json:"responseHeaderModifier,omitempty"`
	RequestRedirect        *HTTPRequestRedirect `json:"requestRedirect,omitempty"`
	URLRewrite             *HTTPURLRewrite      `json:"urlRewrite,omitempty"`
	RequestMirror          *HTTPRequestMirror   `json:"requestMirror,omitempty"`
}

// HTTPHeaderFilter sets, adds and removes header fields.
type HTTPHeaderFilter struct {
	Set []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"set,omitempty"`
	Add []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// HTTPPathModifier replaces a request's path.
type HTTPPathModifier struct {
	Type               string  `json:"type"`
	ReplaceFullPath    *string `json:"replaceFullPath,omitempty"`
	ReplacePrefixMatch *string `json:"replacePrefixMatch,omitempty"`
}

// HTTPRequestRedirect answers with a redirect.
type HTTPRequestRedirect struct {
	Scheme     *string           `json:"scheme,omitempty"`
	Hostname   *string           `json:"hostname,omitempty"`
	Path       *HTTPPathModifier `json:"path,omitempty"`
	Port       *int              `json:"port,omitempty"`
	StatusCode *int              `json:"statusCode,omitempty"`
}

// HTTPURLRewrite changes the request's host and path.
type HTTPURLRewrite struct {
	Hostname *string           `json:"hostname,omitempty"`
	Path     *HTTPPathModifier `json:"path,omitempty"`
}

// HTTPRequestMirror copies requests to a backend.
type HTTPRequestMirror struct {
	BackendRef BackendObjectRef `json:"backendRef"`
	Percent    *int             `json:"percent,omitempty"`
	Fraction   *struct {
		Numerator   int  `json:"numerator"`
		Denominator *int `json:"denominator,omitempty"`
	} `json:"fraction,omitempty"`
}

// BackendObjectRef names a backend: a Service's port.
type BackendObjectRef struct {
	Group     *string `json:"group,omitempty"`
	Kind      *string `json:"kind,omitempty"`
	Name      string  `json:"name"`
	Namespace *string `json:"namespace,omitempty"`
	Port      *int    `json:"port,omitempty"`
}

// HTTPBackendRef is a rule's backend, with its weight.
type HTTPBackendRef struct {
	BackendObjectRef
	Weight  *int              `json:"weight,omitempty"`
	Filters []HTTPRouteFilter `json:"filters,omitempty"`
}

// ReferenceGrant is a gateway.networking.k8s.io/v1beta1 ReferenceGrant:
// what in its namespace may be referred to from others.
type ReferenceGrant struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		From []struct {
			Group     string `json:"group"`
			Kind      string `json:"kind"`
			Namespace string `json:"namespace"`
		} `json:"from"`
		To []struct {
			Group string  `json:"group"`
			Kind  string  `json:"kind"`
			Name  *string `json:"name,omitempty"`
		} `json:"to"`
	} `json:"spec"`
}

// Service is a core/v1 Service.
type Service struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		Type         string        `json:"type,omitempty"`
		ClusterIP    string        `json:"clusterIP,omitempty"`
		ExternalName string        `json:"externalName,omitempty"`
		Ports        []ServicePort `json:"ports,omitempty"`
	} `json:"spec"`
}

// ServicePort is one of a Service's ports.
type ServicePort struct {
	Name        string  `json:"name,omitempty"`
	Protocol    string  `json:"protocol,omitempty"`
	Port        int     `json:"port"`
	AppProtocol *string `json:"appProtocol,omitempty"`
}

// EndpointSlice is a discovery.k8s.io/v1 EndpointSlice.
type EndpointSlice struct {
	Metadata    meta   `json:"metadata"`
	AddressType string `json:"addressType"`
	Endpoints   []struct {
		Addresses  []string `json:"addresses"`
		Conditions struct {
			Ready       *bool `json:"ready,omitempty"`
			Serving     *bool `json:"serving,omitempty"`
			Terminating *bool `json:"terminating,omitempty"`
		} `json:"conditions"`
	} `json:"endpoints"`
	Ports []struct {
		Name     *string `json:"name,omitempty"`
		Protocol *string `json:"protocol,omitempty"`
		Port     *int    `json:"port,omitempty"`
	} `json:"ports"`
}
