// Package forward holds port-forwards on this machine: Lazy, a local port
// that runs `kubectl port-forward` only while something is connected; the
// registry of background forwards (~/.cache/rex8s/pf); and Linker, which
// makes the cluster URLs in log lines clickable through Lazy forwards.
package forward

import (
	"fmt"
	"strings"

	"github.com/vitalbh/rex8s/internal/kube"
)

// Kind is what speaks on a service port, guessed from its appProtocol, its
// name, then its number. It decides what kpf offers once the forward is up:
// a browser for the web, a client command and a URL for databases.
type Kind string

const (
	KindHTTP     Kind = "http"
	KindHTTPS    Kind = "https"
	KindGRPC     Kind = "grpc"
	KindPostgres Kind = "postgres"
	KindMySQL    Kind = "mysql"
	KindRedis    Kind = "redis"
	KindMongo    Kind = "mongo"
	KindKafka    Kind = "kafka"
	KindAMQP     Kind = "amqp"
	KindTCP      Kind = "tcp"
)

// kindNames: words that, inside appProtocol or the port name, give the kind
// away. exact: the whole word only (between -, _, /, . or the ends), so
// "ui" doesn't match "build" or "quic", nor "web" "webhook".
var kindNames = []struct {
	word  string
	kind  Kind
	exact bool
}{
	{"grpc", KindGRPC, false}, {"h2c", KindGRPC, false},
	{"https", KindHTTPS, false}, {"tls", KindHTTPS, false},
	{"postgres", KindPostgres, false}, {"pgsql", KindPostgres, false}, {"psql", KindPostgres, false},
	{"mysql", KindMySQL, false}, {"mariadb", KindMySQL, false},
	{"redis", KindRedis, false},
	{"mongo", KindMongo, false},
	{"kafka", KindKafka, false},
	{"amqp", KindAMQP, false}, {"rabbit", KindAMQP, false},
	{"http", KindHTTP, false}, {"metrics", KindHTTP, false},
	{"web", KindHTTP, true}, {"ui", KindHTTP, true},
}

var kindPorts = map[int]Kind{
	80: KindHTTP, 8080: KindHTTP, 8000: KindHTTP, 3000: KindHTTP, 9090: KindHTTP, 9093: KindHTTP,
	5000: KindHTTP, 8081: KindHTTP, 8888: KindHTTP, 9000: KindHTTP, 4000: KindHTTP,
	443: KindHTTPS, 8443: KindHTTPS,
	5432: KindPostgres, 3306: KindMySQL, 6379: KindRedis, 27017: KindMongo,
	9092: KindKafka, 5672: KindAMQP, 50051: KindGRPC,
}

func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') })
}

// DetectKind guesses what speaks on a service port.
func DetectKind(p kube.ServicePort) Kind {
	for _, s := range []string{p.AppProtocol, p.Name} {
		s = strings.ToLower(s)
		if s == "" {
			continue
		}
		ws := words(s)
		for _, n := range kindNames {
			if !n.exact {
				if strings.Contains(s, n.word) {
					return n.kind
				}
				continue
			}
			for _, w := range ws {
				if w == n.word {
					return n.kind
				}
			}
		}
	}
	if k, ok := kindPorts[p.Port]; ok {
		return k
	}
	return KindTCP
}

// Web kinds open in a browser.
func (k Kind) Web() bool { return k == KindHTTP || k == KindHTTPS }

// URL is what kpf copies and prints for a forward on local.
func (k Kind) URL(local int) string {
	switch k {
	case KindHTTP:
		return fmt.Sprintf("http://localhost:%d", local)
	case KindHTTPS:
		return fmt.Sprintf("https://localhost:%d", local)
	case KindPostgres:
		return fmt.Sprintf("postgres://localhost:%d", local)
	case KindMySQL:
		return fmt.Sprintf("mysql://localhost:%d", local)
	case KindRedis:
		return fmt.Sprintf("redis://localhost:%d", local)
	case KindMongo:
		return fmt.Sprintf("mongodb://localhost:%d/?directConnection=true", local)
	case KindAMQP:
		return fmt.Sprintf("amqp://localhost:%d", local)
	}
	return fmt.Sprintf("localhost:%d", local)
}

// Client is a command that talks to the forward, or "".
func (k Kind) Client(local int) string {
	switch k {
	case KindPostgres:
		return fmt.Sprintf("psql -h localhost -p %d -U USER DB", local)
	case KindMySQL:
		return fmt.Sprintf("mysql -h 127.0.0.1 -P %d -u USER -p", local)
	case KindRedis:
		return fmt.Sprintf("redis-cli -p %d", local)
	case KindMongo:
		return fmt.Sprintf("mongosh %q", k.URL(local))
	case KindGRPC:
		return fmt.Sprintf("grpcurl -plaintext localhost:%d list", local)
	}
	return ""
}

// Caveat warns about kinds a port-forward can't fully carry.
func (k Kind) Caveat() string {
	switch k {
	case KindKafka:
		return "Kafka hands clients the brokers' in-cluster addresses: most clients connect, then fail. kexec into a pod instead."
	case KindMongo:
		return "a replica set hands out in-cluster addresses: directConnection=true talks to this member only."
	case KindHTTPS:
		return "the certificate is for the in-cluster name: expect a browser warning."
	}
	return ""
}
