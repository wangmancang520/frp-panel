package tlsx

import (
	"bufio"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RedirectListener accepts on one port and inspects the first byte of each
// connection: TLS handshakes (0x16) are wrapped with tls.Server, anything else
// is treated as plaintext HTTP and gets a 308 redirect to https:// — the
// request body (if any) is never read or used.
type RedirectListener struct {
	net.Listener
	cfg   *tls.Config
	conns chan net.Conn
	errc  chan error
	once  sync.Once
	done  chan struct{}
}

func NewRedirectListener(inner net.Listener, cfg *tls.Config) *RedirectListener {
	l := &RedirectListener{Listener: inner, cfg: cfg, conns: make(chan net.Conn), errc: make(chan error, 1), done: make(chan struct{})}
	go l.loop()
	return l
}

func (l *RedirectListener) loop() {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			select {
			case l.errc <- err:
			case <-l.done:
			}
			return
		}
		go l.classify(c)
	}
}

type peekConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekConn) Read(b []byte) (int, error) { return p.r.Read(b) }

func (l *RedirectListener) classify(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	if err != nil {
		c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	pc := &peekConn{Conn: c, r: br}
	if first[0] == 0x16 {
		select {
		case l.conns <- tls.Server(pc, l.cfg):
		case <-l.done:
			c.Close()
		}
		return
	}
	redirectPlain(pc, br)
}

func redirectPlain(c net.Conn, br *bufio.Reader) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	host := req.Host
	if host == "" {
		host = c.LocalAddr().String()
	}
	target := "https://" + host + req.URL.RequestURI()
	body := "This panel only accepts HTTPS. Redirecting to " + target + "\n"
	resp := "HTTP/1.1 308 Permanent Redirect\r\n" +
		"Location: " + strings.ReplaceAll(target, "\r\n", "") + "\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Connection: close\r\n" +
		"Content-Length: " + itoa(len(body)) + "\r\n\r\n" + body
	_, _ = c.Write([]byte(resp))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func (l *RedirectListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case err := <-l.errc:
		return nil, err
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *RedirectListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}
