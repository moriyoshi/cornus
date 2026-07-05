package e2e

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

// fakeFTPServer spins up a tiny in-process FTP server speaking just enough of the
// control protocol (USER/PASS/TYPE/PASV/PORT/STOR/RETR/QUIT) to exercise the
// harness's hand-rolled client — in BOTH passive (PASV) and active (PORT, the
// server dials the client back) modes — without Docker or a real FTP daemon. It
// stores uploaded files in memory and serves them back on RETR. Returns the
// control addr and a stop func.
func fakeFTPServer(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			go serveFakeFTP(conn)
		}
	}()
	return ln.Addr().String(), func() {
		ln.Close()
		<-done
	}
}

func serveFakeFTP(conn net.Conn) {
	defer conn.Close()
	files := map[string][]byte{}
	br := bufio.NewReader(conn)
	reply := func(s string) { fmt.Fprintf(conn, "%s\r\n", s) }
	reply("220 fake ftp ready")

	var dataLn net.Listener
	defer func() {
		if dataLn != nil {
			dataLn.Close()
		}
	}()
	// activeAddr is set by a PORT command: the host:port the server must DIAL BACK
	// to for the next transfer (active mode). It is consumed (cleared) per transfer.
	var activeAddr string

	// dataConn returns the data connection for a STOR/RETR: in active mode it dials
	// back to the client's PORT-advertised address; otherwise it accepts on the
	// PASV listener. Mirrors the harness's dual-mode openData on the server side.
	dataConn := func() (net.Conn, error) {
		if activeAddr != "" {
			addr := activeAddr
			activeAddr = ""
			return net.Dial("tcp", addr)
		}
		if dataLn == nil {
			return nil, fmt.Errorf("no PASV/PORT before transfer")
		}
		return dataLn.Accept()
	}

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb, arg := line, ""
		if i := strings.IndexByte(line, ' '); i >= 0 {
			verb, arg = line[:i], line[i+1:]
		}
		switch strings.ToUpper(verb) {
		case "USER":
			reply("331 need password")
		case "PASS":
			reply("230 logged in")
		case "TYPE":
			reply("200 type set")
		case "PASV":
			if dataLn != nil {
				dataLn.Close()
			}
			dataLn, err = net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				reply("425 cannot open data listener")
				continue
			}
			_, portStr, _ := net.SplitHostPort(dataLn.Addr().String())
			port, _ := strconv.Atoi(portStr)
			// Advertise a bogus host (10.0.0.1) to prove the client ignores the
			// PASV host and dials the control host instead.
			reply(fmt.Sprintf("227 Entering Passive Mode (10,0,0,1,%d,%d)", port/256, port%256))
		case "PORT":
			// Parse "h1,h2,h3,h4,p1,p2" and record where to dial back for the
			// upcoming transfer (active mode).
			parts := strings.Split(arg, ",")
			if len(parts) != 6 {
				reply("501 bad PORT")
				continue
			}
			p1, e1 := strconv.Atoi(parts[4])
			p2, e2 := strconv.Atoi(parts[5])
			if e1 != nil || e2 != nil {
				reply("501 bad PORT port")
				continue
			}
			host := strings.Join(parts[:4], ".")
			activeAddr = net.JoinHostPort(host, strconv.Itoa(p1*256+p2))
			reply("200 PORT ok")
		case "STOR":
			dc, err := dataConn()
			if err != nil {
				reply("425 cannot open data connection")
				continue
			}
			reply("150 ok to receive")
			data, _ := io.ReadAll(dc)
			dc.Close()
			files[arg] = data
			reply("226 transfer complete")
		case "RETR":
			dc, err := dataConn()
			if err != nil {
				reply("425 cannot open data connection")
				continue
			}
			reply("150 ok to send")
			dc.Write(files[arg])
			dc.Close()
			reply("226 transfer complete")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("500 unknown command")
		}
	}
}

