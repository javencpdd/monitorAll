// Package media 负责 MediaMTX 相关能力：自签 CA/Server 证书生成（含 LAN IP SAN）、
// RTMP URL 解析与三路播放地址编排、MediaMTX HTTP API v3 客户端。
package media

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monitorall/monitorall/internal/config"
)

// ————————————————— 证书常量 —————————————————

const (
	// CertFileName 为服务端证书文件名。
	CertFileName = "server.crt"
	// KeyFileName 为服务端私钥文件名。
	KeyFileName = "server.key"
	// CAFileName 为自签 CA 证书文件名（供浏览器安装信任）。
	CAFileName = "ca.crt"
	// CAKeyFileName 为 CA 私钥文件名。
	CAKeyFileName = "ca.key"
	// RSABits 为 RSA 密钥长度。
	RSABits = 2048
	// CertValidYears 为证书有效期（年）。
	CertValidYears = 3
	// ContainerHostname 为 docker-compose 中的容器名，必须进 SAN。
	ContainerHostname = "monitorall"
)

// CertInfo 描述当前生效的证书信息（供 /api/v1/system/runtime 与启动横幅使用）。
type CertInfo struct {
	Enabled    bool     `json:"enabled"`
	Dir        string   `json:"dir"`
	CertPath   string   `json:"certPath"`
	KeyPath    string   `json:"keyPath"`
	CAPath     string   `json:"caPath"`
	Fingerprint string  `json:"fingerprint"`
	NotAfter   int64    `json:"notAfter"`
	SANs       []string `json:"sans"`
	Generated  bool     `json:"generated"`
}

// EnsureCertificate 保证证书目录中存在可用的自签 CA 与服务端证书。
// SAN 必须包含 localhost / 127.0.0.1 / 全部 LAN IPv4 / 配置的 lanHost / 容器名，
// 否则 Chrome 会报 NET::ERR_CERT_COMMON_NAME_INVALID（D2）。
func EnsureCertificate(cfg *config.Config, lanHosts []string) (*CertInfo, error) {
	if cfg == nil {
		return nil, errors.New("配置为空")
	}
	dir := cfg.Server.CertDir
	if dir == "" {
		dir = config.DefaultCertDir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建证书目录失败: %w", err)
	}
	certPath := filepath.Join(dir, CertFileName)
	keyPath := filepath.Join(dir, KeyFileName)
	caPath := filepath.Join(dir, CAFileName)
	caKeyPath := filepath.Join(dir, CAKeyFileName)

	info := &CertInfo{Enabled: true, Dir: dir, CertPath: certPath, KeyPath: keyPath, CAPath: caPath}

	if !cfg.Server.TLSEnabled || !cfg.Server.AutoCert {
		info.Enabled = false
		return info, nil
	}

	if fileExists(certPath) && fileExists(keyPath) {
		// 已存在则读取摘要，不重复签发
		if fp, notAfter, sans, err := readCertSummary(certPath); err == nil {
			info.Fingerprint = fp
			info.NotAfter = notAfter
			info.SANs = sans
			return info, nil
		}
	}

	sans := BuildSANs(cfg, lanHosts)
	caCert, caKey, err := generateCA()
	if err != nil {
		return nil, err
	}
	srvCertDER, srvKey, notAfter, err := issueServerCert(caCert, caKey, sans)
	if err != nil {
		return nil, err
	}

	if err := writePEM(caPath, "CERTIFICATE", caCert.Raw); err != nil {
		return nil, err
	}
	if err := writePEM(caKeyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(caKey)); err != nil {
		return nil, err
	}
	if err := writePEM(certPath, "CERTIFICATE", srvCertDER); err != nil {
		return nil, err
	}
	if err := writePEM(keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(srvKey)); err != nil {
		return nil, err
	}

	info.Fingerprint = fingerprintDER(srvCertDER)
	info.NotAfter = notAfter
	info.SANs = sans
	info.Generated = true
	return info, nil
}

// BuildSANs 构造证书 SAN 列表：localhost、127.0.0.1、全部 LAN IPv4、lanHost、容器名。
func BuildSANs(cfg *config.Config, lanHosts []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(lanHosts)+4)
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	add("localhost")
	add("127.0.0.1")
	add("::1")
	add(ContainerHostname)
	if cfg != nil {
		add(cfg.Server.LANHost)
	}
	for _, h := range lanHosts {
		add(h)
	}
	return out
}

// generateCA 生成自签 CA（RSA 2048，有效期 10 年）。
func generateCA() (*x509.Certificate, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, RSABits)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "MonitorAll Local CA", Organization: []string{"MonitorAll"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

// issueServerCert 用 CA 签发服务端证书，SAN 同时写入 DNS 与 IP 两种形式。
func issueServerCert(ca *x509.Certificate, caKey *rsa.PrivateKey, sans []string) ([]byte, *rsa.PrivateKey, int64, error) {
	key, err := rsa.GenerateKey(rand.Reader, RSABits)
	if err != nil {
		return nil, nil, 0, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, 0, err
	}
	notAfter := time.Now().AddDate(CertValidYears, 0, 0)
	dnsNames := make([]string, 0, len(sans))
	ipAddrs := make([]net.IP, 0, len(sans))
	for _, s := range sans {
		if ip := net.ParseIP(s); ip != nil {
			ipAddrs = append(ipAddrs, ip)
			continue
		}
		dnsNames = append(dnsNames, s)
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "MonitorAll", Organization: []string{"MonitorAll"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ipAddrs,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, 0, err
	}
	return der, key, notAfter.UnixMilli(), nil
}

// randomSerial 生成随机证书序列号。
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

// writePEM 把 DER 以 PEM 形式写入文件。
func writePEM(path, blockType string, der []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}

// fileExists 判断文件存在且非空。
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() > 0
}

// readCertSummary 读取已存在证书的指纹、有效期与 SAN。
func readCertSummary(path string) (fingerprint string, notAfter int64, sans []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", 0, nil, errors.New("PEM 解析失败")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", 0, nil, err
	}
	fp := fingerprintDER(cert.Raw)
	all := make([]string, 0, len(cert.DNSNames)+len(cert.IPAddresses))
	all = append(all, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		all = append(all, ip.String())
	}
	return fp, cert.NotAfter.UnixMilli(), all, nil
}

// fingerprintDER 计算证书 DER 的 SHA-256 指纹（冒号分隔十六进制）。
func fingerprintDER(der []byte) string {
	sum := sha256.Sum256(der)
	hexStr := hex.EncodeToString(sum[:])
	var sb strings.Builder
	for i := 0; i < len(hexStr); i += 2 {
		if i > 0 {
			sb.WriteByte(':')
		}
		sb.WriteString(hexStr[i : i+2])
	}
	return sb.String()
}
