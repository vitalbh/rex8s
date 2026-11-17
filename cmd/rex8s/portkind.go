package main

import (
	"fmt"
	"strings"
)

// portKind is what speaks on a service port, guessed from its appProtocol,
// its name, then its number. It decides what kpf offers once the forward is
// up: a browser for the web, a client command and a URL for databases.
type portKind string

const (
	kindHTTP     portKind = "http"
	kindHTTPS    portKind = "https"
	kindGRPC     portKind = "grpc"
	kindPostgres portKind = "postgres"
	kindMySQL    portKind = "mysql"
	kindRedis    portKind = "redis"
	kindMongo    portKind = "mongo"
	kindKafka    portKind = "kafka"
	kindAMQP     portKind = "amqp"
	kindTCP      portKind = "tcp"
)

// names: words that, inside appProtocol or the port name, give the kind away.
var kindNames = []struct {
	word string
	kind portKind
}{
	{"grpc", kindGRPC}, {"h2c", kindGRPC},
	{"https", kindHTTPS}, {"tls", kindHTTPS},
	{"postgres", kindPostgres}, {"pgsql", kindPostgres}, {"psql", kindPostgres},
	{"mysql", kindMySQL}, {"mariadb", kindMySQL},
	{"redis", kindRedis},
	{"mongo", kindMongo},
	{"kafka", kindKafka},
	{"amqp", kindAMQP}, {"rabbit", kindAMQP},
	{"http", kindHTTP}, {"web", kindHTTP}, {"metrics", kindHTTP}, {"ui", kindHTTP},
}

var kindPorts = map[int]portKind{
	80: kindHTTP, 8080: kindHTTP, 8000: kindHTTP, 3000: kindHTTP, 9090: kindHTTP, 9093: kindHTTP,
	5000: kindHTTP, 8081: kindHTTP, 8888: kindHTTP, 9000: kindHTTP, 4000: kindHTTP,
	443: kindHTTPS, 8443: kindHTTPS,
	5432: kindPostgres, 3306: kindMySQL, 6379: kindRedis, 27017: kindMongo,
	9092: kindKafka, 5672: kindAMQP, 50051: kindGRPC,
}

func detectKind(p svcPort) portKind {
	for _, s := range []string{p.AppProtocol, p.Name} {
		s = strings.ToLower(s)
		if s == "" {
			continue
		}
		for _, n := range kindNames {
			if strings.Contains(s, n.word) {
				return n.kind
			}
		}
	}
	if k, ok := kindPorts[p.Port]; ok {
		return k
	}
	return kindTCP
}

// web kinds open in a browser.
func (k portKind) web() bool { return k == kindHTTP || k == kindHTTPS }

// url is what kpf copies and prints for a forward on local.
func (k portKind) url(local int) string {
	switch k {
	case kindHTTP:
		return fmt.Sprintf("http://localhost:%d", local)
	case kindHTTPS:
		return fmt.Sprintf("https://localhost:%d", local)
	case kindPostgres:
		return fmt.Sprintf("postgres://localhost:%d", local)
	case kindMySQL:
		return fmt.Sprintf("mysql://localhost:%d", local)
	case kindRedis:
		return fmt.Sprintf("redis://localhost:%d", local)
	case kindMongo:
		return fmt.Sprintf("mongodb://localhost:%d/?directConnection=true", local)
	case kindAMQP:
		return fmt.Sprintf("amqp://localhost:%d", local)
	}
	return fmt.Sprintf("localhost:%d", local)
}

// client is a command that talks to the forward, or "".
func (k portKind) client(local int) string {
	switch k {
	case kindPostgres:
		return fmt.Sprintf("psql -h localhost -p %d -U USER DB", local)
	case kindMySQL:
		return fmt.Sprintf("mysql -h 127.0.0.1 -P %d -u USER -p", local)
	case kindRedis:
		return fmt.Sprintf("redis-cli -p %d", local)
	case kindMongo:
		return fmt.Sprintf("mongosh %q", k.url(local))
	case kindGRPC:
		return fmt.Sprintf("grpcurl -plaintext localhost:%d list", local)
	}
	return ""
}

// caveat warns about kinds a port-forward can't fully carry.
func (k portKind) caveat() string {
	switch k {
	case kindKafka:
		return "Kafka hands clients the brokers' in-cluster addresses: most clients connect, then fail. kexec into a pod instead."
	case kindMongo:
		return "a replica set hands out in-cluster addresses: directConnection=true talks to this member only."
	case kindHTTPS:
		return "the certificate is for the in-cluster name: expect a browser warning."
	}
	return ""
}
