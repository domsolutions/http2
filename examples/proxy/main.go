package main

import (
	"bytes"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"

	fasthttp2 "github.com/domsolutions/http2"
	"github.com/valyala/fasthttp"
	"golang.org/x/net/http2"
)

var (
	useFastHTTP2 = flag.Bool("fast", false, "Fasthttp backend")
)

func main() {
	certData, priv, err := GenerateTestCertificate("localhost:8080")
	if err != nil {
		log.Fatalln(err)
	}

	cert, err := tls.X509KeyPair(certData, priv)
	if err != nil {
		log.Fatalln(err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2"},
	}

	proxy := &Proxy{
		Backend: "localhost:8081",
	}

	if !*useFastHTTP2 {
		go startSlowBackend() // hehe
	} else {
		go startFastBackend()
	}

	ln, err := tls.Listen("tcp", ":8443", tlsConfig)
	if err != nil {
		log.Fatalln(err)
	}

	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatalln(err)
		}

		go proxy.handleConn(c)
	}
}

type Proxy struct {
	Backend string
}

func (px *Proxy) handleConn(c net.Conn) {
	defer c.Close()

	bc, err := tls.Dial("tcp", px.Backend, &tls.Config{
		NextProtos:         []string{"h2"},
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer bc.Close()

	if !fasthttp2.ReadPreface(c) {
		log.Fatalln("error reading preface")
	}

	err = fasthttp2.WritePreface(bc)
	if err != nil {
		log.Fatalln(err)
	}

	go readFramesFrom(bc, c, false)
	readFramesFrom(c, bc, true)
}

func readFramesFrom(c, c2 net.Conn, primaryIsProxy bool) {
	// TODO reinstate when know what frame needs to be acquired
	//fr := fasthttp2.AcquireFrame()
	//defer fasthttp2.ReleaseFrame(fr)
	//
	//symbol := byte('>')
	//if !primaryIsProxy {
	//	symbol = '<'
	//}
	//
	//fr.SetMaxLen(0)
	//
	//var err error
	//for err == nil {
	//	_, err = fr.ReadFrom(c) // TODO: Use ReadFromLimitPayload?
	//	if err != nil {
	//		if err == io.EOF {
	//			err = nil
	//		}
	//		break
	//	}
	//
	//	debugFrame(c, fr, symbol)
	//
	//	_, err = fr.WriteTo(c2)
	//}
}

func debugSettings(bf *bytes.Buffer, st *fasthttp2.Settings, symbol byte) {
	fmt.Fprintf(bf, "%c   ACK: %v\n", symbol, st.IsAck())
	if !st.IsAck() {
		fmt.Fprintf(bf, "%c   TableSize: %d\n", symbol, st.HeaderTableSize())
		fmt.Fprintf(bf, "%c   EnablePush: %v\n", symbol, st.Push())
		fmt.Fprintf(bf, "%c   MaxStreams: %d\n", symbol, st.MaxConcurrentStreams())
		fmt.Fprintf(bf, "%c   WindowSize: %d\n", symbol, st.MaxWindowSize())
		fmt.Fprintf(bf, "%c   FrameSize: %d\n", symbol, st.MaxFrameSize())
		fmt.Fprintf(bf, "%c   HeaderSize: %d\n", symbol, st.MaxHeaderListSize())
	}
}

func debugHeaders(bf *bytes.Buffer, fr *fasthttp2.Headers, symbol byte) {
	hp := fasthttp2.AcquireHPACK()
	defer fasthttp2.ReleaseHPACK(hp)

	hf := fasthttp2.AcquireHeaderField()
	defer fasthttp2.ReleaseHeaderField(hf)

	fmt.Fprintf(bf, "%c   EndHeaders: %v\n", symbol, fr.EndHeaders())
	fmt.Fprintf(bf, "%c   HasPadding: %v\n", symbol, fr.Padding())
	fmt.Fprintf(bf, "%c   Dependency: %d\n", symbol, fr.Stream())

	var err error
	b := fr.Headers()

	for len(b) > 0 {
		b, err = hp.Next(hf, b)
		if err != nil {
			log.Println(err)
			return
		}

		fmt.Fprintf(bf, "%c   %s: %s\n", symbol, hf.Key(), hf.Value())
	}
}

func debugData(bf *bytes.Buffer, fr *fasthttp2.Data, symbol byte) {
	fmt.Fprintf(bf, "%c   Data: %s\n", symbol, fr.Data())
}

var (
	hostArg = flag.String("host", "localhost:8081", "host")
)

func init() {
	flag.Parse()
}

func startSlowBackend() {
	certData, priv, err := GenerateTestCertificate(*hostArg)
	if err != nil {
		log.Fatalln(err)
	}

	cert, err := tls.X509KeyPair(certData, priv)
	if err != nil {
		log.Fatalln(err)
	}

	tlsConfig := &tls.Config{
		ServerName:   *hostArg,
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		MaxVersion:   tls.VersionTLS13,
	}

	_, port, _ := net.SplitHostPort(*hostArg)

	s := &http.Server{
		Addr:      ":" + port,
		TLSConfig: tlsConfig,
		Handler:   &ReqHandler{},
	}
	s2 := &http2.Server{}

	err = http2.ConfigureServer(s, s2)
	if err != nil {
		log.Fatalln(err)
	}

	ln, err := tls.Listen("tcp", ":"+port, tlsConfig)
	if err != nil {
		log.Fatalln(err)
	}
	defer ln.Close()

	err = s.Serve(ln)
	if err != nil {
		log.Fatalln(err)
	}
}

type ReqHandler struct{}

func (rh *ReqHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("long") == "" {
		fmt.Fprintf(w, "Hello 21th century!\n")
	} else {
		bf := bytes.NewBuffer(nil)
		for i := 0; i < 1<<24; i++ {
			io.WriteString(bf, "A")
		}
		w.Write(bf.Bytes())
	}
}

func startFastBackend() {
	certData, priv, err := GenerateTestCertificate(*hostArg)
	if err != nil {
		log.Fatalln(err)
	}

	s := &fasthttp.Server{
		Name:    "idk",
		Handler: fastHandler,
	}
	s.AppendCertEmbed(certData, priv)

	fasthttp2.ConfigureServer(s, fasthttp2.ServerConfig{})

	_, port, _ := net.SplitHostPort(*hostArg)

	err = s.ListenAndServeTLS(":"+port, "", "")
	if err != nil {
		log.Fatalln(err)
	}
}

func fastHandler(ctx *fasthttp.RequestCtx) {
	if ctx.FormValue("long") == nil {
		fmt.Fprintf(ctx, "Hello 21th century!\n")
	} else {
		for i := 0; i < 1<<24; i++ {
			ctx.Response.AppendBodyString("A")
		}
	}
}