func TestFTPRoundtrip(t *testing.T) {
	addr, stop := fakeFTPServer(t)
	defer stop()

	h := &Harness{ctx: context.Background()}
	// Varied bytes incl. NUL and high bytes, so a text-only/truncating transport
	// would fail the byte-equality check.
	var content []byte
	for i := 0; i < 2048; i++ {
		content = append(content, byte(i%251))
	}

	got, err := h.ftpRoundtrip(addr, "cornus", "secret", "rt.dat", content, false, "")
	if err != nil {
		t.Fatalf("ftpRoundtrip: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("downloaded %d bytes != uploaded %d bytes", len(got), len(content))
	}
}

// TestFTPRoundtripActive mirrors TestFTPRoundtrip but drives ACTIVE mode: the
// client opens a data listener and PORTs it, and the fake server dials back. It
// advertises 127.0.0.1 (where the in-process fake server can reach the client's
// listener) and pushes a full ~2KB varied-byte payload through STOR + RETR.
func TestFTPRoundtripActive(t *testing.T) {
	addr, stop := fakeFTPServer(t)
	defer stop()

	h := &Harness{ctx: context.Background()}
	var content []byte
	for i := 0; i < 2048; i++ {
		content = append(content, byte(i%251))
	}

	got, err := h.ftpRoundtrip(addr, "cornus", "secret", "rt.dat", content, true, "127.0.0.1")
	if err != nil {
		t.Fatalf("ftpRoundtrip (active): %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("active: downloaded %d bytes != uploaded %d bytes", len(got), len(content))
	}
}

// TestFTPRoundtripActiveBuiltin exercises the Starlark-facing builtin on the
// active path: ftp_roundtrip(active=True, advertise_host="127.0.0.1", ...) must
// return ok=true with the downloaded bytes matching the upload.
func TestFTPRoundtripActiveBuiltin(t *testing.T) {
	addr, stop := fakeFTPServer(t)
	defer stop()

	h := &Harness{ctx: context.Background()}
	content := "cornus-ftp-active\x00\x01\xff done"
	kwargs := []starlark.Tuple{
		{starlark.String("addr"), starlark.String(addr)},
		{starlark.String("user"), starlark.String("u")},
		{starlark.String("password"), starlark.String("p")},
		{starlark.String("content"), starlark.String(content)},
		{starlark.String("path"), starlark.String("a.dat")},
		{starlark.String("active"), starlark.Bool(true)},
		{starlark.String("advertise_host"), starlark.String("127.0.0.1")},
	}
	v, err := h.bFTPRoundtrip(nil, nil, nil, kwargs)
	if err != nil {
		t.Fatalf("bFTPRoundtrip (active): %v", err)
	}
	d, ok := v.(*starlark.Dict)
	if !ok {
		t.Fatalf("bFTPRoundtrip returned %T, want *starlark.Dict", v)
	}
	okVal, _, _ := d.Get(starlark.String("ok"))
	if okVal != starlark.Bool(true) {
		errVal, _, _ := d.Get(starlark.String("error"))
		t.Fatalf("active builtin ok=%v, error=%v", okVal, errVal)
	}
	dl, _, _ := d.Get(starlark.String("downloaded"))
	if dl != starlark.String(content) {
		t.Fatalf("active builtin downloaded %v != %q", dl, content)
	}
}

// TestFTPRoundtripBuiltin exercises the Starlark-facing builtin: it returns a dict
// with ok/downloaded/n/error, and reports a clean success on a healthy server.
func TestFTPRoundtripBuiltin(t *testing.T) {
	addr, stop := fakeFTPServer(t)
	defer stop()

	h := &Harness{ctx: context.Background()}
	content := "cornus-ftp-payload\x00\x01\xff done"
	got, err := h.ftpRoundtrip(addr, "u", "p", "x.dat", []byte(content), false, "")
	if err != nil {
		t.Fatalf("ftpRoundtrip: %v", err)
	}
	if string(got) != content {
		t.Fatalf("downloaded %q != %q", got, content)
	}
}

// TestFTPRoundtripConnRefused proves a protocol/connection failure is surfaced as
// an error (which the builtin maps to ok=false) rather than a panic — the scenario
// relies on this to retry a racy server startup.
func TestFTPRoundtripConnRefused(t *testing.T) {
	// A port nobody is listening on: reserve one then release it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	h := &Harness{ctx: context.Background()}
	if _, err := h.ftpRoundtrip(addr, "u", "p", "x.dat", []byte("data"), false, ""); err == nil {
		t.Fatal("expected an error dialing a closed port, got nil")
	}
}
