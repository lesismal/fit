package mesh

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"strings"

	"github.com/lesismal/fit/internal/certpin"
	"github.com/lesismal/fit/llm/internal/model"
)

// Links off loopback go over TLS, the dialer checking the listener's
// certificate by its pin or by the authorities it chains to: the password
// handshake is checked once, when a link opens, and what follows it carries
// no proof of its own, so on a plain link whoever sits between the two ends
// could pass the handshake through and then speak for either. Inside TLS the
// handshake still runs, so that a link needs the certificate and the
// password both.
//
// 回环之外的连接走 TLS，拨号方按固定指纹或证书颁发机构校验监听方的证书：密码握手只在连接建立时校验一次，之后的内容不带
// 自身的证明，所以在明文连接上，处于两端之间的任何一方都可以转发握手，然后冒充任一端。在 TLS 之内握手照常进行，连接需要
// 证书与密码两者。

// Loopback reports whether host is a loopback address or localhost: a link
// there may go without TLS.
// Loopback 表示 host 是否为回环地址或 localhost：到这里的连接可以不用 TLS。
func Loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// serverTLS is the TLS a listener of cfg takes links with, and the
// fingerprint of its certificate; nil without TLS.
func serverTLS(cfg model.ListenConfig) (*tls.Config, string, error) {
	var cert tls.Certificate
	var err error
	switch {
	case cfg.CertFile != "":
		cert, err = tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	case cfg.Cert != "":
		cert, err = tls.X509KeyPair([]byte(cfg.Cert), []byte(cfg.Key))
	case cfg.SelfSigned:
		return nil, "", BadRequest("self_signed: no certificate has been made yet")
	default:
		return nil, "", nil
	}
	if err != nil {
		return nil, "", BadRequest("the listener's certificate: %v", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		// Every link is a full handshake: no tickets to resume with.
		SessionTicketsDisabled: true,
	}, certpin.Fingerprint(cert), nil
}

// clientTLS is the TLS a target of req at host is dialed with, nil without
// TLS: the listener's certificate is checked against req's pin, or against
// the authorities of its CA file with the name, and no session is resumed,
// so that every link checks it.
func clientTLS(req model.DialReq, host string) (*tls.Config, error) {
	if !req.TLS() {
		if !Loopback(host) {
			return nil, BadRequest("%s is not a loopback address: set pin to the fingerprint of the peer listener's certificate, as its pages show it, or ca_file, so that the link is made over TLS to that listener alone", req.Addr)
		}
		return nil, nil
	}
	if req.Pin != "" && req.CAFile != "" {
		return nil, BadRequest("pin or ca_file, one of the two")
	}
	conf := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS13, SessionTicketsDisabled: true}
	if req.Pin != "" {
		pin, err := certpin.Parse(req.Pin)
		if err != nil {
			return nil, BadRequest("%v", err)
		}
		certpin.Pinned(conf, pin, "mesh: the peer listener's")
		return conf, nil
	}
	pem, err := os.ReadFile(req.CAFile)
	if err != nil {
		return nil, BadRequest("ca_file: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, BadRequest("ca_file %s holds no PEM certificate", req.CAFile)
	}
	conf.RootCAs = pool
	if req.ServerName != "" {
		conf.ServerName = req.ServerName
	}
	return conf, nil
}
